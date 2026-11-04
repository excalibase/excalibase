#!/bin/sh
# EXC-683: the platform's networks are bridges with an address on the host, so a
# container on one (a tenant database runs its users' SQL) could open a
# connection to any host service listening on every address. compose names
# those bridges exb-*; this drops new connections from them to the host, and
# keeps the rule in place (a firewall reload flushes it) until stopped.
# DNS stays open: a resolver on the host may be what the engine forwards to.
#
# Runs in the host's network namespace with NET_ADMIN (compose profile hostfirewall).
set -eu

CHAIN=EXCALIBASE-HOST
BRIDGES=${HOST_FIREWALL_BRIDGES:-exb-+}
INTERVAL=${HOST_FIREWALL_INTERVAL:-30}

# guard <iptables binary>: the chain, and INPUT jumping to it from the bridges.
guard() {
  ipt=$1
  $ipt -w -N "$CHAIN" 2>/dev/null || true
  $ipt -w -F "$CHAIN"
  $ipt -w -A "$CHAIN" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  $ipt -w -A "$CHAIN" -p udp --dport 53 -j RETURN
  $ipt -w -A "$CHAIN" -p tcp --dport 53 -j RETURN
  $ipt -w -A "$CHAIN" -j DROP
  $ipt -w -C INPUT -i "$BRIDGES" -j "$CHAIN" 2>/dev/null || $ipt -w -I INPUT 1 -i "$BRIDGES" -j "$CHAIN"
}

# hold <iptables binary>: put the jump back if something removed it.
hold() {
  $1 -w -C INPUT -i "$BRIDGES" -j "$CHAIN" 2>/dev/null && $1 -w -C "$CHAIN" -j DROP 2>/dev/null || guard "$1"
}

guard iptables
ipv6=
if ip6tables -w -S INPUT >/dev/null 2>&1; then
  guard ip6tables
  ipv6=ip6tables
else
  echo "host-firewall: no IPv6 filter table here; IPv4 guarded only"
fi
echo "host-firewall: new connections from $BRIDGES to this host are dropped (DNS excepted)"
[ "${HOST_FIREWALL_ONCE:-}" = 1 ] && exit 0

trap 'exit 0' TERM INT
while :; do
  sleep "$INTERVAL" &
  wait $! || true
  hold iptables
  [ -z "$ipv6" ] || hold "$ipv6"
done
