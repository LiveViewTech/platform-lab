package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/LiveViewTech/platform-lab/pkg/client"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/mesh"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/netpol"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/output"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/security"
)

var allCmd = &cobra.Command{
	Use:   "all",
	Short: "Run all checks (security, mesh, netpol) and display combined per-pod output",
	RunE:  runAll,
}

func runAll(cmd *cobra.Command, args []string) error {
	cs, err := client.New(kubeconfig, kubeCtx)
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}

	ctx := cmd.Context()

	secFindings, err := security.Inspect(ctx, cs, namespace)
	if err != nil {
		return fmt.Errorf("security: %w", err)
	}

	meshFindings, err := mesh.Inspect(ctx, cs, namespace)
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}

	netpolFindings, err := netpol.Inspect(ctx, cs, namespace)
	if err != nil {
		return fmt.Errorf("netpol: %w", err)
	}

	if len(secFindings) == 0 {
		fmt.Fprintln(os.Stderr, "no pods found")
		return nil
	}

	r := output.AllResults{
		Security: secFindings,
		Mesh:     meshFindings,
		Netpol:   netpolFindings,
	}

	switch outputFmt {
	case output.FormatJSON:
		return output.AllJSON(os.Stdout, r)
	default:
		output.AllStacked(os.Stdout, r, findingsOnly)
		return nil
	}
}
