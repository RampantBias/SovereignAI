[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string] $RemoteUrl
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-RemoteHeads {
    param(
        [Parameter(Mandatory)]
        [string] $RepositoryUrl
    )

    $heads = @(& git ls-remote --heads $RepositoryUrl)
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to query the remote repository."
    }
    return $heads
}

function Assert-RemoteEmpty {
    param(
        [Parameter(Mandatory)]
        [string] $RepositoryUrl
    )

    $heads = @(Get-RemoteHeads -RepositoryUrl $RepositoryUrl)
    if ($heads.Count -ne 0) {
        throw "The remote repository already contains a branch; refusing to seed it."
    }
}

function Invoke-Git {
    param(
        [Parameter(Mandatory)]
        [string] $WorkingDirectory,

        [Parameter(Mandatory)]
        [string[]] $Arguments,

        [Parameter(Mandatory)]
        [string] $Operation
    )

    & git -C $WorkingDirectory @Arguments | Out-Null
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "Git operation '$Operation' failed with exit code $exitCode."
    }
}

try {
    $remoteUri = [System.Uri]::new($RemoteUrl, [System.UriKind]::Absolute)
}
catch {
    throw "RemoteUrl must be an absolute HTTPS URL without embedded credentials."
}

if (
    $remoteUri.Scheme -ne "https" -or
    [string]::IsNullOrWhiteSpace($remoteUri.Host) -or
    -not [string]::IsNullOrEmpty($remoteUri.UserInfo) -or
    -not [string]::IsNullOrEmpty($remoteUri.Query) -or
    -not [string]::IsNullOrEmpty($remoteUri.Fragment)
) {
    throw "RemoteUrl must be an absolute HTTPS URL without embedded credentials, a query, or a fragment."
}

# Refuse before creating any local Git repository. An unreachable remote is an
# error, not evidence that the remote is empty.
Assert-RemoteEmpty -RepositoryUrl $RemoteUrl

$scriptDirectory = Split-Path -Parent $PSCommandPath
$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $scriptDirectory "..\..")).Path
$seedRoot = (Resolve-Path -LiteralPath (Join-Path $repositoryRoot "demo\calculator")).Path
$seedPrefix = "demo/calculator/"

$seedChanges = @(& git -C $repositoryRoot status --porcelain=v1 --untracked-files=all -- demo/calculator)
if ($LASTEXITCODE -ne 0) {
    throw "Unable to inspect the calculator seed."
}
if ($seedChanges.Count -ne 0) {
    throw "The calculator seed has uncommitted changes; commit and review it before seeding."
}

$trackedFiles = @(& git -C $repositoryRoot ls-files -- demo/calculator)
if ($LASTEXITCODE -ne 0) {
    throw "Unable to enumerate the calculator seed."
}
if ($trackedFiles.Count -eq 0) {
    throw "The calculator seed contains no tracked files."
}

$temporaryParent = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$temporaryDirectory = Join-Path $temporaryParent ("sovereign-calculator-seed-" + [System.Guid]::NewGuid().ToString("N"))
$temporaryDirectory = [System.IO.Path]::GetFullPath($temporaryDirectory)
$expectedPrefix = $temporaryParent.TrimEnd(
    [System.IO.Path]::DirectorySeparatorChar,
    [System.IO.Path]::AltDirectorySeparatorChar
) + [System.IO.Path]::DirectorySeparatorChar

if (-not $temporaryDirectory.StartsWith($expectedPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to create a seed workspace outside the system temporary directory."
}

New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null

try {
    # Copy only files tracked by SovereignAI. Ignored build outputs, credentials,
    # and a nested .git directory can therefore never enter the seed commit.
    foreach ($trackedFile in $trackedFiles) {
        if (-not $trackedFile.StartsWith($seedPrefix, [System.StringComparison]::Ordinal)) {
            throw "Tracked seed path escaped the calculator directory."
        }

        $relativePath = $trackedFile.Substring($seedPrefix.Length)
        $sourcePath = Join-Path $repositoryRoot $trackedFile
        $destinationPath = Join-Path $temporaryDirectory $relativePath
        $destinationParent = Split-Path -Parent $destinationPath
        New-Item -ItemType Directory -Path $destinationParent -Force | Out-Null
        Copy-Item -LiteralPath $sourcePath -Destination $destinationPath
    }

    # Git 2.24 (still common on Windows) predates `git init
    # --initial-branch`. Set the unborn branch explicitly instead.
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("init") -Operation "initialize temporary repository"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("symbolic-ref", "HEAD", "refs/heads/main") -Operation "select main as the initial branch"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("config", "user.name", "SovereignAI Demo Seeder") -Operation "configure seed author name"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("config", "user.email", "sovereign-demo@invalid") -Operation "configure seed author email"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("config", "commit.gpgSign", "false") -Operation "disable commit signing for the seed"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("config", "core.autocrlf", "false") -Operation "disable seed line-ending conversion"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("add", "--all") -Operation "stage calculator seed files"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("commit", "--no-gpg-sign", "-m", "Seed calculator baseline") -Operation "create calculator seed commit"
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("remote", "add", "origin", $RemoteUrl) -Operation "configure clean origin URL"

    $localCommitOutput = @(& git -C $temporaryDirectory rev-parse HEAD)
    if ($LASTEXITCODE -ne 0 -or $localCommitOutput.Count -ne 1) {
        throw "Unable to resolve the local seed commit."
    }
    $localCommit = $localCommitOutput[0].Trim()

    # Close the ordinary check-to-push window as much as possible. The push is
    # deliberately non-force and is followed by an independent remote proof.
    Assert-RemoteEmpty -RepositoryUrl $RemoteUrl
    Invoke-Git -WorkingDirectory $temporaryDirectory -Arguments @("push", "origin", "HEAD:refs/heads/main") -Operation "push seed commit to remote main"

    $remoteHeads = @(Get-RemoteHeads -RepositoryUrl $RemoteUrl)
    if ($remoteHeads.Count -ne 1) {
        throw "Remote verification expected exactly one branch."
    }

    $remoteFields = @($remoteHeads[0] -split "\s+")
    if (
        $remoteFields.Count -ne 2 -or
        $remoteFields[1] -ne "refs/heads/main" -or
        $remoteFields[0] -ne $localCommit
    ) {
        throw "Remote main did not resolve to the reviewed seed commit."
    }

    [pscustomobject]@{
        RemoteUrl = $RemoteUrl
        Branch    = "main"
        Commit    = $localCommit
    }
}
finally {
    if (
        (Test-Path -LiteralPath $temporaryDirectory) -and
        $temporaryDirectory.StartsWith($expectedPrefix, [System.StringComparison]::OrdinalIgnoreCase)
    ) {
        Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
    }
}
