package argo

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var applicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// App is a minimal representation of an ArgoCD Application CR.
type App struct {
	Name            string
	Namespace       string   // spec.destination.namespace
	DestinationName string   // spec.destination.name — used to look up Prometheus endpoint
	RepoURL         string   // spec.source.repoURL
	TargetRevision  string   // spec.source.targetRevision
	Path            string   // spec.source.path — chart directory within the repo
	ValueFiles      []string // spec.source.helm.valueFiles

	// WorkloadFilter restricts BuildRows to pods whose resolved WorkloadName
	// matches this value or starts with "{value}-". When empty, all pods in
	// the namespace are included (the default for ArgoCD-sourced apps that own
	// their namespace). Set this when evaluating a single workload in a shared
	// namespace (e.g. an Argo Rollout).
	WorkloadFilter string

	// Per-app kri annotation overrides.
	HPAWarningDisabled bool            // kri.io/hpa-warning: "disabled"
	CPUFloor           resource.Quantity // kri.io/cpu-floor — minimum CPU request/limit kri will recommend
	CPUCeiling         resource.Quantity // kri.io/cpu-ceiling — maximum CPU request/limit kri will recommend
	MemFloor           resource.Quantity // kri.io/memory-floor — minimum memory request/limit
	MemCeiling         resource.Quantity // kri.io/memory-ceiling — maximum memory request/limit
}

// List reads all ArgoCD Application CRs from the given namespace.
func List(ctx context.Context, dynClient dynamic.Interface, argoNamespace string) ([]App, error) {
	list, err := dynClient.Resource(applicationGVR).Namespace(argoNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list Applications in namespace %q: %w", argoNamespace, err)
	}

	apps := make([]App, 0, len(list.Items))
	for _, item := range list.Items {
		spec, _ := item.Object["spec"].(map[string]interface{})
		if spec == nil {
			continue
		}

		dest, _ := spec["destination"].(map[string]interface{})
		destName, _ := dest["name"].(string)
		destNS, _ := dest["namespace"].(string)

		// ArgoCD supports both spec.source (single) and spec.sources (multi).
		var repoURL, targetRevision, appPath string
		var valueFiles []string

		if rawSources, hasSources := spec["sources"]; hasSources {
			sources, _ := rawSources.([]interface{})
			var ok bool
			repoURL, targetRevision, appPath, valueFiles, ok = parseMultiSource(sources)
			if !ok {
				fmt.Fprintf(os.Stderr, "warn: app %q: multi-source spec missing values source or helm source; skipping\n", item.GetName())
				continue
			}
		} else {
			source, _ := spec["source"].(map[string]interface{})
			if source == nil {
				continue
			}
			repoURL, _ = source["repoURL"].(string)
			targetRevision, _ = source["targetRevision"].(string)
			appPath, _ = source["path"].(string)
			if helm, ok := source["helm"].(map[string]interface{}); ok {
				if vf, ok := helm["valueFiles"].([]interface{}); ok {
					for _, f := range vf {
						if s, ok := f.(string); ok {
							valueFiles = append(valueFiles, s)
						}
					}
				}
			}
		}

		ann := item.GetAnnotations()
		app := App{
			Name:            item.GetName(),
			Namespace:       destNS,
			DestinationName: destName,
			RepoURL:         repoURL,
			TargetRevision:  targetRevision,
			Path:            appPath,
			ValueFiles:      valueFiles,
			HPAWarningDisabled: ann["kri.io/hpa-warning"] == "disabled",
		}
		app.CPUFloor = parseQuantityAnnotation(ann, "kri.io/cpu-floor")
		app.CPUCeiling = parseQuantityAnnotation(ann, "kri.io/cpu-ceiling")
		app.MemFloor = parseQuantityAnnotation(ann, "kri.io/memory-floor")
		app.MemCeiling = parseQuantityAnnotation(ann, "kri.io/memory-ceiling")
		apps = append(apps, app)
	}

	return apps, nil
}

// parseQuantityAnnotation parses a resource.Quantity from an annotation value.
// Returns a zero Quantity if the annotation is absent or unparseable.
func parseQuantityAnnotation(ann map[string]string, key string) resource.Quantity {
	v, ok := ann[key]
	if !ok || v == "" {
		return resource.Quantity{}
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return resource.Quantity{}
	}
	return q
}

// parseMultiSource extracts App fields from a multi-source Application spec.
// It expects a source with ref: "values" (the write target) and a Helm source
// (no ref) whose valueFiles contains a $values/... entry to derive the path.
func parseMultiSource(sources []interface{}) (repoURL, targetRevision, appPath string, valueFiles []string, ok bool) {
	var valuesSource, helmSource map[string]interface{}

	for _, s := range sources {
		src, _ := s.(map[string]interface{})
		if src == nil {
			continue
		}
		ref, _ := src["ref"].(string)
		switch ref {
		case "values":
			valuesSource = src
		case "":
			helmSource = src
		}
	}

	if valuesSource == nil || helmSource == nil {
		return "", "", "", nil, false
	}

	repoURL, _ = valuesSource["repoURL"].(string)
	targetRevision, _ = valuesSource["targetRevision"].(string)

	if helm, ok2 := helmSource["helm"].(map[string]interface{}); ok2 {
		if vf, ok3 := helm["valueFiles"].([]interface{}); ok3 {
			for _, f := range vf {
				if s, ok4 := f.(string); ok4 {
					valueFiles = append(valueFiles, s)
				}
			}
		}
	}

	// Derive write-target directory from the first $values/... entry in valueFiles.
	for _, vf := range valueFiles {
		if strings.HasPrefix(vf, "$values/") {
			rel := strings.TrimPrefix(vf, "$values/")
			appPath = path.Dir(rel)
			if appPath == "." {
				appPath = ""
			}
			break
		}
	}

	return repoURL, targetRevision, appPath, valueFiles, true
}
