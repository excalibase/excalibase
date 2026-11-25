#!/bin/sh
# Allow Studio's page to send file bytes to the storage origin (EXC-809).
#
# Studio uploads by PUTting straight to a presigned URL, so the page policy's
# connect-src must name the origin those URLs point at. That origin is
# STORAGE_PUBLIC_ORIGIN, set from the same value the platform signs for
# (provisioning's STORAGE_ENDPOINT), so the policy and the links cannot drift.
#
#   unset  -> connect-src stays 'self'; uploads will fail, loudly, not silently open
#   bare scheme://host[:port] (one trailing slash allowed) -> added to connect-src
#   anything else -> refuse to start, so a value cannot widen the policy
#
# Runs on every start and replaces what it set before, so a restart with another
# value does not stack origins.
set -eu

CONF=${NGINX_CONF:-/etc/nginx/conf.d/default.conf}
origin=${STORAGE_PUBLIC_ORIGIN:-}
origin=${origin%/}

refuse() {
  echo "20-storage-origin: STORAGE_PUBLIC_ORIGIN must be a bare scheme://host[:port] (no path, wildcard, quote, semicolon or space); refusing to start" >&2
  exit 1
}

extra=""
if [ -z "$origin" ]; then
  echo "20-storage-origin: WARN STORAGE_PUBLIC_ORIGIN is not set; connect-src stays 'self', so file uploads from Studio will fail" >&2
else
  case $origin in
    *[!A-Za-z0-9:/.-]*) refuse ;;
  esac
  printf '%s\n' "$origin" | grep -Eq '^https?://[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*(:[1-9][0-9]{0,4})?$' || refuse
  extra=" $origin"
fi

sed -i "s|connect-src 'self'[^;]*;|connect-src 'self'${extra};|" "$CONF"
# A reworded policy would leave the sed matching nothing; stop rather than serve it unchanged.
grep -qF "connect-src 'self'${extra};" "$CONF" || { echo "20-storage-origin: connect-src not found in $CONF" >&2; exit 1; }
