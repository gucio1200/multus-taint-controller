package main

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	labelSelector = os.Getenv("MULTUS_LABEL_SELECTOR")
	taintKey      = "multus.network.k8s.io/readiness"
	taintValue    = "false"
	taintEffect   = corev1.TaintEffectNoSchedule
)

func main() {
	// In-cluster configuration for authenticating to Kubernetes API
	config, err := rest.InClusterConfig() // Use InClusterConfig for authentication within the cluster
	if err != nil {
		panic(fmt.Sprintf("Error creating in-cluster config: %s", err))
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(fmt.Sprintf("Error creating clientset: %s", err))
	}

	// Watch nodes for changes
	nodeWatcher, err := clientset.CoreV1().Nodes().Watch(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(fmt.Sprintf("Error watching nodes: %s", err))
	}

	for event := range nodeWatcher.ResultChan() {
		node := event.Object.(*corev1.Node)
		nodeName := node.Name

		// Check if Multus is ready and taint/untaint node accordingly
		if isMultusReady(clientset) {
			untaintNode(clientset, nodeName)
		} else {
			taintNode(clientset, nodeName)
		}
	}
}

// Check if Multus pod is running
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

// Taint the node if Multus is not ready
func taintNode(clientset *kubernetes.Clientset, nodeName string) {
	node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Error getting node %s: %v\n", nodeName, err)
		return
	}

	// Taint the node if not already tainted
	for _, taint := range node.Spec.Taints {
		if taint.Key == taintKey {
			return // Node already tainted
		}
	}

	taint := corev1.Taint{
		Key:    taintKey,
		Value:  taintValue,
		Effect: taintEffect,
	}
	nodeCopy := node.DeepCopy()
	nodeCopy.Spec.Taints = append(nodeCopy.Spec.Taints, taint)

	_, err = clientset.CoreV1().Nodes().Update(context.Background(), nodeCopy, metav1.UpdateOptions{})
	if err != nil {
		fmt.Printf("Error tainting node %s: %v\n", nodeName, err)
	}
}

// Remove the taint from the node if Multus is ready
func untaintNode(clientset *kubernetes.Clientset, nodeName string) {
	node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Error getting node %s: %v\n", nodeName, err)
		return
	}

	// Remove the taint
	nodeCopy := node.DeepCopy()
	var taints []corev1.Taint
	for _, taint := range nodeCopy.Spec.Taints {
		if taint.Key != taintKey {
			taints = append(taints, taint)
		}
	}
	nodeCopy.Spec.Taints = taints

	_, err = clientset.CoreV1().Nodes().Update(context.Background(), nodeCopy, metav1.UpdateOptions{})
	if err != nil {
		fmt.Printf("Error removing taint from node %s: %v\n", nodeName, err)
	}
}
