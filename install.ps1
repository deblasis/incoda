#Requires -Version 5.1
<#
.SYNOPSIS
    Installs the incoda binary on Windows from a private GitHub release.

.DESCRIPTION
    The repository is private, so release assets cannot be fetched with a plain
    web request: they need an authenticated API call. This script uses the gh
    CLI for that, verifies the download against SHA256SUMS, and refuses to
    install anything it could not verify.

.PARAMETER Version
    Release tag to install, e.g. v0.1.0. Defaults to the latest release.

.PARAMETER InstallDir
    Where to put incoda.exe. Defaults to %LOCALAPPDATA%\Programs\incoda.
#>
[CmdletBinding()]
param(
    [string]$Version = '',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\incoda'),
    [string]$Repo = 'deblasis/incoda'
)

$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
    Write-Error $Message
    exit 1
}

# --- preconditions -------------------------------------------------------

if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
    Fail @'
gh (the GitHub CLI) is not on PATH.

incoda is distributed from a PRIVATE repository, so its release assets cannot be
downloaded without authentication. Install gh from https://cli.github.com/ and
run `gh auth login`, then re-run this script.
'@
}

& gh auth status *> $null
if ($LASTEXITCODE -ne 0) {
    Fail @'
gh is installed but not authenticated. Run `gh auth login` and try again.

Downloading assets from a PRIVATE repository needs a token with `repo` scope.
Check yours with `gh auth status`.
'@
}

# --- pick the asset ------------------------------------------------------

switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    'x86'   { Fail 'incoda has no 32-bit Windows build.' }
    default { Fail "Unsupported processor architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}
$asset = "incoda_windows_$arch.exe"

if (-not $Version) {
    $Version = (& gh release view --repo $Repo --json tagName --jq .tagName 2>$null)
    if ($LASTEXITCODE -ne 0 -or -not $Version) {
        Fail "Could not determine the latest release of $Repo. Is the tag pushed and the release published?"
    }
}
Write-Host "incoda: installing $Version ($asset) from $Repo"

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("incoda-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work -Force | Out-Null
try {
    & gh release download $Version --repo $Repo --pattern $asset --pattern 'SHA256SUMS' --dir $work
    if ($LASTEXITCODE -ne 0) { Fail "gh release download failed for $Version." }

    $binary = Join-Path $work $asset
    $sums   = Join-Path $work 'SHA256SUMS'
    if (-not (Test-Path $binary)) { Fail "The release does not contain $asset." }
    if (-not (Test-Path $sums))   { Fail 'The release does not contain SHA256SUMS; refusing to install an unverified binary.' }

    # --- verify ----------------------------------------------------------

    $expected = $null
    foreach ($line in Get-Content $sums) {
        # sha256sum format: "<hex>  <name>" (two spaces, name may be "*name")
        if ($line -match '^\s*([0-9a-fA-F]{64})\s+\*?(\S+)\s*$' -and $Matches[2] -eq $asset) {
            $expected = $Matches[1].ToLowerInvariant()
            break
        }
    }
    if (-not $expected) { Fail "SHA256SUMS has no entry for $asset; refusing to install an unverified binary." }

    $actual = (Get-FileHash -Path $binary -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        Fail "SHA-256 mismatch for ${asset}: expected $expected, got $actual. NOT installing."
    }
    Write-Host "incoda: sha256 verified ($actual)"

    # --- install ---------------------------------------------------------

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $target = Join-Path $InstallDir 'incoda.exe'
    Copy-Item -Path $binary -Destination $target -Force
    Write-Host "incoda: installed to $target"

    & $target version
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

# --- PATH advice ---------------------------------------------------------

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$InstallDir*") {
    Write-Host ''
    Write-Host "incoda: $InstallDir is not on your user PATH. To add it:"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path','User') + ';$InstallDir', 'User')"
    Write-Host '  (then open a new terminal)'
}
