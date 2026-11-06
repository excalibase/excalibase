#!/usr/bin/env sh
# EXC-683 across real reboots: a Docker-in-Docker machine runs the bundle's own
# engine proxy and host firewall from compose.yaml, with tenants created through
# the proxy as provisioning creates them. Stopping or killing the machine loses
# its iptables rules and restarts its daemon, as a host reboot and a power cut
# do. A tenant must not run until the firewall is healthy, however long that takes.
#
#   IMAGE=docker.io/excalibase/provisioning:ci sh deploy/single-host/tests/reboot-order.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
IMAGE=${IMAGE:-docker.io/excalibase/provisioning:ci}
TENANT_IMAGE=docker.io/library/alpine:3.20
DIND=${DIND_IMAGE:-docker.io/library/docker:29-dind}
MACHINE=exc683-reboot-$$
FIREWALL=excalibase-host-firewall
PROXY=excalibase-engine-proxy
TENANT=excalibase-reboot-postgres
PAUSED=excalibase-pausedtenant-postgres
OLD=excalibase-oldtenant-postgres
OLD_PAUSED=excalibase-oldpaused-postgres
STUBBORN=excalibase-stubborn-postgres
# EXC-687: an app with a route out comes back behind the firewall too; one on an isolated network keeps Docker's policy.
EGRESS_NET=excalibase-net-rebootegress
ISOLATED_NET=excalibase-net-rebootisolated
EGRESS_APP=excalibase-app-rebootegress
ISOLATED_APP=excalibase-app-rebootisolated

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }
d() { docker exec "$MACHINE" docker "$@"; }
trap 'docker rm -f -v "$MACHINE" >/dev/null 2>&1 || true' EXIT

state() { d inspect -f '{{.State.Status}}' "$1" 2>/dev/null || echo none; }
health() { d inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$1" 2>/dev/null || echo none; }
policy() { d inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$1"; }
daemon_up() {
  tries=0
  until d version >/dev/null 2>&1; do
    tries=$((tries + 1))
    [ "$tries" -lt 120 ] || fail "the machine's daemon did not come up"
    sleep 1
  done
}
# proxy <path> [body]: POST through the engine proxy, as provisioning calls it.
proxy() {
  d exec "$PROXY" wget -qO- --header 'Content-Type: application/json' --post-data "${2:-}" "http://127.0.0.1:2375$1"
}
create_tenant() {
  proxy "/containers/create?name=$1" '{"Image":"'"$TENANT_IMAGE"'","Cmd":["sleep","1000000"],"Labels":{"excalibase.managed":"true"},"HostConfig":{"RestartPolicy":{"Name":"unless-stopped","MaximumRetryCount":0}},"NetworkingConfig":{"EndpointsConfig":{"excalibase-tenants":{}}}}' >/dev/null
  proxy "/containers/$1/start" >/dev/null
}
create_app() {
  proxy "/containers/create?name=$1" '{"Image":"'"$TENANT_IMAGE"'","Cmd":["sleep","1000000"],"Labels":{"excalibase.managed":"true","excalibase.component":"app"},"HostConfig":{"NetworkMode":"'"$2"'","RestartPolicy":{"Name":"unless-stopped","MaximumRetryCount":0}},"NetworkingConfig":{"EndpointsConfig":{"'"$2"'":{}}}}' >/dev/null
  proxy "/containers/$1/start" >/dev/null
}
reboot() {
  case $1 in
    clean) docker stop -t 30 "$MACHINE" >/dev/null ;;
    power-cut) docker kill "$MACHINE" >/dev/null ;;
  esac
  docker start "$MACHINE" >/dev/null
  daemon_up
}
# Every second until the tenant runs: it must never run ahead of a healthy firewall, rules in place.
tenant_waits_for_firewall() {
  waited=0
  while [ "$(state "$TENANT")" != running ]; do
    waited=$((waited + 1))
    [ "$waited" -lt 240 ] || fail "$1: the tenant never came back ($(state "$TENANT"), firewall $(health "$FIREWALL"))"
    sleep 1
  done
  firewall=$(health "$FIREWALL")
  [ "$firewall" = healthy ] || fail "$1: the tenant runs while the firewall is $firewall"
  d exec "$FIREWALL" true 2>/dev/null || fail "$1: the firewall is not running"
  docker exec "$MACHINE" iptables -S EXCALIBASE-HOST >/dev/null 2>&1 \
    || docker exec "$MACHINE" iptables-legacy -S EXCALIBASE-HOST >/dev/null 2>&1 \
    || fail "$1: the tenant runs with no firewall rules"
  ok "$1: the tenant came back only behind a healthy firewall"
}

echo "== a machine with the bundle's proxy and firewall"
docker run -d --privileged --name "$MACHINE" -v /var/lib/docker -e DOCKER_TLS_CERTDIR= "$DIND" >/dev/null
daemon_up
docker image inspect "$TENANT_IMAGE" >/dev/null 2>&1 || docker pull -q "$TENANT_IMAGE" >/dev/null
docker save "$IMAGE" "$TENANT_IMAGE" | docker exec -i "$MACHINE" docker load >/dev/null
docker cp -q "$DIR/.." "$MACHINE:/bundle"
tag=${IMAGE##*:}
docker exec -w /bundle "$MACHINE" sh -c "./init.sh --domain localhost --admin-email owner@example.com --engine docker >/dev/null && echo TAG=$tag >>.env"
d network create excalibase-tenants >/dev/null
docker exec -w /bundle "$MACHINE" docker compose up -d --wait host-firewall >/dev/null 2>&1 \
  || fail "the firewall did not come up: $(d logs "$FIREWALL" 2>&1 | tail -5)"
create_tenant "$TENANT"
[ "$(state "$TENANT")" = running ] || fail "the tenant did not start"
ok "a tenant runs behind the healthy firewall"

echo "== apps: a bridge the firewall covers, or none to cover"
if proxy /networks/create '{"Name":"'"$EGRESS_NET"'","Driver":"bridge","Labels":{"excalibase.managed":"true"}}' >/dev/null 2>&1; then
  fail "the proxy made a project network with a route out and no bridge the firewall covers"
fi
ok "the proxy refuses a project network off the firewall's wildcard"
bridge=exnet$(printf '%s' "$EGRESS_NET" | sha256sum | cut -c1-10)
proxy /networks/create '{"Name":"'"$EGRESS_NET"'","Driver":"bridge","Labels":{"excalibase.managed":"true"},"Options":{"com.docker.network.bridge.name":"'"$bridge"'"}}' >/dev/null
proxy /networks/create '{"Name":"'"$ISOLATED_NET"'","Driver":"bridge","Internal":true,"Labels":{"excalibase.managed":"true"},"Options":{"com.docker.network.bridge.gateway_mode_ipv4":"isolated","com.docker.network.bridge.gateway_mode_ipv6":"isolated"}}' >/dev/null
create_app "$EGRESS_APP" "$EGRESS_NET"
create_app "$ISOLATED_APP" "$ISOLATED_NET"
[ "$(policy "$EGRESS_APP")" = no ] || fail "an app with a route out kept the policy a reboot acts on: $(policy "$EGRESS_APP")"
[ "$(policy "$ISOLATED_APP")" = unless-stopped ] || fail "an app with no route lost Docker's policy: $(policy "$ISOLATED_APP")"
[ "$(state "$EGRESS_APP")" = running ] && [ "$(state "$ISOLATED_APP")" = running ] || fail "the apps did not start"
docker exec "$MACHINE" iptables -S DOCKER-USER | grep -q -- '-i exnet+ -j EXCALIBASE-APP' \
  || docker exec "$MACHINE" iptables-legacy -S DOCKER-USER | grep -q -- '-i exnet+ -j EXCALIBASE-APP' \
  || fail "no wildcard on project bridges"
ok "the apps run, the wildcard is in place"

echo "== a clean reboot"
reboot clean
tenant_waits_for_firewall "after a clean reboot"
waited=0
until [ "$(state "$EGRESS_APP")" = running ]; do
  waited=$((waited + 1))
  [ "$waited" -lt 60 ] || fail "the app with a route out did not come back"
  sleep 1
done
[ "$(health "$FIREWALL")" = healthy ] || fail "the app runs while the firewall is $(health "$FIREWALL")"
ok "after a clean reboot the app with a route out came back behind the firewall"

echo "== a power cut with the firewall down: tenants stay down until it is healthy"
d stop "$FIREWALL" >/dev/null
reboot power-cut
sleep 45
[ "$(state "$PROXY")" = running ] || fail "the proxy did not come back: $(state "$PROXY")"
[ "$(state "$TENANT")" != running ] || fail "the tenant runs with the firewall down"
ok "45s after the power cut the tenant is still down"
[ "$(state "$EGRESS_APP")" != running ] || fail "the app with a route out runs with the firewall down"
ok "45s after the power cut the app with a route out is still down"
[ "$(state "$ISOLATED_APP")" = running ] || fail "the app with no route is down: $(state "$ISOLATED_APP")"
ok "the app with no route came back on Docker's policy: it can reach nothing anyway"
if proxy "/containers/$EGRESS_APP/start" >/dev/null 2>&1; then fail "the proxy started an app with a route out with the firewall down"; fi
if proxy "/containers/$TENANT/start" >/dev/null 2>&1; then fail "the proxy started a tenant with the firewall down"; fi
[ "$(state "$TENANT")" != running ] || fail "a start through the proxy got past the firewall"
ok "the proxy refuses to start a tenant with the firewall down"
d start "$FIREWALL" >/dev/null
tenant_waits_for_firewall "once the firewall returned"
waited=0
until [ "$(state "$EGRESS_APP")" = running ]; do
  waited=$((waited + 1))
  [ "$waited" -lt 60 ] || fail "the app with a route out did not come back once the firewall did"
  sleep 1
done
ok "the app with a route out came back once the firewall did"
pid=$(d inspect -f '{{.State.Pid}}' "$EGRESS_APP")
docker exec "$MACHINE" kill -9 "$pid"
waited=0
until now=$(d inspect -f '{{.State.Pid}}' "$EGRESS_APP") && [ "$now" != "$pid" ] && [ "$now" != 0 ]; do
  waited=$((waited + 1))
  [ "$waited" -lt 30 ] || fail "a crashed app with a route out did not come back in 30s"
  sleep 1
done
count=$(d exec "$PROXY" wget -qO- "http://127.0.0.1:2375/containers/$EGRESS_APP/json" | sed -n 's/.*"RestartCount":\([0-9]*\).*/\1/p')
[ "${count:-0}" -ge 1 ] || fail "provisioning reads no restart of the crashed app: RestartCount=$count"
ok "a crashed app came back in ${waited}s, its restart counted"
proxy "/containers/$EGRESS_APP/stop?t=1" >/dev/null
sleep 12
[ "$(state "$EGRESS_APP")" = exited ] || fail "a paused app came back"
ok "a paused app stays down"

echo "== a crash comes back; a stop stays stopped, across a reboot too"
pid=$(d inspect -f '{{.State.Pid}}' "$TENANT")
docker exec "$MACHINE" kill -9 "$pid"
waited=0
until [ "$(state "$TENANT")" = running ] && [ "$(d inspect -f '{{.State.Pid}}' "$TENANT")" != "$pid" ]; do
  waited=$((waited + 1))
  [ "$waited" -lt 30 ] || fail "a crashed tenant did not come back in 30s"
  sleep 1
done
ok "a crashed tenant came back in ${waited}s"
create_tenant "$PAUSED"
proxy "/containers/$PAUSED/stop?t=1" >/dev/null
sleep 15
[ "$(state "$PAUSED")" = exited ] || fail "a stopped tenant was restarted: $(state "$PAUSED")"
reboot clean
tenant_waits_for_firewall "after another reboot"
sleep 10
[ "$(state "$PAUSED")" = exited ] || fail "a stopped tenant came back after a reboot: $(state "$PAUSED")"
ok "a stopped tenant stays stopped across a reboot"
proxy "/containers/$PAUSED/start" >/dev/null
sleep 10
proxy "/containers/$PAUSED/stop?t=1" >/dev/null
[ "$(state "$PAUSED")" = exited ] || fail "a resumed tenant did not stop again"
proxy "/containers/$PAUSED/start" >/dev/null
[ "$(state "$PAUSED")" = running ] || fail "a resume did not start the tenant"
pid=$(d inspect -f '{{.State.Pid}}' "$PAUSED")
docker exec "$MACHINE" kill -9 "$pid"
sleep 15
[ "$(state "$PAUSED")" = running ] || fail "a resumed tenant that crashed did not come back"
ok "a resumed tenant is looked after again"

echo "== an upgrade: tenants an older release made are taken over"
d run -d --name "$OLD" --label excalibase.managed=true --restart unless-stopped --network excalibase-tenants "$TENANT_IMAGE" sleep 1000000 >/dev/null
d run -d --name "$OLD_PAUSED" --label excalibase.managed=true --restart unless-stopped --network excalibase-tenants "$TENANT_IMAGE" sleep 1000000 >/dev/null
d stop -t 1 "$OLD_PAUSED" >/dev/null
d restart "$PROXY" >/dev/null
waited=0
until [ "$(policy "$OLD")" = no ] && [ "$(policy "$OLD_PAUSED")" = no ]; do
  waited=$((waited + 1))
  [ "$waited" -lt 60 ] || fail "an older tenant keeps its restart policy: $(policy "$OLD") $(policy "$OLD_PAUSED")"
  sleep 1
done
sleep 10
[ "$(state "$OLD_PAUSED")" = exited ] || fail "a tenant stopped before the upgrade came back: $(state "$OLD_PAUSED")"
reboot power-cut
tenant_waits_for_firewall "after a power cut, the upgrade taken over"
waited=0
until [ "$(state "$OLD")" = running ]; do
  waited=$((waited + 1))
  [ "$waited" -lt 30 ] || fail "an older tenant did not come back"
  sleep 1
done
[ "$(state "$OLD_PAUSED")" = exited ] || fail "a paused older tenant came back after a reboot: $(state "$OLD_PAUSED")"
ok "older tenants follow the firewall too, stopped ones staying stopped"

echo "== a stop whose client hangs up: the engine finishes it, and it stays a stop"
proxy "/containers/create?name=$STUBBORN" '{"Image":"'"$TENANT_IMAGE"'","Cmd":["sh","-c","trap \"\" TERM; while :; do sleep 1; done"],"Labels":{"excalibase.managed":"true"},"HostConfig":{},"NetworkingConfig":{"EndpointsConfig":{"excalibase-tenants":{}}}}' >/dev/null
proxy "/containers/$STUBBORN/start" >/dev/null
d exec "$PROXY" timeout 2 wget -qO- --post-data '' "http://127.0.0.1:2375/containers/$STUBBORN/stop?t=8" >/dev/null 2>&1 || true
sleep 20
[ "$(state "$STUBBORN")" = exited ] || fail "a stop whose client hung up was undone: $(state "$STUBBORN")"
ok "the tenant stays stopped"

echo "== an unreadable stop record: nothing starts on its own, the platform's calls work, the documented way out does"
d exec "$PROXY" sh -c ': > /var/lib/excalibase-bridges/stopped'
d restart "$PROXY" >/dev/null
waited=0
until proxy "/containers/$TENANT/stop?t=1" >/dev/null 2>&1; do
  waited=$((waited + 1))
  [ "$waited" -lt 30 ] || fail "a stop did not go through with the record unreadable"
  sleep 1
done
[ "$(state "$TENANT")" = exited ] || fail "the stop did not stop the tenant"
sleep 15
[ "$(state "$TENANT")" = exited ] || fail "a tenant started on its own with the record unreadable"
d logs "$PROXY" 2>&1 | grep -q "pause those again" || fail "the log does not give the way out"
d exec "$PROXY" rm /var/lib/excalibase-bridges/stopped
d restart "$PROXY" >/dev/null
waited=0
until [ "$(state "$TENANT")" = running ]; do
  waited=$((waited + 1))
  [ "$waited" -lt 60 ] || fail "the way out did not bring the tenant back"
  sleep 1
done
ok "the way out brings the tenants back"
echo "PASS: reboot order"
