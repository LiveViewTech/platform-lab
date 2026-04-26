package analysis

import (
	"fmt"
	"math"
	"strings"

	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/metrics"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Recommendation holds a rightsizing suggestion for a single container.
type Recommendation struct {
	Text         string // human-readable; empty when held below confidence threshold
	IsActionable bool   // true only when a real recommendation was produced (not held or within tolerance)
	Hold         bool
	HoldReason   string
	Resources    *ResourceValues // structured values for plan generation; nil when held or within tolerance
}

// HPARecommendation holds a suggested HPA configuration for a workload.
type HPARecommendation struct {
	Text         string // human-readable summary
	TargetCPU    *int32 // recommended CPU averageUtilization %, nil if memory should be targeted
	TargetMemory *int32 // recommended memory averageUtilization %, nil if CPU should be targeted
	MinReplicas  int32
	Driver      string // "CPU" or "Memory"
	Reason      string // "WontFire", "Tuning", or "NoHPA"
	MaxReplicas *int32 // recommended maxReplicas, nil if no change suggested
}

// ResourceValues holds the final desired state for container resources.
// Used by kri plan to generate values-resources.yaml.
type ResourceValues struct {
	CPURequest string // e.g. "10m"
	CPULimit   string // e.g. "10m"
	MemRequest string // e.g. "16Mi"
	MemLimit   string // e.g. "16Mi"
}

// Recommend generates a recommendation based on behavior, confidence, and usage metrics.
// threshold is the minimum confidence required to emit a recommendation (0.0–1.0).
// minCPUMillis/minMemMi are floor values; maxCPUMillis/maxMemMi are ceiling values (0 = no ceiling).
func Recommend(behavior BehaviorClass, confidence, threshold float64, u metrics.Usage, cpuReq, cpuLim, memReq, memLim resource.Quantity, minCPUMillis, minMemMi, maxCPUMillis, maxMemMi int64) Recommendation {
	if !u.HasData {
		return Recommendation{Hold: true, HoldReason: "no data"}
	}

	if confidence < threshold {
		return Recommendation{Hold: true, HoldReason: fmt.Sprintf("confidence %.0f%% below threshold %.0f%%", confidence*100, threshold*100)}
	}

	switch behavior {
	case BehaviorStatic:
		return recommendStatic(u, cpuReq, cpuLim, memReq, memLim, minCPUMillis, minMemMi, maxCPUMillis, maxMemMi)
	case BehaviorRunaway:
		return recommendRunaway(u, memLim, maxMemMi)
	case BehaviorSpiky:
		// If p99 usage is under 25% of request, the workload is clearly over-provisioned
		// regardless of spike pattern — recommend downsizing.
		cpuPct := u.CPUP99 * 1000 / float64(cpuReq.MilliValue())
		memPct := u.MemP99 / float64(memReq.Value())
		if !cpuReq.IsZero() && !memReq.IsZero() && cpuPct < 0.25 && memPct < 0.25 {
			return recommendStatic(u, cpuReq, cpuLim, memReq, memLim, minCPUMillis, minMemMi, maxCPUMillis, maxMemMi)
		}
		return Recommendation{Hold: true, HoldReason: "SPIKY — monitor before rightsizing"}
	case BehaviorGrowth:
		return Recommendation{Hold: true, HoldReason: "GROWTH — still trending"}
	case BehaviorMixed:
		return Recommendation{Hold: true, HoldReason: "MIXED — investigate pod divergence first"}
	default:
		return Recommendation{Hold: true, HoldReason: "UNKNOWN — insufficient data"}
	}
}

// recommendStatic suggests reducing requests toward p99 + headroom.
// When requests == limits (Guaranteed QoS), both are updated together.
func recommendStatic(u metrics.Usage, cpuReq, cpuLim, memReq, memLim resource.Quantity, minCPUMillis, minMemMi, maxCPUMillis, maxMemMi int64) Recommendation {
	var parts []string

	// CPU: p99 + 20% headroom, rounded up to nearest 10m, floored at minCPUMillis, capped at maxCPUMillis.
	recCPUMillis := int64(math.Ceil(u.CPUP99 * 1200)) // cores * 1000m/core * 1.2 headroom
	recCPUMillis = roundUpTo(recCPUMillis, 10)
	if recCPUMillis < minCPUMillis {
		recCPUMillis = minCPUMillis
	}
	if maxCPUMillis > 0 && recCPUMillis > maxCPUMillis {
		recCPUMillis = maxCPUMillis
	}
	cpuChanged := significantDiff(float64(recCPUMillis), float64(cpuReq.MilliValue()))
	if cpuChanged {
		parts = append(parts, fmt.Sprintf("CPU %s -> %dm", cpuReq.String(), recCPUMillis))
	}

	// Memory: p99 + 30% headroom, rounded up to nearest Mi, floored at minMemMi, capped at maxMemMi.
	var recMemMi int64
	memChanged := false
	if u.MemP99 > 0 {
		recMemMi = int64(math.Ceil(u.MemP99 * 1.3 / 1048576))
		if recMemMi < minMemMi {
			recMemMi = minMemMi
		}
		if maxMemMi > 0 && recMemMi > maxMemMi {
			recMemMi = maxMemMi
		}
		curMemMi := memReq.Value() / 1048576
		memChanged = significantDiff(float64(recMemMi), float64(curMemMi))
		if memChanged {
			parts = append(parts, fmt.Sprintf("MEM %s -> %dMi", memReq.String(), recMemMi))
		}
	}

	if len(parts) == 0 {
		return Recommendation{Text: "within tolerance"}
	}

	// Build structured ResourceValues. When requests == limits (Guaranteed QoS),
	// apply the recommendation to both so QoS class is preserved.
	cpuReqStr := cpuReq.String()
	cpuLimStr := cpuLim.String()
	if cpuChanged {
		cpuReqStr = fmt.Sprintf("%dm", recCPUMillis)
		if cpuReq.Cmp(cpuLim) == 0 {
			cpuLimStr = cpuReqStr
		}
	}
	memReqStr := memReq.String()
	memLimStr := memLim.String()
	if memChanged && recMemMi > 0 {
		memReqStr = fmt.Sprintf("%dMi", recMemMi)
		if memReq.Cmp(memLim) == 0 {
			memLimStr = memReqStr
		}
	}

	return Recommendation{
		Text:         strings.Join(parts, ", "),
		IsActionable: true,
		Resources: &ResourceValues{
			CPURequest: cpuReqStr,
			CPULimit:   cpuLimStr,
			MemRequest: memReqStr,
			MemLimit:   memLimStr,
		},
	}
}

// recommendRunaway suggests increasing the memory limit to give headroom above p99.
func recommendRunaway(u metrics.Usage, memLim resource.Quantity, maxMemMi int64) Recommendation {
	if u.MemP99 <= 0 || memLim.IsZero() {
		return Recommendation{Hold: true, HoldReason: "RUNAWAY — insufficient data"}
	}
	recMi := int64(math.Ceil(u.MemP99 * 1.5 / 1048576))
	if maxMemMi > 0 && recMi > maxMemMi {
		recMi = maxMemMi
	}
	recStr := fmt.Sprintf("%dMi", recMi)
	return Recommendation{
		Text:         fmt.Sprintf("Increase MEM limit to %dMi (RUNAWAY)", recMi),
		IsActionable: true,
		Resources: &ResourceValues{
			MemRequest: recStr,
			MemLimit:   recStr,
		},
	}
}

// RecommendHPAValues returns a suggested HPA configuration given explicit current values.
// driver is "CPU" or "Memory"; reason is "WontFire" or "Tuning".
// currentMinReplicas of 0 is treated as 1.
func RecommendHPAValues(u metrics.Usage, cpuReq, memReq resource.Quantity, currentCPUTarget, currentMemTarget *int32, currentMinReplicas int32, driver, reason string) HPARecommendation {
	if !u.HasData {
		return HPARecommendation{}
	}

	minReplicas := currentMinReplicas
	if minReplicas < 1 {
		minReplicas = 1
	}
	// Raise minReplicas from 1 to 2 to absorb cold-start spikes.
	if minReplicas == 1 {
		minReplicas = 2
	}

	rec := HPARecommendation{
		MinReplicas: minReplicas,
		Driver:      driver,
		Reason:      reason,
	}

	// Populate both metrics when data supports it. The driver field indicates
	// the primary scaling signal; both targets are recommended for dual-metric HPAs.
	var parts []string

	if !cpuReq.IsZero() && u.CPUP99 > 0 {
		target := recommendCPUTarget(u, cpuReq, currentCPUTarget)
		rec.TargetCPU = &target
		parts = append(parts, fmt.Sprintf("CPU target -> %d%%", target))
	}
	if !memReq.IsZero() && u.MemP99 > 0 {
		target := recommendMemTarget(u, memReq, currentMemTarget)
		rec.TargetMemory = &target
		parts = append(parts, fmt.Sprintf("Memory target -> %d%%", target))
	}

	if len(parts) > 0 {
		rec.Text = fmt.Sprintf("%s, minReplicas -> %d", strings.Join(parts, ", "), minReplicas)
	} else {
		rec.Text = fmt.Sprintf("minReplicas -> %d", minReplicas)
	}

	return rec
}

// recommendCPUTarget computes a CPU utilization target between p50 and p99.
// If p99 is already below the current target (won't-fire case), lower the target to just below p99.
func recommendCPUTarget(u metrics.Usage, cpuReq resource.Quantity, current *int32) int32 {
	if cpuReq.IsZero() {
		return 70 // safe default when no request set
	}
	cpuReqCores := float64(cpuReq.MilliValue()) / 1000.0
	p50Pct := (u.CPUP50 / cpuReqCores) * 100
	p99Pct := (u.CPUP99 / cpuReqCores) * 100

	// Target: midpoint between p50 and p99, giving headroom before saturation.
	target := int32(math.Round((p50Pct + p99Pct) / 2))
	if target < 20 {
		target = 20
	}
	if target > 90 {
		target = 90
	}
	return target
}

// recommendMemTarget computes a memory utilization target between p50 and p99.
func recommendMemTarget(u metrics.Usage, memReq resource.Quantity, current *int32) int32 {
	if memReq.IsZero() {
		return 70
	}
	memReqBytes := float64(memReq.Value())
	p50Pct := (u.MemP50 / memReqBytes) * 100
	p99Pct := (u.MemP99 / memReqBytes) * 100

	target := int32(math.Round((p50Pct + p99Pct) / 2))
	if target < 20 {
		target = 20
	}
	if target > 90 {
		target = 90
	}
	return target
}


// significantDiff returns true when recommended and current differ by more than 10%.
func significantDiff(recommended, current float64) bool {
	if current == 0 {
		return recommended > 0
	}
	return math.Abs(recommended-current)/current > 0.10
}

// roundUpTo rounds v up to the nearest multiple of step.
func roundUpTo(v, step int64) int64 {
	if step == 0 {
		return v
	}
	return ((v + step - 1) / step) * step
}
