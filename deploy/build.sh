#!/usr/bin/env bash
# Build release binaries for every target the agent runs on.
# Usage: deploy/build.sh [version]   (default: git describe)
set -euo pipefail
cd "$(dirname "$0")/../agent"
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT=../dist
mkdir -p "$OUT"
build() {
  local os=$1 arch=$2 suffix=$3; shift 3
  env GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$@" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$OUT/sentinel-agent-$VERSION-$suffix" ./cmd/sentinel-agent
  echo "built $OUT/sentinel-agent-$VERSION-$suffix"
}
build linux   amd64 linux-amd64
build linux   arm   linux-armv7 GOARM=7
build windows amd64 windows-amd64.exe
