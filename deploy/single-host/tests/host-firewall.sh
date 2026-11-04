#!/usr/bin/env sh
# EXC-683: host-firewall.sh against recording iptables, a bridges file as the
# engine proxy writes it, and a fake /sys/class/net.
#
#   sh deploy/single-host/tests/host-firewall.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# A fake iptables keeping its rules in a file per family. "absent" answers no
# DOCKER-USER chain, as on a host whose engine is not on this backend.
fake() {
  cat >"$WORK/bin/$1" <<EOF
#!/bin/sh
rules="$WORK/$2.rules"
touch "\$rules"
echo "\$*" >>"$WORK/$2.calls"
[ "${3:-}" = broken ] && exit 4
shift # -w
op=\$1; shift
if [ "${3:-}" = absent ] && [ "\$op" = -S ] && [ "\$1" = DOCKER-USER ]; then exit 1; fi
case \$op in
  -C) grep -qxF -- "\$*" "\$rules" ;;
  -A) echo "\$*" >>"\$rules" ;;
  -I) chain=\$1; shift 2; { echo "\$chain \$*"; cat "\$rules"; } >"\$rules.n"; mv "\$rules.n" "\$rules" ;;
  -D) grep -vxF -- "\$*" "\$rules" >"\$rules.n" || true; mv "\$rules.n" "\$rules" ;;
  -F) grep -v "^\$1 " "\$rules" >"\$rules.n" || true; mv "\$rules.n" "\$rules" ;;
  -S) echo "-N \$1"; grep "^\$1 " "\$rules" | sed 's/^/-A /' || true ;;
  -N) ;;
esac
EOF
  chmod +x "$WORK/bin/$1"
}

reset() {
  rm -rf "$WORK/bin" "$WORK/sys" "$WORK"/*.rules "$WORK"/*.calls
  mkdir -p "$WORK/bin" "$WORK/sys/br-3f1c9a2b7d4e" "$WORK/sys/br-aa11bb22cc33" "$WORK/sys/br-77aa88bb99cc"
  fake iptables-nft v4
  fake ip6tables-nft v6
  # Docker installs FORWARD -> DOCKER-USER; the guard checks it is still there.
  # A host rule accepting all input and Docker's own DOCKER-USER return come before ours.
  printf 'FORWARD -j DOCKER-USER\nINPUT -j ACCEPT\nDOCKER-USER -j RETURN\n' >"$WORK/v4.rules"
  cp "$WORK/v4.rules" "$WORK/v6.rules"
  # v1.6 networks keep their br-<id> names; drains is off (its profile).
  cat >"$WORK/bridges" <<EOF
excalibase-tenants br-3f1c9a2b7d4e 172.19.0.0/16,fd00:19::/64
excalibase-platform br-aa11bb22cc33 172.20.0.0/16
excalibase-edge br-77aa88bb99cc 10.203.250.0/28
excalibase-drains absent
EOF
}

run() {
  PATH="$WORK/bin:$PATH" HOST_FIREWALL_ONCE=1 HOST_FIREWALL_WAIT=0 HOST_FIREWALL_SYSFS="$WORK/sys" \
    HOST_FIREWALL_BRIDGES_FILE="$WORK/bridges" \
    HOST_FIREWALL_NETWORKS=excalibase-tenants,excalibase-platform,excalibase-edge,excalibase-drains \
    HOST_FIREWALL_TENANT_NETWORKS=excalibase-tenants sh "$DIR/../host-firewall.sh" "$@" >"$WORK/out" 2>&1
}

has() { grep -qxF -- "$2" "$WORK/$1.rules" || fail "$1 lacks: $2
$(cat "$WORK/$1.rules")"; }
lacks() { ! grep -qxF -- "$2" "$WORK/$1.rules" || fail "$1 has: $2"; }

# 1. Every resolved bridge, by its real (br-<id>) name, is guarded on INPUT.
reset
run || fail "a first run failed: $(cat "$WORK/out")"
for family in v4 v6; do
  for iface in br-3f1c9a2b7d4e br-aa11bb22cc33 br-77aa88bb99cc; do
    has $family "INPUT -i $iface -j EXCALIBASE-HOST"
  done
  has $family "EXCALIBASE-HOST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN"
  has $family "EXCALIBASE-HOST -p udp --dport 53 -j RETURN"
  has $family "EXCALIBASE-HOST -j DROP"
  lacks $family "INPUT -i exb-+ -j EXCALIBASE-HOST"
done
before() { [ "$(grep -nxF -- "$2" "$WORK/$1.rules" | cut -d: -f1)" -lt "$(grep -nxF -- "$3" "$WORK/$1.rules" | cut -d: -f1)" ] || fail "$1: '$2' is not before '$3'"; }
before v4 "INPUT -i br-3f1c9a2b7d4e -j EXCALIBASE-HOST" "INPUT -j ACCEPT"
before v4 "DOCKER-USER -i br-3f1c9a2b7d4e -j EXCALIBASE-TENANT" "DOCKER-USER -j RETURN"
# IPv6 neighbour discovery to the host keeps working.
has v6 "EXCALIBASE-HOST -p icmpv6 -j RETURN"
lacks v4 "EXCALIBASE-HOST -p icmpv6 -j RETURN"

# 2. Forwarded traffic from the tenant bridge alone: no metadata, no private
#    ranges, except the platform networks' own subnets; first in DOCKER-USER.
has v4 "DOCKER-USER -i br-3f1c9a2b7d4e -j EXCALIBASE-TENANT"
has v6 "DOCKER-USER -i br-3f1c9a2b7d4e -j EXCALIBASE-TENANT"
lacks v4 "DOCKER-USER -i br-aa11bb22cc33 -j EXCALIBASE-TENANT"
tenant4=$(grep '^EXCALIBASE-TENANT' "$WORK/v4.rules")
expected4="EXCALIBASE-TENANT -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
EXCALIBASE-TENANT -d 172.19.0.0/16 -j RETURN
EXCALIBASE-TENANT -d 172.20.0.0/16 -j RETURN
EXCALIBASE-TENANT -d 10.203.250.0/28 -j RETURN
EXCALIBASE-TENANT -p udp --dport 53 -j RETURN
EXCALIBASE-TENANT -p tcp --dport 53 -j RETURN
EXCALIBASE-TENANT -d 169.254.0.0/16 -j DROP
EXCALIBASE-TENANT -d 10.0.0.0/8 -j DROP
EXCALIBASE-TENANT -d 172.16.0.0/12 -j DROP
EXCALIBASE-TENANT -d 192.168.0.0/16 -j DROP
EXCALIBASE-TENANT -d 100.64.0.0/10 -j DROP"
[ "$tenant4" = "$expected4" ] || fail "IPv4 tenant chain:
$tenant4"
tenant6=$(grep '^EXCALIBASE-TENANT' "$WORK/v6.rules")
expected6="EXCALIBASE-TENANT -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
EXCALIBASE-TENANT -d fd00:19::/64 -j RETURN
EXCALIBASE-TENANT -p udp --dport 53 -j RETURN
EXCALIBASE-TENANT -p tcp --dport 53 -j RETURN
EXCALIBASE-TENANT -d fe80::/10 -j DROP
EXCALIBASE-TENANT -d fc00::/7 -j DROP"
[ "$tenant6" = "$expected6" ] || fail "IPv6 tenant chain:
$tenant6"
grep -q 'br-3f1c9a2b7d4e' "$WORK/out" && grep -q 'are dropped' "$WORK/out" || fail "the guard does not say what it guards: $(cat "$WORK/out")"

# 3. The health check passes with the rules, fails without any one of them.
run check || fail "check failed with every rule in place: $(cat "$WORK/out")"
grep -vxF "DOCKER-USER -i br-3f1c9a2b7d4e -j EXCALIBASE-TENANT" "$WORK/v4.rules" >"$WORK/v4.n"; mv "$WORK/v4.n" "$WORK/v4.rules"
if run check; then fail "check passed without the tenant jump"; fi
run || fail "rerun: $(cat "$WORK/out")"
grep -vxF "EXCALIBASE-TENANT -d 169.254.0.0/16 -j DROP" "$WORK/v4.rules" >"$WORK/v4.n"; mv "$WORK/v4.n" "$WORK/v4.rules"
if run check; then fail "check passed without the metadata drop"; fi

# 4. A rerun rebuilds rather than appends, and does not add a jump twice.
reset; run; run
[ "$(grep -c '^EXCALIBASE-HOST -j DROP$' "$WORK/v4.rules")" = 1 ] || fail "the host chain was appended to"
[ "$(grep -c '^INPUT -i br-3f1c9a2b7d4e -j EXCALIBASE-HOST$' "$WORK/v4.rules")" = 1 ] || fail "a jump was added twice"
[ "$(grep -c '^DOCKER-USER -i br-3f1c9a2b7d4e -j EXCALIBASE-TENANT$' "$WORK/v4.rules")" = 1 ] || fail "a tenant jump was added twice"

# 5. A network re-created under a new bridge: the old jump goes, the new one comes.
mv "$WORK/sys/br-3f1c9a2b7d4e" "$WORK/sys/br-0123456789ab"
sed -i 's/br-3f1c9a2b7d4e/br-0123456789ab/' "$WORK/bridges"
run || fail "after a re-created network: $(cat "$WORK/out")"
has v4 "INPUT -i br-0123456789ab -j EXCALIBASE-HOST"
has v4 "DOCKER-USER -i br-0123456789ab -j EXCALIBASE-TENANT"
lacks v4 "INPUT -i br-3f1c9a2b7d4e -j EXCALIBASE-HOST"
lacks v4 "DOCKER-USER -i br-3f1c9a2b7d4e -j EXCALIBASE-TENANT"

# 6. Fails loudly, guarding nothing it cannot name: a network the proxy did
#    not resolve, a bridge not on this host, a missing tenant network, no file.
for broken in unresolved missing-bridge absent-tenant no-file; do
  reset
  case $broken in
    unresolved) sed -i '/^excalibase-platform /d' "$WORK/bridges"; want=excalibase-platform ;;
    missing-bridge) rmdir "$WORK/sys/br-aa11bb22cc33"; want=br-aa11bb22cc33 ;;
    absent-tenant) sed -i 's/^excalibase-tenants .*/excalibase-tenants absent/' "$WORK/bridges"; want=excalibase-tenants ;;
    no-file) rm "$WORK/bridges"; want=bridges ;;
  esac
  if run; then fail "$broken: the guard reported success"; fi
  grep -q "$want" "$WORK/out" || fail "$broken: the error does not name $want: $(cat "$WORK/out")"
  if run check; then fail "$broken: check passed"; fi
done

# 7. Docker's DOCKER-USER chain on neither backend (nftables firewall backend): refuse.
reset
fake iptables-nft v4 absent
if run; then fail "ran without DOCKER-USER"; fi
grep -q 'DOCKER-USER' "$WORK/out" || fail "the missing DOCKER-USER is not said: $(cat "$WORK/out")"
# ... unless the legacy backend holds it.
fake iptables-legacy v4
run || fail "the legacy backend was not used: $(cat "$WORK/out")"
has v4 "DOCKER-USER -i br-3f1c9a2b7d4e -j EXCALIBASE-TENANT"

# 8. No IPv6 filter: refused while the tenant network has an IPv6 subnet,
#    IPv4 alone (and said) when it has none.
reset
fake ip6tables-nft v6 absent
if run; then fail "ran with the tenants' IPv6 subnet unguarded"; fi
reset
fake ip6tables-nft v6 absent
sed -i 's#,fd00:19::/64##' "$WORK/bridges"
run || fail "IPv4-only: $(cat "$WORK/out")"
grep -q 'IPv4 guarded only' "$WORK/out" || fail "the IPv6 gap is not said"

# 9. A failing iptables (no NET_ADMIN) stops the guard rather than run open.
reset
fake iptables-nft v4 broken
if run; then fail "the guard reported success without its rules"; fi

# 10. The platform's own traffic: nothing guards the platform's internal
#     networks' forwarding, and no rule refuses traffic between platform subnets.
reset
run
! grep -q '^DOCKER-USER -i br-aa11bb22cc33\|^DOCKER-USER -i br-77aa88bb99cc' "$WORK/v4.rules" || fail "a platform network's forwarding is filtered"
echo "host-firewall: ok"
