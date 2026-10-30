#!/bin/sh
# collect: hands the built OCI image to the controller as one archive, then
# holds until it has (the Job deadline still applies). Exit 3: no image.
# The build steps share /work, so nothing they left is trusted: no symlinks.
set -eu
rm -rf /work/collect
mkdir -p /work/collect
if [ "$BUILDER" = dockerfile ]; then
  image=/work/out/image.tar
  if [ -L "$image" ] || [ ! -f "$image" ] || [ ! -s "$image" ]; then echo "the build wrote no image"; exit 3; fi
  mv "$image" /work/collect/image.tar
else
  dir=$(cd -P "$IMAGE_DIR" 2>/dev/null && pwd) || { echo "the buildpacks export wrote no image"; exit 3; }
  case "$dir" in /work/out/layout/*) ;; *) echo "the buildpacks export is outside its layout"; exit 3 ;; esac
  if [ -L "$dir/index.json" ] || [ ! -f "$dir/index.json" ]; then echo "the buildpacks export wrote no image"; exit 3; fi
  tar -cf /work/collect/image.tar -C "$dir" .
fi
# Printed before the ready mark, so the log has it whenever the controller sees ready.
echo "packed the image ($(wc -c < /work/collect/image.tar) bytes)"
touch /work/collect/ready
exec sleep 3600
