# Sourced by boot-smoke.sh (EXC-666): a single host runs one copy of each
# database, so its read address reports none and refuses a read port.
read_address_journey() {
  local pid=$1 engine=$2 view code
  echo "== read address ($engine)"
  view=$(c -H "Authorization: Bearer $T" https://studio.localhost:$PORT/api/projects/$pid/db-read-address)
  if echo "$view" | python3 -c 'import json,sys;d=json.load(sys.stdin);sys.exit(0 if d.get("reason")=="single-host" and d.get("singleHost") and d.get("replicas")==0 and not d.get("internal") and not d.get("port") and not d.get("canChange") else 1)' 2>/dev/null; then
    ok "$engine read address reports one copy on a single host"
  else
    ko "$engine read address: ${view:0:300}"
  fi
  code=$(c -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $T" -H 'Content-Type: application/json' https://studio.localhost:$PORT/api/projects/$pid/db-read-address -d '{"enabled":true}')
  [ "$code" = 409 ] && ok "$engine read port refused on a single host" || ko "$engine read port PUT answered $code"
}
