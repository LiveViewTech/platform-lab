package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/mesh"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/netpol"
	"github.com/LiveViewTech/platform-lab/tools/pod-security-inspector/pkg/security"
)

const (
	FormatTable = "table"
	FormatJSON  = "json"
)

// SecurityTable renders security findings with a single ISSUES column.
func SecurityTable(w io.Writer, findings []security.Finding, findingsOnly bool) {
	headers := []string{"NAMESPACE", "POD", "CONTAINER", "ISSUES"}
	var rows [][]cell
	var total, withIssues int

	for _, f := range findings {
		total++
		issues := f.Issues()
		if findingsOnly && len(issues) == 0 {
			continue
		}
		if len(issues) > 0 {
			withIssues++
		}

		issueText, issueColor := issueCell(issues)
		rows = append(rows, []cell{
			plain(f.Namespace),
			plain(f.Pod),
			plain(f.ContainerDisplay()),
			colored(issueText, issueColor),
		})
	}

	renderTable(w, headers, rows)
	printSummary(w, total, withIssues, "security")
}

// SecurityJSON writes security findings as JSON.
func SecurityJSON(w io.Writer, findings []security.Finding) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(findings)
}

// MeshTable renders mesh findings with colored MODE column.
func MeshTable(w io.Writer, findings []mesh.Finding, findingsOnly bool) {
	headers := []string{"NAMESPACE", "POD", "MESH_MODE", "NS_INJECT", "OPTED_OUT", "SIDECAR_READY", "PROXY_VERSION", "AMBIENT_NS", "ZTUNNEL_NODE", "WAYPOINT"}
	var rows [][]cell
	total := len(findings)
	withIssues := 0

	for _, f := range findings {
		unmeshed := f.Mode == mesh.ModeNone
		if unmeshed {
			withIssues++
		}
		if findingsOnly && !unmeshed {
			continue
		}

		modeColor := green
		if f.Mode == mesh.ModeNone {
			modeColor = red
		}

		rows = append(rows, []cell{
			plain(f.Namespace),
			plain(f.Pod),
			colored(string(f.Mode), modeColor),
			plain(boolStr(f.NSInject)),
			plain(boolStr(f.PodOptedOut)),
			plain(boolStr(f.SidecarReady)),
			plain(f.ProxyVersion),
			plain(boolStr(f.AmbientNS)),
			plain(boolStr(f.ZtunnelNode)),
			plain(boolStr(f.WaypointAttached)),
		})
	}

	renderTable(w, headers, rows)
	printSummary(w, total, withIssues, "mesh")
}

// MeshJSON writes mesh findings as JSON.
func MeshJSON(w io.Writer, findings []mesh.Finding) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(findings)
}

// NetpolTable renders network policy findings with colored coverage columns.
func NetpolTable(w io.Writer, findings []netpol.Finding, findingsOnly bool) {
	headers := []string{"NAMESPACE", "POD", "INGRESS_POLICY", "EGRESS_POLICY", "POLICY_NAMES"}
	var rows [][]cell
	total := len(findings)
	withIssues := 0

	for _, f := range findings {
		hasIssue := !f.IngressCovered || !f.EgressCovered
		if hasIssue {
			withIssues++
		}
		if findingsOnly && !hasIssue {
			continue
		}

		rows = append(rows, []cell{
			plain(f.Namespace),
			plain(f.Pod),
			coloredBool(f.IngressCovered),
			coloredBool(f.EgressCovered),
			plain(f.PolicyNamesDisplay()),
		})
	}

	renderTable(w, headers, rows)
	printSummary(w, total, withIssues, "netpol")
}

// NetpolJSON writes network policy findings as JSON.
func NetpolJSON(w io.Writer, findings []netpol.Finding) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(findings)
}

// AllResults bundles findings from all three checks for the all command.
type AllResults struct {
	Security []security.Finding
	Mesh     []mesh.Finding
	Netpol   []netpol.Finding
}

// AllJSON writes combined results as JSON.
func AllJSON(w io.Writer, r AllResults) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// AllStacked writes per-namespace, per-pod stacked sections.
func AllStacked(w io.Writer, r AllResults, findingsOnly bool) {
	meshIdx := make(map[string]mesh.Finding)
	for _, f := range r.Mesh {
		meshIdx[f.Namespace+"/"+f.Pod] = f
	}
	netpolIdx := make(map[string]netpol.Finding)
	for _, f := range r.Netpol {
		netpolIdx[f.Namespace+"/"+f.Pod] = f
	}

	type podKey struct{ ns, pod string }
	var order []podKey
	seen := make(map[podKey]bool)
	secIdx := make(map[podKey][]security.Finding)
	for _, f := range r.Security {
		k := podKey{f.Namespace, f.Pod}
		if !seen[k] {
			order = append(order, k)
			seen[k] = true
		}
		secIdx[k] = append(secIdx[k], f)
	}

	totalPods := len(order)
	podsWithIssues := 0

	currentNS := ""
	for _, k := range order {
		podIssues := podHasIssues(secIdx[k], meshIdx[k.ns+"/"+k.pod], netpolIdx[k.ns+"/"+k.pod])
		if podIssues {
			podsWithIssues++
		}
		if findingsOnly && !podIssues {
			continue
		}

		if k.ns != currentNS {
			if currentNS != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "%s\n", bold("NAMESPACE: "+k.ns))
			currentNS = k.ns
		}

		fmt.Fprintf(w, "  %s\n", bold("POD: "+k.pod))

		// Security section.
		fmt.Fprintln(w, dim("    SECURITY"))
		secHeaders := []string{"CONTAINER", "ISSUES"}
		var secRows [][]cell
		for _, f := range secIdx[k] {
			issues := f.Issues()
			issueText, issueColor := issueCell(issues)
			secRows = append(secRows, []cell{
				plain(f.ContainerDisplay()),
				colored(issueText, issueColor),
			})
		}
		renderIndentedTable(w, "    ", secHeaders, secRows)

		// Mesh section.
		fmt.Fprintln(w, dim("    MESH"))
		mf := meshIdx[k.ns+"/"+k.pod]
		modeColor := green
		if mf.Mode == mesh.ModeNone {
			modeColor = red
		}
		meshHeaders := []string{"MODE", "NS_INJECT", "SIDECAR_READY", "PROXY_VERSION", "AMBIENT_NS", "ZTUNNEL_NODE", "WAYPOINT"}
		meshRows := [][]cell{{
			colored(string(mf.Mode), modeColor),
			plain(boolStr(mf.NSInject)),
			plain(boolStr(mf.SidecarReady)),
			plain(mf.ProxyVersion),
			plain(boolStr(mf.AmbientNS)),
			plain(boolStr(mf.ZtunnelNode)),
			plain(boolStr(mf.WaypointAttached)),
		}}
		renderIndentedTable(w, "    ", meshHeaders, meshRows)

		// Netpol section.
		fmt.Fprintln(w, dim("    NETPOL"))
		nf := netpolIdx[k.ns+"/"+k.pod]
		netpolHeaders := []string{"INGRESS_POLICY", "EGRESS_POLICY", "POLICY_NAMES"}
		netpolRows := [][]cell{{
			coloredBool(nf.IngressCovered),
			coloredBool(nf.EgressCovered),
			plain(nf.PolicyNamesDisplay()),
		}}
		renderIndentedTable(w, "    ", netpolHeaders, netpolRows)
	}

	fmt.Fprintln(w)
	printSummary(w, totalPods, podsWithIssues, "all")
}

// podHasIssues returns true if any check has a finding for this pod.
func podHasIssues(sec []security.Finding, mf mesh.Finding, nf netpol.Finding) bool {
	for _, f := range sec {
		if len(f.Issues()) > 0 {
			return true
		}
	}
	if mf.Mode == mesh.ModeNone {
		return true
	}
	if !nf.IngressCovered || !nf.EgressCovered {
		return true
	}
	return false
}

// issueCell returns display text and color function for an issues list.
func issueCell(issues []string) (string, func(string) string) {
	if len(issues) == 0 {
		return "none", green
	}
	return strings.Join(issues, ", "), red
}

// coloredBool returns a colored YES/NO cell.
func coloredBool(b bool) cell {
	if b {
		return colored("YES", green)
	}
	return colored("NO", red)
}

func boolStr(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

func printSummary(w io.Writer, total, withIssues int, check string) {
	clean := total - withIssues
	unit := "pods"
	if check == "security" {
		unit = "containers"
	}
	msg := fmt.Sprintf("\n%d %s scanned", total, unit)
	if withIssues > 0 {
		msg += fmt.Sprintf("  •  %s  •  %s",
			red(fmt.Sprintf("%d with findings", withIssues)),
			green(fmt.Sprintf("%d clean", clean)),
		)
	} else {
		msg += fmt.Sprintf("  •  %s", green("all clean"))
	}
	fmt.Fprintln(w, msg)
}
