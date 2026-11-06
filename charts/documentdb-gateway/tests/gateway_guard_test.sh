#!/usr/bin/env bash
# The gateway guard against a fake gateway and a fake psql; no cluster needed.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
GUARD="${GUARD:-$HERE/../gateway_guard.sh}"
failures=0

# Runs the guard with psql answering each line of $1 in turn (the last repeats).
# Sets rc to its exit status and gateway_alive to whether the gateway outlived it.
run_guard() {
  local answers="$1" limit="${2:-8}" dir
  dir="$(mktemp -d)"
  printf '%b' "$answers" > "$dir/answers"
  cat > "$dir/psql" <<FAKE
#!/bin/bash
line=\$(head -1 "$dir/answers")
[ "\$(wc -l < "$dir/answers")" -gt 1 ] && sed -i 1d "$dir/answers"
[ "\$line" = down ] && exit 2
echo "\$line"
FAKE
  cat > "$dir/gateway" <<FAKE
#!/bin/bash
echo "\$@" > "$dir/args"
echo \$\$ > "$dir/pid"
exec sleep 300
FAKE
  chmod +x "$dir/psql" "$dir/gateway"
  PATH="$dir:$PATH" GATEWAY_GUARD_INTERVAL=0.1 GATEWAY_ENTRYPOINT="$dir/gateway" \
    timeout "$limit" "$GUARD" --pg-port 5432 > "$dir/out" 2>&1
  rc=$?
  gateway_alive=no
  kill -0 "$(cat "$dir/pid" 2>/dev/null || echo 999999)" 2>/dev/null && gateway_alive=yes
  args="$(cat "$dir/args" 2>/dev/null)"
  kill -KILL "$(cat "$dir/pid" 2>/dev/null || echo 999999)" 2>/dev/null
  rm -rf "$dir"
}

check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then echo "ok   $name"; else echo "FAIL $name: expected $expected, got $actual"; failures=$((failures + 1)); fi
}

run_guard 'f\nf\nt\n'
check 'a primary that is demoted ends the gateway' 'no 1' "$gateway_alive $rc"

run_guard 'f\ndown\ndown\nt\n'
check 'a demotion across a Postgres restart ends the gateway' 'no 1' "$gateway_alive $rc"

run_guard 't\n' 2
check 'a standby that was never primary keeps its gateway' 124 "$rc"

run_guard 't\nf\n' 2
check 'a promoted standby keeps its gateway' 124 "$rc"

run_guard 'f\ndown\nf\n' 2
check 'a primary that restarts as primary keeps its gateway' 124 "$rc"

run_guard 'down\n' 2
check 'Postgres not answering is not a demotion' 124 "$rc"
check 'the plugin arguments reach the gateway' '--pg-port 5432' "$args"

exit "$failures"
