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
#    own subnets and DNS to the host's resolvers, first in DOCKER-USER, before
#    Docker's accept rules;
#  - guards project bridges (exnet<hash>, named by provisioning, EXC-687) by a
#    wildcard the kernel matches on names, so a bridge made later is covered from
#    its first packet: nothing here has to learn of it. Their forwarded traffic
#    drops to the same ranges and APP_EGRESS_EXTRA_DENY_CIDRS; a project's own
#    bridge passes, and Docker's isolation still drops one project's to another's;
#  - re-checks every INTERVAL seconds and puts back whatever was removed.
# It fails loudly, never guessing an interface: a network it cannot name, a
# bridge not on this host, or a Docker without DOCKER-USER stops it, and
# `host-firewall.sh check` (the health check) fails until every rule is in place.
#
# Runs in the host's network namespace with NET_ADMIN. provisioning and the edge
# start only once it is healthy; off on purpose (HOST_FIREWALL=off, or rootless
# Podman, whose bridges live in the user's own namespace) it is healthy with no rule.
set -eu

if [ "${HOST_FIREWALL:-}" = off ] || [ "${ENGINE_SOCKET_UID:-}" = 0 ]; then
  [ "${1:-}" = check ] && exit 0
  echo "host-firewall: off (HOST_FIREWALL=off or rootless Podman): no rule set, the host is guarded elsewhere"
  [ "${HOST_FIREWALL_ONCE:-}" = 1 ] && exit 0
  trap 'exit 0' TERM INT
  while :; do sleep 3600 & wait $! || true; done
fi

HOST_CHAIN=EXCALIBASE-HOST
TENANT_CHAIN=EXCALIBASE-TENANT
APP_CHAIN=EXCALIBASE-APP
# The wildcard on project bridges, and the network whose published web ports apps may reach.
PROJECT_BRIDGES=${HOST_FIREWALL_PROJECT_BRIDGES:-}
EDGE_NETWORK=${HOST_FIREWALL_EDGE_NETWORK:-}
NETWORKS=${HOST_FIREWALL_NETWORKS:?the platform networks to guard}
TENANTS=${HOST_FIREWALL_TENANT_NETWORKS:?the tenant networks}
BRIDGES_FILE=${HOST_FIREWALL_BRIDGES_FILE:-/var/lib/excalibase-bridges/networks}
SYSFS=${HOST_FIREWALL_SYSFS:-/sys/class/net}
# The host's resolv.conf Docker reads: tenants' lookups are forwarded to its resolvers.
RESOLV_CONF=${HOST_FIREWALL_RESOLV_CONF:-/etc/host-resolv.conf}
INTERVAL=${HOST_FIREWALL_INTERVAL:-30}
# How long a first start waits for the engine proxy to name the bridges.
WAIT=${HOST_FIREWALL_WAIT:-120}
# As the Kubernetes policies deny, plus Azure's host endpoint (public, but answers
# its own VMs) and the protocol range holding Oracle's old metadata address.
DENY4="169.254.0.0/16 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 127.0.0.0/8 0.0.0.0/8 224.0.0.0/4 240.0.0.0/4 168.63.129.16/32 192.0.0.0/24"
# And the translation prefixes, which reach those IPv4 ranges through a NAT64 or 6to4 upstream.
DENY6="fe80::/10 fc00::/7 64:ff9b::/96 64:ff9b:1::/48 2002::/16 ff00::/8"

say() { echo "host-firewall: $*"; }
err() { echo "host-firewall: ERROR: $*" >&2; return 1; }
words() { echo "$1" | tr ',' ' '; }
listed() { case " $2 " in *" $1 "*) return 0 ;; esac; return 1; }

# resolvers: DNS4 and DNS6, the resolv.conf's nameservers a container's lookup
# is forwarded to. Loopback ones Docker reaches from the host, and a zoned
# link-local one is not routable from a bridge: neither is let through.
resolvers() {
  DNS4= DNS6=
  [ -r "$RESOLV_CONF" ] || { err "no resolv.conf at $RESOLV_CONF (HOST_RESOLV_CONF)"; return 1; }
  for server in $(awk '$1 == "nameserver" { print $2 }' "$RESOLV_CONF"); do
    case $server in
      127.*|::1|*%*) ;;
      *:*) DNS6="$DNS6 $server/128" ;;
      *.*.*.*) DNS4="$DNS4 $server/32" ;;
    esac
  done
}

# extra_denied: EXTRA4 and EXTRA6, the operator's own ranges. Each becomes words of a
# rule, so anything but a CIDR stops the guard rather than reach iptables.
extra_denied() {
  EXTRA4= EXTRA6=
  [ -n "$PROJECT_BRIDGES" ] || return 0
  for cidr in $(words "${APP_EGRESS_EXTRA_DENY_CIDRS:-}"); do
    case $cidr in
      *[!0-9a-fA-F:./]* | */*/* | /* | */) err "APP_EGRESS_EXTRA_DENY_CIDRS: $cidr is not a CIDR"; return 1 ;;
      *:*/*) EXTRA6="$EXTRA6 $cidr" ;;
      *.*/*) EXTRA4="$EXTRA4 $cidr" ;;
      *) err "APP_EGRESS_EXTRA_DENY_CIDRS: $cidr is not a CIDR"; return 1 ;;
    esac
  done
}

# plan: read the bridges file into IFACES, TENANT_IFACES, OWN4, OWN6, EDGE4 and EDGE6, and the resolvers.
plan() {
  IFACES= TENANT_IFACES= OWN4= OWN6= EDGE4= EDGE6=
  resolvers || return 1
  extra_denied || return 1
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
      if [ "$network" = "$EDGE_NETWORK" ]; then
        case $subnet in *:*) EDGE6="$EDGE6 $subnet" ;; *) EDGE4="$EDGE4 $subnet" ;; esac
      fi
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

# tenant_rules <family>: the platform's own subnets pass, then DNS to the host's
# resolvers (the engine forwards lookups from the container), then metadata and
# private ranges drop.
tenant_rules() {
  if [ "$1" = 6 ]; then own=$OWN6 deny=$DENY6 dns=$DNS6; else own=$OWN4 deny=$DENY4 dns=$DNS4; fi
  echo "$TENANT_CHAIN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN"
  for subnet in $own; do echo "$TENANT_CHAIN -d $subnet -j RETURN"; done
  for server in $dns; do
    echo "$TENANT_CHAIN -d $server -p udp --dport 53 -j RETURN"
    echo "$TENANT_CHAIN -d $server -p tcp --dport 53 -j RETURN"
  done
  for range in $deny; do echo "$TENANT_CHAIN -d $range -j DROP"; done
}

# app_rules <family>: what a project bridge may forward to. Its own bridge passes; for
# another project's, -o only hands over to Docker's isolation, which drops it, but not
# through a published port, which Docker lets cross bridges: that drops here. The deny
# list reads the address the app asked for, so it also holds through a published port.
# What the edge publishes on 80 and 443 stays reachable, as from anywhere on the internet:
# an app calling a public hostname on this host; any other published port drops as private.
app_rules() {
  if [ "$1" = 6 ]; then deny=$DENY6 dns=$DNS6 extra=$EXTRA6 edge=$EDGE6; else deny=$DENY4 dns=$DNS4 extra=$EXTRA4 edge=$EDGE4; fi
  echo "$APP_CHAIN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN"
  echo "$APP_CHAIN -o $PROJECT_BRIDGES -m conntrack --ctstate DNAT -j DROP"
  echo "$APP_CHAIN -o $PROJECT_BRIDGES -j RETURN"
  for cidr in $extra; do echo "$APP_CHAIN -m conntrack --ctorigdst $cidr -j DROP"; done
  for server in $dns; do
    echo "$APP_CHAIN -d $server -p udp --dport 53 -j RETURN"
    echo "$APP_CHAIN -d $server -p tcp --dport 53 -j RETURN"
  done
  for subnet in $edge; do
    echo "$APP_CHAIN -d $subnet -p tcp --dport 80 -m conntrack --ctstate DNAT -j RETURN"
    echo "$APP_CHAIN -d $subnet -p tcp --dport 443 -m conntrack --ctstate DNAT -j RETURN"
  done
  for range in $deny; do echo "$APP_CHAIN -d $range -j DROP"; done
}

# jumps: "<chain> -i <bridge> -j <our chain>", one per guarded bridge, and the project bridges' wildcard.
jumps() {
  for iface in $IFACES; do echo "INPUT -i $iface -j $HOST_CHAIN"; done
  for iface in $TENANT_IFACES; do echo "DOCKER-USER -i $iface -j $TENANT_CHAIN"; done
  if [ -n "$PROJECT_BRIDGES" ]; then
    echo "INPUT -i $PROJECT_BRIDGES -j $HOST_CHAIN"
    echo "DOCKER-USER -i $PROJECT_BRIDGES -j $APP_CHAIN"
  fi
}

# fill <ipt> <chain> <rules>: the chain holds exactly these rules. It is closed while it
# is being filled, and stays closed if iptables refuses one: a jump to a half-filled chain
# would let through whatever the missing DROPs were for.
fill() {
  "$1" -w -N "$2" 2>/dev/null || true
  "$1" -w -F "$2" || return 1
  "$1" -w -A "$2" -j DROP || return 1
  echo "$3" | while read -r rule; do
    # shellcheck disable=SC2086
    "$1" -w -A $rule || exit 1
  done || return 1
  "$1" -w -D "$2" -j DROP
}

# apply <ipt> <family>: rebuild both chains, add missing jumps first in their
# chains, and remove jumps to bridges no longer guarded.
apply() {
  ipt=$1
  fill "$ipt" "$HOST_CHAIN" "$(host_rules "$2")" || return 1
  fill "$ipt" "$TENANT_CHAIN" "$(tenant_rules "$2")" || return 1
  if [ -n "$PROJECT_BRIDGES" ]; then fill "$ipt" "$APP_CHAIN" "$(app_rules "$2")" || return 1; fi
  wanted=$(jumps)
  for chain in INPUT DOCKER-USER; do
    held=$("$ipt" -w -S "$chain") || return 1
    echo "$held" | grep -E "^-A $chain -i [^ ]+ -j ($HOST_CHAIN|$TENANT_CHAIN|$APP_CHAIN)\$" | cut -c4- | while read -r jump; do
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
  app=
  [ -z "$PROJECT_BRIDGES" ] || app="$APP_CHAIN:$(app_rules "$2")"
  for chain_rules in "$HOST_CHAIN:$(host_rules "$2")" "$TENANT_CHAIN:$(tenant_rules "$2")" ${app:+"$app"}; do
    chain=${chain_rules%%:*}
    rules=${chain_rules#*:}
    held=$("$1" -w -S "$chain" 2>/dev/null | grep -c '^-A ' || true)
    [ "$held" = "$(echo "$rules" | grep -c .)" ] || { err "$1: $chain holds $held rules"; return 1; }
  done
  { host_rules "$2"; tenant_rules "$2"; [ -z "$PROJECT_BRIDGES" ] || app_rules "$2"; jumps; } | while read -r rule; do
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
say "new connections from$IFACES${PROJECT_BRIDGES:+ $PROJECT_BRIDGES} to this host are dropped (DNS excepted); from$TENANT_IFACES${PROJECT_BRIDGES:+ $PROJECT_BRIDGES}, metadata and private ranges are dropped too"
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
