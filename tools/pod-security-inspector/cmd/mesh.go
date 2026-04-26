package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/LiveViewTech/platform-lab/pkg/client"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/mesh"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/output"
)

var meshCmd = &cobra.Command{
	Use:   "mesh",
	Short: "Check Istio sidecar and ambient mesh status for pods",
	RunE:  runMesh,
}

func runMesh(cmd *cobra.Command, args []string) error {
	cs, err := client.New(kubeconfig, kubeCtx)
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}

	findings, err := mesh.Inspect(cmd.Context(), cs, namespace)
	if err != nil {
		return err
	}

	if len(findings) == 0 {
		fmt.Fprintln(os.Stderr, "no pods found")
		return nil
	}

	switch outputFmt {
	case output.FormatJSON:
		return output.MeshJSON(os.Stdout, findings)
	default:
		output.MeshTable(os.Stdout, findings, findingsOnly)
		return nil
	}
}
