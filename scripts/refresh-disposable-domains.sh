#!/bin/sh
# Refreshes the vendored disposable-email-domains list (EXC-755). Prints "changed" or "unchanged".
# Source: https://github.com/disposable-email-domains/disposable-email-domains (CC0-1.0).
set -eu
dest="$(dirname "$0")/../server-go/internal/abuse/data/disposable_email_blocklist.conf"
url="${DISPOSABLE_LIST_URL:-https://raw.githubusercontent.com/disposable-email-domains/disposable-email-domains/main/disposable_email_blocklist.conf}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 "$url" -o "$tmp"
# A truncated or error page must never replace the list.
lines="$(grep -c . "$tmp")"
if [ "$lines" -lt 5000 ]; then
  echo "refusing a list of $lines lines (expected thousands)" >&2
  exit 1
fi
if cmp -s "$tmp" "$dest"; then
  echo unchanged
else
  cp "$tmp" "$dest"
  echo changed
fi
