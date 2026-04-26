package cmd

import (
	"context"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/argo"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/config"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/inspect"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/output"
	"k8s.io/client-go/dynamic"
)

// buildRows delegates to inspect.BuildRows so CLI commands share the same
// pipeline as the operator without importing the cmd package.
func buildRows(ctx context.Context, cfg *config.Config, dynClient dynamic.Interface, apps []argo.App, window string, confidenceThreshold float64) ([]output.PodRow, error) {
	return inspect.BuildRows(ctx, cfg, dynClient, apps, window, confidenceThreshold)
}
