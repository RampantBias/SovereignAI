#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

docker buildx build --target controller -t sovereign-controller:dev .
docker buildx build --target api -t sovereign-api:dev .
docker buildx build --target cli -t sovereign-cli:dev .
docker buildx build --target agent-wrapper -t sovereign-agent-wrapper:dev .
docker buildx build --target reference-agent -t sovereign-reference-agent:dev .
docker buildx build --target utility-runner -t sovereign-utility-runner:dev .
docker buildx build --target test-runner -t sovereign-test-runner:dev .
docker buildx build --target build-runner -t sovereign-build-runner:dev .
docker buildx build --target smoke-agent -t sovereign-smoke-agent:dev .
docker buildx build --target mcp-server -t sovereign-mcp-server:dev .
docker buildx build --target artifact-collector -t sovereign-artifact-collector:dev .
docker buildx build --target artifact-bootstrap -t sovereign-artifact-bootstrap:dev .
