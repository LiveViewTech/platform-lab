package cmd

import (
	"sort"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/output"
)

// sortRows sorts rows into canonical order: AppName → Namespace → PodName → Container.
func sortRows(rows []output.PodRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.AppName != b.AppName {
			return a.AppName < b.AppName
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.PodName != b.PodName {
			return a.PodName < b.PodName
		}
		return a.Container < b.Container
	})
}
