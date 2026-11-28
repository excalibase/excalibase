#!/usr/bin/env sh
# EXC-719: check-psl.sh reports whether the user-content domain is on the Public
# Suffix List. The list is read from a local file; nothing reaches the network.
#
#   sh edge/sites-router/test/check-psl.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

cat >"$WORK/list.dat" <<'EOF'
// ===BEGIN PRIVATE DOMAINS===
github.io
*.compute.example
excalibase.app
EOF

PSL_FILE="$WORK/list.dat" sh "$DIR/check-psl.sh" excalibase.app >"$WORK/out" 2>&1 || { cat "$WORK/out"; fail "a listed domain was reported missing"; }
PSL_FILE="$WORK/list.dat" sh "$DIR/check-psl.sh" sites.excalibase.app >"$WORK/out" 2>&1 && fail "a subdomain of a listed entry is not itself listed"
PSL_FILE="$WORK/list.dat" sh "$DIR/check-psl.sh" excalibase.dev >"$WORK/out" 2>&1 && fail "an unlisted domain passed"
grep -q "NOT on the Public Suffix List" "$WORK/out" || fail "the refusal does not say why"
PSL_FILE="$WORK/list.dat" sh "$DIR/check-psl.sh" 'bad domain' >"$WORK/out" 2>&1 && fail "accepted a malformed domain"
PSL_FILE="$WORK/list.dat" sh "$DIR/check-psl.sh" >"$WORK/out" 2>&1 && fail "ran without a domain"

echo "psl ok"
