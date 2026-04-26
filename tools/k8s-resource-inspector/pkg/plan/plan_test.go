package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/analysis"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/output"
	"k8s.io/apimachinery/pkg/api/resource"
)

func mustParse(s string) resource.Quantity { return resource.MustParse(s) }

func actionableRow(app, container, recText string) output.PodRow {
	return output.PodRow{
		AppName:    app,
		Container:  container,
		RepoURL:    "https://github.com/owner/repo",
		ChartPath:  "apps/" + app + "/chart",
		CPUReq:     mustParse("100m"),
		CPULim:     mustParse("100m"),
		MemReq:     mustParse("128Mi"),
		MemLim:     mustParse("128Mi"),
		HasData:    true,
		Behavior:   analysis.BehaviorStatic,
		Confidence: 0.9,
		Recommendation: analysis.Recommendation{
			IsActionable: true,
			Text:         recText,
			Resources: &analysis.ResourceValues{
				CPURequest: "10m",
				CPULimit:   "10m",
				MemRequest: "16Mi",
				MemLimit:   "16Mi",
			},
		},
	}
}

func heldRow(app, container string) output.PodRow {
	return output.PodRow{
		AppName:   app,
		Container: container,
		Recommendation: analysis.Recommendation{
			Hold:       true,
			HoldReason: "SPIKY",
		},
	}
}

func TestBuild_filtersNonActionable(t *testing.T) {
	rows := []output.PodRow{
		heldRow("app-a", "app"),
		{AppName: "app-b", Container: "app", Recommendation: analysis.Recommendation{Text: "within tolerance"}},
		{AppName: "app-c", Container: "app", Recommendation: analysis.Recommendation{}},
	}
	plans := Build(rows, "7d")
	if len(plans) != 0 {
		t.Errorf("Build() returned %d plans, want 0", len(plans))
	}
}

func TestBuild_singleApp(t *testing.T) {
	rows := []output.PodRow{
		actionableRow("demo-app", "server", "CPU 100m -> 10m"),
	}
	plans := Build(rows, "7d")
	if len(plans) != 1 {
		t.Fatalf("Build() returned %d plans, want 1", len(plans))
	}
	p := plans[0]
	if p.App != "demo-app" {
		t.Errorf("App = %q, want %q", p.App, "demo-app")
	}
	if p.Window != "7d" {
		t.Errorf("Window = %q, want %q", p.Window, "7d")
	}
	if !p.Apply {
		t.Error("Apply should default to true")
	}
	if !strings.HasSuffix(p.ValuesFile, "values-resources.yaml") {
		t.Errorf("ValuesFile = %q, want suffix values-resources.yaml", p.ValuesFile)
	}
	if len(p.Containers) != 1 || p.Containers[0].Name != "server" {
		t.Errorf("Containers = %v, want [{server ...}]", p.Containers)
	}
}

func TestBuild_multipleContainersGroupedUnderApp(t *testing.T) {
	rows := []output.PodRow{
		actionableRow("demo-app", "server", "CPU 100m -> 10m"),
		actionableRow("demo-app", "sidecar", "MEM 128Mi -> 16Mi"),
	}
	plans := Build(rows, "7d")
	if len(plans) != 1 {
		t.Fatalf("Build() returned %d plans, want 1 (same app)", len(plans))
	}
	if len(plans[0].Containers) != 2 {
		t.Errorf("Containers count = %d, want 2", len(plans[0].Containers))
	}
}

func TestBuild_deduplicatesPodsForSameContainer(t *testing.T) {
	// Two pods of the same workload, same recommendation → should appear once.
	row := actionableRow("demo-app", "server", "CPU 100m -> 10m")
	rows := []output.PodRow{row, row}
	plans := Build(rows, "7d")
	if len(plans) != 1 {
		t.Fatalf("Build() returned %d plans, want 1", len(plans))
	}
	if len(plans[0].Containers) != 1 {
		t.Errorf("Containers count = %d, want 1 (deduped)", len(plans[0].Containers))
	}
}

func TestBuild_separateApps(t *testing.T) {
	rows := []output.PodRow{
		actionableRow("app-a", "server", "CPU 100m -> 10m"),
		actionableRow("app-b", "server", "CPU 100m -> 10m"),
	}
	plans := Build(rows, "7d")
	if len(plans) != 2 {
		t.Errorf("Build() returned %d plans, want 2", len(plans))
	}
}

func TestBuild_resourceValuesPopulated(t *testing.T) {
	rows := []output.PodRow{actionableRow("demo-app", "server", "CPU 100m -> 10m")}
	plans := Build(rows, "7d")
	c := plans[0].Containers[0]
	if c.CPURequest != "10m" {
		t.Errorf("CPURequest = %q, want 10m", c.CPURequest)
	}
	if c.MemRequest != "16Mi" {
		t.Errorf("MemRequest = %q, want 16Mi", c.MemRequest)
	}
	if c.CurrentCPURequest != "100m" {
		t.Errorf("CurrentCPURequest = %q, want 100m", c.CurrentCPURequest)
	}
}

func TestRead_missingAppField(t *testing.T) {
	dir := t.TempDir()
	content := `- app: ""
  repo: https://github.com/owner/repo
  values_file: apps/demo/chart/values-resources.yaml
  window: 7d
  apply: true
  containers:
    - name: server
      cpu_request: 10m
`
	if err := os.WriteFile(filepath.Join(dir, "kri-plan.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Read(dir)
	if err == nil {
		t.Error("Read() expected error for empty app field, got nil")
	}
}

func TestRead_missingContainers(t *testing.T) {
	dir := t.TempDir()
	content := `- app: demo-app
  repo: https://github.com/owner/repo
  values_file: apps/demo/chart/values-resources.yaml
  window: 7d
  apply: true
  containers: []
`
	if err := os.WriteFile(filepath.Join(dir, "kri-plan.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Read(dir)
	if err == nil {
		t.Error("Read() expected error for empty containers, got nil")
	}
}

func TestRead_validPlan(t *testing.T) {
	dir := t.TempDir()
	content := `- app: demo-app
  repo: https://github.com/owner/repo
  values_file: demo-app/chart/values-resources.yaml
  window: 7d
  apply: true
  containers:
    - name: server
      current_cpu_request: 100m
      cpu_request: 10m
      mem_request: 16Mi
`
	if err := os.WriteFile(filepath.Join(dir, "kri-plan.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	plans, err := Read(dir)
	if err != nil {
		t.Fatalf("Read() unexpected error: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("Read() returned %d plans, want 1", len(plans))
	}
	if plans[0].App != "demo-app" {
		t.Errorf("App = %q, want demo-app", plans[0].App)
	}
}

func TestPlanPath(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{"", "kri-plan.yaml"},
		{"/tmp/foo", filepath.Join("/tmp/foo", "kri-plan.yaml")},
	}
	for _, tt := range tests {
		got := planPath(tt.dir)
		if got != tt.want {
			t.Errorf("planPath(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}
