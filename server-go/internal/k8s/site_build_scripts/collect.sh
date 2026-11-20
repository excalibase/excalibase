#!/bin/bash
# collect: packs the output directory for the controller to read, then holds
# until it has (the Job deadline still applies). Exit 3: no output directory.
set -euo pipefail
# The build step shares /work: drop anything it left here, a fake ready mark included.
work=${WORK_DIR:-/work}
rm -rf "$work/collect"
if [[ -n "${FRAMEWORK:-}" ]]; then
  # The server layout: server/ (the bundled module), assets/ (public files) and site.json. Next.js keeps its
  # assets under .open-next; the other presets name theirs in assets-dir.
  assets_path=.open-next/assets
  [[ -f "$work/assets-dir" ]] && assets_path=$(cat "$work/assets-dir")
  assets="$work/src/${ROOT_DIR:+${ROOT_DIR}/}$assets_path"
  main=$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")).main || "")' "$work/site.json" 2>/dev/null || true)
  if [[ -z "$main" || ! -f "$work/bundle/$main" || ! -d "$assets" ]]; then
    echo "the $FRAMEWORK build wrote no server bundle or assets"
    exit 3
  fi
  pack="$work/collect-pack"
  rm -rf "$pack"
  mkdir -p "$pack/server" "$work/collect"
  tar -C "$work/bundle" --exclude=README.md --exclude='*.map' -cf - . | tar -C "$pack/server" -xf -
  # Server code is never a public file: a worker kept inside the assets folder stays out of it.
  # Like wrangler, nothing the repository's own .assetsignore names is published (a pattern without a slash matches at
  # any depth). A pattern that would leave the folder is ignored.
  excludes=(--exclude=./_worker.js --exclude=./.assetsignore)
  if [[ -f "$assets/.assetsignore" ]]; then
    while IFS= read -r pattern || [[ -n "$pattern" ]]; do
      pattern=${pattern%$'\r'}
      [[ -z "$pattern" || "$pattern" == \#* || "$pattern" == ../* ]] && continue
      anchored=0
      [[ "$pattern" == /* ]] && anchored=1
      pattern=${pattern#/}
      pattern=${pattern%/}
      [[ -z "$pattern" ]] && continue
      excludes+=("--exclude=./$pattern")
      [[ $anchored == 0 && "$pattern" != */* ]] && excludes+=("--exclude=./*/$pattern")
    done < "$assets/.assetsignore"
  fi
  tar -C "$assets" "${excludes[@]}" -cf - . | { mkdir -p "$pack/assets" && tar -C "$pack/assets" -xf -; }
  cp "$work/site.json" "$pack/site.json"
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
