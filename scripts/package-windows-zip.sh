#!/usr/bin/env bash
# #1220: build the Windows release zip and its checksum file.
#
# Usage: package-windows-zip.sh <tag> <out-dir>
#
# Builds through `make build` so the release binary uses the same recipe as a
# developer build (vendored modules, no second build line to drift). Writes
# <out-dir>/sep2server-<tag>-windows-amd64.zip and <out-dir>/SHA256SUMS, whose
# entry names are bare file names so `sha256sum -c` works from <out-dir>.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: ${0##*/} <tag> <out-dir>" >&2
  exit 2
fi
tag=$1
out=$2

# The tag becomes part of a file name: refuse anything but a plain version tag.
if [[ ! $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+.][0-9A-Za-z.-]+)?$ ]]; then
  echo "refusing tag '$tag': expected vMAJOR.MINOR.PATCH[-suffix]" >&2
  exit 2
fi
for tool in make zip sha256sum; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
stage=$(mktemp -d "$out/stage.XXXXXX")
trap 'rm -rf "$stage"' EXIT

cd "$root"
GOOS=windows GOARCH=amd64 make build SERVER="$stage/sep2server.exe"
cp scripts/windows-zip-README.txt "$stage/README.txt"

zipname="sep2server-${tag}-windows-amd64.zip"
rm -f "$out/$zipname" "$out/SHA256SUMS"
(cd "$stage" && zip -q -X "$out/$zipname" sep2server.exe README.txt)
(cd "$out" && sha256sum "$zipname" > SHA256SUMS)
echo "wrote $out/$zipname and $out/SHA256SUMS"
