package analysis

import (
	"math"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/metrics"
	"k8s.io/apimachinery/pkg/api/resource"
)

// BehaviorClass classifies a workload's resource usage pattern.
type BehaviorClass string

const (
	BehaviorStatic  BehaviorClass = "STATIC"  // low variance, consistent usage
	BehaviorSpiky   BehaviorClass = "SPIKY"   // high variance between p50 and p99
	BehaviorGrowth  BehaviorClass = "GROWTH"  // consistent upward memory trend
	BehaviorRunaway BehaviorClass = "RUNAWAY" // p99 at or near memory limit
	BehaviorMixed   BehaviorClass = "MIXED"   // pods within workload disagree
	BehaviorUnknown BehaviorClass = "UNKNOWN" // insufficient data
)

// Classification thresholds.
const (
	// runawayMemRatio: mem p99 at or above 90% of limit signals OOM risk.
	runawayMemRatio = 0.90

	// spikyCPURatio: CPU p99/p50 ≥ 2× indicates burst traffic.
	spikyCPURatio = 2.0

	// spikyMemRatio: mem p99/p50 threshold is intentionally lower than spikyCPURatio
	// because memory doesn't compress — a memory spike is more disruptive than a CPU spike.
	spikyMemRatio = 1.8

	// growthTrendFactor: trend must exceed 1% of p50 per hour to be considered significant.
	growthTrendFactor = 0.01

	// growthMinLimRatio: trend is only actionable when p99 is already above 30% of the limit.
	// Below this threshold the pod has enough headroom that trend noise is not signal.
	growthMinLimRatio = 0.30

	// staticCPURatio: CPU p99/p50 below 1.5× is considered stable.
	staticCPURatio = 1.5

	// staticMemRatio: mem p99/p50 below 1.3× is considered stable.
	staticMemRatio = 1.3
)

// ClassifyPod classifies a single container's behavior from its usage metrics.
// Returns the behavior class and a confidence score (0.0–1.0).
func ClassifyPod(u metrics.Usage, memLim resource.Quantity) (BehaviorClass, float64) {
	if !u.HasData {
		return BehaviorUnknown, 0
	}

	// RUNAWAY: p99 within 10% of or exceeding memory limit — highest priority.
	// When no limit is set (memLimBytes == 0), this check is skipped entirely.
	memLimBytes := float64(memLim.Value())
	if memLimBytes > 0 && u.MemP99 > 0 {
		if u.MemP99/memLimBytes >= runawayMemRatio {
			return BehaviorRunaway, 0.95
		}
	}

	cpuRatio := safeRatio(u.CPUP99, u.CPUP50)
	memRatio := safeRatio(u.MemP99, u.MemP50)

	// SPIKY: high p99/p50 variance.
	if cpuRatio >= spikyCPURatio || memRatio >= spikyMemRatio {
		return BehaviorSpiky, 0.85
	}

	// GROWTH: memory trend > 1% of p50 per hour. When a memory limit is set,
	// also require that p99 is already above 30% of the limit — pods well within
	// their limit have enough headroom that trend noise is not actionable.
	trendThreshold := u.MemP50 * growthTrendFactor
	trendSignificant := u.MemP50 > 0 && u.MemTrend > trendThreshold
	if trendSignificant && memLimBytes > 0 {
		trendSignificant = u.MemP99/memLimBytes >= growthMinLimRatio
	}
	if trendSignificant {
		return BehaviorGrowth, 0.75
	}

	// STATIC: low variance and flat trend.
	trendFlat := u.MemP50 == 0 || math.Abs(u.MemTrend) <= trendThreshold
	if cpuRatio < staticCPURatio && memRatio < staticMemRatio && trendFlat {
		return BehaviorStatic, 0.9
	}

	return BehaviorUnknown, 0
}

// ClassifyWorkload aggregates pod-level behaviors into a workload-level class.
// Returns MIXED (with a breakdown map) when pods disagree.
func ClassifyWorkload(behaviors []BehaviorClass) (BehaviorClass, map[BehaviorClass]int) {
	counts := make(map[BehaviorClass]int)
	for _, b := range behaviors {
		counts[b]++
	}
	if len(counts) == 1 {
		for b := range counts {
			return b, nil
		}
	}
	return BehaviorMixed, counts
}

// safeRatio returns p99/p50, or 1.0 when p50 is zero (no variance data).
func safeRatio(p99, p50 float64) float64 {
	if p50 <= 0 {
		return 1.0
	}
	return p99 / p50
}
