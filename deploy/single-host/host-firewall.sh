#!/bin/sh
# EXC-683: the platform's networks are bridges with an address on the host, so a
# container on one (a tenant database runs its users' SQL) could open a
# connection to any host service listening on every address, and from the
# tenant network, forwarded, to the cloud's metadata service or the LAN.
#
# The engine proxy writes each platform network's real bridge (br-<id> for a
# network an older release made, whatever compose now says) to a file; this:
#  - drops new connections from every platform bridge to the host (DNS excepted);
#  - drops forwarded traffic from the tenant bridges to 169.254.0.0/16, the
#    private ranges and their IPv6 equivalents, except the platform networks'
#    own subnets, first in DOCKER-USER, before Docker's accept rules;
#  - re-checks every INTERVAL seconds and puts back whatever was removed.
# It fails loudly, never guessing an interface: a network it cannot name, a
# bridge not on this host, or a Docker without DOCKER-USER stops it, and
# `host-firewall.sh check` (the health check) fails until every rule is in place.
#
# Runs in the host's network namespace with NET_ADMIN (compose profile hostfirewall).
set -eu

HOST_CHAIN=EXCALIBASE-HOST
TENANT_CHAIN=EXCALIBASE-TENANT
NETWORKS=${HOST_FIREWALL_NETWORKS:?the platform networks to guard}
TENANTS=${HOST_FIREWALL_TENANT_NETWORKS:?the tenant networks}
BRIDGES_FILE=${HOST_FIREWALL_BRIDGES_FILE:-/var/lib/excalibase-bridges/networks}
SYSFS=${HOST_FIREWALL_SYSFS:-/sys/class/net}
INTERVAL=${HOST_FIREWALL_INTERVAL:-30}
# How long a first start waits for the engine proxy to name the bridges.
WAIT=${HOST_FIREWALL_WAIT:-120}
DENY4="169.254.0.0/16 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10"
DENY6="fe80::/10 fc00::/7"

say() { echo "host-firewall: $*"; }
err() { echo "host-firewall: ERROR: $*" >&2; return 1; }
words() { echo "$1" | tr ',' ' '; }
listed() { case " $2 " in *" $1 "*) return 0 ;; esac; return 1; }

# plan: read the bridges file into IFACES, TENANT_IFACES, OWN4 and OWN6.
plan() {
  IFACES= TENANT_IFACES= OWN4= OWN6=
  [ -r "$BRIDGES_FILE" ] || { err "no bridges file $BRIDGES_FILE: the engine proxy has not named the bridges"; return 1; }
  for network in $(words "$NETWORKS"); do
    line=$(grep "^$network " "$BRIDGES_FILE" || true)
    [ -n "$line" ] || { err "network $network is not resolved in $BRIDGES_FILE (see the engine proxy's log)"; return 1; }
    set -- $line
    if [ "$2" = absent ]; then
      if listed "$network" "$(words "$TENANTS")"; then err "tenant network $network does not exist"; return 1; fi
      continue
    fi
    [ $# = 3 ] && [ -e "$SYSFS/$2" ] || { err "bridge $2 of network $network is not on this host"; return 1; }
    IFACES="$IFACES $2"
    listed "$network" "$(words "$TENANTS")" && TENANT_IFACES="$TENANT_IFACES $2"
    for subnet in $(words "$3"); do
      case $subnet in *:*) OWN6="$OWN6 $subnet" ;; *) OWN4="$OWN4 $subnet" ;; esac
    done
  done
  [ -n "$TENANT_IFACES" ] || { err "no tenant network among $NETWORKS"; return 1; }
}

# backend <iptables|ip6tables>: the one holding Docker's DOCKER-USER chain.
backend() {
  for candidate in "$1-nft" "$1-legacy"; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -w -S DOCKER-USER >/dev/null 2>&1; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

# backends: IPT4, and IPT6 unless the host has no IPv6 filter and no tenant needs one.
backends() {
  IPT4=$(backend iptables) || { err "Docker's DOCKER-USER chain is in neither iptables backend (an engine on its nftables firewall backend?): run init.sh --no-host-firewall and guard the host yourself"; return 1; }
  IPT6=$(backend ip6tables) || IPT6=
  if [ -z "$IPT6" ] && [ -n "$OWN6" ]; then
    err "the platform networks have IPv6 subnets ($OWN6) but ip6tables has no DOCKER-USER chain"
    return 1
  fi
}

# host_rules <family>: the host chain's rules, one per line.
host_rules() {
  echo "$HOST_CHAIN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN"
  if [ "$1" = 6 ]; then
    # ICMPv6 carries neighbour discovery: without it IPv6 over the bridge stops.
    # No narrower type match: iptables-nft 1.8.10 cannot -C one, so it could not be verified.
    echo "$HOST_CHAIN -p icmpv6 -j RETURN"
  fi
  echo "$HOST_CHAIN -p udp --dport 53 -j RETURN"
  echo "$HOST_CHAIN -p tcp --dport 53 -j RETURN"
  echo "$HOST_CHAIN -j DROP"
}

# tenant_rules <family>: the platform's own subnets pass, then DNS (the engine
# forwards lookups from the container), then metadata and private ranges drop.
tenant_rules() {
  if [ "$1" = 6 ]; then own=$OWN6 deny=$DENY6; else own=$OWN4 deny=$DENY4; fi
  echo "$TENANT_CHAIN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN"
  for subnet in $own; do echo "$TENANT_CHAIN -d $subnet -j RETURN"; done
  echo "$TENANT_CHAIN -p udp --dport 53 -j RETURN"
  echo "$TENANT_CHAIN -p tcp --dport 53 -j RETURN"
  for range in $deny; do echo "$TENANT_CHAIN -d $range -j DROP"; done
}

# jumps: "<chain> -i <bridge> -j <our chain>", one per guarded bridge.
jumps() {
  for iface in $IFACES; do echo "INPUT -i $iface -j $HOST_CHAIN"; done
  for iface in $TENANT_IFACES; do echo "DOCKER-USER -i $iface -j $TENANT_CHAIN"; done
}

# fill <ipt> <chain> <rules>: the chain holds exactly these rules.
fill() {
  "$1" -w -N "$2" 2>/dev/null || true
  "$1" -w -F "$2" || return 1
  echo "$3" | while read -r rule; do
    # shellcheck disable=SC2086
    "$1" -w -A $rule || exit 1
  done
}

# apply <ipt> <family>: rebuild both chains, add missing jumps first in their
# chains, and remove jumps to bridges no longer guarded.
apply() {
  ipt=$1
  fill "$ipt" "$HOST_CHAIN" "$(host_rules "$2")" || return 1
  fill "$ipt" "$TENANT_CHAIN" "$(tenant_rules "$2")" || return 1
  wanted=$(jumps)
  for chain in INPUT DOCKER-USER; do
    held=$("$ipt" -w -S "$chain") || return 1
    echo "$held" | grep -E "^-A $chain -i [^ ]+ -j ($HOST_CHAIN|$TENANT_CHAIN)\$" | cut -c4- | while read -r jump; do
      # shellcheck disable=SC2086
      echo "$wanted" | grep -qxF -- "$jump" || "$ipt" -w -D $jump || exit 1
    done || return 1
  done
  echo "$wanted" | while read -r chain rule; do
    [ -n "$chain" ] || continue
    # shellcheck disable=SC2086
    "$ipt" -w -C "$chain" $rule 2>/dev/null || "$ipt" -w -I "$chain" 1 $rule || exit 1
  done
}

# verify <ipt> <family>: every rule and jump is there, and Docker still sends forwarded traffic to DOCKER-USER.
verify() {
  "$1" -w -C FORWARD -j DOCKER-USER 2>/dev/null || { err "$1: FORWARD does not jump to DOCKER-USER"; return 1; }
  for chain_rules in "$HOST_CHAIN:$(host_rules "$2")" "$TENANT_CHAIN:$(tenant_rules "$2")"; do
    chain=${chain_rules%%:*}
    rules=${chain_rules#*:}
    held=$("$1" -w -S "$chain" 2>/dev/null | grep -c '^-A ' || true)
    [ "$held" = "$(echo "$rules" | grep -c .)" ] || { err "$1: $chain holds $held rules"; return 1; }
  done
  { host_rules "$2"; tenant_rules "$2"; jumps; } | while read -r rule; do
    # shellcheck disable=SC2086
    "$1" -w -C $rule 2>/dev/null || { err "$1: missing $rule"; return 1; }
  done
}

families() {
  echo "$IPT4 4"
  [ -z "$IPT6" ] || echo "$IPT6 6"
}

guard() {
  plan && backends || return 1
  families | while read -r ipt family; do apply "$ipt" "$family" || exit 1; done
}

check() {
  plan && backends || return 1
  families | while read -r ipt family; do verify "$ipt" "$family" || exit 1; done
}

if [ "${1:-}" = check ]; then
  check
  exit
fi

waited=0
until (plan >/dev/null 2>&1); do
  if [ "$waited" -ge "$WAIT" ]; then
    plan || exit 1
  fi
  sleep 2
  waited=$((waited + 2))
done
guard || { err "the rules are not in place"; exit 1; }
check || { err "the rules did not hold"; exit 1; }
say "new connections from$IFACES to this host are dropped (DNS excepted); from$TENANT_IFACES, metadata and private ranges are dropped too"
[ -n "$IPT6" ] || say "no IPv6 DOCKER-USER chain here; IPv4 guarded only"
[ "${HOST_FIREWALL_ONCE:-}" = 1 ] && exit 0

trap 'exit 0' TERM INT
while :; do
  sleep "$INTERVAL" &
  wait $! || true
  # A rule removed (a firewall reload) or a network re-created: rebuild. A
  # bridge that cannot be named leaves the last rules in place, and the
  # health check failing.
  if ! check >/dev/null 2>&1; then
    guard && check && say "rules restored for$IFACES" || err "the rules are not in place"
  fi
done
