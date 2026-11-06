#!/usr/bin/env sh
# EXC-687 on real bridges and real iptables: a Docker-in-Docker machine (its own
# network namespace, so the host's firewall is untouched) runs the bundle's firewall
# script, with project networks made as provisioning makes them, and upstreams that
# really answer for the metadata address, private ranges and the internet.
#
#   sh deploy/single-host/tests/project-bridge-fence.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
IMG=docker.io/library/python:3.12-alpine
DIND=${DIND_IMAGE:-docker.io/library/docker:29-dind}
M=exc687-fence-$$
PUBLIC=203.0.113.5
DENIED=198.51.100.5
trap 'docker rm -f -v "$M" >/dev/null 2>&1 || true' EXIT
failed=0
ok() { echo "ok: $*"; }
ko() { echo "FAIL: $*" >&2; failed=1; }
d() { docker exec "$M" docker "$@"; }
on_machine() { docker exec "$M" sh -c "$1"; }

SERVE='import socket,sys
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(("0.0.0.0",int(sys.argv[1])));s.listen(50)
while True:
    c,_=s.accept();c.close()'
PROBE='import socket,sys
for spec in sys.argv[1:]:
    name,host,port=spec.split("=")
    s=socket.socket();s.settimeout(2)
    try: s.connect((host,int(port)));r="OPEN"
    except socket.timeout: r="dropped"
    except OSError as e: r="refused" if e.errno==111 else "unreachable"
    finally: s.close()
    print(name,r,flush=True)'

docker run -d --privileged --name "$M" -v /var/lib/docker -e DOCKER_TLS_CERTDIR= "$DIND" >/dev/null
tries=0
until d version >/dev/null 2>&1; do tries=$((tries + 1)); [ "$tries" -lt 120 ] || { ko "no daemon"; exit 1; }; sleep 1; done
docker image inspect "$IMG" >/dev/null 2>&1 || docker pull -q "$IMG" >/dev/null
docker save "$IMG" | docker exec -i "$M" docker load >/dev/null
docker exec "$M" apk add -q iproute2 >/dev/null
docker cp -q "$DIR/../host-firewall.sh" "$M:/host-firewall.sh"

# upstream <name> <machine's address> <its address>: a server off every bridge, routed by the machine.
upstream() {
  d run -d --name "up-$1" --network none "$IMG" python -c "$SERVE" 80 >/dev/null
  pid=$(d inspect -f '{{.State.Pid}}' "up-$1")
  on_machine "ip link add v$1 type veth peer name p$1 && ip link set p$1 netns $pid && ip addr add $2/24 dev v$1 && ip link set v$1 up && nsenter -t $pid -n ip addr add $3/24 dev p$1 && nsenter -t $pid -n ip link set p$1 up && nsenter -t $pid -n ip route add default via $2"
}
upstream meta 169.254.169.1 169.254.169.254
upstream prv 10.77.0.1 10.77.0.5
upstream lan 192.168.77.1 192.168.77.5
upstream inet 203.0.113.1 "$PUBLIC"
upstream deny 198.51.100.1 "$DENIED"
d run -d --name host-service --network host "$IMG" python -c "$SERVE" 2222 >/dev/null
ETH=$(on_machine "ip -4 -o addr show eth0 | awk '{print \$4}' | cut -d/ -f1")

d network create excalibase-tenants >/dev/null
d network create --subnet 10.203.250.0/28 excalibase-edge >/dev/null
# What the edge publishes, and something else published on the host.
d run -d --name edge --network excalibase-edge -p 0.0.0.0:443:443 -p 0.0.0.0:8443:8443 "$IMG" sh -c \
  "python -c '$SERVE' 443 & python -c '$SERVE' 8443 & wait" >/dev/null
d run -d --name tenant-db --network excalibase-tenants "$IMG" python -c "$SERVE" 8080 >/dev/null

mkdir_bridges='mkdir -p /var/lib/excalibase-bridges; : >/var/lib/excalibase-bridges/networks'
on_machine "$mkdir_bridges"
for network in excalibase-tenants excalibase-edge; do
  id=$(d network inspect "$network" -f '{{.Id}}' | cut -c1-12)
  subnet=$(d network inspect "$network" -f '{{(index .IPAM.Config 0).Subnet}}')
  on_machine "echo '$network br-$id $subnet' >>/var/lib/excalibase-bridges/networks"
done
on_machine 'printf "nameserver 8.8.8.8\n" >/tmp/resolv'
firewall() {
  on_machine "HOST_FIREWALL_NETWORKS=excalibase-tenants,excalibase-edge HOST_FIREWALL_TENANT_NETWORKS=excalibase-tenants \
    HOST_FIREWALL_PROJECT_BRIDGES=exnet+ HOST_FIREWALL_EDGE_NETWORK=excalibase-edge HOST_FIREWALL_RESOLV_CONF=/tmp/resolv \
    APP_EGRESS_EXTRA_DENY_CIDRS=$DENIED/32 HOST_FIREWALL_ONCE=1 sh /host-firewall.sh $1"
}
firewall "" >/dev/null
firewall check || ko "the firewall's own check fails on real iptables"
ok "the firewall's rules hold on real iptables, with no project bridge yet"

# As provisioning names them: engineproxy.ProjectBridgeName.
bridge_name() { printf 'exnet%s' "$(printf '%s' "$1" | sha256sum | cut -c1-10)"; }
LABELS="--label excalibase.managed=true"
fenced_network() { d network create --driver bridge -o "com.docker.network.bridge.name=$(bridge_name "$1")" $LABELS "$1" >/dev/null; }

# Nothing told the firewall of this bridge, and the first thing the app does is try.
fenced_network excalibase-net-a
FIRST=$(d run --rm --name excalibase-app-first --network excalibase-net-a "$IMG" python -c "$PROBE" metadata=169.254.169.254=80 private=10.77.0.5=80 public="$PUBLIC"=80)
case "$FIRST" in
  *"metadata dropped"*"private dropped"*"public OPEN"*) ok "a bridge made after the firewall is fenced from an app's first instruction, internet open" ;;
  *) ko "the first instruction on a new bridge: $FIRST" ;;
esac

fenced_network excalibase-net-b
d network create --driver bridge --internal -o com.docker.network.bridge.gateway_mode_ipv4=isolated \
  -o com.docker.network.bridge.gateway_mode_ipv6=isolated $LABELS excalibase-net-c >/dev/null
serve_on() { n=$1; shift; d run -d --name "$n" --label excalibase.managed=true "$@" "$IMG" python -c "$SERVE" 8080 >/dev/null; }
serve_on app-a --network excalibase-net-a
serve_on peer-a --network excalibase-net-a
serve_on app-b --network excalibase-net-b
serve_on app-c --network excalibase-net-c
serve_on db-a --network excalibase-tenants
d network connect excalibase-net-a db-a
d run -d --name documentdb-a --network container:db-a "$IMG" sleep 100000 >/dev/null
d run -d --name control "$IMG" sleep 100000 >/dev/null

ip_on() { d inspect -f "{{(index .NetworkSettings.Networks \"$2\").IPAddress}}" "$1"; }
GW=$(d network inspect excalibase-net-a -f '{{(index .IPAM.Config 0).Gateway}}')
TARGETS="metadata=169.254.169.254=80 private=10.77.0.5=80 lan=192.168.77.5=80 extra-deny=$DENIED=80
host-address=$ETH=2222 host-gateway=$GW=2222 other-published=$ETH=8443 edge-published=$ETH=443
other-project=$(ip_on app-b excalibase-net-b)=8080 tenant-db=$(ip_on tenant-db excalibase-tenants)=8080
public=$PUBLIC=80 own-peer=$(ip_on peer-a excalibase-net-a)=8080"
probe() { d exec "$1" python -c "$PROBE" $TARGETS; }
result() { echo "$1" | awk -v want="$2" '$1 == want { print $2 }'; }

# control: on a bridge nothing guards, what the app must not reach answers, so its refusal means something.
CONTROL=$(probe control)
for target in metadata private lan extra-deny host-address public; do
  [ "$(result "$CONTROL" "$target")" = OPEN ] && ok "control: $target answers from an unguarded bridge" \
    || ko "control: $target does not answer from an unguarded bridge ($(result "$CONTROL" "$target")): the checks below prove nothing"
done

check_from() {
  from=$1
  got=$(probe "$from")
  for target in metadata private lan extra-deny host-address host-gateway other-published other-project; do
    [ "$(result "$got" "$target")" = dropped ] && ok "$from: $target dropped" || ko "$from: $target $(result "$got" "$target")"
  done
  for target in public edge-published; do
    [ "$(result "$got" "$target")" = OPEN ] && ok "$from: $target open" || ko "$from: $target $(result "$got" "$target")"
  done
}
check_from app-a
[ "$(result "$(probe app-a)" own-peer)" = OPEN ] && ok "app-a: its own project answers" || ko "app-a cannot reach its own project"
[ "$(result "$(probe app-a)" tenant-db)" = dropped ] && ok "app-a: another project's database dropped" || ko "app-a reached a tenant directly"
# Joining the project network moves a database's default route onto the project bridge.
d exec db-a ip route show default | grep -q "via $GW" && ok "db-a: its default route is on the project bridge" || ko "db-a's default route: $(d exec db-a ip route show default)"
check_from db-a
check_from documentdb-a

# APP_EGRESS=none on Docker 28+: nothing to guard, and no route anyway.
NONE=$(d exec app-c python -c "$PROBE" metadata=169.254.169.254=80 public="$PUBLIC"=80 host=$ETH=2222)
case "$NONE" in
  *"metadata unreachable"*"public unreachable"*"host unreachable"*) ok "APP_EGRESS=none: no route to anything" ;;
  *) ko "APP_EGRESS=none: $NONE" ;;
esac

# The health check notices, the next pass puts it back: the case a reload leaves open for up to INTERVAL.
on_machine 'iptables -D DOCKER-USER -i exnet+ -j EXCALIBASE-APP'
if firewall check >/dev/null 2>&1; then ko "the check passes with the wildcard gone"; else ok "the check fails with the wildcard gone"; fi
firewall "" >/dev/null
firewall check || ko "the rerun did not restore the wildcard"
ok "a rerun restores it"

[ "$failed" = 0 ] || { echo "project-bridge-fence: FAILED" >&2; exit 1; }
echo "project-bridge-fence: ok"
