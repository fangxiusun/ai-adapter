#!/bin/bash
set -e

echo "========================================"
echo "  resp2chat Build Script (All Platforms)"
echo "========================================"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OUTPUT_DIR="$PROJECT_DIR/dist"
BINARY_NAME="resp2chat"

mkdir -p "$OUTPUT_DIR"

echo ""
echo "Cleaning old builds..."
rm -f "$OUTPUT_DIR/$BINARY_NAME"-*

cd "$PROJECT_DIR"

# Windows
echo ""
echo "Building for Windows (amd64)..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-windows-amd64.exe" ./cmd/server/
echo "  OK"

echo "Building for Windows (arm64)..."
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-windows-arm64.exe" ./cmd/server/
echo "  OK"

# Linux
echo ""
echo "Building for Linux (amd64)..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-linux-amd64" ./cmd/server/
echo "  OK"

echo "Building for Linux (arm64)..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-linux-arm64" ./cmd/server/
echo "  OK"

echo "Building for Linux (arm)..."
GOOS=linux GOARCH=arm CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-linux-arm" ./cmd/server/
echo "  OK"

# macOS
echo ""
echo "Building for macOS (amd64)..."
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-darwin-amd64" ./cmd/server/
echo "  OK"

echo "Building for macOS (arm64)..."
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-darwin-arm64" ./cmd/server/
echo "  OK"

echo ""
echo "========================================"
echo "  Build complete! All binaries:"
echo "========================================"
echo ""
ls -lh "$OUTPUT_DIR/$BINARY_NAME"-*

# Calculate total size
TOTAL=$(du -sh "$OUTPUT_DIR" | cut -f1)
echo ""
echo "Total size: $TOTAL"
