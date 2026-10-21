#!/bin/sh
# Picks the crew engine for this OS/CPU. Started through `git -c alias.crew-run=!sh crew-run <this file> ...`,
# which works on Windows (Git's own sh), macOS and Linux without Node, Python or a configured shell.
# Runs before every edit and shell command, so it forks nothing on Windows (a process start there costs tens of
# ms) and at most one `uname` elsewhere.
case $0 in */*) d=${0%/*} ;; *) d=. ;; esac
if [ "$OS" = Windows_NT ]; then
  os=windows; ext=.exe; m=${PROCESSOR_ARCHITECTURE:-AMD64}
else
  set -- "$(uname -sm)" "$@"; s=$1; shift
  case $s in
    MINGW*|MSYS*|CYGWIN*) os=windows; ext=.exe ;;
    Darwin*) os=darwin; ext= ;;
    *) os=linux; ext= ;;
  esac
  m=${s##* }
fi
case $m in
  arm64|aarch64|ARM64) arch=arm64 ;;
  *) arch=amd64 ;;
esac
bin="$d/crew-$os-$arch$ext"
[ -x "$bin" ] || chmod +x "$bin" 2>/dev/null
exec "$bin" "$@"
