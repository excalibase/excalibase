#!/bin/bash
# collect: packs the output directory for the controller to read, then holds
# until it has (the Job deadline still applies). Exit 3: no output directory.
set -euo pipefail
# The build step shares /work: drop anything it left here, a fake ready mark included.
rm -rf /work/collect
out="/work/src/${ROOT_DIR:+${ROOT_DIR}/}${OUTPUT_DIR}"
if [[ ! -d "$out" ]]; then
  echo "the build wrote no '${OUTPUT_DIR}' directory"
  exit 3
fi
mkdir -p /work/collect
tar -czf /work/collect/site.tar.gz -C "$out" .
touch /work/collect/ready
echo "packed $(find "$out" -type f | wc -l) files"
exec sleep 3600
