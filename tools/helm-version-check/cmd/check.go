package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"

	"github.com/LiveViewTech/platform-lab/pkg/version"
	"github.com/LiveViewTech/platform-lab/tools/helm-version-check/pkg/config"
	"github.com/LiveViewTech/platform-lab/tools/helm-version-check/pkg/helm"
	"github.com/LiveViewTech/platform-lab/tools/helm-version-check/pkg/provider"
)

// CheckResult is one row of comparison output.
type CheckResult struct {
	Release            string `json:"release"`
	Namespace          string `json:"namespace"`
	Chart              string `json:"chart"`
	Deployed           string `json:"deployed"`
	DeployedAppVersion string `json:"deployed_app_version"`
	ImageTag           string `json:"image_tag"`
	Latest             string `json:"latest"`
	LatestAppVersion   string `json:"latest_app_version"`
	Behind             int    `json:"behind"`
	Repo               string `json:"repo"`
	UpToDate           bool   `json:"up_to_date"`
	PinChartVersion    bool   `json:"pin_chart_version,omitempty"`
	PinAppVersion      bool   `json:"pin_app_version,omitempty"`
	PinImageTag        bool   `json:"pin_image_tag,omitempty"`
}

// hasUnpinnedDrift reports whether the release has any drift that is not covered
// by a pin. Only unpinned drift causes the row to be flagged.
func hasUnpinnedDrift(r CheckResult) bool {
	if r.Latest == "unknown" {
		return false
	}
	if !r.UpToDate && !r.PinChartVersion {
		return true
	}
	appDrift := r.DeployedAppVersion != r.LatestAppVersion &&
		r.DeployedAppVersion != "unknown" && r.LatestAppVersion != "unknown"
	if appDrift && !r.PinAppVersion {
		return true
	}
	return false
}

// hasPinnedDrift reports whether any drift exists that is covered by a pin.
func hasPinnedDrift(r CheckResult) bool {
	if r.Latest == "unknown" {
		return false
	}
	if !r.UpToDate && r.PinChartVersion {
		return true
	}
	appDrift := r.DeployedAppVersion != r.LatestAppVersion &&
		r.DeployedAppVersion != "unknown" && r.LatestAppVersion != "unknown"
	if appDrift && r.PinAppVersion {
		return true
	}
	if r.ImageTag != "unknown" && r.PinImageTag {
		return true
	}
	return false
}

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Compare deployed Helm releases against latest available versions",
	RunE:  runCheck,
}

func runCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	providers := provider.FromConfig(cfg)
	if len(providers) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no repos configured — run `hvc check --help` for config instructions")
		fmt.Fprintln(os.Stderr, "         create ~/.hvc/config.yaml with at least one repo entry")
	}

	releases, err := helm.List(kubeconfig, kubeCtx, namespace)
	if err != nil {
		return err
	}
	if len(releases) == 0 {
		fmt.Fprintln(os.Stderr, "no releases found")
		return nil
	}

	var results []CheckResult
	for _, r := range releases {
		latest, latestAppVersion, repo, all := provider.FindVersion(r.Chart, providers)

		behind := 0
		upToDate := false
		if latest != "" {
			behind = version.Behind(r.Version, all)
			upToDate = !version.IsNewer(r.Version, latest)
		}

		pin := cfg.FindPin(r.Name, r.Namespace)

		results = append(results, CheckResult{
			Release:            r.Name,
			Namespace:          r.Namespace,
			Chart:              r.Chart,
			Deployed:           r.Version,
			DeployedAppVersion: orUnknown(r.AppVersion),
			ImageTag:           orUnknown(r.ImageTag),
			Latest:             orUnknown(latest),
			LatestAppVersion:   orUnknown(latestAppVersion),
			Behind:             behind,
			Repo:               orUnknown(repo),
			UpToDate:           upToDate,
			PinChartVersion:    pin != nil && pin.ChartVersion,
			PinAppVersion:      pin != nil && pin.AppVersion,
			PinImageTag:        pin != nil && pin.ImageTag,
		})
	}

	switch outputFmt {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	default:
		printCheckTable(results)
		return nil
	}
}

func printCheckTable(results []CheckResult) {
	headers := []string{"RELEASE", "NAMESPACE", "CHART", "CHART_VER", "L_CHART_VER", "APP_VER", "L_APP_VER", "IMAGE_TAG", "BEHIND", "REPO"}
	rows := make([][]string, len(results))
	for i, r := range results {
		behind := fmt.Sprintf("%d", r.Behind)
		if r.Latest == "unknown" {
			behind = "?"
		}

		latestVer := r.Latest
		if r.PinChartVersion && !r.UpToDate && r.Latest != "unknown" {
			latestVer = "~" + latestVer
			behind = "~" + behind
		}

		latestAppVer := r.LatestAppVersion
		appDrift := r.DeployedAppVersion != r.LatestAppVersion &&
			r.DeployedAppVersion != "unknown" && r.LatestAppVersion != "unknown"
		if r.PinAppVersion && appDrift {
			latestAppVer = "~" + latestAppVer
		}

		imageTag := r.ImageTag
		if r.PinImageTag {
			imageTag = "~" + imageTag
		}

		rows[i] = []string{
			r.Release, r.Namespace, r.Chart, r.Deployed, latestVer,
			r.DeployedAppVersion, latestAppVer, imageTag, behind, r.Repo,
		}
	}

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var hparts []string
	for i, h := range headers {
		hparts = append(hparts, bold(fmt.Sprintf("%-*s", widths[i], h)))
	}
	fmt.Println(strings.Join(hparts, "  "))

	upToDateCount, outdatedCount := 0, 0
	for i, r := range results {
		row := rows[i]
		colorFn := rowColor(r)
		if r.Latest != "unknown" {
			if hasUnpinnedDrift(r) {
				outdatedCount++
			} else {
				upToDateCount++
			}
		}

		var parts []string
		for j, cell := range row {
			padded := fmt.Sprintf("%-*s", widths[j], cell)
			parts = append(parts, colorFn(padded))
		}
		fmt.Println(strings.Join(parts, "  "))
	}

	total := len(results)
	fmt.Printf("\n%d releases scanned", total)
	if outdatedCount > 0 {
		fmt.Printf("  •  %s  •  %s", ansiRed(fmt.Sprintf("%d outdated", outdatedCount)), ansiGreen(fmt.Sprintf("%d up to date", upToDateCount)))
	} else {
		fmt.Printf("  •  %s", ansiGreen("all up to date"))
	}
	fmt.Println()
}

// rowColor returns a color function based on unpinned drift.
// A row is only dimmed when it has drift but all of it is pinned.
// If any unpinned attribute is off, the row is flagged yellow or red.
func rowColor(r CheckResult) func(string) string {
	if r.Latest == "unknown" {
		return ansiDim
	}
	if hasUnpinnedDrift(r) {
		// Use red for major chart version bump, yellow otherwise.
		if !r.UpToDate && !r.PinChartVersion {
			dv, err1 := semver.NewVersion(r.Deployed)
			lv, err2 := semver.NewVersion(r.Latest)
			if err1 == nil && err2 == nil && lv.Major() > dv.Major() {
				return ansiRed
			}
		}
		return ansiYellow
	}
	if hasPinnedDrift(r) {
		return ansiDim
	}
	return ansiGreen
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// ANSI helpers (inline — hvc doesn't share psi's output package).
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDimStr = "\033[2m"
)

func bold(s string) string       { return ansiBold + s + ansiReset }
func ansiGreen(s string) string  { return "\033[32m" + s + ansiReset }
func ansiYellow(s string) string { return "\033[33m" + s + ansiReset }
func ansiRed(s string) string    { return "\033[31m" + s + ansiReset }
func ansiDim(s string) string    { return ansiDimStr + s + ansiReset }
