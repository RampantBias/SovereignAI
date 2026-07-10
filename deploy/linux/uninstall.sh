#!/usr/bin/env bash
set -euo pipefail

OVERLAY="${1:-smoke}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

kubectl delete -k "$ROOT/deploy/overlays/$OVERLAY" --ignore-not-found=true

if [[ "${INCLUDE_CRDS:-false}" == "true" ]]; then
  kubectl delete -k "$ROOT/config/crd/bases" --ignore-not-found=true
fi
