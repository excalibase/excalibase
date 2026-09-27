# Sourced by e2e scripts. STANDARD runs 3 instances, one per node, and the API
# refuses it on a smaller cluster; a test cluster with fewer nodes sizes the
# tier down to one instance first. Usage: standard_tier_fits_nodes <api> <auth-header>
standard_tier_fits_nodes() {
  local api="$1" auth="$2" nodes spec
  nodes=$(kubectl get nodes --no-headers | grep -vc SchedulingDisabled)
  [ "$nodes" -ge 3 ] && return 0
  spec=$(curl -sf -H "$auth" "$api/api/admin/tiers/" | jq -c '.[] | select(.tier=="STANDARD") | .instances=1')
  [ -n "$spec" ] || return 1
  curl -sf -X PUT -H "$auth" -H 'Content-Type: application/json' -d "$spec" "$api/api/admin/tiers/STANDARD" > /dev/null
}
