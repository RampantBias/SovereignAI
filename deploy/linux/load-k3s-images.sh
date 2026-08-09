#!/usr/bin/env bash
set -euo pipefail

IMAGES=(
  sovereign-controller:dev
  sovereign-api:dev
  sovereign-cli:dev
  sovereign-agent-wrapper:dev
  sovereign-reference-agent:dev
  sovereign-utility-runner:dev
  sovereign-smoke-agent:dev
  sovereign-mcp-server:dev
  sovereign-artifact-collector:dev
  sovereign-artifact-bootstrap:dev
)

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to save local images" >&2
  exit 1
fi

if ! command -v k3s >/dev/null 2>&1; then
  echo "k3s is required for direct image import; use a registry on non-k3s clusters" >&2
  exit 1
fi

tmp="$(mktemp -t sovereign-images.XXXXXX.tar)"
trap 'rm -f "$tmp"' EXIT

docker save "${IMAGES[@]}" -o "$tmp"
sudo k3s ctr images import "$tmp"
