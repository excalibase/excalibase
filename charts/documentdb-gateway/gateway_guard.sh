#!/bin/bash
# Runs the upstream gateway and ends it when this instance stops being the
# primary. The gateway keeps its client connections across a demotion and
# answers every write on them with an error for ever; a driver has no reason to
# leave. Ending them sends clients back through the Service, to the new primary.
set -u

INTERVAL="${GATEWAY_GUARD_INTERVAL:-3}"
ENTRYPOINT="${GATEWAY_ENTRYPOINT:-/home/documentdb/gateway/scripts/gateway_entrypoint.sh}"

"$ENTRYPOINT" "$@" &
gateway=$!

# Upstream runs as PID 1, where SIGTERM has no default action.
trap 'kill -KILL "$gateway" 2>/dev/null; exit 0' TERM INT

in_recovery() {
  PGCONNECT_TIMEOUT=2 psql -h localhost -U documentdb -d postgres -XAtc 'SELECT pg_is_in_recovery()' 2>/dev/null
}

was_primary=false
while kill -0 "$gateway" 2>/dev/null; do
  case "$(in_recovery)" in
    f) was_primary=true ;;
    t)
      if [ "$was_primary" = true ]; then
        echo "gateway guard: this instance is no longer the primary, ending client connections"
        kill -KILL "$gateway"
        exit 1
      fi
      ;;
  esac
  sleep "$INTERVAL" &
  wait $! || true
done
wait "$gateway"
exit $?
