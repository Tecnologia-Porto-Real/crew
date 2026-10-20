#!/bin/sh
# Picks the crew engine for this OS/CPU. Started through `git -c alias.crew-run=!sh crew-run <this file> ...`,
# which works on Windows (Git's own sh), macOS and Linux without Node, Python or a configured shell.
d=$(cd "$(dirname "$0")" && pwd)
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*|Windows_NT) os=windows; ext=.exe ;;
  Darwin) os=darwin; ext= ;;
  *) os=linux; ext= ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  *) arch=amd64 ;;
esac
bin="$d/crew-$os-$arch$ext"
[ -x "$bin" ] || chmod +x "$bin" 2>/dev/null
exec "$bin" "$@"
