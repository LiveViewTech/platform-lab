# psi — Pod Security Inspector

Audits running pods across a Kubernetes cluster for three categories of concern: security context misconfigurations, Istio mesh enrollment status, and NetworkPolicy coverage gaps.

## Install

```bash
# From the repo root
go install github.com/LiveViewTech/platform-lab/tools/pod-security-inspector@latest

# Or build locally
go build -o psi ./tools/pod-security-inspector
```

The repo root also ships a pre-built `psi` binary.

## Commands

### `psi security`

Checks pod and container security contexts for misconfigurations.

```bash
psi security
psi security -n demo-app --findings-only
```

Flags checked per container:

- `runAsNonRoot` not set to `true` (or `runAsUser`/`runAsGroup` == 0)
- `privileged: true`
- `allowPrivilegeEscalation` not explicitly set to `false`
- `readOnlyRootFilesystem` not set to `true`
- Seccomp profile missing or set to `Unconfined`
- Linux capabilities not fully dropped (`capabilities.drop` missing `ALL`)
- Added Linux capabilities (`capabilities.add` non-empty)
- `hostNetwork`, `hostPID`, or `hostIPC` enabled on the pod

### `psi mesh`

Reports Istio mesh enrollment for each pod — sidecar injection, ambient mode, and waypoint proxy status.

```bash
psi mesh
psi mesh -n istio-system --findings-only
```

### `psi netpol`

Checks whether each pod is covered by at least one NetworkPolicy for both ingress and egress.

```bash
psi netpol
psi netpol -n production --findings-only
```

### `psi all`

Runs all three checks in a single pass and displays stacked per-pod output.

```bash
psi all
psi all -n demo-app -o json
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | all | Limit to a single namespace |
| `--output` | `-o` | `table` | Output format: `table` or `json` |
| `--findings-only` | | false | Suppress rows with no issues |
| `--kubeconfig` | | `~/.kube/config` | Path to kubeconfig |
| `--context` | | current context | Kubeconfig context to use |

## Examples

```bash
# Audit entire cluster
psi all

# Show only pods with security issues in a namespace
psi security -n production --findings-only

# JSON output for scripting / CI
psi all -o json | jq '.security[] | select(.issues | length > 0)'

# Use a specific kubeconfig context
psi all --context gke_prod_us-east1_cluster --findings-only

# Check NetworkPolicy gaps across all namespaces
psi netpol --findings-only
```
