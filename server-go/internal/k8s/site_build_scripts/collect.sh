#!/bin/bash
# collect: packs the output directory for the controller to read, then holds
# until it has (the Job deadline still applies). Exit 3: no output directory.
set -euo pipefail
# The build step shares /work: drop anything it left here, a fake ready mark included.
work=${WORK_DIR:-/work}
rm -rf "$work/collect"
if [[ "${FRAMEWORK:-}" == "nextjs" ]]; then
  # The server layout: server/ (the bundled module), assets/ (public files) and site.json.
  assets="$work/src/${ROOT_DIR:+${ROOT_DIR}/}.open-next/assets"
  if [[ ! -f "$work/bundle/worker.js" || ! -d "$assets" ]]; then
    echo "the Next.js build wrote no server bundle or assets"
    exit 3
  fi
  pack="$work/collect-pack"
  rm -rf "$pack"
  mkdir -p "$pack/server" "$work/collect"
  tar -C "$work/bundle" --exclude=README.md --exclude='*.map' -cf - . | tar -C "$pack/server" -xf -
  cp -R "$assets" "$pack/assets"
  printf '%s' '{"kind":"server","main":"worker.js","compatibilityDate":"2025-09-01","compatibilityFlags":["nodejs_compat"],"framework":"nextjs"}' > "$pack/site.json"
  tar -czf "$work/collect/site.tar.gz" -C "$pack" .
  printf %s "${READY_TOKEN:-}" > "$work/collect/ready"
  echo "packed $(find "$pack" -type f | wc -l) files"
  exec sleep "${HOLD_SECONDS:-3600}"
fi
out="$work/src/${ROOT_DIR:+${ROOT_DIR}/}${OUTPUT_DIR}"
if [[ ! -d "$out" ]]; then
  echo "the build wrote no '${OUTPUT_DIR}' directory"
  exit 3
fi
mkdir -p "$work/collect"
tar -czf "$work/collect/site.tar.gz" -C "$out" .
printf %s "${READY_TOKEN:-}" > "$work/collect/ready"
echo "packed $(find "$out" -type f | wc -l) files"
exec sleep "${HOLD_SECONDS:-3600}"
