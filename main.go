package main

import (
	"context"
	"fmt"
	"os"
	"io/ioutil"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	labelSelector = os.Getenv("MULTUS_LABEL_SELECTOR")
	taintKey      = "multus.network.k8s.io/readiness"
	taintValue    = "false"
	taintEffect   = corev1.TaintEffectNoSchedule
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(fmt.Sprintf("Error creating in-cluster config: %s", err))
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(fmt.Sprintf("Error creating clientset: %s", err))
	}

	namespace, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		panic(fmt.Sprintf("Error reading namespace from file: %s", err))
	}

	runController(clientset, string(namespace))
}

func runController(clientset *kubernetes.Clientset, namespace string) {
	nodeWatcher, err := clientset.CoreV1().Nodes().Watch(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(fmt.Sprintf("Error watching nodes: %s", err))
	}

	for event := range nodeWatcher.ResultChan() {
		node := event.Object.(*corev1.Node)
		nodeName := node.Name

		if isMultusReady(clientset) {
			untaintNode(clientset, nodeName)
		} else {
			taintNode(clientset, nodeName)
		}
	}
}

func isMultusReady(clientset *kubernetes.Clientset) bool {
	pods, err := clientset.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		fmt.Println("Error checking Multus pod readiness:", err)
		return false
	}
	return len(pods.Items) > 0
}

func taintNode(clientset *kubernetes.Clientset, nodeName string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("Error getting node %s: %v\n", nodeName, err)
			return err
		}

		for _, taint := range node.Spec.Taints {
			if taint.Key == taintKey {
				return nil // Already tainted
			}
		}

		nodeCopy := node.DeepCopy()
		nodeCopy.Spec.Taints = append(nodeCopy.Spec.Taints, corev1.Taint{
			Key:    taintKey,
			Value:  taintValue,
			Effect: taintEffect,
		})

		_, err = clientset.CoreV1().Nodes().Update(context.Background(), nodeCopy, metav1.UpdateOptions{})
		return err
	})

	if err != nil {
		fmt.Printf("Error tainting node %s: %v\n", nodeName, err)
	}
}

func untaintNode(clientset *kubernetes.Clientset, nodeName string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("Error getting node %s: %v\n", nodeName, err)
			return err
		}

		nodeCopy := node.DeepCopy()
		var newTaints []corev1.Taint
		for _, taint := range nodeCopy.Spec.Taints {
			if taint.Key != taintKey {
				newTaints = append(newTaints, taint)
			}
		}
		nodeCopy.Spec.Taints = newTaints

		_, err = clientset.CoreV1().Nodes().Update(context.Background(), nodeCopy, metav1.UpdateOptions{})
		return err
	})

	if err != nil {
		fmt.Printf("Error removing taint from node %s: %v\n", nodeName, err)
	}
}
