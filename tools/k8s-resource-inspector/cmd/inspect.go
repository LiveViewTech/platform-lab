package cmd

import (
	"context"
	"fmt"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/analysis"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/argo"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/config"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/output"
	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var window string
var confidenceThreshold float64
var findingsOnly bool

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Inspect workload resource utilization",
	RunE:  runInspect,
}

func init() {
	inspectCmd.Flags().StringVar(&window, "window", "7d", "Observation window for Prometheus queries (e.g. 7d, 24h, 1h)")
	inspectCmd.Flags().Float64Var(&confidenceThreshold, "confidence", 0.8, "Minimum confidence threshold for recommendations (0.0–1.0)")
	inspectCmd.Flags().BoolVar(&findingsOnly, "findings-only", false, "Only show workloads with recommendations or HPA warnings")
	rootCmd.AddCommand(inspectCmd)
}

func runInspect(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	dynClient, err := buildDynamicClient(kubeconfig, kubeCtx)
	if err != nil {
		return fmt.Errorf("build kubernetes client: %w", err)
	}

	apps, err := listApps(ctx, dynClient, cfg.ArgoNS())
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return nil
	}

	rows, err := buildRows(ctx, cfg, dynClient, apps, window, confidenceThreshold)
	if err != nil {
		return err
	}

	sortRows(rows)

	if findingsOnly {
		filtered := rows[:0]
		for _, r := range rows {
			if hasFinding(r) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
		if len(rows) == 0 {
			fmt.Println("No findings.")
			return nil
		}
	}

	if outputFmt == "json" {
		return output.PrintJSON(rows)
	}
	return output.PrintTable(rows)
}

// hasFinding returns true when a row warrants attention.
func hasFinding(r output.PodRow) bool {
	if r.HPAStatus.Status == "WARN" || r.HPAStatus.Status == "ERROR" {
		return true
	}
	if r.Recommendation.IsActionable {
		return true
	}
	if r.Behavior == analysis.BehaviorRunaway || r.Behavior == analysis.BehaviorSpiky {
		return true
	}
	return false
}

// listApps fetches and filters ArgoCD applications.
func listApps(ctx context.Context, dynClient dynamic.Interface, argoNS string) ([]argo.App, error) {
	apps, err := argo.List(ctx, dynClient, argoNS)
	if err != nil {
		return nil, fmt.Errorf("list ArgoCD applications: %w", err)
	}

	if len(apps) == 0 {
		fmt.Println("No ArgoCD applications found.")
		return nil, nil
	}

	if appFilter != "" {
		filtered := apps[:0]
		for _, a := range apps {
			if a.Name == appFilter {
				filtered = append(filtered, a)
			}
		}
		apps = filtered
	}

	if namespace != "" {
		filtered := apps[:0]
		for _, a := range apps {
			if a.Namespace == namespace {
				filtered = append(filtered, a)
			}
		}
		apps = filtered
	}

	if len(apps) == 0 {
		fmt.Println("No applications match the specified filters.")
		return nil, nil
	}

	return apps, nil
}

func buildDynamicClient(kubeconfigPath, kubeCtx string) (dynamic.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if kubeCtx != "" {
		configOverrides.CurrentContext = kubeCtx
	}

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, configOverrides,
	).ClientConfig()
	if err != nil {
		return nil, err
	}

	return dynamic.NewForConfig(restConfig)
}
