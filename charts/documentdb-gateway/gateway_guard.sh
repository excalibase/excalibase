#!/bin/bash
# Runs the upstream gateway and ends it when its clients can only be failing:
# this instance was primary and is now a standby, or Postgres has stopped
# answering. The gateway keeps client connections across both, reports itself
# writable and answers with errors that make no driver reconnect. Ending it
# resets them, so they come back through the Service, to the current primary.
set -u

INTERVAL="${GATEWAY_GUARD_INTERVAL:-3}"
PROBE_TIMEOUT="${GATEWAY_GUARD_PROBE_TIMEOUT:-5}"
OUTAGE_LIMIT="${GATEWAY_GUARD_OUTAGE_LIMIT:-15}"
STOP_GRACE="${GATEWAY_GUARD_STOP_GRACE:-5}"
ENTRYPOINT="${GATEWAY_ENTRYPOINT:-/home/documentdb/gateway/scripts/gateway_entrypoint.sh}"

pg_port=5432
previous=""
for argument in "$@"; do
  [ "$previous" = "--pg-port" ] && pg_port="$argument"
  previous="$argument"
done

log() { echo "gateway guard: $*"; }

"$ENTRYPOINT" "$@" &
gateway=$!

# SIGINT is the gateway's clean exit; until it has started, the entrypoint's
# wait for Postgres has it ignored, hence the kill.
stop_gateway() {
  kill -INT "$gateway" 2>/dev/null
  local deadline=$((SECONDS + STOP_GRACE))
  while kill -0 "$gateway" 2>/dev/null && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
  kill -KILL "$gateway" 2>/dev/null
}

end_clients() {
  log "$1, ending client connections"
  stop_gateway
  exit 1
}

on_signal() {
  stop_gateway
  wait "$gateway"
  exit $?
}
trap on_signal TERM INT

was_primary=false
postgres_seen=false
down_since=""
probe_failing=false

postgres_answered() {
  postgres_seen=true
  down_since=""
}

note_role() {
  if [ "$probe_failing" = true ]; then
    log "role probe recovered"
    probe_failing=false
  fi
  postgres_answered
  if [ "$1" = t ] && [ "$was_primary" = true ]; then
    end_clients "this instance is no longer the primary"
  fi
  if [ "$1" = f ] && [ "$was_primary" = false ]; then
    log "armed: this instance is the primary"
    was_primary=true
  fi
}

# Not a failed login: pg_isready says whether Postgres itself is there.
note_probe_failure() {
  if [ "$probe_failing" = false ]; then
    log "role probe failing: $(echo "$1" | head -1 | cut -c1-200)"
    probe_failing=true
  fi
  if timeout "$PROBE_TIMEOUT" pg_isready -h localhost -p "$pg_port" -q >/dev/null 2>&1; then
    postgres_answered
    return
  fi
  # Before Postgres has first answered, this is only the normal start order.
  [ "$postgres_seen" = true ] || return
  [ -n "$down_since" ] || down_since=$SECONDS
  if [ $((SECONDS - down_since)) -ge "$OUTAGE_LIMIT" ]; then
    end_clients "Postgres has not answered for ${OUTAGE_LIMIT}s"
  fi
}

while kill -0 "$gateway" 2>/dev/null; do
  answer="$(timeout "$PROBE_TIMEOUT" psql -h localhost -p "$pg_port" -U documentdb -d postgres -XAtc 'SELECT pg_is_in_recovery()' 2>&1)"
  case "$answer" in
    t | f) note_role "$answer" ;;
    *) note_probe_failure "${answer:-no answer within ${PROBE_TIMEOUT}s}" ;;
  esac
  sleep "$INTERVAL" &
  wait $! || true
done
wait "$gateway"
exit $?
