package hpa

import (
	"strings"
	"testing"

	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/analysis"
	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/metrics"
	"k8s.io/apimachinery/pkg/api/resource"
)

const Mi = 1048576 // bytes in a MiB

func int32p(v int32) *int32 { return &v }

func mustParse(s string) resource.Quantity { return resource.MustParse(s) }

// ---------- Validate ----------

func TestValidate(t *testing.T) {
	tests := []struct {
		name         string
		hpa          *Info
		usage        metrics.Usage
		cpuReq       resource.Quantity
		memReq       resource.Quantity
		behavior     analysis.BehaviorClass
		wantStatus   string
		wantContains []string // substrings that must appear in at least one finding
		wantAbsent   []string // substrings that must NOT appear in any finding
	}{
		{
			name:       "nil HPA",
			hpa:        nil,
			wantStatus: "NONE",
		},
		{
			name: "dual-metric HPA, healthy usage → OK",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:    metrics.Usage{HasData: true, CPUP50: 0.035, CPUP95: 0.05, MemP50: 60 * Mi, MemP95: 70 * Mi},
			cpuReq:   mustParse("100m"),
			memReq:   mustParse("128Mi"),
			behavior: analysis.BehaviorStatic,
			wantStatus: "OK",
		},
		{
			name: "CPU-only HPA, missing CPU request → ERROR",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			cpuReq:       resource.Quantity{},
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorStatic,
			wantStatus:   "ERROR",
			wantContains: []string{"no CPU request"},
		},
		{
			name: "memory-only HPA, missing memory request → ERROR",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				MemTarget: int32p(80),
			},
			cpuReq:       mustParse("100m"),
			memReq:       resource.Quantity{},
			behavior:     analysis.BehaviorStatic,
			wantStatus:   "ERROR",
			wantContains: []string{"no memory request"},
		},
		// --- CPU utilization checks ---
		{
			name: "CPU p95 exceeds target → perpetual scaling",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(50),
			},
			usage:        metrics.Usage{HasData: true, CPUP95: 0.08},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorStatic,
			wantStatus:   "WARN",
			wantContains: []string{"CPU p95 utilization", "perpetually scaling"},
		},
		{
			name: "CPU p50 far below target → never trigger",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(80),
			},
			usage:        metrics.Usage{HasData: true, CPUP50: 0.005, CPUP95: 0.01},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorStatic,
			wantStatus:   "WARN",
			wantContains: []string{"CPU HPA target", "may never trigger"},
		},
		// --- Memory utilization checks ---
		{
			name: "Memory p95 exceeds target → perpetual scaling",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				MemTarget: int32p(50),
			},
			usage:        metrics.Usage{HasData: true, MemP95: 100 * Mi},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorStatic,
			wantStatus:   "WARN",
			wantContains: []string{"Memory p95 utilization", "perpetually scaling"},
		},
		{
			name: "Memory p50 far below target → never trigger",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				MemTarget: int32p(80),
			},
			usage:        metrics.Usage{HasData: true, MemP50: 5 * Mi, MemP95: 10 * Mi},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorStatic,
			wantStatus:   "WARN",
			wantContains: []string{"Memory HPA target", "may never trigger"},
		},
		// --- Dual-metric with both utilization checks firing ---
		{
			name: "dual-metric HPA, both p95 exceed targets",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(30), MemTarget: int32p(30),
			},
			usage:        metrics.Usage{HasData: true, CPUP95: 0.05, MemP95: 80 * Mi},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorStatic,
			wantStatus:   "WARN",
			wantContains: []string{"CPU p95 utilization", "Memory p95 utilization"},
		},
		// --- Metric mismatch warnings ---
		{
			name: "CPU-only HPA on GROWTH workload → mismatch warning",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			usage:        metrics.Usage{HasData: true},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorGrowth,
			wantStatus:   "WARN",
			wantContains: []string{"CPU-only HPA on a memory-trending workload"},
		},
		{
			name: "CPU-only HPA on RUNAWAY workload → mismatch warning",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			usage:        metrics.Usage{HasData: true},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorRunaway,
			wantStatus:   "WARN",
			wantContains: []string{"CPU-only HPA on a memory-trending workload"},
		},
		{
			name: "memory-only HPA on SPIKY workload → reverse mismatch",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				MemTarget: int32p(70),
			},
			usage:        metrics.Usage{HasData: true},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorSpiky,
			wantStatus:   "WARN",
			wantContains: []string{"Memory-only HPA on a CPU-spiky workload"},
		},
		{
			name: "dual-metric HPA on GROWTH workload → no mismatch",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:      metrics.Usage{HasData: true, CPUP50: 0.035, CPUP95: 0.05, MemP50: 60 * Mi, MemP95: 70 * Mi},
			cpuReq:     mustParse("100m"),
			memReq:     mustParse("128Mi"),
			behavior:   analysis.BehaviorGrowth,
			wantStatus: "OK",
			wantAbsent: []string{"memory-trending", "CPU-spiky"},
		},
		{
			name: "dual-metric HPA on SPIKY workload → no mismatch",
			hpa: &Info{
				MinReplicas: 2, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:      metrics.Usage{HasData: true, CPUP50: 0.035, CPUP95: 0.05, MemP50: 60 * Mi, MemP95: 70 * Mi},
			cpuReq:     mustParse("100m"),
			memReq:     mustParse("128Mi"),
			behavior:   analysis.BehaviorSpiky,
			wantStatus: "OK",
			wantAbsent: []string{"CPU-spiky"},
		},
		// --- minReplicas check ---
		{
			name: "minReplicas=1 on SPIKY workload",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:        metrics.Usage{HasData: true, CPUP50: 0.035, CPUP95: 0.05, MemP50: 60 * Mi, MemP95: 70 * Mi},
			cpuReq:       mustParse("100m"),
			memReq:       mustParse("128Mi"),
			behavior:     analysis.BehaviorSpiky,
			wantStatus:   "WARN",
			wantContains: []string{"minReplicas=1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Validate(tt.hpa, tt.usage, tt.cpuReq, tt.memReq, tt.behavior)
			if v.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q; findings: %v", v.Status, tt.wantStatus, v.Findings)
			}
			allMessages := findingsText(v.Findings)
			for _, s := range tt.wantContains {
				if !strings.Contains(allMessages, s) {
					t.Errorf("expected finding containing %q, got %q", s, allMessages)
				}
			}
			for _, s := range tt.wantAbsent {
				if strings.Contains(allMessages, s) {
					t.Errorf("did not expect finding containing %q, got %q", s, allMessages)
				}
			}
		})
	}
}

// ---------- WontFire ----------

func TestWontFire(t *testing.T) {
	tests := []struct {
		name   string
		hpa    *Info
		usage  metrics.Usage
		cpuReq resource.Quantity
		memReq resource.Quantity
		driver MetricDriver
		want   bool
	}{
		{
			name: "nil HPA",
			hpa:  nil,
			want: false,
		},
		{
			name: "no usage data",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			usage: metrics.Usage{HasData: false},
			want:  false,
		},
		{
			name: "structural: max <= min",
			hpa: &Info{
				MinReplicas: 3, MaxReplicas: 3,
				CPUTarget: int32p(70),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   true,
		},
		{
			name: "structural: no metrics configured",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
			},
			usage:  metrics.Usage{HasData: true},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   true,
		},
		{
			name: "structural: CPU target but no CPU request",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09},
			cpuReq: resource.Quantity{},
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   true,
		},
		{
			name: "structural: memory target but no memory request",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, MemP99: 100 * Mi},
			cpuReq: mustParse("100m"),
			memReq: resource.Quantity{},
			driver: DriverMemory,
			want:   true,
		},
		// --- Wrong metric (single-metric HPAs only) ---
		{
			name: "CPU-only HPA but memory-driven workload",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverMemory,
			want:   true,
		},
		{
			name: "memory-only HPA but CPU-driven workload",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, MemP99: 100 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   true,
		},
		{
			name: "dual-metric HPA, memory driver → wrong-metric check skipped",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09, MemP99: 110 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverMemory,
			want:   false, // both metrics exceed targets
		},
		{
			name: "dual-metric HPA, CPU driver → wrong-metric check skipped",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09, MemP99: 110 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   false,
		},
		// --- Behavioural: all below target ---
		{
			name: "single-metric CPU: p99 below target → won't fire",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.05},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   true,
		},
		{
			name: "single-metric CPU: p99 exceeds target → will fire",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   false,
		},
		{
			name: "single-metric memory: p99 below target → won't fire",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, MemP99: 50 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverMemory,
			want:   true,
		},
		// --- Dual-metric behavioural checks ---
		{
			name: "dual-metric: both below targets → won't fire",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.05, MemP99: 50 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   true,
		},
		{
			name: "dual-metric: CPU exceeds, memory below → will fire (OR semantics)",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09, MemP99: 50 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   false,
		},
		{
			name: "dual-metric: memory exceeds, CPU below → will fire (OR semantics)",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.05, MemP99: 110 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   false,
		},
		{
			name: "dual-metric: both exceed → will fire",
			hpa: &Info{
				MinReplicas: 1, MaxReplicas: 5,
				CPUTarget: int32p(70), MemTarget: int32p(80),
			},
			usage:  metrics.Usage{HasData: true, CPUP99: 0.09, MemP99: 110 * Mi},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
			driver: DriverCPU,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WontFire(tt.hpa, tt.usage, tt.cpuReq, tt.memReq, tt.driver)
			if got != tt.want {
				t.Errorf("WontFire() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------- RecommendMetricDriver ----------

func TestRecommendMetricDriver(t *testing.T) {
	tests := []struct {
		name     string
		cpuRatio float64
		memRatio float64
		want     MetricDriver
	}{
		{"CPU above spike threshold", 2.5, 1.0, DriverCPU},
		{"Memory above spike threshold", 1.0, 2.0, DriverMemory},
		{"both above spike → CPU wins", 3.0, 2.0, DriverCPU},
		{"both below spike, CPU higher relative", 1.5, 1.0, DriverCPU},
		{"both below spike, Memory higher relative", 0.5, 1.5, DriverMemory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RecommendMetricDriver(tt.cpuRatio, tt.memRatio)
			if got != tt.want {
				t.Errorf("RecommendMetricDriver(%v, %v) = %v, want %v",
					tt.cpuRatio, tt.memRatio, got, tt.want)
			}
		})
	}
}

// ---------- RecommendMaxReplicasForSpike ----------

func TestRecommendMaxReplicasForSpike(t *testing.T) {
	tests := []struct {
		name       string
		currentMax int32
		min        int32
		ratio      float64
		want       int32
	}{
		{"sufficient headroom", 10, 2, 3.0, 0},
		{"insufficient headroom", 5, 2, 3.0, 6},
		{"zero min", 5, 0, 3.0, 0},
		{"zero current", 0, 2, 3.0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RecommendMaxReplicasForSpike(tt.currentMax, tt.min, tt.ratio)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

// ---------- FindForTarget ----------

func TestFindForTarget(t *testing.T) {
	hpas := []Info{
		{Name: "hpa-a", TargetName: "deploy-a"},
		{Name: "hpa-b", TargetName: "deploy-b"},
	}
	if got := FindForTarget(hpas, "deploy-b"); got == nil || got.Name != "hpa-b" {
		t.Errorf("expected hpa-b, got %v", got)
	}
	if got := FindForTarget(hpas, "missing"); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// ---------- helpers ----------

func findingsText(fs []Finding) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.Message)
	}
	return strings.Join(parts, " | ")
}
