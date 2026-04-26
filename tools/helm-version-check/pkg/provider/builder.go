package provider

import (
	"github.com/LiveViewTech/platform-lab/pkg/version"
	"github.com/LiveViewTech/platform-lab/tools/helm-version-check/pkg/config"
)

// FromConfig builds a slice of RepoProviders from the loaded configuration.
// Unknown types are silently skipped — they will be implemented in later phases.
func FromConfig(cfg config.Config) []RepoProvider {
	var providers []RepoProvider
	for _, r := range cfg.Repos {
		switch r.Type {
		case "helm-http":
			providers = append(providers, NewHTTP(r.Name, r.URL, r.Auth))
		// "artifactory" → Phase 3
		// "oci"         → Phase 4
		// "chartmuseum" → Phase 4
		}
	}
	return providers
}

// FindVersion queries each provider in order for the given chart and returns
// the latest version, its app version, all known versions, and which repo matched.
// Returns empty strings (not an error) if the chart is not found in any repo.
func FindVersion(chart string, providers []RepoProvider) (latest, latestAppVersion, repo string, all []string) {
	for _, p := range providers {
		versions, err := p.AllVersions(chart)
		if err != nil {
			// ErrNotFound means this repo doesn't have the chart — try next.
			// Other errors (network, parse) — also skip silently.
			continue
		}
		if len(versions) == 0 {
			continue
		}
		lat := version.Latest(versions)
		return lat, p.AppVersionFor(chart, lat), p.Name(), versions
	}
	return "", "", "", nil
}
