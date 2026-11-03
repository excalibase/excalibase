# Sourced by boot-smoke.sh with LOG_DRAINS=1 (EXC-658): log drains on a single host.
# drains_setup runs before the stack starts: a test HTTPS intake on a test-net
# network (198.51.100.0/24, public to the forwarder's dialer) whose CA the
# forwarder trusts; TLS stays verified. drains_journey then checks that a line
# reaches its own project's drain only, a private address is refused on save,
# the host's own address (that network's gateway) is refused at send, the key
# is in no log or table in the clear, and with the flag off nothing is sent.
# Needs W, T, PID, ORG, PORT, DOCKER, COMPOSE, P, c, ok, ko, status; sets PID2.
DRAIN_SINK=smoke-drain-sink-excalibase
DRAIN_NET=excalibase-smoke-sink
DRAIN_SINK_IP=198.51.100.10
DRAIN_HOST_IP=198.51.100.1

drains_teardown() {
  $DOCKER rm -f "$DRAIN_SINK" >/dev/null 2>&1
  $DOCKER network rm "$DRAIN_NET" >/dev/null 2>&1
}

drains_setup() {
  drains_teardown
  local dir=$W/sink
  mkdir -p "$dir"
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=smoke-drain-ca -keyout "$dir/ca.key" -out "$dir/ca.pem" 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -subj /CN=smoke-drain-sink -keyout "$dir/sink.key" -out "$dir/sink.csr" 2>/dev/null
  printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\n' "$DRAIN_SINK_IP" > "$dir/sink.ext"
  openssl x509 -req -in "$dir/sink.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial -days 1 -extfile "$dir/sink.ext" -out "$dir/sink.pem" 2>/dev/null
  cp "$TESTS/drain_sink.py" "$dir/"
  chmod 644 "$dir"/*
  $DOCKER network create --subnet 198.51.100.0/24 --gateway "$DRAIN_HOST_IP" "$DRAIN_NET" >/dev/null
  $DOCKER run -d --name "$DRAIN_SINK" --network "$DRAIN_NET" --ip "$DRAIN_SINK_IP" --tmpfs /data \
    -v "$dir:/sink:ro" docker.io/library/python:3.12-alpine python /sink/drain_sink.py >/dev/null
  cat > "$W/drains.override.yml" <<YAML
services:
  log-forwarder:
    image: docker.io/excalibase/provisioning:\${PLATFORM_TAG:-\${TAG:-latest}}
    environment:
      SSL_CERT_FILE: /sink/ca.pem
    volumes:
      - $dir/ca.pem:/sink/ca.pem:ro
    networks:
      - smoke-sink
networks:
  smoke-sink:
    name: $DRAIN_NET
    external: true
YAML
  P="$P -f $W/drains.override.yml"
}

dapi() { local path=$1; shift; c -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@" "https://studio.localhost:$PORT/api/projects$path"; }
sink_log() { $DOCKER exec "$DRAIN_SINK" cat "/data/$1.log" 2>/dev/null; }
drain_field() {
  dapi "/$1/log-drains/" | python3 -c 'import json,sys
for d in json.load(sys.stdin):
    if d["name"] == sys.argv[1]: print(d.get(sys.argv[2]) or "")' "$2" "$3" 2>/dev/null
}
new_drain() {
  dapi "/$1/log-drains/" -X POST -o /dev/null -w '%{http_code}' \
    -d "{\"name\":\"$2\",\"kind\":\"https\",\"url\":\"$3\",\"headerName\":\"Authorization\",\"secret\":\"$4\"}"
}
log_line() {
  c -o /dev/null -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
    "https://studio.localhost:$PORT/api/schema/$1/query" -d "{\"query\":\"SELECT * FROM $2\"}"
}
unseal_after_restart() {
  if [ -n "${MANUAL_UNSEAL:-}" ]; then
    for i in $(seq 60); do [ "$(status)" = sealed ] && break; sleep 2; done
    printf '%s\n%s\n' "$T" "$UNSEAL_KEY" | $DOCKER exec -i excalibase-provisioning excalibase-provisioning vault unseal >/dev/null 2>&1
  fi
  for i in $(seq 60); do c -o /dev/null -w "%{http_code}" "https://studio.localhost:$PORT/api/auth/me" 2>/dev/null | grep -q 401 && break; sleep 2; done
}

drains_journey() {
  echo "== log drains"
  [ "$($DOCKER inspect excalibase-log-forwarder --format '{{.State.Status}}' 2>/dev/null)" = running ] && ok "the log forwarder runs" || ko "no running log forwarder: $($DOCKER logs excalibase-log-forwarder 2>&1 | tail -3)"
  fmounts=$($DOCKER inspect excalibase-log-forwarder --format '{{range .Mounts}}{{.Source}} {{end}}')
  case "$fmounts" in *sock*) ko "the forwarder mounts a socket: $fmounts" ;; *) ok "the forwarder holds no engine socket" ;; esac
  $DOCKER exec excalibase-log-forwarder getent hosts postgres >/dev/null 2>&1 && ko "the forwarder resolves the platform database" || ok "the forwarder cannot reach the platform database"
  logs_have excalibase-log-forwarder "refusing the host's own" && ok "the forwarder read the host's own addresses" || ko "forwarder: $($DOCKER logs excalibase-log-forwarder 2>&1 | tail -3)"

  for url in https://10.1.2.3/x https://169.254.169.254/latest https://127.0.0.1/x; do
    code=$(new_drain "$PID" private "$url" "Bearer x")
    [ "$code" = 400 ] && ok "a drain aimed at $url is refused on save" || ko "drain at $url answered $code"
  done

  R2=$(c --max-time 600 -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "https://studio.localhost:$PORT/api/provision/" \
    -d "{\"projectName\":\"other\",\"orgId\":\"$ORG\",\"databaseType\":\"POSTGRESQL\",\"postgresVersion\":\"17\"}")
  PID2=$(echo "$R2" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("projectId",""))' 2>/dev/null)
  for i in $(seq 60); do st2=$(c -H "Authorization: Bearer $T" "https://studio.localhost:$PORT/api/provision/$PID2/" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status"))' 2>/dev/null); case "$st2" in ACTIVE|FAILED) break;; esac; sleep 3; done
  [ "$st2" = ACTIVE ] && ok "a second project ($PID2) for isolation" || ko "second project: $st2 ${R2:0:300}"

  SECRET_A="Bearer smoke-drain-$(openssl rand -hex 12)"
  [ "$(new_drain "$PID" own "https://$DRAIN_SINK_IP/a" "$SECRET_A")" = 201 ] && ok "drain A saved" || ko "drain A not saved"
  [ "$(new_drain "$PID2" own "https://$DRAIN_SINK_IP/b" "Bearer smoke-drain-b")" = 201 ] && ok "drain B saved" || ko "drain B not saved"
  [ "$(new_drain "$PID" host "https://$DRAIN_HOST_IP/host" "Bearer smoke-drain-host")" = 201 ] && ok "a drain at the host's own address saves (only the forwarder knows the host)" || ko "drain at the host not saved"

  MARK_A=exc658a_$(date +%s)_$RANDOM MARK_B=exc658b_$(date +%s)_$RANDOM
  log_line "$PID" "$MARK_A"; log_line "$PID2" "$MARK_B"
  for i in $(seq 60); do
    case "$(sink_log a) $(sink_log b)" in *"$MARK_A"*"$MARK_B"*) break ;; esac
    sleep 3
  done
  case "$(sink_log a)" in *"$MARK_A"*) ok "the line reached its own project's drain" ;; *) ko "drain A got: $(sink_log a | tail -c 300)" ;; esac
  case "$(sink_log b)" in *"$MARK_B"*) ok "the other project's line reached its drain" ;; *) ko "drain B got: $(sink_log b | tail -c 300)" ;; esac
  case "$(sink_log a)" in *"$MARK_B"*) ko "drain A received the other project's line" ;; *) ok "drain A holds no line of the other project" ;; esac
  case "$(sink_log b)" in *"$MARK_A"*) ko "drain B received project A's line" ;; *) ok "drain B holds no line of project A" ;; esac
  grep -qxF "auth=$SECRET_A" <<< "$(sink_log a)" && ok "drain A was sent its header over verified TLS" || ko "drain A's header did not arrive"

  herr=
  for i in $(seq 30); do herr=$(drain_field "$PID" host lastError); [ -n "$herr" ] && break; sleep 3; done
  case "$herr" in *"not a public address"*) ok "a drain at the host's own address is refused at send ($herr)" ;; *) ko "drain at the host: '$herr'" ;; esac
  [ -z "$(sink_log host)" ] && ok "nothing reached the host address" || ko "the host address was sent lines"

  leaked=0
  for name in excalibase-provisioning excalibase-log-forwarder; do logs_have "$name" "$SECRET_A" && leaked=1; done
  $DOCKER exec excalibase-postgres pg_dump -U excalibase excalibase_platform 2>/dev/null | grep -qF "$SECRET_A" && leaked=1
  $DOCKER exec excalibase-provisioning grep -rqF "$SECRET_A" /var/lib/excalibase 2>/dev/null && leaked=1
  [ "$leaked" = 0 ] && ok "the drain's key is in no log, table or file in the clear" || ko "the drain's key leaked"

  echo "== log drains: flag off"
  sed -i 's/^FEATURE_LOGDRAINS=true$/FEATURE_LOGDRAINS=false/' "$W/.env"
  $COMPOSE $P up -d provisioning >/dev/null 2>&1
  unseal_after_restart
  [ "$(dapi "/$PID/log-drains/" -o /dev/null -w '%{http_code}')" = 404 ] && ok "flag off: the drain routes answer 404" || ko "flag off: drain routes still answer"
  MARK_OFF=exc658off_$(date +%s)_$RANDOM
  log_line "$PID" "$MARK_OFF"
  stored=
  for i in $(seq 20); do
    stored=$($DOCKER exec excalibase-provisioning wget -qO- "http://loki:3100/loki/api/v1/query_range?query=$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1],safe=""))' "{project_id=\"$PID\"} |= \"$MARK_OFF\"")&start=$(( ($(date +%s) - 600) * 1000000000 ))" 2>/dev/null)
    case "$stored" in *"$MARK_OFF"*) break ;; esac
    sleep 3
  done
  case "$stored" in *"$MARK_OFF"*) ok "flag off: the line is still stored, so a drain would take it" ;; *) ko "flag off: the line is not in Loki" ;; esac
  sleep 40
  [ "$($DOCKER inspect excalibase-log-forwarder --format '{{.State.Status}}' 2>/dev/null)" = running ] && ok "flag off: the forwarder still runs and pulls" || ko "flag off: the forwarder stopped"
  case "$(sink_log a)" in *"$MARK_OFF"*) ko "flag off: a line was still sent" ;; *) ok "flag off: nothing is sent" ;; esac
}
