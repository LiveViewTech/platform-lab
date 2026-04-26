package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/LiveViewTech/platform-lab/pkg/client"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/netpol"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/output"
)

var netpolCmd = &cobra.Command{
	Use:   "netpol",
	Short: "Check NetworkPolicy ingress/egress coverage for pods",
	RunE:  runNetpol,
}

func runNetpol(cmd *cobra.Command, args []string) error {
	cs, err := client.New(kubeconfig, kubeCtx)
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}

	findings, err := netpol.Inspect(cmd.Context(), cs, namespace)
	if err != nil {
		return err
	}

	if len(findings) == 0 {
		fmt.Fprintln(os.Stderr, "no pods found")
		return nil
	}

	switch outputFmt {
	case output.FormatJSON:
		return output.NetpolJSON(os.Stdout, findings)
	default:
		output.NetpolTable(os.Stdout, findings, findingsOnly)
		return nil
	}
}
