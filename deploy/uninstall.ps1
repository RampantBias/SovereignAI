param(
    [string]$Overlay = "smoke",
    [switch]$IncludeCrds
)

$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")

kubectl delete -k (Join-Path $Root "deploy\overlays\$Overlay") --ignore-not-found=true

if ($IncludeCrds) {
    kubectl delete -k (Join-Path $Root "config\crd\bases") --ignore-not-found=true
}
