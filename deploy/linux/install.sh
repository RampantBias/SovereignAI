#!/usr/bin/env bash
set -euo pipefail

OVERLAY="${1:-smoke}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

kubectl apply -k "$ROOT/config/crd/bases"
kubectl apply -k "$ROOT/deploy/overlays/$OVERLAY"
