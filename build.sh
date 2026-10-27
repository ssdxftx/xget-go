#!/usr/bin/env bash
# Build Xget-Go static binaries.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-v3.0.5}"
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || GO="/home/z/toolchain/go/bin/go"

LDFLAGS="-s -w -X main.version=${VERSION}"

echo "==> building linux-amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -ldflags "${LDFLAGS}" -o xget-linux-amd64 .

echo "==> building linux-arm64"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$GO" build -trimpath -ldflags "${LDFLAGS}" -o xget-linux-arm64 .

echo "==> building darwin-arm64 (macOS Apple Silicon)"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$GO" build -trimpath -ldflags "${LDFLAGS}" -o xget-darwin-arm64 .

echo "==> done"
ls -lh xget-linux-amd64 xget-linux-arm64 xget-darwin-arm64
