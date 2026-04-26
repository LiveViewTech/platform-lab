package hpa

import (
	"context"
	"fmt"
	"math"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/analysis"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/metrics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var hpaGVR = schema.GroupVersionResource{
	Group:    "autoscaling",
	Version:  "v2",
	Resource: "horizontalpodautoscalers",
}

// Info holds the fields from an HPA that are relevant to kri validation.
type Info struct {
	Name            string
	Namespace       string
	TargetKind      string
	TargetName      string // scaleTargetRef.name — used to join to workloads
	MinReplicas     int32
	MaxReplicas     int32
	CurrentReplicas int32  // status.currentReplicas
	CPUTarget       *int32 // averageUtilization %, nil if HPA does not target CPU
	MemTarget       *int32 // averageUtilization %, nil if HPA does not target memory
}

// MetricDriver indicates which resource metric is the primary driver of scaling.
type MetricDriver string

const (
	DriverCPU    MetricDriver = "CPU"
	DriverMemory MetricDriver = "Memory"
)

// Finding is a single HPA validation result.
type Finding struct {
	Severity string // ERROR or WARN
	Message  string
}

// Validation is the aggregated result of all HPA checks for a workload.
type Validation struct {
	Status   string    // OK, WARN, ERROR, NONE (no HPA present)
	Findings []Finding
}

// List returns all HPAs in the given namespace.
func List(ctx context.Context, dynClient dynamic.Interface, namespace string) ([]Info, error) {
	list, err := dynClient.Resource(hpaGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list HPAs in %q: %w", namespace, err)
	}

	var infos []Info
	for _, item := range list.Items {
		spec, _ := item.Object["spec"].(map[string]interface{})
		if spec == nil {
			continue
		}

		ref, _ := spec["scaleTargetRef"].(map[string]interface{})
		targetKind, _ := ref["kind"].(string)
		targetName, _ := ref["name"].(string)

		minReplicas := int32(1)
		if v, ok := spec["minReplicas"].(int64); ok {
			minReplicas = int32(v)
		}
		maxReplicas := int32(0)
		if v, ok := spec["maxReplicas"].(int64); ok {
			maxReplicas = int32(v)
		}

		currentReplicas := int32(0)
		if status, ok := item.Object["status"].(map[string]interface{}); ok {
			if v, ok := status["currentReplicas"].(int64); ok {
				currentReplicas = int32(v)
			}
		}

		info := Info{
			Name:            item.GetName(),
			Namespace:       item.GetNamespace(),
			TargetKind:      targetKind,
			TargetName:      targetName,
			MinReplicas:     minReplicas,
			MaxReplicas:     maxReplicas,
			CurrentReplicas: currentReplicas,
		}

		metricsList, _ := spec["metrics"].([]interface{})
		for _, m := range metricsList {
			metric, _ := m.(map[string]interface{})
			if metric["type"] != "Resource" {
				continue
			}
			res, _ := metric["resource"].(map[string]interface{})
			name, _ := res["name"].(string)
			target, _ := res["target"].(map[string]interface{})
			if target["type"] != "Utilization" {
				continue
			}
			pct, _ := target["averageUtilization"].(int64)
			pct32 := int32(pct)
			switch name {
			case "cpu":
				info.CPUTarget = &pct32
			case "memory":
				info.MemTarget = &pct32
			}
		}

		infos = append(infos, info)
	}

	return infos, nil
}

// FindForTarget returns the HPA whose scaleTargetRef.name matches targetName, or nil.
func FindForTarget(hpas []Info, targetName string) *Info {
	for i := range hpas {
		if hpas[i].TargetName == targetName {
			return &hpas[i]
		}
	}
	return nil
}

// Validate runs all HPA checks for a single container against its usage metrics.
func Validate(h *Info, u metrics.Usage, cpuReq, memReq resource.Quantity, behavior analysis.BehaviorClass) Validation {
	if h == nil {
		return Validation{Status: "NONE"}
	}

	var findings []Finding

	// CPU request missing — HPA cannot function without a request to compute % against.
	if h.CPUTarget != nil && cpuReq.IsZero() {
		findings = append(findings, Finding{"ERROR", "HPA targets CPU but no CPU request is set"})
	}

	// Memory request missing.
	if h.MemTarget != nil && memReq.IsZero() {
		findings = append(findings, Finding{"ERROR", "HPA targets memory but no memory request is set"})
	}

	// CPU utilization checks — only meaningful when we have usage data.
	if h.CPUTarget != nil && u.HasData && !cpuReq.IsZero() {
		cpuReqCores := float64(cpuReq.MilliValue()) / 1000.0
		if cpuReqCores > 0 {
			p95Pct := (u.CPUP95 / cpuReqCores) * 100
			p50Pct := (u.CPUP50 / cpuReqCores) * 100
			target := float64(*h.CPUTarget)

			if p95Pct > target {
				findings = append(findings, Finding{"WARN", fmt.Sprintf(
					"CPU p95 utilization (%.0f%%) exceeds HPA target (%d%%) — HPA may be perpetually scaling",
					p95Pct, *h.CPUTarget,
				)})
			}

			if p50Pct > 0 && p50Pct < target*0.3 {
				findings = append(findings, Finding{"WARN", fmt.Sprintf(
					"CPU HPA target (%d%%) is far above p50 utilization (%.0f%%) — HPA may never trigger",
					*h.CPUTarget, p50Pct,
				)})
			}
		}
	}

	// Memory utilization checks — symmetric with CPU checks above.
	if h.MemTarget != nil && u.HasData && !memReq.IsZero() {
		memReqBytes := float64(memReq.Value())
		if memReqBytes > 0 {
			p95Pct := (u.MemP95 / memReqBytes) * 100
			p50Pct := (u.MemP50 / memReqBytes) * 100
			target := float64(*h.MemTarget)

			if p95Pct > target {
				findings = append(findings, Finding{"WARN", fmt.Sprintf(
					"Memory p95 utilization (%.0f%%) exceeds HPA target (%d%%) — HPA may be perpetually scaling",
					p95Pct, *h.MemTarget,
				)})
			}

			if p50Pct > 0 && p50Pct < target*0.3 {
				findings = append(findings, Finding{"WARN", fmt.Sprintf(
					"Memory HPA target (%d%%) is far above p50 utilization (%.0f%%) — HPA may never trigger",
					*h.MemTarget, p50Pct,
				)})
			}
		}
	}

	// minReplicas=1 on a SPIKY workload — cold starts will absorb spikes poorly.
	if h.MinReplicas == 1 && behavior == analysis.BehaviorSpiky {
		findings = append(findings, Finding{"WARN",
			"minReplicas=1 on SPIKY workload — consider raising to absorb traffic spikes"})
	}

	// Scaling metric mismatch — only relevant for single-metric HPAs.
	if h.CPUTarget != nil && h.MemTarget == nil {
		if behavior == analysis.BehaviorGrowth || behavior == analysis.BehaviorRunaway {
			findings = append(findings, Finding{"WARN",
				"CPU-only HPA on a memory-trending workload — consider adding a memory metric"})
		}
	}
	if h.MemTarget != nil && h.CPUTarget == nil {
		if behavior == analysis.BehaviorSpiky {
			findings = append(findings, Finding{"WARN",
				"Memory-only HPA on a CPU-spiky workload — consider adding a CPU metric"})
		}
	}

	if len(findings) == 0 {
		return Validation{Status: "OK"}
	}

	status := "WARN"
	for _, f := range findings {
		if f.Severity == "ERROR" {
			status = "ERROR"
			break
		}
	}
	return Validation{Status: status, Findings: findings}
}

// WontFire returns true when the HPA is structurally broken or will never
// trigger under observed conditions. Requires sufficient usage data.
//
// Cases:
//   - maxReplicas == minReplicas: scaling range is zero
//   - no metrics configured: no trigger condition
//   - missing resource request for targeted metric (ERROR finding)
//   - p99 utilization never crosses the configured target
//   - wrong metric: memory-driven workload with a CPU-only HPA (or vice versa)
func WontFire(h *Info, u metrics.Usage, cpuReq, memReq resource.Quantity, driver MetricDriver) bool {
	if h == nil {
		return false
	}
	if !u.HasData {
		return false
	}

	// Structural: scaling range is zero.
	if h.MaxReplicas <= h.MinReplicas {
		return true
	}

	// Structural: no metrics configured.
	if h.CPUTarget == nil && h.MemTarget == nil {
		return true
	}

	// Structural: missing request for targeted metric.
	if h.CPUTarget != nil && cpuReq.IsZero() {
		return true
	}
	if h.MemTarget != nil && memReq.IsZero() {
		return true
	}

	// Wrong metric — only flag when HPA has a single metric that doesn't
	// match the workload's primary driver. Dual-metric HPAs cover both.
	if h.CPUTarget != nil && h.MemTarget == nil {
		if driver == DriverMemory {
			return true
		}
	}
	if h.MemTarget != nil && h.CPUTarget == nil {
		if driver == DriverCPU {
			return true
		}
	}

	// Behavioural: p99 never crosses the configured target under observed
	// conditions. An HPA fires when ANY configured metric exceeds its target,
	// so it won't fire only when ALL configured metrics are below their targets.
	allBelowTarget := true

	if h.CPUTarget != nil && !cpuReq.IsZero() {
		cpuReqCores := float64(cpuReq.MilliValue()) / 1000.0
		if cpuReqCores > 0 {
			p99Pct := (u.CPUP99 / cpuReqCores) * 100
			if p99Pct >= float64(*h.CPUTarget) {
				allBelowTarget = false
			}
		}
	}
	if h.MemTarget != nil && !memReq.IsZero() {
		memReqBytes := float64(memReq.Value())
		if memReqBytes > 0 {
			p99Pct := (u.MemP99 / memReqBytes) * 100
			if p99Pct >= float64(*h.MemTarget) {
				allBelowTarget = false
			}
		}
	}

	return allBelowTarget
}

// RecommendMaxReplicasForSpike returns the maxReplicas needed so that the HPA
// can scale from minReplicas to absorb a peak of spikeRatio × minReplicas.
// Returns 0 when currentMax already provides sufficient headroom.
func RecommendMaxReplicasForSpike(currentMax, minReplicas int32, spikeRatio float64) int32 {
	if currentMax <= 0 || minReplicas <= 0 || spikeRatio <= 0 {
		return 0
	}
	rec := int32(math.Ceil(float64(minReplicas) * spikeRatio))
	if rec <= currentMax {
		return 0
	}
	return rec
}

// RecommendMaxReplicasForGrowth returns a suggested maxReplicas for a workload
// that has hit its scaling ceiling. growthFactor is applied to currentMax
// (e.g. 1.5 for 50% headroom). Returns 0 if no increase is warranted.
func RecommendMaxReplicasForGrowth(currentMax int32, growthFactor float64) int32 {
	if currentMax <= 0 {
		return 0
	}
	rec := int32(math.Ceil(float64(currentMax) * growthFactor))
	if rec <= currentMax {
		return 0
	}
	return rec
}

// RecommendMetricDriver returns the metric that should drive HPA scaling based
// on observed usage ratios. CPU takes precedence when both exceed their spike
// thresholds.
func RecommendMetricDriver(cpuRatio, memRatio float64) MetricDriver {
	const spikyCPURatio = 2.0
	const spikyMemRatio = 1.8
	if cpuRatio >= spikyCPURatio {
		return DriverCPU
	}
	if memRatio >= spikyMemRatio {
		return DriverMemory
	}
	// Below spike thresholds: use whichever ratio is higher relative to its threshold.
	if cpuRatio/spikyCPURatio >= memRatio/spikyMemRatio {
		return DriverCPU
	}
	return DriverMemory
}
