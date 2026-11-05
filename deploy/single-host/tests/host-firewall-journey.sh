# Sourced by boot-smoke.sh after the first project (EXC-683): on Docker the host
# firewall is healthy with its rules on the networks' real bridges; a tenant
# database cannot open a connection to a host service listening on every
# address, nor reach the metadata address or a private range forwarded, while a
# container on a bridge the firewall does not cover can reach the host (so the
# probe and the listener work). The rest of boot-smoke.sh is the platform's own
# traffic still flowing. Needs from boot-smoke.sh: ok ko DOCKER ENGINE TC.

# fw_ipt <args>: iptables in the firewall, on the backend holding DOCKER-USER.
fw_ipt() {
  $DOCKER exec excalibase-host-firewall sh -c \
    'for b in iptables-nft iptables-legacy; do $b -w -S DOCKER-USER >/dev/null 2>&1 && exec $b -w "$@"; done; exit 1' fw "$@"
}

# drops <range>: packets the tenant chain dropped to range so far.
drops() {
  fw_ipt -L EXCALIBASE-TENANT -v -n -x | awk -v range="$1" '$3 == "DROP" && $9 == range { print $1 }'
}

host_firewall_journey() {
  local gw port listener reach tenants_bridge health before after target
  echo "== host firewall"
  if [ "$ENGINE" != docker ]; then
    ok "rootless Podman: bridges live in the user's namespace, no host firewall needed"
    return
  fi
  for _ in $(seq 90); do
    health=$($DOCKER inspect excalibase-host-firewall --format '{{.State.Health.Status}}' 2>/dev/null)
    [ "$health" = healthy ] && break
    sleep 2
  done
  [ "$health" = healthy ] && ok "host firewall healthy: its rules hold" \
    || ko "host firewall $health: $($DOCKER logs excalibase-host-firewall 2>&1 | tail -3)"

  # The bridge the engine gave the tenants' network, not a name compose asked for.
  tenants_bridge=$($DOCKER network inspect excalibase-tenants --format '{{index .Options "com.docker.network.bridge.name"}}')
  [ -n "$tenants_bridge" ] || tenants_bridge=br-$($DOCKER network inspect excalibase-tenants --format '{{.Id}}' | cut -c1-12)
  fw_ipt -C INPUT -i "$tenants_bridge" -j EXCALIBASE-HOST && fw_ipt -C DOCKER-USER -i "$tenants_bridge" -j EXCALIBASE-TENANT \
    && ok "the guard is on the tenants' real bridge $tenants_bridge" || ko "no rule on $tenants_bridge: $(fw_ipt -S INPUT | tail -3)"

  port=$(python3 -c 'import socket;s=socket.socket();s.bind(("0.0.0.0",0));print(s.getsockname()[1])')
  python3 -m http.server --bind 0.0.0.0 "$port" >/dev/null 2>&1 &
  listener=$!
  # bash's /dev/tcp: the tenant image has no client of its own.
  reach='timeout 5 bash -c "exec 3<>/dev/tcp/$0/$1" 2>/dev/null && echo open || echo closed'
  gw=$($DOCKER network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
  case "$($DOCKER run --rm --network bridge "$($DOCKER inspect "$TC" --format '{{.Config.Image}}')" bash -c "$reach" "$gw" "$port" 2>&1)" in
    *open*) ok "control: a container on an unguarded bridge reaches the host listener" ;;
    *) ko "control: the host listener is unreachable from any bridge; the check below proves nothing" ;;
  esac
  gw=$($DOCKER network inspect excalibase-tenants --format '{{(index .IPAM.Config 0).Gateway}}')
  case "$($DOCKER exec "$TC" bash -c "$reach" "$gw" "$port" 2>&1)" in
    *closed*) ok "a tenant database cannot reach a host service through $tenants_bridge" ;;
    *) ko "a tenant database reached the host at $gw:$port" ;;
  esac
  kill "$listener" 2>/dev/null

  # Metadata and the LAN: whether or not this host has a metadata service, the
  # tenant's attempt must end on the firewall's DROP for its range.
  for target in 169.254.169.254:169.254.0.0/16 10.255.255.254:10.0.0.0/8; do
    before=$(drops "${target#*:}")
    case "$($DOCKER exec "$TC" bash -c "$reach" "${target%%:*}" 80 2>&1)" in
      *closed*) ;;
      *) ko "a tenant database reached ${target%%:*}:80" ;;
    esac
    after=$(drops "${target#*:}")
    [ -n "$before" ] && [ "${after:-0}" -gt "$before" ] \
      && ok "a tenant database cannot reach ${target%%:*} (dropped by the ${target#*:} rule)" \
      || ko "${target%%:*} from a tenant: drops $before -> $after on ${target#*:}"
  done
}
