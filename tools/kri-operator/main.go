package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/config"
	"github.com/LiveViewTech/platform-lab/tools/kri-operator/controller"
)

func main() {
	var configPath string
	var probeAddr string
	flag.StringVar(&configPath, "config", "/etc/kri/config.yaml", "Path to kri config file")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address for health probes")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	restConfig := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		LeaderElection:         true,
		LeaderElectionID:       "kri-operator.kri.io",
		HealthProbeBindAddress: probeAddr,
		// Metrics disabled in Phase 1.
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create manager: %v\n", err)
		os.Exit(1)
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create dynamic client: %v\n", err)
		os.Exit(1)
	}

	token := os.Getenv("GITHUB_TOKEN")

	runner := controller.NewRightsizingRunner(cfg, dynClient, token)
	if err := mgr.Add(runner); err != nil {
		fmt.Fprintf(os.Stderr, "add runner: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fmt.Fprintf(os.Stderr, "add healthz: %v\n", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		fmt.Fprintf(os.Stderr, "add readyz: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "manager exited: %v\n", err)
		os.Exit(1)
	}
}
