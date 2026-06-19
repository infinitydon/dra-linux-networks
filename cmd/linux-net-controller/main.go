package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	"github.com/infinitydon/dra-linux-networks/internal/controller"
)

func main() {
	var kubeconfig, bindAddress, leaseName, leaseNamespace, identity string
	var interval, leaseDuration, renewDeadline, retryPeriod, healthTimeout time.Duration
	var leaderElect bool
	klog.InitFlags(nil)
	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to kubeconfig")
	flag.StringVar(&bindAddress, "bind-address", ":9179", "health endpoint bind address")
	flag.DurationVar(&interval, "reconcile-interval", 30*time.Second, "allocation reconciliation interval")
	flag.BoolVar(&leaderElect, "leader-elect", true, "enable Lease-based leader election")
	flag.StringVar(&leaseName, "leader-election-lease-name", "linux-net-dra-controller", "leader election Lease name")
	flag.StringVar(&leaseNamespace, "leader-election-lease-namespace", envOrDefault("POD_NAMESPACE", "kube-system"), "leader election Lease namespace")
	flag.StringVar(&identity, "leader-election-identity", envOrDefault("POD_NAME", ""), "unique leader election identity")
	flag.DurationVar(&leaseDuration, "leader-election-lease-duration", 15*time.Second, "leader election lease duration")
	flag.DurationVar(&renewDeadline, "leader-election-renew-deadline", 10*time.Second, "leader election renew deadline")
	flag.DurationVar(&retryPeriod, "leader-election-retry-period", 2*time.Second, "leader election retry period")
	flag.DurationVar(&healthTimeout, "leader-election-health-timeout", 20*time.Second, "maximum unhealthy leader-election duration")
	flag.Parse()
	if identity == "" {
		var err error
		identity, err = os.Hostname()
		if err != nil {
			klog.Fatalf("resolve leader election identity: %v", err)
		}
	}

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

	watchdog := leaderelection.NewLeaderHealthzAdaptor(healthTimeout)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, request *http.Request) {
		if leaderElect {
			if err := watchdog.Check(request); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	server := &http.Server{Addr: bindAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.ErrorS(err, "Health server exited")
			cancel()
		}
	}()

	exitCode := 0
	if !leaderElect {
		if err := reconciler.Run(ctx); err != nil {
			klog.ErrorS(err, "Controller exited")
			exitCode = 1
		}
	} else {
		lock := &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{Name: leaseName, Namespace: leaseNamespace},
			Client:    client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{
				Identity: identity,
			},
		}
		var lostLeadership atomic.Bool
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			LeaseDuration:   leaseDuration,
			RenewDeadline:   renewDeadline,
			RetryPeriod:     retryPeriod,
			ReleaseOnCancel: true,
			WatchDog:        watchdog,
			Name:            "linux-net-dra-controller",
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					klog.InfoS("Started leading", "identity", identity)
					if err := reconciler.Run(leaderCtx); err != nil && leaderCtx.Err() == nil {
						klog.ErrorS(err, "Leader reconciliation loop exited")
						cancel()
					}
				},
				OnStoppedLeading: func() {
					if ctx.Err() == nil {
						lostLeadership.Store(true)
						klog.ErrorS(nil, "Lost leader election Lease", "identity", identity)
						cancel()
					}
				},
				OnNewLeader: func(newIdentity string) {
					klog.InfoS("Observed leader", "identity", newIdentity)
				},
			},
		})
		if lostLeadership.Load() {
			exitCode = 1
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
