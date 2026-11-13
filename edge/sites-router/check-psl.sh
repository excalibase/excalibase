#!/usr/bin/env sh
# Confirms the user-content base domain is on the Public Suffix List (EXC-719).
# Until it is, a script on one customer site can set a cookie with
# Domain=<base domain> that every other site receives; the router strips such
# cookies from responses, but it cannot see document.cookie in the browser.
#
#   sh edge/sites-router/check-psl.sh excalibase.app
#
# PSL_FILE names a local copy of the list; without it the list is fetched.
set -eu

domain=${1-}
printf '%s' "$domain" | grep -Eq '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$' || {
  echo "check-psl.sh: give the base domain, for example excalibase.app" >&2
  exit 2
}

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
if [ -n "${PSL_FILE-}" ]; then
  cp "$PSL_FILE" "$WORK/list.dat"
else
  curl -fsS --max-time 30 https://publicsuffix.org/list/public_suffix_list.dat -o "$WORK/list.dat" ||
    { echo "check-psl.sh: could not fetch the Public Suffix List" >&2; exit 2; }
fi

if grep -Fxq "$domain" "$WORK/list.dat"; then
  echo "$domain is on the Public Suffix List"
  exit 0
fi
echo "$domain is NOT on the Public Suffix List: sites under it can set cookies for each other." >&2
echo "Submit it at https://github.com/publicsuffix/list (private section) and run this again once merged." >&2
exit 1
