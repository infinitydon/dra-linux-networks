package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/infinitydon/dra-linux-networks/internal/controller"
)

func main() {
	var kubeconfig, bindAddress string
	var interval time.Duration
	klog.InitFlags(nil)
	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to kubeconfig")
	flag.StringVar(&bindAddress, "bind-address", ":9179", "health endpoint bind address")
	flag.DurationVar(&interval, "reconcile-interval", 30*time.Second, "allocation reconciliation interval")
	flag.Parse()

	restConfig, err := rest.InClusterConfig()
	if kubeconfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		klog.Fatalf("create kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		klog.Fatalf("create kube client: %v", err)
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		klog.Fatalf("create dynamic kube client: %v", err)
	}
	reconciler, err := controller.New(client, dynamicClient, interval)
	if err != nil {
		klog.Fatalf("create controller: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	server := &http.Server{Addr: bindAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.ErrorS(err, "Health server exited")
			cancel()
		}
	}()

	if err := reconciler.Run(ctx); err != nil {
		klog.ErrorS(err, "Controller exited")
		os.Exit(1)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
}
