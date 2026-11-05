#!/usr/bin/env bash
# #1220: attach the Windows zip and SHA256SUMS to an existing release without
# ever replacing a published asset.
#
# Usage: attach-windows-zip.sh <tag> <dist-dir> <owner/repo>
#
# <dist-dir> is the output of package-windows-zip.sh. If the release holds no
# zip yet, both files are uploaded (no --clobber, so an asset that appeared in
# between makes the upload fail). If it already holds the zip, the rebuilt
# SHA256SUMS must equal the published one: equal exits 0 with nothing
# uploaded, different fails.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: ${0##*/} <tag> <dist-dir> <owner/repo>" >&2
  exit 2
fi
tag=$1
dist=$2
repo=$3

zip=$(cd "$dist" && ls -- sep2server-*-windows-amd64.zip)
sums="$dist/SHA256SUMS"
[[ -f $sums ]] || { echo "missing $sums" >&2; exit 1; }

assets=$(gh release view "$tag" --repo "$repo" --json assets -q '.assets[].name')
if ! grep -qxF -- "$zip" <<<"$assets"; then
  gh release upload "$tag" "$dist/$zip" "$sums" --repo "$repo"
  exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
if ! gh release download "$tag" --repo "$repo" --pattern SHA256SUMS --dir "$work"; then
  echo "release $tag holds $zip but SHA256SUMS could not be downloaded; not replacing it" >&2
  exit 1
fi
if cmp -s "$work/SHA256SUMS" "$sums"; then
  echo "release $tag already holds $zip with the same checksum; nothing to upload"
  exit 0
fi
echo "release $tag already holds $zip and the rebuilt checksum differs from the published SHA256SUMS; refusing to replace a published asset" >&2
exit 1
