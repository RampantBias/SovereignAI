#!/usr/bin/env bash
set -euo pipefail

OVERLAY="${1:-smoke}"
AUTH_OUTPUT_DIR="${2:-${SOVEREIGN_DEMO_AUTH_OUTPUT_DIR:-}}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

kubectl apply -k "$ROOT/config/crd/bases"

if [[ -n "$AUTH_OUTPUT_DIR" ]]; then
  bash "$ROOT/deploy/linux/bootstrap-demo-auth.sh" "$AUTH_OUTPUT_DIR"
elif ! kubectl -n sovereign-orchestrator-system get secret sovereign-api-tls >/dev/null 2>&1 ||
     ! kubectl -n sovereign-orchestrator-system get secret sovereign-human-auth >/dev/null 2>&1; then
  echo "API authentication Secrets are missing." >&2
  echo "Rerun with: $0 $OVERLAY /secure/sovereign-demo" >&2
  echo "or set SOVEREIGN_DEMO_AUTH_OUTPUT_DIR to a protected directory outside the repository." >&2
  exit 1
fi

kubectl apply -k "$ROOT/deploy/overlays/$OVERLAY"
