param(
    [string]$Output = "dist\sovereign-ai-smoke-bundle.zip"
)

$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
$OutputPath = Join-Path $Root $Output
$OutputDir = Split-Path $OutputPath -Parent

if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir | Out-Null
}

if (Test-Path $OutputPath) {
    Remove-Item -LiteralPath $OutputPath
}

$Items = @(
    "cmd",
    "config",
    "deploy",
    "internal",
    "resources",
    "schemas",
    "Dockerfile",
    "go.mod",
    "go.sum",
    "Makefile",
    "README.md"
)

$Paths = $Items | ForEach-Object { Join-Path $Root $_ }
Compress-Archive -Path $Paths -DestinationPath $OutputPath
Write-Host "Created $OutputPath"
