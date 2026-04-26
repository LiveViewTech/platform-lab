package metrics

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// ResourcePressure summarises resource usage observed for a single container
// during a specific time window (typically a failed rollout).
type ResourcePressure struct {
	MemPeakBytes    float64 // max working set bytes during the window
	MemRequestBytes float64 // configured memory request (0 = not set)
	MemLimitBytes   float64 // configured memory limit (0 = not set)
	CPUCores        float64 // average CPU usage during the window (cores)
	CPUThrottleRatio float64 // fraction of CPU periods that were throttled
	// HasMemPressure is true when peak usage exceeded the configured request.
	// False when no request is set.
	HasMemPressure bool
	// HasCPUThrottle is true when any CPU throttling was observed.
	HasCPUThrottle bool
	HasData        bool
}

// ContainerKey uniquely identifies a container within a pod.
type ContainerKey struct {
	Namespace, Pod, Container string
}

// Usage holds Prometheus-derived usage metrics for a single container
// over an observation window.
type Usage struct {
	CPUP50 float64 // cores
	CPUP95 float64
	CPUP99 float64
	MemP50 float64 // bytes
	MemP95 float64
	MemP99    float64
	MemTrend  float64 // bytes/hour (positive = growing)
	HasData   bool    // false when window has insufficient samples
}

// Client wraps the Prometheus HTTP API for usage metric queries.
type Client struct {
	api v1.API
}

func NewClient(prometheusURL string) (*Client, error) {
	c, err := api.NewClient(api.Config{Address: prometheusURL})
	if err != nil {
		return nil, fmt.Errorf("create prometheus client: %w", err)
	}
	return &Client{api: v1.NewAPI(c)}, nil
}

// UsageMetrics queries CPU/memory percentiles and memory trend for all running
// containers in namespace over the observation window.
// window is a Prometheus duration string: "7d", "24h", "1h", etc.
func (c *Client) UsageMetrics(ctx context.Context, namespace, window string) (map[ContainerKey]Usage, error) {
	now := time.Now()
	step := subqueryStep(window)

	type querySpec struct {
		name  string
		query string
		apply func(u *Usage, v float64)
	}

	queries := []querySpec{
		{
			name: "cpu p50",
			query: fmt.Sprintf(
				`quantile_over_time(0.50, rate(container_cpu_usage_seconds_total{namespace=%q,container!=""}[5m])[%s:%s])`,
				namespace, window, step,
			),
			apply: func(u *Usage, v float64) { u.CPUP50 = v },
		},
		{
			name: "cpu p95",
			query: fmt.Sprintf(
				`quantile_over_time(0.95, rate(container_cpu_usage_seconds_total{namespace=%q,container!=""}[5m])[%s:%s])`,
				namespace, window, step,
			),
			apply: func(u *Usage, v float64) { u.CPUP95 = v },
		},
		{
			name: "cpu p99",
			query: fmt.Sprintf(
				`quantile_over_time(0.99, rate(container_cpu_usage_seconds_total{namespace=%q,container!=""}[5m])[%s:%s])`,
				namespace, window, step,
			),
			apply: func(u *Usage, v float64) { u.CPUP99 = v },
		},
		{
			name: "mem p50",
			query: fmt.Sprintf(
				`quantile_over_time(0.50, container_memory_working_set_bytes{namespace=%q,container!=""}[%s:%s])`,
				namespace, window, step,
			),
			apply: func(u *Usage, v float64) { u.MemP50 = v },
		},
		{
			name: "mem p95",
			query: fmt.Sprintf(
				`quantile_over_time(0.95, container_memory_working_set_bytes{namespace=%q,container!=""}[%s:%s])`,
				namespace, window, step,
			),
			apply: func(u *Usage, v float64) { u.MemP95 = v },
		},
		{
			name: "mem p99",
			query: fmt.Sprintf(
				`quantile_over_time(0.99, container_memory_working_set_bytes{namespace=%q,container!=""}[%s:%s])`,
				namespace, window, step,
			),
			apply: func(u *Usage, v float64) { u.MemP99 = v },
		},
		{
			// deriv returns bytes/second — multiply by 3600 for bytes/hour.
			name: "mem trend",
			query: fmt.Sprintf(
				`deriv(container_memory_working_set_bytes{namespace=%q,container!=""}[%s]) * 3600`,
				namespace, window,
			),
			apply: func(u *Usage, v float64) { u.MemTrend = v },
		},
	}

	result := make(map[ContainerKey]Usage)

	for _, q := range queries {
		vec, err := c.queryVector(ctx, q.query, now)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q.name, err)
		}
		for _, s := range vec {
			k := ContainerKey{
				Namespace: string(s.Metric["namespace"]),
				Pod:       string(s.Metric["pod"]),
				Container: string(s.Metric["container"]),
			}
			u := result[k]
			u.HasData = true
			q.apply(&u, float64(s.Value))
			result[k] = u
		}
	}

	return result, nil
}

// PressureAt queries CPU and memory pressure for the given pods during [from, to].
// The window duration is derived from to-from and used as the Prometheus range.
// Returns a map keyed by container name; values are the worst-case (max-across-pods)
// pressure observed. Only containers present in the queried metrics are included.
func (c *Client) PressureAt(ctx context.Context, namespace string, podNames []string, from, to time.Time) (map[string]ResourcePressure, error) {
	if len(podNames) == 0 {
		return nil, nil
	}

	// Build a regex that matches any of the pod names exactly.
	escaped := make([]string, len(podNames))
	for i, p := range podNames {
		escaped[i] = regexp.QuoteMeta(p)
	}
	podRegex := strings.Join(escaped, "|")

	// Round the window up to the nearest second; Prometheus requires an integer.
	windowSecs := int(to.Sub(from).Seconds())
	if windowSecs < 1 {
		windowSecs = 1
	}
	window := fmt.Sprintf("%ds", windowSecs)

	baseSelector := fmt.Sprintf(`namespace=%q,pod=~%q,container!=""`, namespace, podRegex)

	type querySpec struct {
		name  string
		query string
		apply func(p *ResourcePressure, v float64)
	}

	queries := []querySpec{
		{
			// Peak working set — memory is stateful so the max matters more than average.
			name:  "mem peak",
			query: fmt.Sprintf(`max_over_time(container_memory_working_set_bytes{%s}[%s])`, baseSelector, window),
			apply: func(p *ResourcePressure, v float64) {
				if v > p.MemPeakBytes {
					p.MemPeakBytes = v
				}
			},
		},
		{
			name:  "mem request",
			query: fmt.Sprintf(`container_spec_memory_request_bytes{%s}`, baseSelector),
			apply: func(p *ResourcePressure, v float64) {
				if v > 0 && v > p.MemRequestBytes {
					p.MemRequestBytes = v
				}
			},
		},
		{
			name:  "mem limit",
			query: fmt.Sprintf(`container_spec_memory_limit_bytes{%s}`, baseSelector),
			apply: func(p *ResourcePressure, v float64) {
				if v > 0 && v > p.MemLimitBytes {
					p.MemLimitBytes = v
				}
			},
		},
		{
			// Average CPU over the rollout window. rate() over the full window gives
			// a single average-cores value; no subquery needed for a tight window.
			name:  "cpu usage",
			query: fmt.Sprintf(`rate(container_cpu_usage_seconds_total{%s}[%s])`, baseSelector, window),
			apply: func(p *ResourcePressure, v float64) {
				if v > p.CPUCores {
					p.CPUCores = v
				}
			},
		},
		{
			// Throttle ratio: fraction of CPU periods that were throttled during the window.
			name: "cpu throttle",
			query: fmt.Sprintf(
				`rate(container_cpu_cfs_throttled_periods_total{%s}[%s]) / rate(container_cpu_cfs_periods_total{%s}[%s])`,
				baseSelector, window, baseSelector, window,
			),
			apply: func(p *ResourcePressure, v float64) {
				if v > p.CPUThrottleRatio {
					p.CPUThrottleRatio = v
				}
			},
		},
	}

	result := make(map[string]ResourcePressure)

	for _, q := range queries {
		vec, err := c.queryVector(ctx, q.query, to)
		if err != nil {
			return nil, fmt.Errorf("pressure query %s: %w", q.name, err)
		}
		for _, s := range vec {
			container := string(s.Metric["container"])
			if container == "" {
				continue
			}
			p := result[container]
			p.HasData = true
			q.apply(&p, float64(s.Value))
			result[container] = p
		}
	}

	// Compute derived fields now that all queries are done.
	for container, p := range result {
		p.HasMemPressure = p.MemRequestBytes > 0 && p.MemPeakBytes > p.MemRequestBytes
		p.HasCPUThrottle = p.CPUThrottleRatio > 0
		result[container] = p
	}

	return result, nil
}

// subqueryStep picks a step size appropriate for the observation window.
// Short windows (k6 load tests) need fine granularity; long windows
// (historical analysis) use coarser steps to keep query cost down.
func subqueryStep(window string) string {
	d, err := model.ParseDuration(window)
	if err != nil {
		return "5m"
	}
	dur := time.Duration(d)
	switch {
	case dur <= 5*time.Minute:
		return "15s"
	case dur <= time.Hour:
		return "1m"
	default:
		return "5m"
	}
}

func (c *Client) queryVector(ctx context.Context, query string, ts time.Time) (model.Vector, error) {
	result, _, err := c.api.Query(ctx, query, ts)
	if err != nil {
		return nil, err
	}
	vec, ok := result.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("expected vector, got %T", result)
	}
	return vec, nil
}
