package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/LiveViewTech/platform-lab/pkg/client"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/output"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/security"
)

var securityCmd = &cobra.Command{
	Use:   "security",
	Short: "Check pod and container security contexts for misconfigurations",
	RunE:  runSecurity,
}

func runSecurity(cmd *cobra.Command, args []string) error {
	cs, err := client.New(kubeconfig, kubeCtx)
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}

	findings, err := security.Inspect(cmd.Context(), cs, namespace)
	if err != nil {
		return err
	}

	if len(findings) == 0 {
		fmt.Fprintln(os.Stderr, "no pods found")
		return nil
	}

	switch outputFmt {
	case output.FormatJSON:
		return output.SecurityJSON(os.Stdout, findings)
	default:
		output.SecurityTable(os.Stdout, findings, findingsOnly)
		return nil
	}
}
