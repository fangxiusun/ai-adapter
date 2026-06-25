@echo off
setlocal enabledelayedexpansion

set "SCRIPT_DIR=%~dp0"
set "PROJECT_DIR=%SCRIPT_DIR%.."
for /f "delims=" %%P in ("%PROJECT_DIR%") do set "PROJECT_DIR=%%~fP"
set "OUTPUT_DIR=%PROJECT_DIR%\dist"
set "BINARY_NAME=ai-adapter"
set "TARGET=%~1"
if "%TARGET%"=="" set "TARGET=windows"

set "CLEAN=0"
if /i "%TARGET%"=="clean" (
    set "CLEAN=1"
    set "TARGET=%~2"
)
if "%TARGET%"=="" set "TARGET=windows"

:: normalize PowerShell-style switches
set "NORM=%TARGET%"
if "!NORM:~0,1!"=="-" set "NORM=!NORM:~1!"
if /i "!NORM!"=="windows" set "TARGET=windows"
if /i "!NORM!"=="linux" set "TARGET=linux"
if /i "!NORM!"=="darwin" set "TARGET=darwin"
if /i "!NORM!"=="macos" set "TARGET=darwin"
if /i "!NORM!"=="all" set "TARGET=all"

if not exist "%PROJECT_DIR%\go.mod" (
    echo go.mod not found: %PROJECT_DIR%\go.mod
    exit /b 1
)

for /f "tokens=*" %%V in ('git -C "%PROJECT_DIR%" describe --tags --always --dirty 2^>nul') do set "VERSION=%%V"
if "!VERSION!"=="" set "VERSION=dev"
for /f "tokens=*" %%C in ('git -C "%PROJECT_DIR%" rev-parse --short HEAD 2^>nul') do set "COMMIT=%%C"
if "!COMMIT!"=="" set "COMMIT=unknown"
set "LDFLAGS=-s -w -X main.version=!VERSION! -X main.commit=!COMMIT!"
set "CGO_ENABLED=0"

if "!CLEAN!"=="1" (
    echo Cleaning old builds...
    for /f "delims=" %%F in ('dir /b "%OUTPUT_DIR%\%BINARY_NAME%-%*" 2^>nul') do (
        del /f /q "%OUTPUT_DIR%\%%F"
    )
)

if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"

echo.
echo ========================================
echo   ai-adapter Build
echo   target: !TARGET!
echo   version: !VERSION!
echo ========================================
echo.

set "FAILED=0"
set "COUNT=0"

if /i "!TARGET!"=="linux" (
    call :build linux amd64
    call :build linux arm64
    goto summary
)
if /i "!TARGET!"=="darwin" (
    call :build darwin amd64
    call :build darwin arm64
    goto summary
)
if /i "!TARGET!"=="all" (
    call :build windows amd64
    call :build windows arm64
    call :build linux amd64
    call :build linux arm64
    call :build darwin amd64
    call :build darwin arm64
    goto summary
)

call :build windows amd64
call :build windows arm64
goto summary

:build
set "GOOS=%~1"
set "GOARCH=%~2"
set "EXT="
if /i "!GOOS!"=="windows" set "EXT=.exe"
set "OUT_NAME=!BINARY_NAME!-!GOOS!-!GOARCH!!EXT!"
set "OUT_PATH=!OUTPUT_DIR!\!OUT_NAME!"

echo [!GOOS!/!GOARCH!]
go build -trimpath -ldflags="!LDFLAGS!" -o "!OUT_PATH!" "!PROJECT_DIR!\cmd\server"
if errorlevel 1 (
    echo   build failed: !OUT_NAME!
    set "FAILED=1"
    exit /b 0
)

set /a COUNT+=1
for %%F in ("!OUT_PATH!") do echo   ok !OUT_NAME! - %%~zF bytes
exit /b 0

:summary
echo.
echo ========================================
echo   Build finished
echo ========================================
echo.

set "LISTED=0"
for /f "delims=" %%F in ('dir /b "!OUTPUT_DIR!\!BINARY_NAME!-*" 2^>nul') do (
    echo %%F
    set /a LISTED+=1
)
if "!LISTED!"=="0" echo no binaries

if "!FAILED!"=="1" (
    echo.
    echo One or more builds failed.
    exit /b 1
)
if "!COUNT!"=="0" (
    echo.
    echo No binaries were built.
    exit /b 1
)
exit /b 0
