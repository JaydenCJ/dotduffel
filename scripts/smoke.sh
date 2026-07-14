#!/usr/bin/env bash
# End-to-end smoke test for dotduffel. No network, idempotent, runs from
# a clean tree. This script plus 'go test ./...' is the whole
# verification story — the repository intentionally ships no CI.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/dotduffel"
CFG="$WORKDIR/cfg"
MANIFEST="$CFG/duffel.json"

echo "[1/9] build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/dotduffel) || fail "build failed"

echo "[2/9] --version matches the manifest version"
VERSION_OUT="$("$BIN" --version)"
[ "$VERSION_OUT" = "dotduffel 0.1.0" ] || fail "unexpected version output: $VERSION_OUT"

echo "[3/9] init writes a working starter config"
# Capture first, grep second: piping straight into `grep -q` would let
# grep exit on the first match and SIGPIPE the still-writing binary,
# which pipefail then reports as a flaky failure.
INIT_OUT="$("$BIN" init --dir "$CFG")" || fail "init failed"
echo "$INIT_OUT" | grep -q "duffel.json" || fail "init did not confirm"
[ -f "$CFG/duffelrc" ] || fail "starter entry file missing"

echo "[4/9] ls resolves the starter and stays inside the budget"
LS_OUT="$("$BIN" --manifest "$MANIFEST" ls)"
echo "$LS_OUT" | grep -q "duffelrc" || fail "ls missing entry file"
echo "$LS_OUT" | grep -q "budget"   || fail "ls missing budget line"

echo "[5/9] local session: env, entry, aliases and bundled bin all load"
mkdir -p "$CFG/tools"
printf '#!/bin/sh\necho tool-on-path\n' > "$CFG/tools/hello.sh"
chmod +x "$CFG/tools/hello.sh"
cat > "$MANIFEST" <<'EOF'
{
  "files": [
    { "from": "duffelrc" },
    { "from": "aliases.sh" },
    { "from": "tools/*.sh", "to": "bin/" }
  ],
  "env": { "DUFFEL": "1" }
}
EOF
SH_OUT="$("$BIN" --manifest "$MANIFEST" sh --command 'echo dir=$DUFFEL_DIR; echo env=$DUFFEL; ll >/dev/null 2>&1 && echo alias-ok; hello.sh')"
echo "$SH_OUT" | grep -q "env=1"       || fail "manifest env not exported"
echo "$SH_OUT" | grep -q "alias-ok"    || fail "aliases not loaded"
echo "$SH_OUT" | grep -q "tool-on-path" || fail "bundled bin/ not on PATH"

echo "[6/9] the session tempdir is removed afterwards (ephemeral by design)"
TEMPDIR="$(echo "$SH_OUT" | sed -n 's/^dir=//p')"
[ -n "$TEMPDIR" ] || fail "bootstrap did not report DUFFEL_DIR"
[ ! -e "$TEMPDIR" ] || fail "tempdir $TEMPDIR survived the session"

echo "[7/9] pack is byte-reproducible"
"$BIN" --manifest "$MANIFEST" pack -o "$WORKDIR/one.tgz" | grep -q "reproducible" || fail "pack did not confirm"
"$BIN" --manifest "$MANIFEST" pack -o "$WORKDIR/two.tgz" >/dev/null
cmp -s "$WORKDIR/one.tgz" "$WORKDIR/two.tgz" || fail "two packs differ"

echo "[8/9] the secret guard refuses a private key with exit 1"
printf -- '-----BEGIN RSA PRIVATE KEY-----\nAAAA\n' > "$CFG/oops.txt"
cat > "$WORKDIR/leaky.json" <<EOF
{ "files": [ { "from": "$CFG/duffelrc" }, { "from": "$CFG/oops.txt" } ] }
EOF
set +e
GUARD_OUT="$("$BIN" --manifest "$WORKDIR/leaky.json" emit 2>&1)"
GUARD_CODE=$?
set -e
[ "$GUARD_CODE" -eq 1 ] || fail "expected exit 1 on secret, got $GUARD_CODE"
echo "$GUARD_OUT" | grep -q "private-key" || fail "guard did not name the rule"

echo "[9/9] ssh/docker transports shape the right argv (--print)"
"$BIN" --manifest "$MANIFEST" ssh --print devbox -p 2222 \
  | grep -q "^ssh -t -p 2222 devbox " || fail "ssh argv wrong"
"$BIN" --manifest "$MANIFEST" docker --print --command ls mybox \
  | grep -q "^docker exec -i mybox sh -c " || fail "docker argv wrong"

echo "SMOKE OK"
