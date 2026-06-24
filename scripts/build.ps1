# resp2chat Build Script (PowerShell)
# Usage: .\scripts\build.ps1 [-All] [-Linux] [-Windows]

param(
    [switch]$All,
    [switch]$Linux,
    [switch]$Windows,
    [switch]$Clean
)

$ErrorActionPreference = "Stop"

$ProjectDir = Split-Path -Parent $PSScriptRoot
$OutputDir = Join-Path $ProjectDir "dist"
$BinaryName = "resp2chat"

# If no flag specified, build for current platform only
if (-not $All -and -not $Linux -and -not $Windows) {
    $All = $true
}

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  resp2chat Build Script" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

# Create output directory
if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir | Out-Null
}

# Clean old builds
if ($Clean -or $All) {
    Write-Host "Cleaning old builds..." -ForegroundColor Yellow
    Get-ChildItem -Path $OutputDir -Filter "$BinaryName-*" -ErrorAction SilentlyContinue | Remove-Item -Force
}

$buildFlags = "-trimpath", "-ldflags=-s -w"
$env:CGO_ENABLED = "0"
$failed = $false

# Windows builds
if ($All -or $Windows) {
    Write-Host ""
    Write-Host "Building for Windows (amd64)..." -ForegroundColor Green
    $env:GOOS = "windows"; $env:GOARCH = "amd64"
    & go build @buildFlags -o (Join-Path $OutputDir "$BinaryName-windows-amd64.exe") ./cmd/server/
    if ($LASTEXITCODE -ne 0) { Write-Host "  FAILED" -ForegroundColor Red; $failed = $true }
    else { Write-Host "  OK" -ForegroundColor Green }

    Write-Host "Building for Windows (arm64)..." -ForegroundColor Green
    $env:GOARCH = "arm64"
    & go build @buildFlags -o (Join-Path $OutputDir "$BinaryName-windows-arm64.exe") ./cmd/server/
    if ($LASTEXITCODE -ne 0) { Write-Host "  FAILED" -ForegroundColor Red; $failed = $true }
    else { Write-Host "  OK" -ForegroundColor Green }
}

# Linux builds
if ($All -or $Linux) {
    Write-Host ""
    Write-Host "Building for Linux (amd64)..." -ForegroundColor Green
    $env:GOOS = "linux"; $env:GOARCH = "amd64"
    & go build @buildFlags -o (Join-Path $OutputDir "$BinaryName-linux-amd64") ./cmd/server/
    if ($LASTEXITCODE -ne 0) { Write-Host "  FAILED" -ForegroundColor Red; $failed = $true }
    else { Write-Host "  OK" -ForegroundColor Green }

    Write-Host "Building for Linux (arm64)..." -ForegroundColor Green
    $env:GOARCH = "arm64"
    & go build @buildFlags -o (Join-Path $OutputDir "$BinaryName-linux-arm64") ./cmd/server/
    if ($LASTEXITCODE -ne 0) { Write-Host "  FAILED" -ForegroundColor Red; $failed = $true }
    else { Write-Host "  OK" -ForegroundColor Green }
}

# Clean env
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  Build Results" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

Get-ChildItem -Path $OutputDir -Filter "$BinaryName-*" | ForEach-Object {
    $size = [math]::Round($_.Length / 1MB, 2)
    Write-Host ("  {0,-40} {1,8} MB" -f $_.Name, $size)
}

if ($failed) {
    Write-Host ""
    Write-Host "Some builds failed!" -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "Build complete!" -ForegroundColor Green
