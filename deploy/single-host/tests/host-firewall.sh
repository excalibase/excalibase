#!/usr/bin/env sh
# EXC-683: host-firewall.sh against a recording iptables: the chain drops new
# connections from the exb-* bridges except DNS, the INPUT jump is added once,
# a run with the rules already there changes nothing, and a host without IPv6
# is guarded on IPv4 alone.
#
#   sh deploy/single-host/tests/host-firewall.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# A fake iptables keeping its rules in a file per family: -C answers from it.
fake() {
  cat >"$WORK/bin/$1" <<EOF
#!/bin/sh
rules="$WORK/$1.rules"
touch "\$rules"
echo "\$*" >>"$WORK/$1.calls"
[ "${2:-}" = absent ] && exit 3
shift # -w
op=\$1; shift
case \$op in
  -C) grep -qxF -- "\$*" "\$rules" ;;
  -A|-I) echo "\$*" | sed 's/^INPUT 1 /INPUT /' >>"\$rules" ;;
  -F) grep -v "^\$1 " "\$rules" >"\$rules.n" || true; mv "\$rules.n" "\$rules" ;;
  -N|-S) ;;
esac
EOF
  chmod +x "$WORK/bin/$1"
}

mkdir "$WORK/bin"
fake iptables
fake ip6tables
run() { PATH="$WORK/bin:$PATH" HOST_FIREWALL_ONCE=1 sh "$DIR/../host-firewall.sh" >"$WORK/out"; }

run
for family in iptables ip6tables; do
  rules=$(cat "$WORK/$family.rules")
  expected="EXCALIBASE-HOST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
EXCALIBASE-HOST -p udp --dport 53 -j RETURN
EXCALIBASE-HOST -p tcp --dport 53 -j RETURN
EXCALIBASE-HOST -j DROP
INPUT -i exb-+ -j EXCALIBASE-HOST"
  [ "$rules" = "$expected" ] || fail "$family rules:
$rules"
done
grep -q '^-w -I INPUT 1 -i exb-+ -j EXCALIBASE-HOST$' "$WORK/iptables.calls" || fail "the jump is not first in INPUT"

# Again, as after a restart: the jump is not added twice.
: >"$WORK/iptables.calls"
run
[ "$(grep -c '^EXCALIBASE-HOST -j DROP$' "$WORK/iptables.rules")" = 1 ] || fail "the chain was appended to, not rebuilt"
! grep -q -- '-I INPUT' "$WORK/iptables.calls" || fail "a second run inserted the jump again"

# No IPv6 filter table: IPv4 is still guarded, and the run says so.
rm -f "$WORK"/*.rules "$WORK"/*.calls
fake ip6tables absent
run
grep -q 'INPUT -i exb-+ -j EXCALIBASE-HOST' "$WORK/iptables.rules" || fail "IPv4 left open on a host without IPv6"
grep -q 'IPv4 guarded only' "$WORK/out" || fail "the IPv6 gap is not said"

# A failing iptables (no NET_ADMIN, no nf_tables) stops the guard rather than run open.
printf '#!/bin/sh\nexit 4\n' >"$WORK/bin/iptables"
if run 2>/dev/null; then
  fail "the guard reported success without its rules"
fi
echo "host-firewall: ok"
