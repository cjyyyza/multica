#!/bin/sh
set -eu

# Repair a mounted upload directory before the all-in-one image drops to
# nextjs. The image-layer chown does not apply to a volume, and the previous
# root-owned backend left this tree owned by root: readable, but not writable.
# A later start whose tree is already owned by nextjs must not rewrite it.

upload_dir="${LOCAL_UPLOAD_DIR:-./data/uploads}"
owner="${UPLOAD_OWNER:-nextjs:nodejs}"
owner_user="${owner%%:*}"

case "$upload_dir" in
  /*) ;;
  *) upload_dir="$(pwd)/${upload_dir#./}" ;;
esac

mkdir -p "$upload_dir"
# Stop at the first foreign path. head closes the pipe, so a root-owned child
# does not require a full walk before chown.
if find "$upload_dir" ! -user "$owner_user" -print | head -n 1 | grep -q .; then
  chown -R "$owner" "$upload_dir"
fi
