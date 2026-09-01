#Requires -Version 5.1
<#
.SYNOPSIS
    Installs the incoda binary on Windows from a GitHub release.

.DESCRIPTION
    Downloads the prebuilt binary for this platform, verifies it against the
    release's SHA256SUMS, and installs it. Nothing unverifiable is installed.

.PARAMETER Version
    Release tag to install, e.g. v0.1.1. Defaults to the latest release.

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

function Fetch([string]$Url, [string]$Out) {
    # curl.exe ships with Windows 10+ and follows redirects with -L; fall back
    # to Invoke-WebRequest where it is absent.
    if (Get-Command curl.exe -ErrorAction SilentlyContinue) {
        & curl.exe -fsSL $Url -o $Out
        if ($LASTEXITCODE -ne 0) { Fail "Could not download $Url" }
    } else {
        try {
            Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing
        } catch {
            Fail "Could not download $Url"
        }
    }
}

# --- pick the asset ------------------------------------------------------

switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    'x86'   { Fail 'incoda has no 32-bit Windows build.' }
    default { Fail "Unsupported processor architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}
$asset = "incoda_windows_$arch.exe"

if ($Version) {
    $base = "https://github.com/$Repo/releases/download/$Version"
} else {
    $base = "https://github.com/$Repo/releases/latest/download"
}
Write-Host "incoda: installing $(if ($Version) { $Version } else { 'latest' }) ($asset) from $Repo"

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("incoda-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work -Force | Out-Null
try {
    Fetch "$base/$asset" (Join-Path $work $asset)
    Fetch "$base/SHA256SUMS" (Join-Path $work 'SHA256SUMS')

    $binary = Join-Path $work $asset
    $sums   = Join-Path $work 'SHA256SUMS'

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
