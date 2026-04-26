package analysis

import (
	"testing"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/metrics"
	"k8s.io/apimachinery/pkg/api/resource"
)

const Mi = 1048576

func usage(cpuP50, cpuP99, memP50MiB, memP99MiB, memTrendMiBPerHr float64) metrics.Usage {
	return metrics.Usage{
		HasData:  true,
		CPUP50:   cpuP50,
		CPUP99:   cpuP99,
		MemP50:   memP50MiB * Mi,
		MemP99:   memP99MiB * Mi,
		MemTrend: memTrendMiBPerHr * Mi,
	}
}

func mib(n float64) resource.Quantity {
	return *resource.NewQuantity(int64(n*Mi), resource.BinarySI)
}

func TestClassifyPod(t *testing.T) {
	lim128 := mib(128)
	lim512 := mib(512)
	noLim := resource.Quantity{}

	tests := []struct {
		name       string
		u          metrics.Usage
		memLim     resource.Quantity
		wantClass  BehaviorClass
		wantConfGt float64 // confidence must be strictly greater than this
	}{
		{
			name:      "no data → UNKNOWN",
			u:         metrics.Usage{HasData: false},
			memLim:    lim128,
			wantClass: BehaviorUnknown,
		},
		{
			name:       "RUNAWAY: p99 ≥ 90% of limit",
			u:          usage(0.01, 0.01, 100, 116, 0), // 116/128 = 90.6%
			memLim:     lim128,
			wantClass:  BehaviorRunaway,
			wantConfGt: 0,
		},
		{
			name:       "RUNAWAY: no limit → not RUNAWAY",
			u:          usage(0.01, 0.01, 100, 116, 0),
			memLim:     noLim,
			wantClass:  BehaviorStatic, // low ratios, flat trend
			wantConfGt: 0,
		},
		{
			name:       "SPIKY CPU: p99/p50 ≥ 2.0",
			u:          usage(0.05, 0.11, 64, 70, 0), // 0.11/0.05 = 2.2
			memLim:     lim128,
			wantClass:  BehaviorSpiky,
			wantConfGt: 0,
		},
		{
			name:       "SPIKY MEM: p99/p50 ≥ 1.8",
			u:          usage(0.05, 0.06, 40, 74, 0), // 74/40 = 1.85
			memLim:     lim128,
			wantClass:  BehaviorSpiky,
			wantConfGt: 0,
		},
		{
			name:       "GROWTH: trend significant + p99 ≥ 30% of limit",
			u:          usage(0.01, 0.01, 60, 50, 1.0), // trend=1.0 MiB/hr > 1% of 60=0.6; p99=50/128=39%
			memLim:     lim128,
			wantClass:  BehaviorGrowth,
			wantConfGt: 0,
		},
		{
			name:      "GROWTH suppressed: p99 < 30% of limit",
			u:         usage(0.01, 0.01, 10, 15, 0.5), // trend significant but p99=15/128=11.7% < 30%; memRatio=1.5 ≥ 1.3 → UNKNOWN
			memLim:    lim128,
			wantClass: BehaviorUnknown, // not GROWTH (suppressed) and not STATIC (memRatio too high)
		},
		{
			name:       "STATIC: low ratios, flat trend",
			u:          usage(0.05, 0.06, 64, 68, 0),
			memLim:     lim128,
			wantClass:  BehaviorStatic,
			wantConfGt: 0,
		},
		{
			name:      "STATIC: no limit, low ratios",
			u:         usage(0.05, 0.06, 64, 68, 0),
			memLim:    noLim,
			wantClass: BehaviorStatic,
		},
		{
			name:      "UNKNOWN: CPU ratio in gray zone between static and spiky thresholds",
			u:         usage(0.05, 0.085, 64, 75, 0), // cpuRatio=1.7: not spiky (≥2.0) but not static (<1.5) → UNKNOWN
			memLim:    lim512,
			wantClass: BehaviorUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conf := ClassifyPod(tt.u, tt.memLim)
			if got != tt.wantClass {
				t.Errorf("ClassifyPod() = %s, want %s", got, tt.wantClass)
			}
			if conf <= tt.wantConfGt && tt.wantClass != BehaviorUnknown {
				t.Errorf("ClassifyPod() confidence = %f, want > %f", conf, tt.wantConfGt)
			}
		})
	}
}

func TestClassifyWorkload(t *testing.T) {
	tests := []struct {
		name      string
		behaviors []BehaviorClass
		want      BehaviorClass
		wantMixed bool
	}{
		{
			name:      "all STATIC → STATIC",
			behaviors: []BehaviorClass{BehaviorStatic, BehaviorStatic, BehaviorStatic},
			want:      BehaviorStatic,
		},
		{
			name:      "all RUNAWAY → RUNAWAY",
			behaviors: []BehaviorClass{BehaviorRunaway, BehaviorRunaway},
			want:      BehaviorRunaway,
		},
		{
			name:      "mixed → MIXED",
			behaviors: []BehaviorClass{BehaviorStatic, BehaviorGrowth},
			want:      BehaviorMixed,
			wantMixed: true,
		},
		{
			name:      "single pod → that behavior",
			behaviors: []BehaviorClass{BehaviorSpiky},
			want:      BehaviorSpiky,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, breakdown := ClassifyWorkload(tt.behaviors)
			if got != tt.want {
				t.Errorf("ClassifyWorkload() = %s, want %s", got, tt.want)
			}
			if tt.wantMixed && len(breakdown) == 0 {
				t.Error("ClassifyWorkload() expected non-empty breakdown for MIXED")
			}
			if !tt.wantMixed && len(breakdown) > 0 {
				t.Error("ClassifyWorkload() unexpected breakdown for non-MIXED result")
			}
		})
	}
}
