@echo off
setlocal enabledelayedexpansion

echo ========================================
echo   resp2chat Build Script (Windows)
echo ========================================

set "SCRIPT_DIR=%~dp0"
set "PROJECT_DIR=%SCRIPT_DIR%.."
set "OUTPUT_DIR=%PROJECT_DIR%\dist"
set "BINARY_NAME=resp2chat"

if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"

echo.
echo [1/3] Cleaning old builds...
if exist "%OUTPUT_DIR%\%BINARY_NAME%-windows-amd64.exe" del "%OUTPUT_DIR%\%BINARY_NAME%-windows-amd64.exe"
if exist "%OUTPUT_DIR%\%BINARY_NAME%-windows-arm64.exe" del "%OUTPUT_DIR%\%BINARY_NAME%-windows-arm64.exe"

echo.
echo [2/3] Building for Windows (amd64)...
cd /d "%PROJECT_DIR%"
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o "%OUTPUT_DIR%\%BINARY_NAME%-windows-amd64.exe" ./cmd/server/
if %ERRORLEVEL% neq 0 (
    echo ERROR: Build failed for windows/amd64
    exit /b 1
)
echo   OK: %OUTPUT_DIR%\%BINARY_NAME%-windows-amd64.exe

echo.
echo [3/3] Building for Windows (arm64)...
set GOARCH=arm64
go build -trimpath -ldflags="-s -w" -o "%OUTPUT_DIR%\%BINARY_NAME%-windows-arm64.exe" ./cmd/server/
if %ERRORLEVEL% neq 0 (
    echo ERROR: Build failed for windows/arm64
    exit /b 1
)
echo   OK: %OUTPUT_DIR%\%BINARY_NAME%-windows-arm64.exe

echo.
echo ========================================
echo   Build complete!
echo ========================================
echo.
echo Output files:
dir "%OUTPUT_DIR%\%BINARY_NAME%-windows-*.exe"

cd /d "%SCRIPT_DIR%"
