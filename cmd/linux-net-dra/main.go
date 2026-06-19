package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	nodeutil "k8s.io/component-helpers/node/util"
	"k8s.io/klog/v2"

	"github.com/infinitydon/dra-linux-networks/internal/config"
	"github.com/infinitydon/dra-linux-networks/internal/driver"
)

func main() {
	var (
		kubeconfig       string
		configPath       string
		driverName       string
		nodeName         string
		hostnameOverride string
		bindAddress      string
		stateFile        string
		kubeletPlugins   string
		kubeletRegistry  string
	)

	klog.InitFlags(nil)
	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to kubeconfig")
	flag.StringVar(&configPath, "config", "/etc/linux-net-dra/config.json", "driver config path")
	flag.StringVar(&driverName, "driver-name", config.DefaultDriverName, "DRA driver name")
	flag.StringVar(&nodeName, "node-name", "", "Kubernetes node name")
	flag.StringVar(&hostnameOverride, "hostname-override", "", "hostname override used when node-name is empty")
	flag.StringVar(&bindAddress, "bind-address", ":9178", "health endpoint bind address")
	flag.StringVar(&stateFile, "state-file", "/var/run/linux-net-dra/state.json", "state file shared by DRA and NRI handlers")
	flag.StringVar(&kubeletPlugins, "kubelet-plugins-dir", "", "kubelet plugins directory")
	flag.StringVar(&kubeletRegistry, "kubelet-registration-dir", "", "kubelet plugin registration directory")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		klog.Fatalf("load config: %v", err)
	}
	if driverName == "" {
		driverName = cfg.DriverName
	}
	if nodeName == "" {
		nodeName, err = nodeutil.GetHostname(hostnameOverride)
		if err != nil {
			klog.Fatalf("resolve node name: %v", err)
		}
	}

	restCfg, err := rest.InClusterConfig()
	if kubeconfig != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		klog.Fatalf("create kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		klog.Fatalf("create kube client: %v", err)
	}

	ready := false
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	go func() {
		if err := http.ListenAndServe(bindAddress, mux); err != nil {
			klog.ErrorS(err, "health server exited")
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := driver.Start(ctx, driver.Options{
		NodeName:               nodeName,
		DriverName:             driverName,
		KubeletPluginsDir:      kubeletPlugins,
		KubeletRegistrationDir: kubeletRegistry,
		StateFile:              stateFile,
		Config:                 cfg,
		Client:                 client,
	})
	if err != nil {
		klog.Fatalf("start driver: %v", err)
	}
	defer d.Stop()
	ready = true

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case s := <-sig:
		klog.Infof("received %s", s)
	case <-ctx.Done():
	}
	fmt.Fprintln(os.Stderr, "linux-net-dra stopped")
}
