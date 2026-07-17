#!/usr/bin/env bash
set -euo pipefail

watch -n 2 'kubectl -n sovereign-smoke get sovereignworkflow,stepattempt,pod,job,artifact'
