#!/bin/sh
# Cross-compiles the crew engine for every supported OS/CPU into plugin/runtime/.
set -e
cd "$(dirname "$0")/.."
version=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' plugin/.claude-plugin/plugin.json | head -1)
for target in windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  os=${target%/*}; arch=${target#*/}; ext=
  [ "$os" = windows ] && ext=.exe
  (cd engine && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "../plugin/runtime/crew-$os-$arch$ext" .)
  echo "built crew-$os-$arch$ext"
done
ls -la plugin/runtime
