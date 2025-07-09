package main

import (
	"context"
	"flag"
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
	flag.Set("v", "6") // Max verbosity
	flag.Parse()
	defer klog.Flush()

	klog.Infof("Starting Multus taint controller with label selector: %s", labelSelector)

	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Error creating in-cluster config: %s", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Error creating clientset: %s", err)
	}

	namespace, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		klog.Fatalf("Error reading namespace from file: %s", err)
	}

	klog.Infof("Controller running in namespace: %s", string(namespace))
	runController(clientset, string(namespace))
}

func runController(clientset *kubernetes.Clientset, namespace string) {
	nodeWatcher, err := clientset.CoreV1().Nodes().Watch(context.Background(), metav1.ListOptions{})
	if err != nil {
		klog.Fatalf("Error watching nodes: %s", err)
	}

	klog.Info("Watching for node events...")

	for event := range nodeWatcher.ResultChan() {
		node := event.Object.(*corev1.Node)
		nodeName := node.Name
		klog.V(5).Infof("Received node event: %s, type: %s", nodeName, event.Type)

		if isMultusReady(clientset) {
			klog.V(3).Infof("Multus is ready, untainting node %s", nodeName)
			untaintNode(clientset, nodeName)
		} else {
			klog.V(3).Infof("Multus is NOT ready, tainting node %s", nodeName)
			taintNode(clientset, nodeName)
		}
	}
}

func isMultusReady(clientset *kubernetes.Clientset) bool {
	klog.V(6).Info("Checking Multus pod readiness...")
	pods, err := clientset.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		klog.Errorf("Error listing Multus pods: %v", err)
		return false
	}

	klog.V(5).Infof("Found %d running Multus pods", len(pods.Items))
	for _, pod := range pods.Items {
		klog.V(7).Infof("Multus pod: %s (node: %s)", pod.Name, pod.Spec.NodeName)
	}

	return len(pods.Items) > 0
}

func taintNode(clientset *kubernetes.Clientset, nodeName string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		klog.V(6).Infof("Getting node %s for tainting", nodeName)
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Error getting node %s: %v", nodeName, err)
			return err
		}

		for _, taint := range node.Spec.Taints {
			if taint.Key == taintKey {
				klog.V(4).Infof("Node %s already tainted, skipping", nodeName)
				return nil
			}
		}

		nodeCopy := node.DeepCopy()
		nodeCopy.Spec.Taints = append(nodeCopy.Spec.Taints, corev1.Taint{
			Key:    taintKey,
			Value:  taintValue,
			Effect: taintEffect,
		})

		klog.Infof("Applying taint to node %s", nodeName)
		_, err = clientset.CoreV1().Nodes().Update(context.Background(), nodeCopy, metav1.UpdateOptions{})
		return err
	})

	if err != nil {
		klog.Errorf("Error tainting node %s: %v", nodeName, err)
	}
}

func untaintNode(clientset *kubernetes.Clientset, nodeName string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		klog.V(6).Infof("Getting node %s for untainting", nodeName)
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Error getting node %s: %v", nodeName, err)
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

		klog.Infof("Removing taint from node %s", nodeName)
		_, err = clientset.CoreV1().Nodes().Update(context.Background(), nodeCopy, metav1.UpdateOptions{})
		return err
	})

	if err != nil {
		klog.Errorf("Error removing taint from node %s: %v", nodeName, err)
	}
}
