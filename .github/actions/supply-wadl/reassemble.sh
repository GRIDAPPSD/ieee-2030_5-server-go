#!/usr/bin/env bash
# Reassemble one licensed document from a base64 xz payload into a file outside
# the checkout, verify its transport digest, and export its path.
#
# Shared by every document the supply-wadl action supplies, so the decode, the
# digest check and the "write it outside the checkout" rule have one home.
#
# Usage: reassemble.sh <label> <dest-dir-name> <dest-file> <want-md5> <want-bytes> <path-env-var>
# Input: the payload in the environment variable REASSEMBLE_PAYLOAD (already
# concatenated if it came in parts). On success appends <path-env-var>=<dest> to
# $GITHUB_ENV. The caller exports the REQUIRED switch.
set -euo pipefail

label=${1:?label}
dest_dir_name=${2:?dest dir name}
dest_file=${3:?dest file}
want_md5=${4:?want md5}
want_bytes=${5:?want bytes}
path_var=${6:?path env var}
payload=${REASSEMBLE_PAYLOAD:?REASSEMBLE_PAYLOAD is unset}

if ! command -v xz > /dev/null; then
  echo "::error::xz is not installed on this runner, so the $label cannot be decompressed."
  exit 1
fi

# RUNNER_TEMP is not the repo working tree, so the file cannot be committed, is
# not visible to anything that globs the tree, and is not part of any path a
# later step would upload as an artifact. The runner deletes it when the job ends.
dest_dir="${RUNNER_TEMP:?RUNNER_TEMP is unset}/$dest_dir_name"
case "$dest_dir" in
  "${GITHUB_WORKSPACE:?GITHUB_WORKSPACE is unset}"/*)
    echo "::error::Refusing to write the $label inside the repository working tree."
    exit 1
    ;;
esac
umask 077
mkdir -p "$dest_dir"
dest="$dest_dir/$dest_file"

# printf is a builtin, so the payload never reaches a process argument list.
if ! printf '%s' "$payload" \
  | tr -d '[:space:]' \
  | base64 -d \
  | xz -d > "$dest"; then
  rm -f "$dest"
  echo "::error::Could not reassemble $dest_file from its secret. The value must be one base64 string holding an xz stream."
  exit 1
fi

got_bytes=$(wc -c < "$dest")
got_md5=$(md5sum < "$dest" | cut -d ' ' -f 1)
if [ "$got_bytes" != "$want_bytes" ] || [ "$got_md5" != "$want_md5" ]; then
  rm -f "$dest"
  echo "::error::Reassembled $label is not the expected document: $got_bytes bytes, md5 $got_md5; want $want_bytes bytes, md5 $want_md5. A truncated or half-rotated secret is the likely cause; reseal from one copy."
  exit 1
fi
echo "Reassembled $dest_file: $got_bytes bytes, md5 matches."

echo "$path_var=$dest" >> "$GITHUB_ENV"
