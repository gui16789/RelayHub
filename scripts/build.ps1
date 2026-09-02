<#
.SYNOPSIS
  Build the RelayHub desktop (Wails) and/or headless binaries.

.DESCRIPTION
  One command for the local release build. Wraps the two supported paths so
  the required Wails tags/ldflags never have to be remembered:

    - Desktop : wails build            -> build/bin/RelayHub.exe  (icon, prod tag)
    - Headless: go build -tags=production ./cmd/headless
                -> build/bin/relayhub-headless.exe (console program)

  Version metadata is injected via -ldflags only when a version can be
  resolved (wails.json productVersion, or -Version); otherwise the build
  keeps the "(dev)" fallback from internal/version.

.EXAMPLE
  pwsh ./scripts/build.ps1                      # desktop + headless
  pwsh ./scripts/build.ps1 -DesktopOnly          # desktop only
  pwsh ./scripts/build.ps1 -HeadlessOnly -UpdateCli   # headless + ensure CLI

.PARAMETER DesktopOnly
  Build only the desktop app (skip headless).

.PARAMETER HeadlessOnly
  Build only the headless binary (skip desktop).

.PARAMETER Version
  Override the version injected into internal/version. Defaults to the
  productVersion in wails.json. Pass an empty string to skip injection.

.PARAMETER UpdateCli
  Pin the Wails CLI to the version required by go.mod (v2.15.0) before
  building. A mismatched CLI is reported as an error without this flag.

.PARAMETER Verbose
  Show full command lines and wails build output.
#>
param(
    [switch]$DesktopOnly,
    [switch]$HeadlessOnly,
    [string]$Version,
    [switch]$UpdateCli,
    [switch]$Verbose
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$wailsVersion = "v2.15.0"   # keep in sync with go.mod require github.com/wailsapp/wails/v2

if ($DesktopOnly -and $HeadlessOnly) {
    throw "-DesktopOnly and -HeadlessOnly are mutually exclusive."
}
$buildDesktop = -not $HeadlessOnly
$buildHeadless = -not $DesktopOnly

# ── 0. Toolchain checks ─────────────────────────────────────────────
$goBin = (go env GOPATH).Trim() + "\bin"
$wails = Join-Path $goBin "wails.exe"

if (-not (Test-Path $wails)) {
    if ($UpdateCli) {
        Write-Host "Wails CLI not found; installing $wailsVersion ..." -ForegroundColor Yellow
        go install "github.com/wailsapp/wails/v2/cmd/wails@$wailsVersion"
    } else {
        throw "Wails CLI not found at $wails. Run: go install github.com/wailsapp/wails/v2/cmd/wails@$wailsVersion  (or use -UpdateCli)"
    }
}

if ($UpdateCli) {
    # Pin the CLI to the same version as the runtime in go.mod. Build it
    # natively (blank GOOS/GOARCH) so a cross-compile env never breaks the
    # CLI itself, mirroring the release workflow.
    Write-Host "Pinning Wails CLI to $wailsVersion ..." -ForegroundColor Yellow
    $env:GOOS = ""; $env:GOARCH = ""; $env:CGO_ENABLED = "0"
    go install "github.com/wailsapp/wails/v2/cmd/wails@$wailsVersion"
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}

$installed = & $wails version 2>$null | Select-Object -First 1
if ($installed -notlike "*$wailsVersion*") {
    if ($UpdateCli) {
        throw "Wails CLI still reports '$installed' after -UpdateCli; expected $wailsVersion."
    }
    # Don't hard-fail on a mismatched CLI: wails build usually still works,
    # but warn loudly because CLI/runtime skew is a known source of weirdness.
    Write-Host "Warning: Wails CLI is '$installed', go.mod requires $wailsVersion." -ForegroundColor Yellow
    Write-Host "  Fix: pwsh ./scripts/build.ps1 -UpdateCli  (or go install github.com/wailsapp/wails/v2/cmd/wails@$wailsVersion)" -ForegroundColor Yellow
}

# ── 1. Resolve version + ldflags ────────────────────────────────────
if (-not $PSBoundParameters.ContainsKey("Version")) {
    $wailsJson = Get-Content (Join-Path $root "wails.json") -Raw | ConvertFrom-Json
    $Version = $wailsJson.info.productVersion
}
$ldflags = ""
if ($Version) {
    $commit = git -C $root rev-parse --short HEAD 2>$null
    if (-not $commit) { $commit = "unknown" }
    $buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $ldflags = "-s -w" +
        " -X github.com/local/relayhub/internal/version.Version=$Version" +
        " -X github.com/local/relayhub/internal/version.Commit=$commit" +
        " -X github.com/local/relayhub/internal/version.BuildTime=$buildTime"
    Write-Host "Version: $Version (commit $commit, $buildTime)" -ForegroundColor Cyan
} else {
    Write-Host "No version resolved; keeping internal/version '(dev)' metadata." -ForegroundColor Cyan
}

# ── 2. Desktop: wails build ─────────────────────────────────────────
if ($buildDesktop) {
    $dist = Join-Path $root "frontend\dist\index.html"
    if (-not (Test-Path $dist)) {
        throw "Missing $dist — wails.json has no frontend:build step, so frontend/dist must already exist (build it or check it out)."
    }
    Write-Host "`n==> Building desktop app (wails build) ..." -ForegroundColor Green
    Push-Location $root
    try {
        if ($Verbose) {
            if ($ldflags) { & $wails build -ldflags $ldflags } else { & $wails build }
        } else {
            if ($ldflags) { & $wails build -ldflags $ldflags 2>&1 | Out-Host } else { & $wails build 2>&1 | Out-Host }
        }
        if ($LASTEXITCODE -ne 0) { throw "wails build failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
    $desktopExe = Join-Path $root "build\bin\RelayHub.exe"
    Write-Host "Desktop OK: $desktopExe" -ForegroundColor Green
} else {
    Write-Host "`n==> Skipping desktop app (-DesktopOnly not set / headless-only)." -ForegroundColor DarkGray
}

# ── 3. Headless: go build -tags=production ──────────────────────────
if ($buildHeadless) {
    $outDir = Join-Path $root "build\bin"
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null
    $headlessExe = Join-Path $outDir "relayhub-headless.exe"
    Write-Host "`n==> Building headless binary ..." -ForegroundColor Green
    # Console program on purpose: no -H windowsgui, so logs stay visible and
    # Ctrl+C keeps working (the release workflow relies on the same behavior).
    $args = @("-tags=production")
    if ($ldflags) { $args += "-ldflags"; $args += $ldflags }
    $args += "-o"; $args += $headlessExe; $args += "./cmd/headless"
    if ($Verbose) {
        & go build @args
    } else {
        & go build @args 2>&1 | Out-Host
    }
    if ($LASTEXITCODE -ne 0) { throw "headless build failed (exit $LASTEXITCODE)" }
    Write-Host "Headless OK: $headlessExe" -ForegroundColor Green
} else {
    Write-Host "`n==> Skipping headless binary (desktop-only)." -ForegroundColor DarkGray
}

Write-Host "`nBuild finished." -ForegroundColor Green