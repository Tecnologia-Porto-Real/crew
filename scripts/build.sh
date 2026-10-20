#!/bin/sh
# Cross-compiles the crew engine for every supported OS/CPU into plugin/runtime/.
# Builds are reproducible (pinned toolchain in engine/go.mod, -trimpath, no VCS stamp), so anyone can
# rebuild and compare with the committed binaries; CI does exactly that on every push.
set -e
cd "$(dirname "$0")/.."
version=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' plugin/.claude-plugin/plugin.json | head -1)
for target in windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  os=${target%/*}; arch=${target#*/}; ext=
  [ "$os" = windows ] && ext=.exe
  (cd engine && CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOFLAGS=-mod=readonly go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X main.version=$version" -o "../plugin/runtime/crew-$os-$arch$ext" .)
  echo "built crew-$os-$arch$ext"
done
(cd plugin/runtime && sha256sum -b crew-* > SHA256SUMS)
cat plugin/runtime/SHA256SUMS
