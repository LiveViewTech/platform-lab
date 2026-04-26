package analysis

import (
	"strings"
	"testing"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/metrics"
	"k8s.io/apimachinery/pkg/api/resource"
)

func mustParse(s string) resource.Quantity {
	return resource.MustParse(s)
}

// defaultArgs returns common resource values for test calls.
func defaultArgs() (cpuReq, cpuLim, memReq, memLim resource.Quantity) {
	return mustParse("100m"), mustParse("200m"), mustParse("128Mi"), mustParse("256Mi")
}

func TestRecommend_holdCases(t *testing.T) {
	cpuReq, cpuLim, memReq, memLim := defaultArgs()

	tests := []struct {
		name     string
		behavior BehaviorClass
		u        metrics.Usage
		wantHold bool
		wantText string
	}{
		{
			name:     "no data → hold",
			behavior: BehaviorStatic,
			u:        metrics.Usage{HasData: false},
			wantHold: true,
		},
		{
			name:     "confidence below threshold → hold",
			behavior: BehaviorStatic,
			u:        metrics.Usage{HasData: true, CPUP99: 0.05, MemP99: 20 * Mi},
			wantHold: true, // confidence 0.5 < threshold 0.8
		},
		{
			name:     "SPIKY → hold",
			behavior: BehaviorSpiky,
			u:        usage(0.05, 0.11, 64, 74, 0),
			wantHold: true,
		},
		{
			name:     "GROWTH → hold",
			behavior: BehaviorGrowth,
			u:        usage(0.01, 0.01, 60, 50, 1.0),
			wantHold: true,
		},
		{
			name:     "MIXED → hold",
			behavior: BehaviorMixed,
			u:        usage(0.01, 0.01, 64, 68, 0),
			wantHold: true,
		},
		{
			name:     "UNKNOWN → hold",
			behavior: BehaviorUnknown,
			u:        usage(0.01, 0.01, 64, 68, 0),
			wantHold: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Recommend(tt.behavior, 0.5, 0.8, tt.u, cpuReq, cpuLim, memReq, memLim, 10, 16, 0, 0)
			if r.Hold != tt.wantHold {
				t.Errorf("Hold = %v, want %v", r.Hold, tt.wantHold)
			}
			if r.IsActionable {
				t.Error("IsActionable should be false for held recommendations")
			}
		})
	}
}

func TestRecommend_withinTolerance(t *testing.T) {
	// Current: 100m CPU, 128Mi mem. Usage: CPU p99 ~92m, mem p99 ~100Mi.
	// Recommended CPU: ceil(0.092 * 1200) = ceil(110.4) = 120m → rounded to 120m.
	// Diff from 100m: (120-100)/100 = 20% > 10% → actionable for CPU.
	// Let's pick values that are within tolerance instead.
	// CPU within tolerance: current 100m, p99 = 0.083 cores → rec = ceil(0.083*1200)=ceil(99.6)=100 → rounded to 100m → diff 0% → not actionable
	// Mem within tolerance: current 128Mi, p99 = 100Mi → rec = ceil(100*1.3)=130Mi → diff (130-128)/128=1.6% < 10% → not actionable
	u := metrics.Usage{
		HasData: true,
		CPUP99:  0.083, // → 99.6m → rounded to 100m, same as current
		MemP99:  100 * Mi,
	}
	cpuReq := mustParse("100m")
	cpuLim := mustParse("200m")
	memReq := mustParse("128Mi")
	memLim := mustParse("256Mi")

	r := Recommend(BehaviorStatic, 0.9, 0.8, u, cpuReq, cpuLim, memReq, memLim, 10, 16, 0, 0)
	if r.IsActionable {
		t.Errorf("expected within tolerance, got actionable: %s", r.Text)
	}
	if r.Hold {
		t.Errorf("expected within tolerance, got hold: %s", r.HoldReason)
	}
	if r.Text != "within tolerance" {
		t.Errorf("Text = %q, want %q", r.Text, "within tolerance")
	}
}

func TestRecommend_staticActionable(t *testing.T) {
	// Low usage: CPU p99 = 0m, mem p99 = 12Mi → both well below current 100m/128Mi.
	u := metrics.Usage{
		HasData: true,
		CPUP99:  0,
		MemP99:  12 * Mi,
	}
	cpuReq := mustParse("100m")
	cpuLim := mustParse("200m")
	memReq := mustParse("128Mi")
	memLim := mustParse("256Mi")

	r := Recommend(BehaviorStatic, 0.9, 0.8, u, cpuReq, cpuLim, memReq, memLim, 10, 16, 0, 0)

	if !r.IsActionable {
		t.Fatal("expected actionable recommendation")
	}
	if r.Resources == nil {
		t.Fatal("expected non-nil Resources")
	}
	// CPU: floor(10m minimum)
	if r.Resources.CPURequest != "10m" {
		t.Errorf("CPURequest = %q, want %q", r.Resources.CPURequest, "10m")
	}
	// CPU limit should NOT be updated (requests != limits here)
	if r.Resources.CPULimit != "200m" {
		t.Errorf("CPULimit = %q, want 200m (unchanged)", r.Resources.CPULimit)
	}
	// Mem: 12Mi * 1.3 = 15.6 → ceil = 16Mi (minimum)
	if r.Resources.MemRequest != "16Mi" {
		t.Errorf("MemRequest = %q, want %q", r.Resources.MemRequest, "16Mi")
	}
	if !strings.Contains(r.Text, "->") {
		t.Errorf("Text %q missing arrow", r.Text)
	}
}

func TestRecommend_guaranteedQoSPreserved(t *testing.T) {
	// requests == limits → Guaranteed QoS. Both must be updated together.
	u := metrics.Usage{
		HasData: true,
		CPUP99:  0,
		MemP99:  12 * Mi,
	}
	cpuReq := mustParse("100m")
	cpuLim := mustParse("100m") // same as req → Guaranteed
	memReq := mustParse("128Mi")
	memLim := mustParse("128Mi") // same as req → Guaranteed

	r := Recommend(BehaviorStatic, 0.9, 0.8, u, cpuReq, cpuLim, memReq, memLim, 10, 16, 0, 0)

	if !r.IsActionable {
		t.Fatal("expected actionable recommendation")
	}
	if r.Resources.CPURequest != r.Resources.CPULimit {
		t.Errorf("Guaranteed QoS violated: CPURequest %q != CPULimit %q",
			r.Resources.CPURequest, r.Resources.CPULimit)
	}
	if r.Resources.MemRequest != r.Resources.MemLimit {
		t.Errorf("Guaranteed QoS violated: MemRequest %q != MemLimit %q",
			r.Resources.MemRequest, r.Resources.MemLimit)
	}
}

func TestRecommend_runaway(t *testing.T) {
	u := metrics.Usage{
		HasData: true,
		MemP99:  120 * Mi, // 93.75% of 128Mi limit
	}
	cpuReq, cpuLim, memReq, memLim := mustParse("100m"), mustParse("100m"), mustParse("128Mi"), mustParse("128Mi")

	r := Recommend(BehaviorRunaway, 0.95, 0.8, u, cpuReq, cpuLim, memReq, memLim, 10, 16, 0, 0)

	if !r.IsActionable {
		t.Fatal("RUNAWAY should produce an actionable recommendation")
	}
	if !strings.Contains(r.Text, "RUNAWAY") {
		t.Errorf("RUNAWAY recommendation text missing label: %q", r.Text)
	}
	if r.Resources == nil {
		t.Fatal("expected non-nil Resources for RUNAWAY")
	}
}

func TestRecommend_floorApplied(t *testing.T) {
	// Extremely low usage — floor should kick in.
	u := metrics.Usage{
		HasData: true,
		CPUP99:  0.0001, // → 0.12m → floor to 5m (custom min)
		MemP99:  1 * Mi, // → 1.3Mi → floor to 8Mi (custom min)
	}
	cpuReq := mustParse("100m")
	cpuLim := mustParse("100m")
	memReq := mustParse("128Mi")
	memLim := mustParse("128Mi")

	r := Recommend(BehaviorStatic, 0.9, 0.8, u, cpuReq, cpuLim, memReq, memLim, 5, 8, 0, 0)

	if !r.IsActionable {
		t.Fatal("expected actionable recommendation")
	}
	// Floor is 5m, but roundUpTo(5, 10) = 10m since we round to nearest 10m.
	if r.Resources.CPURequest != "10m" {
		t.Errorf("CPURequest = %q, want 10m (floor 5m rounded up to 10m)", r.Resources.CPURequest)
	}
	// Floor is 8Mi; memory rounds to nearest Mi so stays 8Mi.
	if r.Resources.MemRequest != "8Mi" {
		t.Errorf("MemRequest = %q, want 8Mi (floor)", r.Resources.MemRequest)
	}
}

func TestSignificantDiff(t *testing.T) {
	tests := []struct {
		rec, cur float64
		want     bool
	}{
		{90, 100, false},  // exactly 10% — not > 10%, boundary is exclusive
		{89, 100, true},   // 11% diff
		{95, 100, false},  // 5% diff
		{100, 0, true},    // current zero, rec > 0
		{0, 0, false},     // both zero
		{110, 100, false}, // +10% — not significant
		{111, 100, true},  // +11% — significant
	}
	for _, tt := range tests {
		got := significantDiff(tt.rec, tt.cur)
		if got != tt.want {
			t.Errorf("significantDiff(%v, %v) = %v, want %v", tt.rec, tt.cur, got, tt.want)
		}
	}
}

func TestRoundUpTo(t *testing.T) {
	tests := []struct {
		v, step, want int64
	}{
		{0, 10, 0},
		{1, 10, 10},
		{10, 10, 10},
		{11, 10, 20},
		{100, 10, 100},
		{101, 10, 110},
		{5, 0, 5}, // zero step → unchanged
	}
	for _, tt := range tests {
		got := roundUpTo(tt.v, tt.step)
		if got != tt.want {
			t.Errorf("roundUpTo(%d, %d) = %d, want %d", tt.v, tt.step, got, tt.want)
		}
	}
}

func int32p(v int32) *int32 { return &v }

func TestRecommendHPAValues(t *testing.T) {
	tests := []struct {
		name            string
		usage           metrics.Usage
		cpuReq          resource.Quantity
		memReq          resource.Quantity
		currentCPU      *int32
		currentMem      *int32
		currentMin      int32
		driver          string
		reason          string
		wantBothTargets bool   // expect both TargetCPU and TargetMemory non-nil
		wantCPUOnly     bool   // expect TargetCPU non-nil, TargetMemory nil
		wantMemOnly     bool   // expect TargetMemory non-nil, TargetCPU nil
		wantMinReplicas int32
		wantTextContain string
	}{
		{
			name:   "no data → empty recommendation",
			usage:  metrics.Usage{HasData: false},
			cpuReq: mustParse("100m"),
			memReq: mustParse("128Mi"),
		},
		{
			name: "CPU driver with both requests and data → both targets populated",
			usage: metrics.Usage{
				HasData: true,
				CPUP50:  0.04, CPUP99: 0.06,
				MemP50:  60 * Mi, MemP99: 80 * Mi,
			},
			cpuReq:          mustParse("100m"),
			memReq:          mustParse("128Mi"),
			currentCPU:      int32p(70),
			currentMem:      int32p(80),
			currentMin:      1,
			driver:          "CPU",
			reason:          "Tuning",
			wantBothTargets: true,
			wantMinReplicas: 2,
			wantTextContain: "CPU target",
		},
		{
			name: "Memory driver with both requests and data → both targets populated",
			usage: metrics.Usage{
				HasData: true,
				CPUP50:  0.04, CPUP99: 0.06,
				MemP50:  60 * Mi, MemP99: 80 * Mi,
			},
			cpuReq:          mustParse("100m"),
			memReq:          mustParse("128Mi"),
			currentCPU:      int32p(70),
			currentMem:      int32p(80),
			currentMin:      2,
			driver:          "Memory",
			reason:          "WontFire",
			wantBothTargets: true,
			wantMinReplicas: 2,
			wantTextContain: "Memory target",
		},
		{
			name: "only CPU data available → CPU-only target",
			usage: metrics.Usage{
				HasData: true,
				CPUP50:  0.04, CPUP99: 0.06,
				MemP99:  0, // no memory data
			},
			cpuReq:          mustParse("100m"),
			memReq:          mustParse("128Mi"),
			currentCPU:      int32p(70),
			currentMin:      1,
			driver:          "CPU",
			reason:          "Tuning",
			wantCPUOnly:     true,
			wantMinReplicas: 2,
			wantTextContain: "CPU target",
		},
		{
			name: "only memory data available → memory-only target",
			usage: metrics.Usage{
				HasData: true,
				CPUP99:  0, // no CPU data
				MemP50:  60 * Mi, MemP99: 80 * Mi,
			},
			cpuReq:          mustParse("100m"),
			memReq:          mustParse("128Mi"),
			currentMem:      int32p(80),
			currentMin:      3,
			driver:          "Memory",
			reason:          "Tuning",
			wantMemOnly:     true,
			wantMinReplicas: 3,
			wantTextContain: "Memory target",
		},
		{
			name: "zero CPU request → only memory target",
			usage: metrics.Usage{
				HasData: true,
				CPUP50:  0.04, CPUP99: 0.06,
				MemP50:  60 * Mi, MemP99: 80 * Mi,
			},
			cpuReq:          resource.Quantity{},
			memReq:          mustParse("128Mi"),
			currentMin:      1,
			driver:          "Memory",
			reason:          "Tuning",
			wantMemOnly:     true,
			wantMinReplicas: 2,
		},
		{
			name: "zero memory request → only CPU target",
			usage: metrics.Usage{
				HasData: true,
				CPUP50:  0.04, CPUP99: 0.06,
				MemP50:  60 * Mi, MemP99: 80 * Mi,
			},
			cpuReq:          mustParse("100m"),
			memReq:          resource.Quantity{},
			currentMin:      1,
			driver:          "CPU",
			reason:          "Tuning",
			wantCPUOnly:     true,
			wantMinReplicas: 2,
		},
		{
			name: "minReplicas=0 treated as 1 then raised to 2",
			usage: metrics.Usage{
				HasData: true,
				CPUP50:  0.04, CPUP99: 0.06,
			},
			cpuReq:          mustParse("100m"),
			memReq:          resource.Quantity{},
			currentMin:      0,
			driver:          "CPU",
			reason:          "Tuning",
			wantMinReplicas: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := RecommendHPAValues(tt.usage, tt.cpuReq, tt.memReq, tt.currentCPU, tt.currentMem, tt.currentMin, tt.driver, tt.reason)

			if !tt.usage.HasData {
				if rec.Text != "" {
					t.Errorf("expected empty rec for no data, got %q", rec.Text)
				}
				return
			}

			if tt.wantMinReplicas != 0 && rec.MinReplicas != tt.wantMinReplicas {
				t.Errorf("MinReplicas = %d, want %d", rec.MinReplicas, tt.wantMinReplicas)
			}

			if tt.wantBothTargets {
				if rec.TargetCPU == nil {
					t.Error("expected TargetCPU to be set")
				}
				if rec.TargetMemory == nil {
					t.Error("expected TargetMemory to be set")
				}
			}
			if tt.wantCPUOnly {
				if rec.TargetCPU == nil {
					t.Error("expected TargetCPU to be set")
				}
				if rec.TargetMemory != nil {
					t.Errorf("expected TargetMemory nil, got %d", *rec.TargetMemory)
				}
			}
			if tt.wantMemOnly {
				if rec.TargetMemory == nil {
					t.Error("expected TargetMemory to be set")
				}
				if rec.TargetCPU != nil {
					t.Errorf("expected TargetCPU nil, got %d", *rec.TargetCPU)
				}
			}

			if tt.wantTextContain != "" && !strings.Contains(rec.Text, tt.wantTextContain) {
				t.Errorf("Text %q missing %q", rec.Text, tt.wantTextContain)
			}
		})
	}
}
