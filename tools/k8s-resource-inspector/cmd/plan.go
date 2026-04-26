package cmd

import (
	"context"
	"fmt"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/config"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/plan"
	"github.com/spf13/cobra"
)

var planDir string

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Generate kri-plan.yaml with recommended resource changes",
	Long: `plan runs the inspect pipeline and writes kri-plan.yaml containing
recommended resource changes for each app. Edit the file to adjust values or
set apply: false to skip an app, then run: kri apply`,
	RunE: runPlan,
}

func init() {
	planCmd.Flags().StringVar(&window, "window", "7d", "Observation window for Prometheus queries (e.g. 7d, 24h, 1h)")
	planCmd.Flags().Float64Var(&confidenceThreshold, "confidence", 0.8, "Minimum confidence threshold for recommendations (0.0–1.0)")
	planCmd.Flags().StringVar(&planDir, "dir", "", "Directory to write kri-plan.yaml (default: current directory)")
	rootCmd.AddCommand(planCmd)
}

func runPlan(cmd *cobra.Command, args []string) error {
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

	plans := plan.Build(rows, window)
	return plan.Write(plans, planDir)
}
