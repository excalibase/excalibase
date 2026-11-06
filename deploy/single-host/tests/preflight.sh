#!/usr/bin/env sh
# EXC-489: the stack refuses to start on a known or weak secret.
#
#   sh deploy/single-host/tests/preflight.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
fail() { echo "FAIL: $*" >&2; exit 1; }
hex=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
good() {
  env -i POSTGRES_PASSWORD=$hex SETUP_TOKEN=${hex%?}d OBJECTSTORE_ROOT_SECRET=${hex%?}1 \
    OBJECTSTORE_BACKUPS_SECRET=${hex%?}2 OBJECTSTORE_FILES_SECRET=${hex%?}3 \
    ENGINE_SOCKET_UID=1000 PLATFORM_TAG=1.7.0 HOST_RESOLV_CONF=/etc/resolv.conf \
    "$@" sh "$DIR/../preflight.sh"
}

good >/dev/null || fail "generated secrets refused"
for bad in POSTGRES_PASSWORD=password123 POSTGRES_PASSWORD= SETUP_TOKEN=abc123 \
  "SETUP_TOKEN=$(printf 'a%.0s' $(seq 64))" SETUP_TOKEN=$hex ADMIN_PASSWORD=password123 ADMIN_PASSWORD=short \
  OBJECTSTORE_ROOT_SECRET= OBJECTSTORE_ROOT_SECRET=changeme OBJECTSTORE_FILES_SECRET=${hex%?}2 OBJECTSTORE_BACKUPS_SECRET=$hex; do
  if good "$bad" >/dev/null 2>&1; then
    fail "preflight accepted $bad"
  fi
done
good ADMIN_PASSWORD='a-long-passphrase-of-mine' >/dev/null || fail "a strong admin password refused"

# EXC-687: the deny list and rootless Podman's egress are refused before anything starts.
for bad in APP_EGRESS_EXTRA_DENY_CIDRS=300.1.1.1/8 APP_EGRESS_EXTRA_DENY_CIDRS=203.0.113.7 'APP_EGRESS_EXTRA_DENY_CIDRS=203.0.113.0/24 -j ACCEPT'; do
  if good "$bad" >/dev/null 2>&1; then fail "preflight accepted $bad"; fi
done
good APP_EGRESS_EXTRA_DENY_CIDRS='203.0.113.0/24, 198.51.100.7/32' >/dev/null || fail "a good deny list refused"
# IPv4 alone, by notation, as provisioning's parser reads it: it stops the boot otherwise.
for bad in APP_EGRESS_EXTRA_DENY_CIDRS=2001:db8::/32 APP_EGRESS_EXTRA_DENY_CIDRS=::ffff:203.0.113.0/120; do
  if good "$bad" >/dev/null 2>&1; then fail "preflight accepted $bad"; fi
done
if good ENGINE_SOCKET_UID=0 APP_HOSTING_ENABLED=true APP_EGRESS=internet >/dev/null 2>&1; then fail "rootless Podman's unguarded egress passed"; fi
good ENGINE_SOCKET_UID=0 APP_HOSTING_ENABLED=true APP_EGRESS=internet APP_EGRESS_UNGUARDED=accept >/dev/null || fail "accepted egress refused"
good ENGINE_SOCKET_UID=0 APP_EGRESS=internet >/dev/null || fail "refused egress with app hosting off, where nothing uses it"
good ENGINE_SOCKET_UID=0 >/dev/null || fail "rootless Podman without egress refused"

# EXC-662: auth hooks may not start without the host's own addresses refused to them.
if good FEATURE_AUTHHOOKS=true >/dev/null 2>&1; then
  fail "auth hooks started with no AUTH_HOOKS_DENY_CIDRS"
fi
for blank in , ' ' ' , ' 203.0.113.7 'not-a-cidr/32' 203.0.113.7/32,bogus 203.0.113.7/33 2001:db8::7/129 '203.0.113.7/32 198.51.100.9/32'; do
  if good FEATURE_AUTHHOOKS=true "AUTH_HOOKS_DENY_CIDRS=$blank" >/dev/null 2>&1; then
    fail "auth hooks started with AUTH_HOOKS_DENY_CIDRS='$blank'"
  fi
done
good FEATURE_AUTHHOOKS=true AUTH_HOOKS_DENY_CIDRS=203.0.113.7/32 >/dev/null || fail "auth hooks with a deny list refused"
good FEATURE_AUTHHOOKS=true 'AUTH_HOOKS_DENY_CIDRS=203.0.113.0/24, 2001:db8::7/128,' >/dev/null \
  || fail "a deny list with spaces and a trailing comma refused, which the chart accepts"
good FEATURE_AUTHHOOKS=false >/dev/null || fail "auth hooks off needs no deny list"

# EXC-658: the log forwarder's token unlocks every drain's key; checked when drains are on.
good LOG_FORWARDER_TOKEN=weak >/dev/null || fail "the forwarder token was checked with drains off"
good FEATURE_LOGDRAINS=true LOG_FORWARDER_TOKEN=${hex%?}4 >/dev/null || fail "a generated forwarder token refused"
for bad in LOG_FORWARDER_TOKEN= LOG_FORWARDER_TOKEN=changeme LOG_FORWARDER_TOKEN=$hex; do
  if good FEATURE_LOGDRAINS=true "$bad" >/dev/null 2>&1; then
    fail "preflight accepted $bad with drains on"
  fi
done
# EXC-683: on Docker the host firewall runs unless turned off on purpose; a
# v1.6 .env says nothing, so it is on, and preflight (on every up) stops an
# upgrade missing what it needs rather than let it start unhealthy.
refused() {
  if good "$@" >"$WORK_OUT" 2>&1; then fail "preflight accepted $*"; fi
  grep -q "$want" "$WORK_OUT" || fail "preflight on $* does not say '$want': $(cat "$WORK_OUT")"
}
WORK_OUT=$(mktemp); trap 'rm -f "$WORK_OUT"' EXIT
want=HOST_FIREWALL refused HOST_FIREWALL=no
want=HOST_FIREWALL refused HOST_FIREWALL=on
good HOST_FIREWALL=off HOST_RESOLV_CONF= >/dev/null || fail "an explicit opt-out refused"
good COMPOSE_PROFILES=hostlogs >/dev/null || fail "another profile refused"
# Rootless Podman (init.sh writes uid 0 for it alone): its bridges live in the
# user's own namespace and its engine has no DOCKER-USER chain, so no firewall.
good ENGINE_SOCKET_UID=0 HOST_RESOLV_CONF= PLATFORM_TAG=latest >/dev/null || fail "rootless Podman was asked for the host firewall"
# The firewall runs the release's image: 1.6.2 and before have no iptables in it
# and their proxy writes no bridges, so the firewall would never be healthy.
# Fail closed: floating tags (release.yml publishes 1.6, 1 and latest, today
# 1.6.2) and anything not a plain release or trunk build are refused.
for old in 1.5.0 1.6.2 1.0.9 latest 1.6 1 V1.5.0 v1.6.2 v2.0.0 1.6.2-rc1 1.7.0-rc1 main main- main-xyz '' ' 1.7.0'; do
  want=main- refused "PLATFORM_TAG=$old"
done
for newer in 1.6.3 1.7.0 1.6.10 2.0.0 main-1a2b3c4 main-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b ci; do
  good PLATFORM_TAG=$newer >/dev/null || fail "release $newer refused"
done
good PLATFORM_TAG=1.5.0 HOST_FIREWALL=off >/dev/null || fail "an old release without the firewall refused"
# The firewall allows DNS only to the resolvers Docker forwards to: init.sh names that file.
want=HOST_RESOLV_CONF refused HOST_RESOLV_CONF=
echo "preflight ok"
