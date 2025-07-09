package main

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
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
		klog.Fatalf("Error creating in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Error creating clientset: %v", err)
	}

	ctx := context.Background()

	// Create shared informer factory
	informerFactory := informers.NewSharedInformerFactory(clientset, time.Minute)
	nodeInformer := informerFactory.Core().V1().Nodes().Informer()

	// Workqueue with rate-limiting
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "nodes")

	// Add event handlers to informer
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(newObj)
			queue.Add(key)
		},
	})

	// Start informers
	stopCh := make(chan struct{})
	defer close(stopCh)
	informerFactory.Start(stopCh)

	// Wait for cache sync
	if !cache.WaitForCacheSync(stopCh, nodeInformer.HasSynced) {
		klog.Fatal("Failed to sync caches")
	}

	// Start workers
	numWorkers := 4
	for i := 0; i < numWorkers; i++ {
		go runWorker(ctx, clientset, queue)
	}

	<-stopCh
}

func runWorker(ctx context.Context, clientset *kubernetes.Clientset, queue workqueue.RateLimitingInterface) {
	for {
		key, shutdown := queue.Get()
		if shutdown {
			return
		}

		err := func() error {
			defer queue.Done(key)

			obj, exists, err := cache.DeletionHandlingMetaNamespaceKeyFunc(key)
			if err != nil {
				klog.Errorf("Error getting key %s: %v", key, err)
				queue.Forget(key)
				return err
			}
			if !exists {
				queue.Forget(key)
				return nil
			}

			nodeName := key.(string)

			// Reconcile node
			if isMultusReady(clientset) {
				untaintNode(clientset, nodeName)
			} else {
				taintNode(clientset, nodeName)
			}

			queue.Forget(key)
			return nil
		}()

		if err != nil {
			klog.Errorf("Error processing node: %v", err)
			queue.AddRateLimited(key)
		}
	}
}

func isMultusReady(clientset *kubernetes.Clientset) bool {
	pods, err := clientset.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		klog.Warningf("Error listing pods: %v", err)
		return false
	}
	return len(pods.Items) > 0
}

func taintNode(clientset *kubernetes.Clientset, nodeName string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Error getting node %s: %v", nodeName, err)
			return err
		}

		for _, taint := range node.Spec.Taints {
			if taint.Key == taintKey {
				return nil
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
		klog.Errorf("Failed to taint node %s: %v", nodeName, err)
	}
}

func untaintNode(clientset *kubernetes.Clientset, nodeName string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
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

		_, err = clientset.CoreV1().Nodes().Update(context.Background(), nodeCopy, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		klog.Errorf("Failed to remove taint from node %s: %v", nodeName, err)
	}
}
