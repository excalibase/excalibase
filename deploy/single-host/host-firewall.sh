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
#    bridge passes, and the firewall drops one project's to another's;
#  - hooks the tenant and project chains on FORWARD as well as DOCKER-USER: Docker keeps
#    DOCKER-USER first across its own restart, and FORWARD needs nothing of Docker's when a
#    flushed ruleset (restarting nftables.service) takes its chains away;
#  - lets through only replies of an established connection, so one opened while the rules
#    were gone stops when they return;
#  - re-checks every INTERVAL seconds and puts back whatever was removed, staying up and
#    unhealthy while it cannot. Jumps go in first, to closed chains.
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
# The host ports the edge publishes.
EDGE_PORTS=${HOST_FIREWALL_EDGE_PORTS:-80 443}
EDGE_NETWORK=${HOST_FIREWALL_EDGE_NETWORK:-}
NETWORKS=${HOST_FIREWALL_NETWORKS:?the platform networks to guard}
TENANTS=${HOST_FIREWALL_TENANT_NETWORKS:?the tenant networks}
BRIDGES_FILE=${HOST_FIREWALL_BRIDGES_FILE:-/var/lib/excalibase-bridges/networks}
SYSFS=${HOST_FIREWALL_SYSFS:-/sys/class/net}
# The host's resolv.conf Docker reads: tenants' lookups are forwarded to its resolvers.
RESOLV_CONF=${HOST_FIREWALL_RESOLV_CONF:-/etc/host-resolv.conf}
INTERVAL=${HOST_FIREWALL_INTERVAL:-30}
BRIDGE_NF=${HOST_FIREWALL_BRIDGE_NF:-/proc/sys/net/bridge}
# How long a first start waits for the engine proxy to name the bridges.
WAIT=${HOST_FIREWALL_WAIT:-120}
# As the Kubernetes policies deny, plus Azure's host endpoint (public, but answers
# its own VMs) and the protocol range holding Oracle's old metadata address.
DENY4="169.254.0.0/16 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 127.0.0.0/8 0.0.0.0/8 224.0.0.0/4 240.0.0.0/4 168.63.129.16/32 192.0.0.0/24"
# And the translation prefixes, which reach those IPv4 ranges through a NAT64 or 6to4 upstream.
DENY6="fe80::/10 fc00::/7 64:ff9b::/96 64:ff9b:1::/48 2002::/16 ff00::/8"

IFACES= TENANT_IFACES= OWN4= OWN6= EDGE4= EDGE6= DNS4= DNS6= EXTRA4= PLANNED= KNOWN4= KNOWN6=

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

# extra_denied: EXTRA4, the operator's own ranges. Each becomes words of a
# rule, so anything but a CIDR stops the guard rather than reach iptables.
extra_denied() {
  EXTRA4=
  [ -n "$PROJECT_BRIDGES" ] || return 0
  for cidr in $(words "${APP_EGRESS_EXTRA_DENY_CIDRS:-}"); do
    case $cidr in
      *[!0-9a-fA-F:./]* | */*/* | /* | */) err "APP_EGRESS_EXTRA_DENY_CIDRS: $cidr is not a CIDR"; return 1 ;;
      *:*) err "APP_EGRESS_EXTRA_DENY_CIDRS: $cidr is IPv6, and the list is IPv4 alone, as provisioning reads it"; return 1 ;;
      *.*/*) EXTRA4="$EXTRA4 $cidr" ;;
      *) err "APP_EGRESS_EXTRA_DENY_CIDRS: $cidr is not a CIDR"; return 1 ;;
    esac
  done
}

# plan: read what to guard, or keep the last plan that could be read: a reload while the
# bridges file is away still puts back what was there.
plan() {
  kept="$IFACES|$TENANT_IFACES|$OWN4|$OWN6|$EDGE4|$EDGE6|$DNS4|$DNS6|$EXTRA4"
  if read_plan; then
    PLANNED=1
    return 0
  fi
  IFS='|' read -r IFACES TENANT_IFACES OWN4 OWN6 EDGE4 EDGE6 DNS4 DNS6 EXTRA4 <<KEPT
$kept
KEPT
  return 1
}

# read_plan: the bridges file into IFACES, TENANT_IFACES, OWN4, OWN6, EDGE4 and EDGE6, the resolvers and the deny list.
read_plan() {
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

# any_backend <iptables|ip6tables> <known>: one that works at all, the one last used first.
any_backend() {
  for candidate in "$2" "$1-nft" "$1-legacy"; do
    if [ -n "$candidate" ] && command -v "$candidate" >/dev/null 2>&1 && "$candidate" -w -S INPUT >/dev/null 2>&1; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

# backends: IPT4, and IPT6 unless the host has no IPv6 filter at all. Without DOCKER-USER
# they still name what the hooks of our own go in; the failure says why.
backends() {
  IPT4=$(backend iptables) || {
    IPT4=$(any_backend iptables "$KNOWN4") || IPT4=
    IPT6=$(any_backend ip6tables "$KNOWN6") || IPT6=
    err "Docker's DOCKER-USER chain is in neither iptables backend (an engine on its nftables firewall backend, or a flushed ruleset?): if it is the former, run init.sh --no-host-firewall and guard the host yourself"
    return 1
  }
  if ! IPT6=$(backend ip6tables); then
    IPT6=$(any_backend ip6tables "$KNOWN6") || IPT6=
    if [ -n "$OWN6" ]; then
      err "the platform networks have IPv6 subnets ($OWN6) but ip6tables has no DOCKER-USER chain"
      return 1
    fi
  fi
}

# replies <chain>: what answers a connection from outside passes. The container's own side
# of a connection does not: one opened while the rules were gone meets them when they return.
replies() {
  echo "$1 -m conntrack --ctstate ESTABLISHED --ctdir REPLY -j RETURN"
  echo "$1 -m conntrack --ctstate RELATED -j RETURN"
}

# host_rules <family>: the host chain's rules, one per line.
host_rules() {
  replies "$HOST_CHAIN"
  if [ "$1" = 6 ]; then
    # ICMPv6 carries neighbour discovery: without it IPv6 over the bridge stops.
    # No narrower type match: iptables-nft 1.8.10 cannot -C one, so it could not be verified.
    echo "$HOST_CHAIN -p icmpv6 -j RETURN"
  fi
  echo "$HOST_CHAIN -p udp --dport 53 -j RETURN"
  echo "$HOST_CHAIN -p tcp --dport 53 -j RETURN"
  # Docker before 29 serves a call to a published port from another bridge by docker-proxy, on INPUT.
  if [ -n "$PROJECT_BRIDGES" ] && { [ "$1" = 6 ] && [ -n "$EDGE6" ] || [ "$1" != 6 ] && [ -n "$EDGE4" ]; }; then
    for port in $EDGE_PORTS; do echo "$HOST_CHAIN -i $PROJECT_BRIDGES -p tcp --dport $port -j RETURN"; done
  fi
  echo "$HOST_CHAIN -j DROP"
}

# tenant_rules <family>: the platform's own subnets pass, then DNS to the host's
# resolvers (the engine forwards lookups from the container), then metadata and
# private ranges drop.
tenant_rules() {
  if [ "$1" = 6 ]; then own=$OWN6 deny=$DENY6 dns=$DNS6; else own=$OWN4 deny=$DENY4 dns=$DNS4; fi
  replies "$TENANT_CHAIN"
  for subnet in $own; do echo "$TENANT_CHAIN -d $subnet -j RETURN"; done
  for server in $dns; do
    echo "$TENANT_CHAIN -d $server -p udp --dport 53 -j RETURN"
    echo "$TENANT_CHAIN -d $server -p tcp --dport 53 -j RETURN"
  done
  for range in $deny; do echo "$TENANT_CHAIN -d $range -j DROP"; done
}

# app_rules <family>: what a project bridge may forward to. Its own bridge passes; any
# other project's bridge drops here, by our rule rather than Docker's isolation chains, which
# a flushed ruleset removes, and a published port, which Docker lets cross bridges, drops too. The deny
# list reads the address the app asked for, so it also holds through a published port.
# What the edge publishes on 80 and 443 stays reachable, as from anywhere on the internet:
# an app calling a public hostname on this host; any other published port drops as private.
app_rules() {
  extra=
  if [ "$1" = 6 ]; then deny=$DENY6 dns=$DNS6 edge=$EDGE6; else deny=$DENY4 dns=$DNS4 extra=$EXTRA4 edge=$EDGE4; fi
  replies "$APP_CHAIN"
  echo "$APP_CHAIN -o $PROJECT_BRIDGES -m conntrack --ctstate DNAT -j DROP"
  # Same-bridge traffic reaches FORWARD only once br_netfilter is loaded, and naming physdev would load it
  # (breaking other bridges on the host), so the pass is written only while it is there; every pass re-reads this.
  [ ! -e "$BRIDGE_NF" ] || echo "$APP_CHAIN -o $PROJECT_BRIDGES -m physdev --physdev-is-bridged -j RETURN"
  echo "$APP_CHAIN -o $PROJECT_BRIDGES -j DROP"
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

# has_user_chain <ipt>: Docker's chain is there.
has_user_chain() { "$1" -w -S DOCKER-USER >/dev/null 2>&1; }

# jumps <ipt>: "<chain> -i <bridge> -j <our chain>", one per guarded bridge, and the project bridges' wildcard.
jumps() {
  for iface in $IFACES; do echo "INPUT -i $iface -j $HOST_CHAIN"; done
  [ -z "$PROJECT_BRIDGES" ] || echo "INPUT -i $PROJECT_BRIDGES -j $HOST_CHAIN"
  for chain in FORWARD DOCKER-USER; do
    [ "$chain" = FORWARD ] || has_user_chain "$1" || continue
    for iface in $TENANT_IFACES; do echo "$chain -i $iface -j $TENANT_CHAIN"; done
    [ -z "$PROJECT_BRIDGES" ] || echo "$chain -i $PROJECT_BRIDGES -j $APP_CHAIN"
  done
}

# chain_holds <ipt> <chain> <rules>: exactly these rules are there, so it need not be closed to refill.
chain_holds() {
  listed=$("$1" -w -S "$2" 2>/dev/null) || return 1
  [ "$(echo "$listed" | grep -c '^-A ' || true)" = "$(echo "$3" | grep -c .)" ] || return 1
  echo "$3" | while read -r rule; do
    case "$rule" in
      # iptables-nft 1.8.10 can answer no to -C here with the rule in place: read the listing instead.
      *" -p icmpv6 -j RETURN")
        echo "$listed" | grep -qE -- "^-A $2 -p (ipv6-icmp|icmpv6|58) -j RETURN\$" || exit 1 ;;
      *)
        # shellcheck disable=SC2086
        "$1" -w -C $rule 2>/dev/null || exit 1 ;;
    esac
  done
}

# fill <ipt> <chain> <rules>: the chain holds exactly these rules. It is closed for as long
# as it is being filled, from before the first rule goes, and stays closed if iptables refuses
# one: a jump to a half-filled chain would let through whatever the missing DROPs were for.
fill() {
  chain_holds "$@" && return 0
  "$1" -w -I "$2" 1 -j DROP || return 1
  stale=$("$1" -w -S "$2" | grep -c '^-A ')
  while [ "$stale" -gt 1 ]; do
    "$1" -w -D "$2" 2 || return 1
    stale=$((stale - 1))
  done
  echo "$3" | while read -r rule; do
    # shellcheck disable=SC2086
    "$1" -w -A $rule || exit 1
  done || return 1
  "$1" -w -D "$2" 1
}

# closed <ipt> <chain>: a chain that did not exist exists, dropping everything.
closed() {
  if "$1" -w -N "$2" 2>/dev/null; then "$1" -w -A "$2" -j DROP; fi
}

# leading <ipt> <chain>: the rules on top that accept nothing: ours, and Docker's jump to its user chain.
leading() {
  "$1" -w -S "$2" | sed -n 's/^-A //p' | awk -v forward="FORWARD -j DOCKER-USER" \
    -v ours=" -j ($HOST_CHAIN|$TENANT_CHAIN|$APP_CHAIN)\$" '$0 != forward && $0 !~ ours { exit } { print }'
}

# placed <ipt> <chain> <rule> <leading rules>: nothing can accept the bridge's traffic before the rule
# sees it. FORWARD has a second way: Docker's jump on top, to a user chain holding it first.
placed() {
  if [ "$2" = FORWARD ] && echo "$4" | grep -qxF "FORWARD -j DOCKER-USER"; then
    # shellcheck disable=SC2086
    "$1" -w -C FORWARD $3 2>/dev/null
    return
  fi
  echo "$4" | grep -qxF -- "$2 $3"
}

# place <ipt> <jumps>: every jump in place, moved up if something got above it; others of ours go.
place() {
  for chain in INPUT FORWARD DOCKER-USER; do
    held=$("$1" -w -S "$chain" 2>/dev/null) || continue
    echo "$held" | grep -E "^-A $chain -i [^ ]+ -j ($HOST_CHAIN|$TENANT_CHAIN|$APP_CHAIN)\$" | cut -c4- | while read -r jump; do
      # shellcheck disable=SC2086
      echo "$2" | grep -qxF -- "$jump" || "$1" -w -D $jump || exit 1
    done || return 1
  done
  echo "$2" | while read -r chain rule; do
    [ -n "$chain" ] || continue
    placed "$1" "$chain" "$rule" "$(leading "$1" "$chain")" && continue
    # shellcheck disable=SC2086
    "$1" -w -D "$chain" $rule 2>/dev/null || true
    # shellcheck disable=SC2086
    "$1" -w -I "$chain" 1 $rule || exit 1
  done
}

# apply <ipt> <family>: closed chains, the jumps to them, and then what they hold, so a
# failure at any step leaves every bridge guarded by a chain dropping everything.
apply() {
  ipt=$1
  closed "$ipt" "$HOST_CHAIN" || return 1
  closed "$ipt" "$TENANT_CHAIN" || return 1
  [ -z "$PROJECT_BRIDGES" ] || closed "$ipt" "$APP_CHAIN" || return 1
  place "$ipt" "$(jumps "$ipt")" || return 1
  fill "$ipt" "$HOST_CHAIN" "$(host_rules "$2")" || return 1
  fill "$ipt" "$TENANT_CHAIN" "$(tenant_rules "$2")" || return 1
  [ -z "$PROJECT_BRIDGES" ] || fill "$ipt" "$APP_CHAIN" "$(app_rules "$2")" || return 1
}

# hooks_only <ipt>: with nothing read yet, the project bridges' wildcard to closed chains; chains that exist keep their rules.
hooks_only() {
  [ -n "$PROJECT_BRIDGES" ] || return 0
  closed "$1" "$HOST_CHAIN" || return 1
  closed "$1" "$APP_CHAIN" || return 1
  { echo "INPUT -i $PROJECT_BRIDGES -j $HOST_CHAIN"; echo "FORWARD -i $PROJECT_BRIDGES -j $APP_CHAIN"
    has_user_chain "$1" && echo "DOCKER-USER -i $PROJECT_BRIDGES -j $APP_CHAIN"; } | while read -r chain rule; do
    # shellcheck disable=SC2086
    "$1" -w -C "$chain" $rule 2>/dev/null || "$1" -w -I "$chain" 1 $rule || exit 1
  done
}

# verify <ipt> <family>: every rule is there, each jump before anything that could accept, and Docker
# still sends forwarded traffic to DOCKER-USER (IPv6 goes without, where the engine has no such chain).
verify() {
  if has_user_chain "$1"; then
    "$1" -w -C FORWARD -j DOCKER-USER 2>/dev/null || { err "$1: FORWARD does not jump to DOCKER-USER"; return 1; }
  fi
  for chain_rules in "$HOST_CHAIN:$(host_rules "$2")" "$TENANT_CHAIN:$(tenant_rules "$2")"; do
    chain_holds "$1" "${chain_rules%%:*}" "${chain_rules#*:}" || { err "$1: ${chain_rules%%:*} is not as written"; return 1; }
  done
  if [ -n "$PROJECT_BRIDGES" ]; then
    chain_holds "$1" "$APP_CHAIN" "$(app_rules "$2")" || { err "$1: $APP_CHAIN is not as written"; return 1; }
  fi
  jumps "$1" | while read -r chain rule; do
    placed "$1" "$chain" "$rule" "$(leading "$1" "$chain")" || { err "$1: $chain $rule is missing, or below a rule that accepts"; exit 1; }
  done
}

families() {
  echo "$IPT4 4"
  [ -z "$IPT6" ] || echo "$IPT6 6"
}

# guard: everything that can go in, whatever else failed, and fails if anything did not.
guard() {
  ok=1
  plan || ok=
  backends || ok=
  if [ -z "$IPT4" ]; then
    err "no iptables backend works here"
    return 1
  fi
  families | while read -r ipt family; do
    if [ -n "$PLANNED" ]; then apply "$ipt" "$family" || exit 1; else hooks_only "$ipt" || exit 1; fi
  done || ok=
  KNOWN4=$IPT4 KNOWN6=$IPT6
  [ -n "$ok" ]
}

check() {
  plan && backends || return 1
  families | while read -r ipt family; do verify "$ipt" "$family" || exit 1; done
}

if [ "${1:-}" = check ]; then
  check
  exit
fi

# In place from the start: a restart with nothing to read yet must not leave project bridges open.
guard >/dev/null 2>&1 || true

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
if [ -z "$IPT6" ]; then say "no IPv6 filter here; IPv4 guarded only"
elif ! has_user_chain "$IPT6"; then say "no IPv6 DOCKER-USER chain here; IPv6 guarded through FORWARD alone"; fi
[ "${HOST_FIREWALL_ONCE:-}" = 1 ] && exit 0

trap 'exit 0' TERM INT
while :; do
  sleep "$INTERVAL" &
  wait $! || true
  # A rule removed (a firewall reload) or a network re-created: rebuild. What cannot be
  # read or written leaves the rest in place and the health check failing, and it tries
  # again: this process is what puts the rules back, so nothing here may end it.
  if ! check >/dev/null 2>&1; then
    if guard && check; then say "rules restored for$IFACES"; else err "the rules are not in place" || true; fi
  fi
done
