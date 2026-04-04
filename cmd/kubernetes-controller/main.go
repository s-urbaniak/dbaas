package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/s-urbaniak/dbaas/internal/controller"
)

func main() {
	var (
		metricsAddr        string
		proxyAddr          string
		kcpKubeconfig      string
		consumersWorkspace string
		proxyBaseURL       string
		enableLeaderElect  bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8081", "Metrics endpoint address.")
	flag.StringVar(&proxyAddr, "proxy-bind-address", ":8080", "Mounted workspace proxy listen address.")
	flag.StringVar(&kcpKubeconfig, "kcp-kubeconfig", "/etc/kcp/kubeconfig", "Path to the kcp admin kubeconfig.")
	flag.StringVar(&consumersWorkspace, "consumers-workspace", "root:consumers", "kcp path of the consumer org workspace.")
	flag.StringVar(&proxyBaseURL, "proxy-base-url", "http://kubernetes-controller.default.svc.cluster.local:8080", "Base URL published into mounted workspace status.")
	flag.BoolVar(&enableLeaderElect, "leader-elect", false, "Enable leader election for controller manager.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	kcpConfig, err := clientcmd.BuildConfigFromFlags("", kcpKubeconfig)
	if err != nil {
		ctrl.Log.Error(err, "unable to load kcp kubeconfig")
		os.Exit(1)
	}
	k8sConfig := ctrl.GetConfigOrDie()
	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		ctrl.Log.Error(err, "unable to build Kubernetes client")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(k8sConfig, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		LeaderElection:          enableLeaderElect,
		LeaderElectionID:        "kubernetes-controller.dbaas.mongodb.com",
		LeaderElectionNamespace: "default",
	})
	if err != nil {
		ctrl.Log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	reconciler := &controller.KubernetesReconciler{
		Client:             mgr.GetClient(),
		K8sClient:          k8sClient,
		KCPConfig:          kcpConfig,
		ConsumersWorkspace: consumersWorkspace,
		ProxyBaseURL:       proxyBaseURL,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to create Kubernetes controller")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	proxyServer := &http.Server{
		Addr:    proxyAddr,
		Handler: (&controller.KubernetesMountProxy{K8sClient: k8sClient}).Handler(),
	}
	go func() {
		<-ctx.Done()
		_ = proxyServer.Shutdown(context.Background())
	}()
	go func() {
		ctrl.Log.Info("starting kubernetes mount proxy", "addr", proxyAddr)
		if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			ctrl.Log.Error(err, "mount proxy failed")
			stop()
		}
	}()

	ctrl.Log.Info("starting kubernetes controller")
	if err := mgr.Start(ctx); err != nil {
		ctrl.Log.Error(err, "problem running manager")
		os.Exit(1)
	}
}
