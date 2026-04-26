package inspect

import (
	"context"
	"fmt"
	"os"

	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/analysis"
	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/argo"
	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/config"
	gitvals "github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/git"
	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/hpa"
	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/metrics"
	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/output"
	"github.com/davidacain/platform-lab/tools/k8s-resource-inspector/pkg/pods"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/dynamic"
)

// BuildRows runs the full inspect pipeline and returns one PodRow per container.
// Rows are unsorted and unfiltered — callers apply their own sort/filter.
func BuildRows(ctx context.Context, cfg *config.Config, dynClient dynamic.Interface, apps []argo.App, window string, confidenceThreshold float64) ([]output.PodRow, error) {
	listerCache := map[string]pods.PodLister{}
	metricsCache := map[string]*metrics.Client{}

	type gitCacheKey struct{ repoURL, revision, valuesFile string }
	type gitCacheEntry struct{ config *gitvals.ValuesConfig }
	gitCache := map[gitCacheKey]*gitCacheEntry{}

	var rows []output.PodRow

	for _, app := range apps {
		promURL, ok := cfg.PrometheusFor(app.DestinationName)
		if !ok {
			fmt.Fprintf(os.Stderr, "warn: no Prometheus configured for cluster %q (app %s), skipping\n",
				app.DestinationName, app.Name)
			continue
		}

		lister, ok := listerCache[promURL]
		if !ok {
			var err error
			lister, err = pods.NewPromLister(promURL)
			if err != nil {
				return nil, fmt.Errorf("create pod lister for app %s: %w", app.Name, err)
			}
			listerCache[promURL] = lister
		}

		podList, err := lister.ListPods(ctx, app.Namespace, app.WorkloadFilter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: list pods for app %s: %v\n", app.Name, err)
			continue
		}

		metricsClient, ok := metricsCache[promURL]
		if !ok {
			metricsClient, err = metrics.NewClient(promURL)
			if err != nil {
				return nil, fmt.Errorf("create metrics client for app %s: %w", app.Name, err)
			}
			metricsCache[promURL] = metricsClient
		}

		usageMap, err := metricsClient.UsageMetrics(ctx, app.Namespace, window)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: fetch usage metrics for app %s: %v\n", app.Name, err)
		}

		var gitConfig *gitvals.ValuesConfig
		if app.RepoURL != "" && len(app.ValueFiles) > 0 {
			gk := gitCacheKey{app.RepoURL, app.TargetRevision, app.ValueFiles[0]}
			if cached, ok := gitCache[gk]; ok {
				gitConfig = cached.config
			} else {
				gitConfig, err = gitvals.ReadValues(app.RepoURL, app.TargetRevision, app.Path, app.ValueFiles[0])
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: read git values for app %s: %v\n", app.Name, err)
				}
				gitCache[gk] = &gitCacheEntry{config: gitConfig}
			}
		}

		hpas, err := hpa.List(ctx, dynClient, app.Namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: list HPAs for app %s: %v\n", app.Name, err)
		}
		appHPA := hpa.FindForTarget(hpas, app.Name)

		// Resolve per-app floor/ceiling, falling back to global config.
		cpuFloorMillis := cfg.MinCPUMillis()
		memFloorMi := cfg.MinMemoryMi()
		if !app.CPUFloor.IsZero() {
			cpuFloorMillis = app.CPUFloor.MilliValue()
		}
		if !app.MemFloor.IsZero() {
			memFloorMi = app.MemFloor.Value() / (1024 * 1024)
		}
		cpuCeilingMillis := int64(0)
		memCeilingMi := int64(0)
		if !app.CPUCeiling.IsZero() {
			cpuCeilingMillis = app.CPUCeiling.MilliValue()
		}
		if !app.MemCeiling.IsZero() {
			memCeilingMi = app.MemCeiling.Value() / (1024 * 1024)
		}

		type podWithBehavior struct {
			row      output.PodRow
			behavior analysis.BehaviorClass
		}
		var appPods []podWithBehavior

		for _, p := range podList {
			row := output.PodRow{
				AppName:            app.Name,
				Cluster:            app.DestinationName,
				Namespace:          p.Namespace,
				PodName:            p.PodName,
				WorkloadName:       p.WorkloadName,
				Container:          p.ContainerName,
				CPUReq:             p.CPURequest,
				CPULim:             p.CPULimit,
				MemReq:             p.MemRequest,
				MemLim:             p.MemLimit,
				RepoURL:            app.RepoURL,
				ChartPath:          app.Path,
				HPAWarningDisabled: app.HPAWarningDisabled,
			}

			if u, ok := usageMap[metrics.ContainerKey{
				Namespace: p.Namespace,
				Pod:       p.PodName,
				Container: p.ContainerName,
			}]; ok {
				row.HasData = u.HasData
				row.CPUP50 = u.CPUP50
				row.CPUP95 = u.CPUP95
				row.CPUP99 = u.CPUP99
				row.MemP50 = u.MemP50
				row.MemP95 = u.MemP95
				row.MemP99 = u.MemP99
				row.MemTrend = u.MemTrend

				behavior, confidence := analysis.ClassifyPod(u, p.MemLimit)
				row.Behavior = behavior
				row.Confidence = confidence

				cpuRatio := safeRatio(u.CPUP99, u.CPUP50)
				memRatio := safeRatio(u.MemP99, u.MemP50)
				driver := hpa.RecommendMetricDriver(cpuRatio, memRatio)

				row.HPAStatus = hpa.Validate(appHPA, u, p.CPURequest, p.MemRequest, behavior)

				// Layer 1: HPA present but won't fire — takes full precedence.
				if appHPA != nil && hpa.WontFire(appHPA, u, p.CPURequest, p.MemRequest, driver) {
					hpaRec := analysis.RecommendHPAValues(u, p.CPURequest, p.MemRequest,
						appHPA.CPUTarget, appHPA.MemTarget, appHPA.MinReplicas,
						string(driver), "WontFire")
					row.HPARecommendation = &hpaRec
					row.Recommendation = analysis.Recommendation{Hold: true, HoldReason: "HPA fix takes precedence"}
				} else {
					// Layer 2: behavior-based routing.
					row.Recommendation = analysis.Recommend(behavior, confidence, confidenceThreshold, u,
						p.CPURequest, p.CPULimit, p.MemRequest, p.MemLimit,
						cpuFloorMillis, memFloorMi, cpuCeilingMillis, memCeilingMi)
					row = applyHPARouting(row, appHPA, u, p.CPURequest, p.MemRequest, driver, behavior)
				}

				if gitConfig != nil && !row.Recommendation.Hold && row.Recommendation.Text != "" {
					row.ValuesFilePath = gitConfig.FilePath
				}
			} else {
				row.Behavior = analysis.BehaviorUnknown
				row.HPAStatus = hpa.Validate(appHPA, metrics.Usage{}, p.CPURequest, p.MemRequest, analysis.BehaviorUnknown)
			}

			appPods = append(appPods, podWithBehavior{row: row, behavior: row.Behavior})
		}

		// Workload-level MIXED detection.
		if len(appPods) > 1 {
			behaviors := make([]analysis.BehaviorClass, len(appPods))
			for i, p := range appPods {
				behaviors[i] = p.behavior
			}
			workloadBehavior, _ := analysis.ClassifyWorkload(behaviors)
			if workloadBehavior == analysis.BehaviorMixed {
				for i := range appPods {
					appPods[i].row.Behavior = analysis.BehaviorMixed
					appPods[i].row.Recommendation = analysis.Recommendation{
						Hold:       true,
						HoldReason: "MIXED — investigate pod divergence first",
					}
					appPods[i].row.HPARecommendation = nil
				}
			}
		}

		for _, p := range appPods {
			rows = append(rows, p.row)
		}
	}

	return rows, nil
}

// applyHPARouting applies Layer 2 routing: for behaviors where HPA is the right
// lever (SPIKY with working HPA), replaces the resource recommendation with an
// HPA recommendation. Also sets no-HPA warnings on resource increase PRs.
func applyHPARouting(row output.PodRow, appHPA *hpa.Info, u metrics.Usage, cpuReq, memReq resource.Quantity, driver hpa.MetricDriver, behavior analysis.BehaviorClass) output.PodRow {
	switch behavior {
	case analysis.BehaviorSpiky:
		spikeRatio := safeRatio(u.CPUP99, u.CPUP50)
		if appHPA == nil {
			if !row.HPAWarningDisabled {
				row.HPAWarning = "no HPA configured; consider adding one to handle traffic spikes"
				hpaRec := analysis.RecommendHPAValues(u, cpuReq, memReq, nil, nil, 1, string(driver), "NoHPA")
				row.HPARecommendation = &hpaRec
			}
		} else {
			// HPA present: tune it. For large spikes (>3x), also recommend a
			// maxReplicas increase so the HPA has enough headroom to absorb peaks.
			hpaRec := analysis.RecommendHPAValues(u, cpuReq, memReq,
				appHPA.CPUTarget, appHPA.MemTarget, appHPA.MinReplicas,
				string(driver), "Tuning")
			if spikeRatio > 3.0 {
				recMax := hpa.RecommendMaxReplicasForSpike(appHPA.MaxReplicas, appHPA.MinReplicas, spikeRatio)
				if recMax > 0 {
					hpaRec.MaxReplicas = &recMax
					hpaRec.Text += fmt.Sprintf(", maxReplicas -> %d (spike %.1fx)", recMax, spikeRatio)
				}
			}
			row.HPARecommendation = &hpaRec
			row.Recommendation = analysis.Recommendation{Hold: true, HoldReason: "HPA tuning preferred for SPIKY workload"}
		}

	case analysis.BehaviorGrowth:
		if appHPA == nil {
			if !row.HPAWarningDisabled {
				row.HPAWarning = "no HPA configured; resource increase may be a short-term fix"
				hpaRec := analysis.RecommendHPAValues(u, cpuReq, memReq, nil, nil, 1, string(driver), "NoHPA")
				row.HPARecommendation = &hpaRec
			}
		} else if appHPA.MaxReplicas > appHPA.CurrentReplicas {
			// HPA has headroom — noop, let it scale out.
			row.Recommendation = analysis.Recommendation{Hold: true, HoldReason: "GROWTH — HPA has headroom to scale out"}
		} else {
			// HPA maxed — recommend increasing maxReplicas instead of per-pod resources.
			recMax := hpa.RecommendMaxReplicasForGrowth(appHPA.MaxReplicas, 1.5)
			if recMax > 0 {
				hpaRec := analysis.RecommendHPAValues(u, cpuReq, memReq,
					appHPA.CPUTarget, appHPA.MemTarget, appHPA.MinReplicas,
					string(driver), "Tuning")
				hpaRec.MaxReplicas = &recMax
				hpaRec.Text = fmt.Sprintf("increase maxReplicas -> %d (HPA at ceiling)", recMax)
				row.HPARecommendation = &hpaRec
				row.Recommendation = analysis.Recommendation{Hold: true, HoldReason: fmt.Sprintf("GROWTH — HPA maxed, increase maxReplicas to %d", recMax)}
			}
			// If RecommendMaxReplicasForGrowth returns 0, keep the resource increase.
		}

	case analysis.BehaviorRunaway:
		if appHPA == nil && !row.HPAWarningDisabled {
			row.HPAWarning = "no HPA configured; pod is at OOM risk with no scaling relief"
			hpaRec := analysis.RecommendHPAValues(u, cpuReq, memReq, nil, nil, 1, string(driver), "NoHPA")
			row.HPARecommendation = &hpaRec
		}
		// Resource increase recommendation stands regardless of HPA state.

	case analysis.BehaviorStatic:
		// Resource optimization — no HPA warning needed on reductions.
	}

	return row
}

func safeRatio(p99, p50 float64) float64 {
	if p50 <= 0 {
		return 1.0
	}
	return p99 / p50
}
