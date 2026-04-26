package pods

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Pod holds the inventory data for a single running container.
type Pod struct {
	Namespace     string
	PodName       string
	ContainerName string
	NodeName      string
	WorkloadName  string // Deployment/StatefulSet/DaemonSet name; falls back to pod name
	CPURequest    resource.Quantity
	CPULimit      resource.Quantity
	MemRequest    resource.Quantity
	MemLimit      resource.Quantity
}

// PodLister can list running pods in a namespace.
// workloadFilter is optional — when non-empty, only pods whose names match
// the "{workloadFilter}-*" pattern are returned (scoped at the PromQL level).
type PodLister interface {
	ListPods(ctx context.Context, namespace, workloadFilter string) ([]Pod, error)
}

// PromLister implements PodLister backed by Prometheus kube-state-metrics.
type PromLister struct {
	api v1.API
}

func NewPromLister(prometheusURL string) (*PromLister, error) {
	client, err := api.NewClient(api.Config{Address: prometheusURL})
	if err != nil {
		return nil, fmt.Errorf("create prometheus client: %w", err)
	}
	return &PromLister{api: v1.NewAPI(client)}, nil
}

// ListPods returns running pods in the namespace with their resource configuration.
// When workloadFilter is non-empty, only pods matching "{workloadFilter}-.*" are
// returned — this scopes the PromQL queries so unrelated pods are never fetched.
func (p *PromLister) ListPods(ctx context.Context, namespace, workloadFilter string) ([]Pod, error) {
	now := time.Now()

	type key struct{ namespace, pod, container string }
	pods := make(map[key]*Pod)

	podSelector := ""
	if workloadFilter != "" {
		podSelector = fmt.Sprintf(`,pod=~%q`, workloadFilter+"-.*")
	}

	runningFilter := fmt.Sprintf(`kube_pod_status_phase{namespace=%q%s,phase="Running"} == 1`, namespace, podSelector)

	queries := []struct {
		name     string
		resource string
		kind     string // "requests" or "limits"
	}{
		{"cpu requests", "cpu", "requests"},
		{"cpu limits", "cpu", "limits"},
		{"memory requests", "memory", "requests"},
		{"memory limits", "memory", "limits"},
	}

	type rawResult struct {
		resource string
		kind     string
		vec      model.Vector
	}
	results := make([]rawResult, 0, len(queries))

	for _, q := range queries {
		query := fmt.Sprintf(
			`kube_pod_container_resource_%s{namespace=%q%s,resource=%q,container!=""} * on(namespace,pod) group_left() (%s)`,
			q.kind, namespace, podSelector, q.resource, runningFilter,
		)
		vec, err := p.queryVector(ctx, query, now)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q.name, err)
		}
		results = append(results, rawResult{q.resource, q.kind, vec})
	}

	// Populate pods map from query results.
	for _, r := range results {
		for _, s := range r.vec {
			k := key{
				namespace: string(s.Metric["namespace"]),
				pod:       string(s.Metric["pod"]),
				container: string(s.Metric["container"]),
			}
			if _, ok := pods[k]; !ok {
				pods[k] = &Pod{
					Namespace:     k.namespace,
					PodName:       k.pod,
					ContainerName: k.container,
				}
			}

			val := float64(s.Value)
			switch {
			case r.resource == "cpu" && r.kind == "requests":
				pods[k].CPURequest = *resource.NewMilliQuantity(int64(val*1000), resource.DecimalSI)
			case r.resource == "cpu" && r.kind == "limits":
				pods[k].CPULimit = *resource.NewMilliQuantity(int64(val*1000), resource.DecimalSI)
			case r.resource == "memory" && r.kind == "requests":
				pods[k].MemRequest = *resource.NewQuantity(int64(val), resource.BinarySI)
			case r.resource == "memory" && r.kind == "limits":
				pods[k].MemLimit = *resource.NewQuantity(int64(val), resource.BinarySI)
			}
		}
	}

	// Enrich with node name from kube_pod_info.
	nodeVec, err := p.queryVector(ctx, fmt.Sprintf(`kube_pod_info{namespace=%q%s}`, namespace, podSelector), now)
	if err != nil {
		return nil, fmt.Errorf("query pod info: %w", err)
	}
	nodeMap := make(map[string]string) // pod → node
	for _, s := range nodeVec {
		nodeMap[string(s.Metric["pod"])] = string(s.Metric["node"])
	}
	for k, pod := range pods {
		pod.NodeName = nodeMap[k.pod]
	}

	// Resolve workload name: pod → ReplicaSet/StatefulSet/DaemonSet → Deployment.
	workloadMap, err := p.resolveWorkloadNames(ctx, namespace, podSelector, now)
	if err != nil {
		// Non-fatal — fall back to pod name.
		workloadMap = map[string]string{}
	}
	for k, pod := range pods {
		if wl, ok := workloadMap[k.pod]; ok {
			pod.WorkloadName = wl
		} else {
			pod.WorkloadName = k.pod
		}
	}

	// Return as a flat slice. Order is non-deterministic (map iteration); Phase 7 will sort.
	out := make([]Pod, 0, len(pods))
	for _, pod := range pods {
		out = append(out, *pod)
	}
	return out, nil
}

// resolveWorkloadNames returns a map of pod name → workload name (Deployment/StatefulSet/DaemonSet).
// For Deployment pods it follows pod → ReplicaSet → Deployment.
// For StatefulSet and DaemonSet pods it uses the owner name directly.
func (p *PromLister) resolveWorkloadNames(ctx context.Context, namespace, podSelector string, now time.Time) (map[string]string, error) {
	// pod → immediate owner (RS, SS, DS, etc.)
	ownerVec, err := p.queryVector(ctx,
		fmt.Sprintf(`kube_pod_owner{namespace=%q%s,owner_kind=~"ReplicaSet|StatefulSet|DaemonSet"}`, namespace, podSelector), now)
	if err != nil {
		return nil, fmt.Errorf("query pod owner: %w", err)
	}

	type ownerEntry struct {
		ownerName string
		ownerKind string
	}
	podOwner := make(map[string]ownerEntry) // pod → owner
	for _, s := range ownerVec {
		podOwner[string(s.Metric["pod"])] = ownerEntry{
			ownerName: string(s.Metric["owner_name"]),
			ownerKind: string(s.Metric["owner_kind"]),
		}
	}

	// ReplicaSet → Deployment (follow the chain for Deployment pods).
	rsVec, err := p.queryVector(ctx,
		fmt.Sprintf(`kube_replicaset_owner{namespace=%q,owner_kind="Deployment"}`, namespace), now)
	if err != nil {
		return nil, fmt.Errorf("query replicaset owner: %w", err)
	}
	rsToDeployment := make(map[string]string) // RS name → Deployment name
	for _, s := range rsVec {
		rsToDeployment[string(s.Metric["replicaset"])] = string(s.Metric["owner_name"])
	}

	result := make(map[string]string, len(podOwner))
	for pod, owner := range podOwner {
		switch owner.ownerKind {
		case "ReplicaSet":
			if deployment, ok := rsToDeployment[owner.ownerName]; ok {
				result[pod] = deployment
			} else {
				result[pod] = owner.ownerName // RS without a Deployment owner
			}
		default:
			result[pod] = owner.ownerName // StatefulSet or DaemonSet
		}
	}
	return result, nil
}

func (p *PromLister) queryVector(ctx context.Context, query string, ts time.Time) (model.Vector, error) {
	result, _, err := p.api.Query(ctx, query, ts)
	if err != nil {
		return nil, err
	}
	vec, ok := result.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("expected vector result, got %T", result)
	}
	return vec, nil
}
