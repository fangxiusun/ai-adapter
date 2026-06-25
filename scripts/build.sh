#!/bin/bash
# ai-adapter Build Script
# Usage:
#   ./scripts/build.sh              # Build all platforms
#   ./scripts/build.sh linux        # Linux only
#   ./scripts/build.sh darwin       # macOS only
#   ./scripts/build.sh windows      # Windows only
#   ./scripts/build.sh clean        # Clean dist first, then build all

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OUTPUT_DIR="${OUTPUT_DIR:-$PROJECT_DIR/dist}"
BINARY_NAME="ai-adapter"

cd "$PROJECT_DIR"

# Version info
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"

# Parse target
TARGET="${1:-all}"
CLEAN="false"
if [ "$TARGET" = "clean" ]; then
    CLEAN="true"
    TARGET="${2:-all}"
fi

mkdir -p "$OUTPUT_DIR"

if [ "$CLEAN" = "true" ]; then
    echo "Cleaning old builds..."
    rm -f "$OUTPUT_DIR/$BINARY_NAME"-*
fi

declare -a BUILD_LIST

case "$TARGET" in
    linux)
        BUILD_LIST=("linux/amd64" "linux/arm64")
        ;;
    darwin)
        BUILD_LIST=("darwin/amd64" "darwin/arm64")
        ;;
    windows)
        BUILD_LIST=("windows/amd64" "windows/arm64")
        ;;
    all|*)
        BUILD_LIST=("linux/amd64" "linux/arm64" "darwin/amd64" "darwin/arm64" "windows/amd64" "windows/arm64")
        ;;
esac

echo ""
echo "========================================"
echo "  ai-adapter Build"
echo "  version: $VERSION"
echo "========================================"
echo ""

TOTAL=${#BUILD_LIST[@]}
CURRENT=0
FAILED=0

for target in "${BUILD_LIST[@]}"; do
    CURRENT=$((CURRENT + 1))
    GOOS="${target%%/*}"
    GOARCH="${target##*/}"
    EXT=""
    if [ "$GOOS" = "windows" ]; then
        EXT=".exe"
    fi
    OUT_NAME="$BINARY_NAME-$GOOS-$GOARCH$EXT"
    OUT_PATH="$OUTPUT_DIR/$OUT_NAME"

    printf "[%d/%d] %s/%s... " "$CURRENT" "$TOTAL" "$GOOS" "$GOARCH"

    if GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
        go build -trimpath -ldflags="$LDFLAGS" -o "$OUT_PATH" ./cmd/server/ 2>/dev/null; then
        SIZE=$(du -h "$OUT_PATH" | cut -f1)
        echo "OK ($SIZE)"
    else
        echo "FAILED"
        FAILED=$((FAILED + 1))
    fi
done

echo ""
echo "========================================"
echo "  Results"
echo "========================================"
echo ""
ls -lh "$OUTPUT_DIR/$BINARY_NAME"-* 2>/dev/null || echo "  (no binaries)"

if [ "$FAILED" -gt 0 ]; then
    echo ""
    echo "$FAILED build(s) failed!"
    exit 1
fi

echo ""
echo "All builds succeeded."