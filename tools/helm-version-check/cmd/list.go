package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/LiveViewTech/platform-lab/tools/helm-version-check/pkg/helm"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all Helm releases in the cluster",
	RunE:  runList,
}

func runList(cmd *cobra.Command, args []string) error {
	releases, err := helm.List(kubeconfig, kubeCtx, namespace)
	if err != nil {
		return err
	}

	if len(releases) == 0 {
		fmt.Fprintln(os.Stderr, "no releases found")
		return nil
	}

	switch outputFmt {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(releases)
	default:
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "RELEASE\tNAMESPACE\tCHART\tVERSION\tAPP_VERSION\tSTATUS")
		for _, r := range releases {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Name, r.Namespace, r.Chart, r.Version, r.AppVersion, strings.ToUpper(r.Status))
		}
		return tw.Flush()
	}
}
