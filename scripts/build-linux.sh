#!/bin/bash
set -e

echo "========================================"
echo "  resp2chat Build Script (Linux/macOS)"
echo "========================================"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OUTPUT_DIR="$PROJECT_DIR/dist"
BINARY_NAME="resp2chat"

mkdir -p "$OUTPUT_DIR"

echo ""
echo "[1/5] Cleaning old builds..."
rm -f "$OUTPUT_DIR/$BINARY_NAME"-linux-*
rm -f "$OUTPUT_DIR/$BINARY_NAME"-darwin-*
rm -f "$OUTPUT_DIR/$BINARY_NAME"-windows-*

cd "$PROJECT_DIR"

echo ""
echo "[2/5] Building for Linux (amd64)..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-linux-amd64" ./cmd/server/
echo "  OK: $OUTPUT_DIR/$BINARY_NAME-linux-amd64"

echo ""
echo "[3/5] Building for Linux (arm64)..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-linux-arm64" ./cmd/server/
echo "  OK: $OUTPUT_DIR/$BINARY_NAME-linux-arm64"

echo ""
echo "[4/5] Building for macOS (amd64)..."
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-darwin-amd64" ./cmd/server/
echo "  OK: $OUTPUT_DIR/$BINARY_NAME-darwin-amd64"

echo ""
echo "[5/5] Building for macOS (arm64)..."
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$BINARY_NAME-darwin-arm64" ./cmd/server/
echo "  OK: $OUTPUT_DIR/$BINARY_NAME-darwin-arm64"

echo ""
echo "========================================"
echo "  Build complete!"
echo "========================================"
echo ""
echo "Output files:"
ls -lh "$OUTPUT_DIR/$BINARY_NAME"-*
