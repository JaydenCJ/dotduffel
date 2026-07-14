#!/bin/sh
# Tiny example of a tool that rides in the duffel's bin/ — on the target
# it is on PATH for the whole session and gone afterwards.
# Usage: portcheck.sh [port]
port="${1:-8080}"
if command -v ss >/dev/null 2>&1; then
  ss -ltn "( sport = :$port )"
elif command -v netstat >/dev/null 2>&1; then
  netstat -ltn 2>/dev/null | grep ":$port "
else
  echo "portcheck: neither ss nor netstat found" >&2
  exit 1
fi
