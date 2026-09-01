<#
.SYNOPSIS
  Sync the project version across all files in one step.

.DESCRIPTION
  Run this script whenever you bump the version, so every file stays
  consistent:

      pwsh .\scripts\sync-version.ps1 -Version "1.0.2"

  It updates:
    - CHANGELOG.md   (promotes [Unreleased] to [x.y.z])
    - wails.json
    - build/windows/info.json
    - build/windows/wails.exe.manifest

  The CHANGELOG section gets today's date automatically.
#>
param(
    [Parameter(Mandatory)]
    [string]$Version
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not $Version -match '^(\d+)\.(\d+)\.(\d+)$') {
    throw "Version must be SemVer x.y.z, got: $Version"
}
$Date = (Get-Date).ToString("yyyy-MM-dd")
$FileMajorMinor = "$($Version).0"

$root = Split-Path -Parent $PSScriptRoot

# ── 1. wails.json ──────────────────────────────────────────────────
$wailsPath = Join-Path $root "wails.json"
$wails = Get-Content $wailsPath -Raw
$wails = $wails -replace '"productVersion"\s*:\s*"\d+\.\d+\.\d+"', "`"productVersion`": `"$Version`""
Set-Content -Path $wailsPath -Value $wails -Encoding UTF8 -NoNewline

# ── 2. build/windows/info.json ─────────────────────────────────────
$infoPath = Join-Path $root "build\windows\info.json"
$info = Get-Content $infoPath -Raw
$info = $info -replace '"file_version"\s*:\s*"\d+\.\d+\.\d+\.\d+"', "`"file_version`": `"$FileMajorMinor`""
$info = $info -replace '"ProductVersion"\s*:\s*"\d+\.\d+\.\d+"', "`"ProductVersion`": `"$Version`""
Set-Content -Path $infoPath -Value $info -Encoding UTF8 -NoNewline

# ── 3. build/windows/wails.exe.manifest ────────────────────────────
$manifestPath = Join-Path $root "build\windows\wails.exe.manifest"
$manifest = Get-Content $manifestPath -Raw
$manifest = $manifest -replace 'version="\d+\.\d+\.\d+\.\d+"', "version=`"$FileMajorMinor`""
Set-Content -Path $manifestPath -Value $manifest -Encoding UTF8 -NoNewline

# ── 4. CHANGELOG.md ────────────────────────────────────────────────
$changelogPath = Join-Path $root "CHANGELOG.md"
$changelog = Get-Content $changelogPath -Raw

# Idempotency: skip if this version already exists in CHANGELOG.
if ($changelog -match "## \[$Version\]") {
    Write-Host "CHANGELOG already contains [$Version]; skipping."
} else {
    # Promote [Unreleased] to [x.y.z] - YYYY-MM-DD
    $changelog = $changelog -replace '## \[Unreleased\]\s*\n\n', "## [Unreleased]`n`n## [$Version] - $Date`n`n"
    # Add release link under the existing link list (insert before [1.0.0]).
    $linkLine = "[$Version]: https://github.com/local/relayhub/releases/tag/v$Version`n"
    $changelog = $changelog -replace '(\[1\.0\.0\]:)', "$linkLine`$1"
    Set-Content -Path $changelogPath -Value $changelog -Encoding UTF8
}

Write-Host "Version $Version synced successfully ($Date)"