# ai-adapter Build Script (PowerShell)
# Usage:
#   .\scripts\build.ps1              # Build for current platform
#   .\scripts\build.ps1 -All         # Build for all platforms
#   .\scripts\build.ps1 -Windows     # Build Windows only
#   .\scripts\build.ps1 -Linux       # Build Linux only
#   .\scripts\build.ps1 -Darwin      # Build macOS only
#   .\scripts\build.ps1 -Clean       # Clean dist before building

param(
    [switch]$All,
    [switch]$Linux,
    [switch]$Windows,
    [switch]$Darwin,
    [switch]$Clean,
    [string]$Output = ""
)

$ErrorActionPreference = "Stop"

$ProjectDir = Split-Path -Parent $PSScriptRoot
if ($Output) {
    $OutputDir = $Output
} else {
    $OutputDir = Join-Path $ProjectDir "dist"
}
$BinaryName = "ai-adapter"

# Detect version from git
$Version = "dev"
try {
    $Version = (git describe --tags --always --dirty 2>$null)
    if (-not $Version) { $Version = "dev" }
} catch {}

$Commit = "unknown"
try {
    $Commit = (git rev-parse --short HEAD 2>$null)
    if (-not $Commit) { $Commit = "unknown" }
} catch {}

# Default: build current platform if no target specified
if (-not $All -and -not $Linux -and -not $Windows -and -not $Darwin) {
    if ($IsWindows -or $env:OS -eq "Windows_NT") { $Windows = $true }
    elseif ($IsMacOS) { $Darwin = $true }
    else { $Linux = $true }
}

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  ai-adapter Build" -ForegroundColor Cyan
Write-Host "  version: $Version" -ForegroundColor DarkGray
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir | Out-Null
}

if ($Clean) {
    Write-Host "Cleaning old builds..." -ForegroundColor Yellow
    Get-ChildItem -Path $OutputDir -Filter "$BinaryName-*" -ErrorAction SilentlyContinue | Remove-Item -Force
}

$buildFlags = @("-trimpath", "-ldflags=-s -w -X main.version=$Version -X main.commit=$Commit")
$env:CGO_ENABLED = "0"
$targets = @()

if ($Windows) {
    $targets += @{OS="windows"; Arch="amd64"; Ext=".exe"}
    $targets += @{OS="windows"; Arch="arm64"; Ext=".exe"}
}
if ($Linux) {
    $targets += @{OS="linux"; Arch="amd64"; Ext=""}
    $targets += @{OS="linux"; Arch="arm64"; Ext=""}
}
if ($Darwin) {
    $targets += @{OS="darwin"; Arch="amd64"; Ext=""}
    $targets += @{OS="darwin"; Arch="arm64"; Ext=""}
}

$total = $targets.Count
$current = 0
$failed = 0
$results = @()

foreach ($t in $targets) {
    $current++
    $name = "$BinaryName-$($t.OS)-$($t.Arch)$($t.Ext)"
    $outPath = Join-Path $OutputDir $name
    Write-Host "[$current/$total] $($t.OS)/$($t.Arch)..." -NoNewline

    $env:GOOS = $t.OS
    $env:GOARCH = $t.Arch

    & go build @buildFlags -o $outPath ./cmd/server/ 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Host " FAILED" -ForegroundColor Red
        $failed++
        $results += @{Name=$name; Status="FAILED"; Size="-"}
    } else {
        $size = [math]::Round((Get-Item $outPath).Length / 1MB, 2)
        Write-Host " OK ($size MB)" -ForegroundColor Green
        $results += @{Name=$name; Status="OK"; Size="$size MB"}
    }
}

# Clean env
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Results" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""
foreach ($r in $results) {
    $color = if ($r.Status -eq "OK") { "Green" } else { "Red" }
    Write-Host ("  {0,-45} {1,8}" -f $r.Name, $r.Size) -ForegroundColor $color
}

if ($failed -gt 0) {
    Write-Host ""
    Write-Host "$failed build(s) failed!" -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "All builds succeeded." -ForegroundColor Green