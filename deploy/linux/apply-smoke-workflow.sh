#!/usr/bin/env bash
set -euo pipefail

VARIANT="${1:-success}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

kubectl apply -f "$ROOT/resources/smoke-namespace.yaml"

case "$VARIANT" in
  success)
    kubectl apply -f "$ROOT/resources/smoke-workflow.yaml"
    ;;
  invalid-result)
    kubectl apply -f "$ROOT/resources/smoke-workflow-invalid-result.yaml"
    ;;
  *)
    echo "unsupported smoke workflow variant: $VARIANT" >&2
    echo "supported variants: success, invalid-result" >&2
    exit 1
    ;;
esac

kubectl -n sovereign-smoke get sovereignworkflow,stepattempt,pod,job,artifact
