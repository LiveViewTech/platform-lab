package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Clusters        []ClusterConfig     `yaml:"clusters"`
	Minimums        Minimums            `yaml:"minimums"`
	ArgoCDNamespace string              `yaml:"argocd_namespace"`
	Git             GitConfig           `yaml:"git"`
	GitHub          GitHubConfig        `yaml:"github"`
	Coralogix       CoralogixConfig     `yaml:"coralogix"`
	Notifications   NotificationsConfig `yaml:"notifications"`
	Operator        OperatorConfig      `yaml:"operator"`
}

// CoralogixConfig holds Coralogix log query settings.
type CoralogixConfig struct {
	// APIKey is the Coralogix API key. Typically injected via ${CORALOGIX_API_KEY}.
	APIKey string `yaml:"api_key"`

	// Endpoint is the Coralogix DataPrime query endpoint (e.g. https://api.coralogix.us/api/v1/dataprime/query).
	Endpoint string `yaml:"endpoint"`

	// AppNameField is the log attribute used to match ArgoCD app names
	// (e.g. "resource.attributes.k8s_deployment_name"). Set to "" to query by pod name instead.
	AppNameField string `yaml:"app_name_field"`

	// AppNameSuffixes lists environment suffixes to strip from ArgoCD app names
	// to derive the Deployment name (e.g. ["-prod", "-staging", "-dev"]).
	AppNameSuffixes []string `yaml:"app_name_suffixes"`

	// SeverityFilter lists log severities to retrieve for diagnosis (e.g. ["ERROR", "CRITICAL"]).
	SeverityFilter []string `yaml:"severity_filter"`

	// LogWindowSecs is the number of seconds after the probe failure timestamp to include in the query window.
	LogWindowSecs int `yaml:"log_window_seconds"`
}

// SlackConfig holds Slack notification settings.
type SlackConfig struct {
	WebhookURL string `yaml:"webhook_url"`
	Channel    string `yaml:"channel"`
}

// NotificationsConfig holds notification channel settings.
type NotificationsConfig struct {
	Slack SlackConfig `yaml:"slack"`
}

// GitConfig holds committer identity for kri-authored commits.
type GitConfig struct {
	AuthorName  string `yaml:"author_name"`
	AuthorEmail string `yaml:"author_email"`
}

// GitHubConfig holds GitHub-specific settings.
type GitHubConfig struct {
	BaseBranch string `yaml:"base_branch"` // default "main"
	APIURL     string `yaml:"api_url"`     // default "https://api.github.com"
}

type ClusterConfig struct {
	ArgoCDCluster string `yaml:"argocd_cluster"`
	Prometheus    string `yaml:"prometheus"`
}

// Minimums defines floor values for resource recommendations.
type Minimums struct {
	CPUMillis int64  `yaml:"cpu_millicores"` // default 10
	MemoryMi  int64  `yaml:"memory_mi"`      // default 16
}

// MinCPUMillis returns the configured minimum CPU in millicores, or 10 if unset.
func (c *Config) MinCPUMillis() int64 {
	if c.Minimums.CPUMillis > 0 {
		return c.Minimums.CPUMillis
	}
	return 10
}

// MinMemoryMi returns the configured minimum memory in Mi, or 32 if unset.
// 32Mi is the practical floor for compiled-language runtimes (Go, Rust) that
// need headroom for GC, stack growth, and startup allocation.
func (c *Config) MinMemoryMi() int64 {
	if c.Minimums.MemoryMi > 0 {
		return c.Minimums.MemoryMi
	}
	return 32
}

// ArgoNS returns the configured ArgoCD namespace, or "argocd" if unset.
func (c *Config) ArgoNS() string {
	if c.ArgoCDNamespace != "" {
		return c.ArgoCDNamespace
	}
	return "argocd"
}

// GitAuthorName returns the configured git author name, or "kri" if unset.
func (c *Config) GitAuthorName() string {
	if c.Git.AuthorName != "" {
		return c.Git.AuthorName
	}
	return "kri"
}

// GitAuthorEmail returns the configured git author email, or "kri@noreply.local" if unset.
func (c *Config) GitAuthorEmail() string {
	if c.Git.AuthorEmail != "" {
		return c.Git.AuthorEmail
	}
	return "kri@noreply.local"
}

// BaseBranch returns the configured PR base branch, or "main" if unset.
func (c *Config) BaseBranch() string {
	if c.GitHub.BaseBranch != "" {
		return c.GitHub.BaseBranch
	}
	return "main"
}

// GitHubAPIURL returns the configured GitHub API URL, or the public GitHub URL if unset.
func (c *Config) GitHubAPIURL() string {
	if c.GitHub.APIURL != "" {
		return c.GitHub.APIURL
	}
	return "https://api.github.com"
}

// OperatorConfig holds kri-operator-specific settings, nested under the
// "operator" key in the shared config file.
type OperatorConfig struct {
	// RequeueInterval is how often the operator re-runs the full inspection
	// loop (e.g. "6h"). Defaults to "6h" if unset.
	RequeueInterval string `yaml:"requeue_interval"`

	// DiagnosisCooldown is the minimum time between successive annotation
	// updates for the same app (e.g. "1h"). Defaults to "1h" if unset.
	DiagnosisCooldown string `yaml:"diagnosis_cooldown"`

	// IgnoreApps lists ArgoCD Application names the operator should skip.
	IgnoreApps []string `yaml:"ignore_apps"`

	// IgnoreNamespaces lists namespaces the operator should skip.
	IgnoreNamespaces []string `yaml:"ignore_namespaces"`
}

// Load reads the kri config file. If path is empty, defaults to ~/.kri/config.yaml.
func Load(path string) (*Config, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		path = filepath.Join(home, ".kri", "config.yaml")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.Coralogix.APIKey = os.Expand(cfg.Coralogix.APIKey, os.Getenv)
	cfg.Notifications.Slack.WebhookURL = os.Expand(cfg.Notifications.Slack.WebhookURL, os.Getenv)

	return &cfg, nil
}

// PrometheusFor returns the Prometheus URL for the given ArgoCD cluster name.
func (c *Config) PrometheusFor(clusterName string) (string, bool) {
	for _, cl := range c.Clusters {
		if cl.ArgoCDCluster == clusterName {
			return cl.Prometheus, true
		}
	}
	return "", false
}
