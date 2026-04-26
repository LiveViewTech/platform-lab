package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/argo"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/config"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/github"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/gitops"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/inspect"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/plan"
)

const (
	annotationLastRightsized = "kri.io/last-rightsized"
	annotationLastError      = "kri.io/last-error"
	annotationErrorCount     = "kri.io/error-count"
	annotationRightsizing    = "kri.io/rightsizing"

	defaultRequeueInterval    = 6 * time.Hour
	defaultConfidenceThreshold = 0.8
	defaultWindow             = "7d"
	backoffBase               = time.Hour
)

var applicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// RightsizingRunner implements manager.Runnable for schedule-based rightsizing.
// It ticks at the configured requeue_interval and runs the full inspect →
// plan → apply pipeline for each eligible ArgoCD Application.
type RightsizingRunner struct {
	cfg       *config.Config
	dynClient dynamic.Interface
	token     string
	interval  time.Duration
}

// NewRightsizingRunner creates a runner. The tick interval is read from
// cfg.Operator.RequeueInterval (default 6h).
func NewRightsizingRunner(cfg *config.Config, dynClient dynamic.Interface, token string) *RightsizingRunner {
	interval := defaultRequeueInterval
	if cfg.Operator.RequeueInterval != "" {
		if d, err := time.ParseDuration(cfg.Operator.RequeueInterval); err == nil {
			interval = d
		}
	}
	return &RightsizingRunner{cfg: cfg, dynClient: dynClient, token: token, interval: interval}
}

// NeedLeaderElection satisfies manager.LeaderElectionRunnable.
// Returns true so only one operator replica runs the loop at a time.
func (r *RightsizingRunner) NeedLeaderElection() bool { return true }

// Start runs the periodic rightsizing loop until ctx is cancelled.
// It fires once immediately on startup, then on each interval tick.
func (r *RightsizingRunner) Start(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.runOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

// runOnce lists all Applications and runs the pipeline for each eligible one.
func (r *RightsizingRunner) runOnce(ctx context.Context) {
	apps, err := argo.List(ctx, r.dynClient, r.cfg.ArgoNS())
	if err != nil {
		fmt.Fprintf(os.Stderr, "kri-operator: list applications: %v\n", err)
		return
	}

	ignoreApps := toSet(r.cfg.Operator.IgnoreApps)
	ignoreNS := toSet(r.cfg.Operator.IgnoreNamespaces)

	for _, app := range apps {
		if ignoreApps[app.Name] || ignoreNS[app.Namespace] {
			continue
		}
		annotations := r.getAnnotations(ctx, app.Name)
		if annotations[annotationRightsizing] == "disabled" {
			continue
		}
		if !r.shouldRun(annotations) {
			continue
		}
		r.processApp(ctx, app, annotations)
	}
}

// shouldRun returns true when the app is not within its requeue window and
// not within its exponential backoff window from the last error.
func (r *RightsizingRunner) shouldRun(annotations map[string]string) bool {
	now := time.Now()

	if ts, ok := annotations[annotationLastRightsized]; ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			if now.Sub(t) < r.interval {
				return false
			}
		}
	}

	if errTS, ok := annotations[annotationLastError]; ok {
		if t, err := time.Parse(time.RFC3339, errTS); err == nil {
			count, _ := strconv.Atoi(annotations[annotationErrorCount])
			if count < 1 {
				count = 1
			}
			backoff := time.Duration(float64(backoffBase) * math.Pow(2, float64(count-1)))
			if backoff > r.interval {
				backoff = r.interval
			}
			if now.Sub(t) < backoff {
				return false
			}
		}
	}

	return true
}

// processApp runs the full rightsizing pipeline for a single app.
func (r *RightsizingRunner) processApp(ctx context.Context, app argo.App, annotations map[string]string) {
	rows, err := inspect.BuildRows(ctx, r.cfg, r.dynClient, []argo.App{app}, defaultWindow, defaultConfidenceThreshold)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kri-operator: %s: pipeline error: %v\n", app.Name, err)
		r.setErrorAnnotations(ctx, app.Name, annotations)
		return
	}

	plans := plan.Build(rows, defaultWindow)
	if len(plans) == 0 {
		fmt.Fprintf(os.Stderr, "kri-operator: %s: no actionable findings\n", app.Name)
		r.setAnnotation(ctx, app.Name, annotationLastRightsized, time.Now().UTC().Format(time.RFC3339))
		r.clearErrorAnnotations(ctx, app.Name)
		return
	}

	if r.token == "" {
		fmt.Fprintf(os.Stderr, "kri-operator: %s: GITHUB_TOKEN not set; skipping PR\n", app.Name)
		return
	}

	cache := gitops.NewRepoCache(r.token)
	defer cache.Close()

	var errs []error
	for _, p := range plans {
		chartPath := path.Dir(p.ValuesFile)
		branch, err := gitops.PushValuesFile(cache, p.Repo, p.App, chartPath, p.Containers, r.cfg.GitAuthorName(), r.cfg.GitAuthorEmail())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: push: %w", p.App, err))
			continue
		}
		if _, _, err = github.EnsurePR(r.token, p.Repo, branch, p, r.cfg.BaseBranch(), r.cfg.GitHubAPIURL()); err != nil {
			errs = append(errs, fmt.Errorf("%s: PR: %w", p.App, err))
		}
	}

	if err := errors.Join(errs...); err != nil {
		fmt.Fprintf(os.Stderr, "kri-operator: %s: %v\n", app.Name, err)
		r.setErrorAnnotations(ctx, app.Name, annotations)
		return
	}

	r.setAnnotation(ctx, app.Name, annotationLastRightsized, time.Now().UTC().Format(time.RFC3339))
	r.clearErrorAnnotations(ctx, app.Name)
}

// getAnnotations returns the annotations on the named Application CR.
// Returns an empty map on error (safe to read from without nil check).
func (r *RightsizingRunner) getAnnotations(ctx context.Context, appName string) map[string]string {
	obj, err := r.dynClient.Resource(applicationGVR).Namespace(r.cfg.ArgoNS()).Get(ctx, appName, metav1.GetOptions{})
	if err != nil {
		return map[string]string{}
	}
	if a := obj.GetAnnotations(); a != nil {
		return a
	}
	return map[string]string{}
}

// setAnnotation writes a single annotation via merge-patch.
func (r *RightsizingRunner) setAnnotation(ctx context.Context, appName, key, value string) {
	patch, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{key: value},
		},
	})
	if _, err := r.dynClient.Resource(applicationGVR).Namespace(r.cfg.ArgoNS()).Patch(
		ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		fmt.Fprintf(os.Stderr, "kri-operator: annotate %s %s: %v\n", appName, key, err)
	}
}

// setErrorAnnotations bumps error-count and records last-error timestamp.
func (r *RightsizingRunner) setErrorAnnotations(ctx context.Context, appName string, annotations map[string]string) {
	count, _ := strconv.Atoi(annotations[annotationErrorCount])
	count++
	patch, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				annotationLastError:  time.Now().UTC().Format(time.RFC3339),
				annotationErrorCount: strconv.Itoa(count),
			},
		},
	})
	if _, err := r.dynClient.Resource(applicationGVR).Namespace(r.cfg.ArgoNS()).Patch(
		ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		fmt.Fprintf(os.Stderr, "kri-operator: annotate error for %s: %v\n", appName, err)
	}
}

// clearErrorAnnotations removes last-error and error-count on successful run.
func (r *RightsizingRunner) clearErrorAnnotations(ctx context.Context, appName string) {
	patch, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				annotationLastError:  nil,
				annotationErrorCount: nil,
			},
		},
	})
	if _, err := r.dynClient.Resource(applicationGVR).Namespace(r.cfg.ArgoNS()).Patch(
		ctx, appName, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		fmt.Fprintf(os.Stderr, "kri-operator: clear error annotations for %s: %v\n", appName, err)
	}
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
