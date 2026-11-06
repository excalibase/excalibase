#!/usr/bin/env bash
# The gateway guard against a fake gateway, psql and pg_isready; no cluster or
# Postgres. gateway_guard_demotion_test.sh runs the real image.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
GUARD="${GUARD:-$HERE/../gateway_guard.sh}"
failures=0
ROLE_SQL='SELECT pg_is_in_recovery()'

# The fakes refuse anything but the exact connection the real ones need, so a
# wrong role, host, port, database or statement fails rather than passes.
make_fakes() {
  local dir="$1" port="$2"
  cat > "$dir/psql" <<FAKE
#!/bin/bash
if [ "\$*" != "-h localhost -p $port -U documentdb -d postgres -XAtc $ROLE_SQL" ]; then
  echo "unexpected psql arguments: \$*" >&2; exit 99
fi
line=\$(head -1 "$dir/role")
[ "\$(wc -l < "$dir/role")" -gt 1 ] && sed -i 1d "$dir/role"
case "\$line" in
  hang) exec sleep 300 ;;
  fail) echo 'FATAL: role "documentdb" does not exist' >&2; exit 2 ;;
esac
echo "\$line"
FAKE
  cat > "$dir/pg_isready" <<FAKE
#!/bin/bash
if [ "\$*" != "-h localhost -p $port -q" ]; then echo "unexpected pg_isready arguments: \$*" >&2; exit 99; fi
line=\$(head -1 "$dir/ready")
[ "\$(wc -l < "$dir/ready")" -gt 1 ] && sed -i 1d "$dir/ready"
[ "\$line" = hang ] && exec sleep 300
exit "\$line"
FAKE
  # Python, because bash cannot handle a signal it inherited as ignored, which
  # a background job's SIGINT is; the real gateway installs its own handler.
  cat > "$dir/gateway.py" <<FAKE
import os, signal, sys, time
open("$dir/pid", "w").write(str(os.getpid()))
open("$dir/args", "w").write(" ".join(sys.argv[1:]))
mode = open("$dir/mode").read().strip()
def received(number, _):
    open("$dir/signals", "a").write(signal.Signals(number).name + "\n")
    if mode != "deaf":
        sys.exit(0)
for number in (signal.SIGINT, signal.SIGTERM):
    signal.signal(number, received)
if mode.startswith("exit"):
    sys.exit(int(mode[4:]))
time.sleep(300)
FAKE
  cat > "$dir/gateway" <<FAKE
#!/bin/bash
exec python3 "$dir/gateway.py" "\$@"
FAKE
  chmod +x "$dir/psql" "$dir/pg_isready" "$dir/gateway"
}

# Starts the guard in the background; sets dir and guard_pid.
start_guard() {
  local role="$1" ready="$2" mode="${3:-sleep}" port="${4:-5432}"
  dir="$(mktemp -d)"
  printf '%b' "$role" > "$dir/role"
  printf '%b' "$ready" > "$dir/ready"
  echo "$mode" > "$dir/mode"
  make_fakes "$dir" "$port"
  PATH="$dir:$PATH" GATEWAY_GUARD_INTERVAL=0.1 GATEWAY_GUARD_PROBE_TIMEOUT=1 GATEWAY_GUARD_OUTAGE_LIMIT=1 \
    GATEWAY_GUARD_STOP_GRACE=1 GATEWAY_ENTRYPOINT="$dir/gateway" \
    env --default-signal=INT "$GUARD" --pg-port "$port" > "$dir/out" 2>&1 &
  guard_pid=$!
  # A background job starts with SIGINT ignored; the guard, as PID 1, does not.
  for _ in $(seq 50); do [ -s "$dir/pid" ] && break; sleep 0.05; done
}

gateway_alive() { kill -0 "$(cat "$dir/pid" 2>/dev/null || echo 999999)" 2>/dev/null && echo yes || echo no; }

# Waits for the guard to end by itself and sets rc; 124 if it is still up after $1 seconds.
await_guard() {
  local deadline=$((SECONDS + $1))
  while kill -0 "$guard_pid" 2>/dev/null; do
    if [ "$SECONDS" -ge "$deadline" ]; then rc=124; return; fi
    sleep 0.05
  done
  wait "$guard_pid"; rc=$?
}

finish() {
  kill -KILL "$guard_pid" 2>/dev/null; wait "$guard_pid" 2>/dev/null
  kill -KILL "$(cat "$dir/pid" 2>/dev/null || echo 999999)" 2>/dev/null
  rm -rf "$dir"
}

check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then echo "ok   $name"; else echo "FAIL $name: expected '$expected', got '$actual'"; failures=$((failures + 1)); fi
}

logged() { grep -c -- "$1" "$dir/out"; }

start_guard 'f\nf\nt\n' '0\n'; await_guard 5
check 'a demoted primary ends the gateway' 'no 1' "$(gateway_alive) $rc"
check 'it says when it arms' 1 "$(logged 'armed')"
check 'it says when it ends the gateway' 1 "$(logged 'no longer the primary')"
finish

start_guard 'f\nfail\nfail\nt\n' '0\n'; await_guard 5
check 'a demotion across a Postgres restart ends the gateway' 'no 1' "$(gateway_alive) $rc"
finish

start_guard 't\n' '0\n'; await_guard 2
check 'a standby that was never primary keeps its gateway' 'yes 124' "$(gateway_alive) $rc"
check 'and is not armed' 0 "$(logged 'armed')"
finish

start_guard 't\nf\n' '0\n'; await_guard 2
check 'a promoted standby keeps its gateway' 'yes 124' "$(gateway_alive) $rc"
check 'and arms' 1 "$(logged 'armed')"
finish

start_guard 'f\nfail\nfail\nf\n' '1\n0\n'; await_guard 2
check 'a primary back as primary inside the limit keeps its gateway' 'yes 124' "$(gateway_alive) $rc"
check 'and says the probe recovered' 1 "$(logged 'probe recovered')"
finish

start_guard 'fail\n' '2\n'; await_guard 3
check 'Postgres not up yet is not an outage, however long' 'yes 124' "$(gateway_alive) $rc"
finish

start_guard "$(for _ in $(seq 20); do printf 'f\\nfail\\n'; done)f\\n" '2\n'; await_guard 2
check 'short outages do not add up to the limit' 'yes 124' "$(gateway_alive) $rc"
finish

start_guard 'f\nfail\n' '2\n'; await_guard 5
check 'Postgres gone past the limit ends the gateway' 'no 1' "$(gateway_alive) $rc"
check 'and says why' 1 "$(logged 'not answered')"
finish

start_guard 't\nfail\n' '2\n'; await_guard 5
check 'that holds for a standby too' 'no 1' "$(gateway_alive) $rc"
finish

start_guard 'f\nfail\n' '0\n'; await_guard 3
check 'a refused login with Postgres up keeps the gateway' 'yes 124' "$(gateway_alive) $rc"
check 'the probe failure is logged with its reason' 1 "$(grep -c 'probe failing.*does not exist' "$dir/out")"
finish

start_guard 'f\nfail\n' '1\n'; await_guard 5
check 'pg_isready rejecting counts as down' 'no 1' "$(gateway_alive) $rc"
finish

start_guard 'f\nhang\n' '0\n'; await_guard 3
check 'a hung psql does not stop the guard working' 'yes 124' "$(gateway_alive) $rc"
check 'it is logged as failing' 1 "$(logged 'probe failing')"
kill -KILL "$(cat "$dir/pid")"; await_guard 4
check 'a gateway dying during hung probes is noticed' 137 "$rc"
finish

start_guard 'f\nhang\n' 'hang\n'; await_guard 6
check 'a hung pg_isready counts as down' 'no 1' "$(gateway_alive) $rc"
finish

start_guard 'f\n' '0\n' exit7; await_guard 3
check "the gateway's exit code comes through" 7 "$rc"
finish

start_guard 'f\n' '0\n'
kill -KILL "$(cat "$dir/pid")"; await_guard 3
check 'the guard ends when the gateway dies' 137 "$rc"
finish

for signal_name in TERM INT; do
  start_guard 'f\n' '0\n'; sleep 0.3
  kill "-$signal_name" "$guard_pid"; await_guard 3
  check "SIG$signal_name ends guard and gateway" 'no 0' "$(gateway_alive) $rc"
  check "SIG$signal_name reaches the gateway as a clean SIGINT" SIGINT "$(cat "$dir/signals")"
  finish
done

start_guard 'f\n' '0\n' deaf; sleep 0.3
kill -TERM "$guard_pid"; await_guard 4
check 'a gateway deaf to SIGINT is killed after the grace' no "$(gateway_alive)"
check 'and the guard does not wait on it for ever' 137 "$rc"
finish

start_guard 'f\nt\n' '0\n' deaf; await_guard 4
check 'a gateway deaf to SIGINT on a demotion is killed too' 'no 1' "$(gateway_alive) $rc"
finish

start_guard 'f\nfail\n' '0\n' sleep 5433; await_guard 3
check 'pg_isready follows the port too' 'yes 124' "$(gateway_alive) $rc"
finish

start_guard 'f\n' '0\n' sleep 5433; sleep 0.3
check 'the plugin arguments reach the gateway' '--pg-port 5433' "$(cat "$dir/args")"
check 'and the probe follows the port' 0 "$(logged 'probe failing')"
check 'and arms on it' 1 "$(logged 'armed')"
finish

exit "$failures"
