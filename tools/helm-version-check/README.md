# hvc — Helm Version Check

Compares deployed Helm releases in a Kubernetes cluster against the latest versions available in upstream chart repositories. Useful for keeping a quick eye on drift without leaving the terminal.

## Install

```bash
# From the repo root
go install github.com/LiveViewTech/platform-lab/tools/helm-version-check@latest

# Or build locally
go build -o hvc ./tools/helm-version-check
```

The repo root also ships a pre-built `hvc` binary.

## Commands

### `hvc check`

Fetches deployed releases and compares them to the latest chart version in configured repos.

```
RELEASE      NAMESPACE   CHART         CHART_VER  L_CHART_VER  APP_VER  L_APP_VER  BEHIND  REPO
cert-manager  cert-manager  cert-manager  v1.13.0    v1.17.2      v1.13.0  v1.17.2    4       jetstack
ingress-nginx kube-system   ingress-nginx 4.8.3      4.12.1       1.9.4    1.12.1     3       ingress-nginx
```

Row colors:
- **Green** — up to date
- **Yellow** — minor or patch versions behind
- **Red** — major version behind
- *Dim* — chart not found in any configured repo

### `hvc list`

Lists all deployed Helm releases without any version comparison.

```bash
hvc list
hvc list -n kube-system
hvc list -o json
```

## Configuration

`hvc check` requires a config file at `~/.hvc/config.yaml` (override with `--config`).

```yaml
repos:
  - name: jetstack
    type: helm-http
    url: https://charts.jetstack.io

  - name: ingress-nginx
    type: helm-http
    url: https://kubernetes.github.io/ingress-nginx

  # Auth example (token or basic)
  - name: internal
    type: helm-http
    url: https://charts.example.com
    auth:
      type: token
      token: ${CHART_REPO_TOKEN}   # env var interpolation supported
```

Supported repo types:

| Type | Status |
|------|--------|
| `helm-http` | Supported |
| `artifactory` | Planned |
| `oci` | Planned |
| `chartmuseum` | Planned |

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | all | Limit to a single namespace |
| `--output` | `-o` | `table` | Output format: `table` or `json` |
| `--kubeconfig` | | `~/.kube/config` | Path to kubeconfig |
| `--context` | | current context | Kubeconfig context to use |
| `--config` | | `~/.hvc/config.yaml` | Config file path |

## Examples

```bash
# Check all namespaces against configured repos
hvc check

# Check a single namespace, JSON output
hvc check -n kube-system -o json

# Use a non-default kubeconfig and context
hvc check --kubeconfig ~/clusters/prod.yaml --context gke_prod_us-east1_cluster

# Pipe outdated releases to a script
hvc check -o json | jq '[.[] | select(.up_to_date == false)]'
```
