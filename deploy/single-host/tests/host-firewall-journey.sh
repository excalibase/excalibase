# Sourced by boot-smoke.sh after the first project (EXC-683): on Docker a tenant
# database cannot open a connection to a host service listening on every address
# through its bridge, while a container on a bridge the firewall does not cover can
# (so the probe and the listener work). Needs from boot-smoke.sh: ok ko DOCKER ENGINE TC.

host_firewall_journey() {
  local gw port listener reach
  echo "== host firewall"
  if [ "$ENGINE" != docker ]; then
    ok "rootless Podman: bridges live in the user's namespace, no host firewall needed"
    return
  fi
  for i in $(seq 30); do $DOCKER logs excalibase-host-firewall 2>&1 | grep -q 'are dropped' && break; sleep 2; done
  $DOCKER logs excalibase-host-firewall 2>&1 | grep -q 'are dropped' && ok "host firewall set its rules" \
    || ko "host firewall: $($DOCKER logs excalibase-host-firewall 2>&1 | tail -3)"

  port=$(python3 -c 'import socket;s=socket.socket();s.bind(("0.0.0.0",0));print(s.getsockname()[1])')
  python3 -m http.server --bind 0.0.0.0 "$port" >/dev/null 2>&1 &
  listener=$!
  # bash's /dev/tcp: the tenant image has no client of its own.
  reach='timeout 5 bash -c "exec 3<>/dev/tcp/$0/$1" && echo open || echo closed'
  gw=$($DOCKER network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
  case "$($DOCKER run --rm --network bridge "$($DOCKER inspect "$TC" --format '{{.Config.Image}}')" bash -c "$reach" "$gw" "$port" 2>&1)" in
    *open*) ok "control: a container on an unguarded bridge reaches the host listener" ;;
    *) ko "control: the host listener is unreachable from any bridge; the check below proves nothing" ;;
  esac
  gw=$($DOCKER network inspect excalibase-tenants --format '{{(index .IPAM.Config 0).Gateway}}')
  case "$($DOCKER exec "$TC" bash -c "$reach" "$gw" "$port" 2>&1)" in
    *closed*) ok "a tenant database cannot reach a host service through exb-tenants" ;;
    *) ko "a tenant database reached the host at $gw:$port" ;;
  esac
  kill "$listener" 2>/dev/null
}
