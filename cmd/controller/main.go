package main

import (
	"flag"
	"os"
	"time"

	"github.com/loft-demos/kubevirt-provider-controller/internal/controller"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func main() {
	var kubeconfig string
	var workers int
	var resync time.Duration
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig; defaults to in-cluster configuration")
	flag.IntVar(&workers, "workers", 2, "Number of reconciliation workers")
	flag.DurationVar(&resync, "resync-period", 30*time.Second, "Full informer resync period")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := loadConfig(kubeconfig)
	if err != nil {
		klog.ErrorS(err, "load Kubernetes configuration")
		os.Exit(1)
	}
	cfg.UserAgent = "kubevirt-provider-controller"

	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		klog.ErrorS(err, "create dynamic client")
		os.Exit(1)
	}
	c, err := controller.New(cfg, dynamicClient, resync)
	if err != nil {
		klog.ErrorS(err, "create controller")
		os.Exit(1)
	}
	if err := c.Run(controller.SignalContext(), workers); err != nil {
		klog.ErrorS(err, "controller stopped")
		os.Exit(1)
	}
}

func loadConfig(path string) (*rest.Config, error) {
	if path != "" {
		return clientcmd.BuildConfigFromFlags("", path)
	}
	return rest.InClusterConfig()
}
