# SovereignAI Deployment

This directory contains installable Kubernetes packaging for SovereignAI.

Generated CRD manifests remain under `config/crd/bases`. That directory is
also a Kustomize package so API generation and deployment packaging stay
separate without duplicating CRD YAML.

## Profiles

| Path | Purpose |
| --- | --- |
| `../config/crd/bases` | Installs SovereignAI custom resource definitions. |
| `crds` | Documents the CRD deployment boundary without duplicating generated manifests. |
| `base` | Installs the core control plane: controller, API, MCP service, RBAC, and namespace. |
| `overlays/smoke` | Minimal development profile for local cluster testing. |
| `demo` | Installs the control plane components needed for the demo. |

## Install

The controller and API require the generated `sovereign-audit-client` Secret.
Create PostgreSQL and that Secret in the order documented in
[`../docs/GettingStarted.md`](../docs/GettingStarted.md) before applying the
control plane.

```bash
kubectl apply -k config/crd/bases
```

```bash
mkdir -p ~/sovereign-ai
unzip -o /tmp/sovereign-ai-smoke-bundle.zip -d ~/sovereign-ai
cd ~/sovereign-ai
bash deploy/linux/build-images.sh
bash deploy/linux/load-k3s-images.sh
```

For a non-k3s cluster, push the images to a registry reachable by the cluster
and patch the image names in the selected overlay/workflow.

Smoke workflow variants:

```bash
bash deploy/linux/apply-smoke-workflow.sh success
bash deploy/linux/apply-smoke-workflow.sh invalid-result
```

To show conflict retry logs while debugging reconciliation timing:

```bash
kubectl -n sovereign-orchestrator-system set env deploy/sovereign-controller SOVEREIGN_LOG_VERBOSITY=1
kubectl -n sovereign-orchestrator-system logs deploy/sovereign-controller -f
```

## Uninstall

Remove control-plane workloads while keeping CRDs and workflow data:

Workflow namespaces, PVCs, and external audit storage should be handled
deliberately before a destructive purge flow is added.

