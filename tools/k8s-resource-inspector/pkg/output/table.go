package output

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/analysis"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/hpa"
	"k8s.io/apimachinery/pkg/api/resource"
)

// PodRow is one row in the output table.
type PodRow struct {
	AppName   string
	Cluster   string
	Namespace string
	PodName      string
	WorkloadName string
	Container    string

	// Inventory (from kube-state-metrics)
	CPUReq resource.Quantity
	CPULim resource.Quantity
	MemReq resource.Quantity
	MemLim resource.Quantity

	// Usage percentiles (from Prometheus)
	CPUP50   float64
	CPUP95   float64
	CPUP99   float64
	MemP50   float64
	MemP95   float64
	MemP99   float64
	MemTrend float64 // bytes/hour
	HasData  bool

	// Classification (Phase 3)
	Behavior   analysis.BehaviorClass
	Confidence float64

	// HPA validation (Phase 4)
	HPAStatus hpa.Validation

	// Recommendation (Phase 5)
	Recommendation analysis.Recommendation

	// HPA recommendation — non-nil when routing selects an HPA change over a resource change.
	HPARecommendation *analysis.HPARecommendation

	// HPAWarningDisabled suppresses the no-HPA warning for this app.
	HPAWarningDisabled bool
	// HPAWarning is set when a resource increase PR is opened but no HPA is configured.
	HPAWarning string

	// Values file path from git (Phase 6)
	ValuesFilePath string

	// Source repo info (Phase 8 / plan)
	RepoURL   string
	ChartPath string
}

func PrintTable(rows []PodRow) error {
	if len(rows) == 0 {
		fmt.Println("No pods found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tCLUSTER\tNAMESPACE\tPOD\tCONTAINER\tCPU_REQ\tCPU_P95\tCPU_P99\tMEM_REQ\tMEM_P95\tMEM_P99\tMEM/LIM\tBEHAVIOR\tCONF\tHPA\tREC")

	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.AppName,
			r.Cluster,
			r.Namespace,
			r.PodName,
			r.Container,
			qOrDash(r.CPUReq),
			coresOrDash(r.CPUP95, r.HasData),
			coresOrDash(r.CPUP99, r.HasData),
			qOrDash(r.MemReq),
			bytesOrDash(r.MemP95, r.HasData),
			bytesOrDash(r.MemP99, r.HasData),
			memLimRatio(r.MemP99, r.MemLim),
			behaviorOrDash(r.Behavior),
			confOrDash(r.Confidence),
			hpaFlag(r.HPAStatus),
			recFlag(r.Recommendation),
		)
	}

	if err := w.Flush(); err != nil {
		return err
	}

	printFindings(rows)
	return nil
}

// printFindings prints a findings block below the table, grouped by app+container
// so that multi-replica deployments don't repeat the same finding for every pod.
func printFindings(rows []PodRow) {
	type finding struct {
		label string
		lines []string
	}
	type section struct {
		header   string
		findings []finding
	}

	seen := map[string]bool{}
	var sections []section

	for _, r := range rows {
		key := r.AppName + "/" + r.WorkloadName + "/" + r.Container
		if seen[key] {
			continue
		}

		var ff []finding

		if r.HPAStatus.Status == "WARN" || r.HPAStatus.Status == "ERROR" {
			var lines []string
			for _, f := range r.HPAStatus.Findings {
				lines = append(lines, fmt.Sprintf("[%s] %s", f.Severity, f.Message))
			}
			ff = append(ff, finding{"HPA", lines})
		}

		if r.Recommendation.IsActionable {
			text := r.Recommendation.Text
			if r.ValuesFilePath != "" {
				text += fmt.Sprintf("  [%s]", r.ValuesFilePath)
			}
			ff = append(ff, finding{"REC", []string{text}})
		} else if r.Recommendation.Hold && r.Recommendation.HoldReason != "" {
			ff = append(ff, finding{"REC", []string{"hold: " + r.Recommendation.HoldReason}})
		}

		if r.HPARecommendation != nil {
			label := "add HPA"
			switch r.HPARecommendation.Reason {
			case "WontFire":
				label = "fix HPA"
			case "Tuning":
				label = "tune HPA"
			}
			ff = append(ff, finding{label, []string{r.HPARecommendation.Text}})
		}

		if len(ff) > 0 {
			seen[key] = true
			sections = append(sections, section{
				header:   fmt.Sprintf("  %-30s %-20s %-30s %-20s", r.AppName, r.Namespace, r.WorkloadName, r.Container),
				findings: ff,
			})
		}
	}

	if len(sections) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("── Findings ──────────────────────────────────────────────────────")
	fmt.Printf("  %-30s %-20s %-30s %-20s\n", "ARGO_APP", "NAMESPACE", "WORKLOAD", "CONTAINER")
	for _, s := range sections {
		fmt.Printf("\n%s\n", s.header)
		for _, f := range s.findings {
			for _, line := range f.lines {
				fmt.Printf("    %-4s  %s\n", f.label, line)
			}
		}
	}
	fmt.Println()
}

func qOrDash(q resource.Quantity) string {
	if q.IsZero() {
		return "-"
	}
	return q.String()
}

func coresOrDash(cores float64, hasData bool) string {
	if !hasData {
		return "-"
	}
	milli := int64(cores * 1000)
	return resource.NewMilliQuantity(milli, resource.DecimalSI).String()
}

func bytesOrDash(bytes float64, hasData bool) string {
	if !hasData {
		return "-"
	}
	return fmt.Sprintf("%.1fMi", bytes/1048576)
}

func memLimRatio(memP99 float64, memLim resource.Quantity) string {
	if memP99 == 0 || memLim.IsZero() {
		return "-"
	}
	limBytes := float64(memLim.Value())
	if limBytes == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", (memP99/limBytes)*100)
}

func behaviorOrDash(b analysis.BehaviorClass) string {
	if b == "" {
		return "-"
	}
	return string(b)
}

func confOrDash(c float64) string {
	if c == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", c*100)
}

// hpaFlag returns a compact status indicator for the HPA column.
func hpaFlag(v hpa.Validation) string {
	switch v.Status {
	case "NONE", "":
		return "-"
	default:
		return v.Status
	}
}

// recFlag returns a compact indicator for the REC column.
func recFlag(r analysis.Recommendation) string {
	if r.Hold {
		return "hold"
	}
	if r.Text == "" {
		return "-"
	}
	if !r.IsActionable {
		return "ok"
	}
	return "YES"
}
