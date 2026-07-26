#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

docker build --target controller -t sovereign-controller:dev .
docker build --target api -t sovereign-api:dev .
docker build --target cli -t sovereign-cli:dev .
docker build --target agent-wrapper -t sovereign-agent-wrapper:dev .
docker build --target utility-runner -t sovereign-utility-runner:dev .
docker build --target smoke-agent -t sovereign-smoke-agent:dev .
docker build --target mcp-server -t sovereign-mcp-server:dev .
docker build --target artifact-collector -t sovereign-artifact-collector:dev .
docker build --target artifact-bootstrap -t sovereign-artifact-bootstrap:dev .
