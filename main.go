package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

var (
	labelSelector     = os.Getenv("MULTUS_LABEL_SELECTOR") // e.g. app=multus
	taintKey          = "multus.network.k8s.io/readiness"
	taintValue        = "false"
	taintEffect       = corev1.TaintEffectNoSchedule
	multusReadyByNode sync.Map // map[nodeName]bool
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Failed to create in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create clientset: %v", err)
	}

	ctx := context.Background()

	// Set up informer factory with pod filtering
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, time.Minute,
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = labelSelector
		}),
	)

	podInformer := factory.Core().V1().Pods().Informer()
	nodeInformer := factory.Core().V1().Nodes().Informer()

	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "nodes")

	// Watch for Multus pod events
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { handlePod(obj, queue) },
		UpdateFunc: func(_, newObj interface{}) { handlePod(newObj, queue) },
		DeleteFunc: func(obj interface{}) { handlePod(obj, queue) },
	})

	// Watch for node events
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
		},
		UpdateFunc: func(_, newObj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(newObj)
			queue.Add(key)
		},
	})

	stopCh := make(chan struct{})
	defer close(stopCh)

	factory.Start(stopCh)

	if !cache.WaitForCacheSync(stopCh, podInformer.HasSynced, nodeInformer.HasSynced) {
		klog.Fatalf("Failed to sync caches")
	}

	// Start workers
	numWorkers := 4
	for i := 0; i < numWorkers; i++ {
		go wait.Until(func() {
			runWorker(ctx, clientset, queue)
		}, time.Second, stopCh)
	}

	<-stopCh
}

func handlePod(obj interface{}, queue workqueue.RateLimitingInterface) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	nodeName := pod.Spec.NodeName
	if nodeName == "" {
		return
	}

	ready := pod.Status.Phase == corev1.PodRunning
	prev, exists := multusReadyByNode.Load(nodeName)

	// Update only if changed
	if !exists || prev != ready {
		multusReadyByNode.Store(nodeName, ready)
		queue.Add(nodeName) // Trigger node reconciliation
	}
}

func runWorker(ctx context.Context, clientset *kubernetes.Clientset, queue workqueue.RateLimitingInterface) {
	for {
		key, shutdown := queue.Get()
		if shutdown {
			return
		}

		func() {
			defer queue.Done(key)

			nodeName, ok := key.(string)
			if !ok {
				queue.Forget(key)
				return
			}

			err := reconcileNode(ctx, clientset, nodeName)
			if err != nil {
				klog.Errorf("Error reconciling node %s: %v", nodeName, err)
				queue.AddRateLimited(key)
			} else {
				queue.Forget(key)
			}
		}()
	}
}

func reconcileNode(ctx context.Context, clientset *kubernetes.Clientset, nodeName string) error {
	ready, _ := multusReadyByNode.Load(nodeName)
	isReady := ready == true

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("getting node: %w", err)
		}

		hasTaint := false
		for _, taint := range node.Spec.Taints {
			if taint.Key == taintKey {
				hasTaint = true
				break
			}
		}

		nodeCopy := node.DeepCopy()

		if isReady && hasTaint {
			// remove taint
			newTaints := []corev1.Taint{}
			for _, t := range nodeCopy.Spec.Taints {
				if t.Key != taintKey {
					newTaints = append(newTaints, t)
				}
			}
			nodeCopy.Spec.Taints = newTaints
			klog.Infof("Removing taint from node %s", nodeName)
		} else if !isReady && !hasTaint {
			// add taint
			nodeCopy.Spec.Taints = append(nodeCopy.Spec.Taints, corev1.Taint{
				Key:    taintKey,
				Value:  taintValue,
				Effect: taintEffect,
			})
			klog.Infof("Adding taint to node %s", nodeName)
		} else {
			return nil // nothing to do
		}

		_, err = clientset.CoreV1().Nodes().Update(ctx, nodeCopy, metav1.UpdateOptions{})
		return err
	})
}
