package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	networkingv1 "k8s.io/api/networking/v1"
	"github.com/fanux/envoy-ingress-controller/pkg/controller"
	"github.com/fanux/envoy-ingress-controller/pkg/xds"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var xdsAddr string

	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&xdsAddr, "xds-addr", ":18000", "The address the xDS server binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "envoy-ingress-controller-leader",
		LeaderElectionConfig:   nil,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create and start xDS server
	xdsServer := xds.NewServer(context.Background())
	go func() {
		if err := xdsServer.Run(xdsAddr); err != nil {
			setupLog.Error(err, "problem running xDS server")
			os.Exit(1)
		}
	}()

	// Create and setup ingress controller
	if err = (&controller.IngressReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("Ingress"),
		Scheme: mgr.GetScheme(),
		XDSServer: xdsServer,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Ingress")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
