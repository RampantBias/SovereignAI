param(
    [string]$Overlay = "smoke",
    [switch]$SkipCrds,
    [string]$DemoAuthOutputDir = $env:SOVEREIGN_DEMO_AUTH_OUTPUT_DIR
)

$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")

if (-not $SkipCrds) {
    kubectl apply -k (Join-Path $Root "config\crd\bases")
}

if ($DemoAuthOutputDir) {
    kubectl apply -f (Join-Path $Root "deploy\base\namespace.yaml")
    Push-Location $Root
    try {
        go run ./cmd/demo-credentials --output-dir $DemoAuthOutputDir
    } finally {
        Pop-Location
    }
} else {
    kubectl -n sovereign-orchestrator-system get secret sovereign-api-tls *> $null
    $TlsExists = $LASTEXITCODE -eq 0
    kubectl -n sovereign-orchestrator-system get secret sovereign-human-auth *> $null
    $AuthExists = $LASTEXITCODE -eq 0
    if (-not ($TlsExists -and $AuthExists)) {
        throw "API authentication Secrets are missing. Rerun with -DemoAuthOutputDir pointing to a protected directory outside the repository."
    }
}

kubectl apply -k (Join-Path $Root "deploy\overlays\$Overlay")
