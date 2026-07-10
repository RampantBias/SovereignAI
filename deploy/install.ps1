param(
    [string]$Overlay = "smoke",
    [switch]$SkipCrds
)

$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")

if (-not $SkipCrds) {
    kubectl apply -k (Join-Path $Root "config\crd\bases")
}

kubectl apply -k (Join-Path $Root "deploy\overlays\$Overlay")
