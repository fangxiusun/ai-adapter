@echo off
setlocal

set "SCRIPT_DIR=%~dp0"
set "PS_SCRIPT=%SCRIPT_DIR%build.ps1"
set "CMD_SCRIPT=%SCRIPT_DIR%build.cmd"

where powershell >nul 2>nul
if errorlevel 1 (
    echo PowerShell is not available, falling back to native CMD build script.
    call "%CMD_SCRIPT%" %*
    exit /b %errorlevel%
)

echo ========================================
echo   ai-adapter Build Script
echo ========================================
powershell -ExecutionPolicy Bypass -File "%PS_SCRIPT%" %*
set "EXIT_CODE=%errorlevel%"
if not "%EXIT_CODE%"=="0" (
    echo.
    echo PowerShell build failed (%EXIT_CODE%). Retrying with native CMD build script.
    call "%CMD_SCRIPT%" %*
    exit /b %errorlevel%
)
endlocal
