#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIR="${1:-}"

if [[ -z "$OUTPUT_DIR" ]]; then
  echo "usage: $0 /secure/output/directory" >&2
  exit 2
fi

cd "$ROOT"
kubectl apply -f deploy/base/namespace.yaml
go run ./cmd/demo-credentials --output-dir "$OUTPUT_DIR"

if kubectl -n sovereign-orchestrator-system get deployment sovereign-api >/dev/null 2>&1; then
  kubectl -n sovereign-orchestrator-system rollout restart deployment/sovereign-api
  kubectl -n sovereign-orchestrator-system rollout status deployment/sovereign-api --timeout=3m
fi
