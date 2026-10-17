# Runs before MOCO's backup (EXC-586). kube-router, k3s's policy controller,
# admits a new pod to its peers' NetworkPolicies a moment after the pod starts,
# and moco-backup dials once, so this waits until the object store and one
# database instance answer. The headless Service lists instances that are not
# ready too, so one answer is enough: kube-router admits the pod everywhere at once.
set -u

deadline=$((SECONDS + ${EGRESS_WAIT_SECONDS:-60}))

answers() {
  timeout 1 bash -c 'exec 3<>"/dev/tcp/$1/$2"' _ "$1" "$2" 2> /dev/null
}

store_answers() {
  answers "${EGRESS_WAIT_STORE%:*}" "${EGRESS_WAIT_STORE##*:}"
}

database_answers() {
  local address _ seen=" "
  while read -r address _; do
    [[ "$seen" == *" $address "* ]] && continue
    seen+="$address "
    answers "$address" "${EGRESS_WAIT_DATABASE##*:}" && return 0
  done < <(getent ahosts "${EGRESS_WAIT_DATABASE%:*}")
  return 1
}

until store_answers && database_answers; do
  if ((SECONDS >= deadline)); then
    if store_answers; then
      echo "no database instance at ${EGRESS_WAIT_DATABASE} answered within ${EGRESS_WAIT_SECONDS:-60}s"
    else
      echo "the object store at ${EGRESS_WAIT_STORE} did not answer within ${EGRESS_WAIT_SECONDS:-60}s"
    fi
    exit 1
  fi
  sleep 0.2
done
echo "egress answers after ${SECONDS}s"
