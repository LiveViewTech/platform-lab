package provider

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/LiveViewTech/platform-lab/pkg/version"
	"github.com/LiveViewTech/platform-lab/tools/helm-version-check/pkg/config"
)

// indexFile mirrors the structure of a Helm repo index.yaml.
type indexFile struct {
	Entries map[string][]indexEntry `yaml:"entries"`
}

type indexEntry struct {
	Version    string `yaml:"version"`
	AppVersion string `yaml:"appVersion"`
}

// HTTPProvider queries a Helm HTTP repository via index.yaml.
// The index is fetched once and cached for the lifetime of the provider.
type HTTPProvider struct {
	name   string
	url    string
	auth   config.AuthConfig
	client *http.Client

	once  sync.Once
	index *indexFile
	err   error
}

func NewHTTP(name, url string, auth config.AuthConfig) *HTTPProvider {
	return &HTTPProvider{
		name:   name,
		url:    strings.TrimRight(url, "/"),
		auth:   auth,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *HTTPProvider) Name() string { return p.name }

func (p *HTTPProvider) LatestVersion(chart string) (string, error) {
	all, err := p.AllVersions(chart)
	if err != nil {
		return "", err
	}
	latest := version.Latest(all)
	if latest == "" {
		return "", &ErrNotFound{Chart: chart, Repo: p.name}
	}
	return latest, nil
}

func (p *HTTPProvider) AllVersions(chart string) ([]string, error) {
	idx, err := p.loadIndex()
	if err != nil {
		return nil, err
	}

	entries, ok := idx.Entries[chart]
	if !ok || len(entries) == 0 {
		return nil, &ErrNotFound{Chart: chart, Repo: p.name}
	}

	versions := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Version != "" {
			versions = append(versions, e.Version)
		}
	}
	return versions, nil
}

func (p *HTTPProvider) AppVersionFor(chart, chartVersion string) string {
	idx, err := p.loadIndex()
	if err != nil {
		return ""
	}
	for _, e := range idx.Entries[chart] {
		if e.Version == chartVersion {
			return e.AppVersion
		}
	}
	return ""
}

// loadIndex fetches and parses index.yaml exactly once, caching the result.
func (p *HTTPProvider) loadIndex() (*indexFile, error) {
	p.once.Do(func() {
		p.index, p.err = fetchIndex(p.client, p.url+"/index.yaml", p.auth)
	})
	return p.index, p.err
}

func fetchIndex(client *http.Client, url string, auth config.AuthConfig) (*indexFile, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}

	switch auth.Type {
	case "basic":
		req.SetBasicAuth(auth.Username, auth.Password)
	case "token":
		req.Header.Set("Authorization", "Bearer "+auth.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}

	var idx indexFile
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index.yaml from %s: %w", url, err)
	}
	return &idx, nil
}
