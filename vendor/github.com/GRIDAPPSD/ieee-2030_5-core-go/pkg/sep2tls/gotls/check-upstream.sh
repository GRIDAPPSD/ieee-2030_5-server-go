#!/usr/bin/env bash
# Repeatable diff of pkg/sep2tls/gotls against its recorded upstream base,
# plus a manifest check for files that have no upstream counterpart. See
# UPSTREAM.md for what a clean run proves and how to record a deliberate
# change. Run from the repository root (the directory containing go.mod).
#
# Exit codes:
#   0  clean: every shared file matches upstream (or a recorded patch
#      whose upstream side still matches its recorded base hash), every
#      manifest entry matches its recorded hash, and no unrecorded file
#      was found under pkg/sep2tls/gotls.
#   2  environment: a required tool is missing, the script was not run
#      from the repository root, the upstream clone/checkout could not be
#      produced as recorded (bad tag, commit mismatch, sparse-checkout
#      failure, network failure, an upstream file absent after checkout),
#      a step this script depends on (the file walk, the normalization
#      pass, a manifest type/upstream-hash lookup) could not be
#      completed, or a "patched" manifest entry has no recorded
#      upstream_sha256 to compare against. This is a tooling or
#      setup failure, not evidence of drift. 2 is reserved for these and
#      is never combined with a bit below: it is returned directly,
#      ending the run.
#   Any other nonzero exit is a bitwise OR of the codes below, so a run
#   that hits more than one condition reports all of them at once instead
#   of the last one silently winning. Read stderr for which fired.
#     1  drift: a shared file differs from upstream with no recorded
#        patch, a manifest entry's hash no longer matches the file's
#        content, an expected shared file is missing, the recorded
#        upstream_sha256 of a patched file no longer matches the pinned
#        upstream copy (UPSTREAM_TAG moved without this manifest entry
#        being re-verified; a crypto/tls security release published
#        while the pin stays put is not caught this way, see
#        UPSTREAM.md), or a manifest line is malformed (missing a
#        required field). A
#        malformed line is reported under this same bit because fixing it
#        takes the same action as fixing a hash mismatch: edit the
#        manifest.
#     4  unrecorded: a file (or symlink) exists under pkg/sep2tls/gotls
#        that is neither a shared file, a manifest entry, nor an ignored
#        path. Add it to FILES or record it in upstream-manifest.sha256.
#   So 5 (1|4) means both drift and an unrecorded file were found in the
#   same run.
#   130 and 143 are 128+SIGINT and 128+SIGTERM: the run was interrupted
#     while cloning upstream and exited at once instead of continuing.
#     Read either as "interrupted," never as a combination of the drift
#     and unrecorded bits above; nothing here bitwise-ORs a signal number
#     with 1 or 4.
set -euo pipefail

UPSTREAM_TAG="go1.22.0"
UPSTREAM_COMMIT="a10e42f219abb9c5bc4e7d86d9464700a42c7d57"
FORK_DIR_REL="pkg/sep2tls/gotls"
FORK_DIR="$(pwd)/${FORK_DIR_REL}"
MANIFEST="${FORK_DIR}/upstream-manifest.sha256"

# Wall-clock bound on the upstream clone. Without this an unreachable
# proxy or a stalled connection hides behind git's own retry backoff for
# minutes with no output, and callers timing the check see it as a hang
# rather than the environment failure it is.
CLONE_TIMEOUT_SECS=90

# Files shared between the fork and upstream crypto/tls. Excludes the
# fork's own additions (cipher_suites_ccm.go, ccm_*_test.go, stubs/**),
# upstream's generate_cert.go and fipsonly/fipsonly.go (standalone tools
# the fork drops), and all _test.go files (the fork carries none of
# upstream's crypto/tls tests).
FILES=(
  alert.go auth.go boring.go cache.go cipher_suites.go common.go
  common_string.go conn.go handshake_client.go handshake_client_tls13.go
  handshake_messages.go handshake_server.go handshake_server_tls13.go
  key_agreement.go key_schedule.go notboring.go prf.go quic.go ticket.go
  tls.go
)

# Paths under FORK_DIR that are check machinery or documentation, not fork
# content, so they are excluded from the unrecorded-file scan.
IGNORED=(check-upstream.sh check-upstream.bats upstream-manifest.sha256 UPSTREAM.md)

in_array() {
  local needle="$1" straw
  shift
  for straw in "$@"; do
    [ "$straw" = "$needle" ] && return 0
  done
  return 1
}

require_tools() {
  local t
  for t in git gofmt sed diff sha256sum find awk cut mktemp timeout; do
    command -v "$t" >/dev/null 2>&1 || {
      echo "error: required tool '$t' not found on PATH" >&2
      exit 2
    }
  done
}

# manifest_type PATH prints the recorded type ("fork-only" or "patched")
# for PATH, or nothing if PATH has no manifest entry. PATH is passed
# through the environment (ENVIRON), not `-v`: `-v var=value` runs awk's
# backslash-escape processing on value, so a literal backslash in PATH
# (gawk silently drops an unrecognized escape; mawk leaves it) makes the
# comparison inconsistent across awk implementations and can match the
# wrong manifest entry. ENVIRON copies the value verbatim.
manifest_type() {
  p="$1" awk -F'\t' '$1 !~ /^#/ && $2 == ENVIRON["p"] { print $1; exit }' "$MANIFEST"
}

# manifest_upstream_sha PATH prints the recorded upstream_sha256 column
# for a "patched" PATH: the sha256 of the upstream file at UPSTREAM_TAG
# as it stood when the patch was recorded. "-" for fork-only entries,
# which have no upstream counterpart to compare. See manifest_type for
# why PATH goes through ENVIRON rather than `-v`.
manifest_upstream_sha() {
  p="$1" awk -F'\t' '$1 !~ /^#/ && $2 == ENVIRON["p"] { print $4; exit }' "$MANIFEST"
}

# check_manifest verifies every manifest entry's file exists and its
# sha256 still matches, and that every non-comment line has all four
# required fields. Prints one message per problem to stderr and returns
# 1 if any was found, 0 otherwise.
check_manifest() {
  local rc=0 type path expected upstream note full actual
  [ -f "$MANIFEST" ] || {
    echo "error: manifest not found at $MANIFEST" >&2
    exit 2
  }
  # `|| [ -n "$type" ]` picks up a final line with no trailing newline:
  # `read` returns nonzero at EOF-without-newline but still populates the
  # fields from it, so without this the last entry is silently skipped
  # while awk-based readers elsewhere (manifest_type) still see it as
  # recorded, and a changed or wrongly hashed last entry passes as clean.
  while IFS=$'\t' read -r type path expected upstream note || [ -n "$type" ]; do
    [ -z "$type" ] && continue
    [[ "$type" == \#* ]] && continue
    if [ -z "$path" ] || [ -z "$expected" ] || [ -z "$upstream" ]; then
      echo "drift: malformed manifest line for type '$type': need type<TAB>path<TAB>sha256<TAB>upstream_sha256<TAB>note, got a line with a missing field" >&2
      rc=1
      continue
    fi
    full="$FORK_DIR/$path"
    if [ ! -f "$full" ]; then
      echo "drift: manifest entry '$path' ($note) is missing from the fork" >&2
      rc=1
      continue
    fi
    # A failing sha256sum is a broken or missing tool, not a signal about
    # the file's content: reporting it as drift would hide the real cause
    # behind a message that says the wrong thing happened.
    if ! actual="$(sha256sum "$full" | cut -d' ' -f1)"; then
      echo "error: could not hash $full (sha256sum failed)" >&2
      exit 2
    fi
    if [ "$actual" != "$expected" ]; then
      echo "drift: $path content changed and its manifest hash was not updated ($note)" >&2
      rc=1
    fi
  done <"$MANIFEST"
  return "$rc"
}

# scan_for_unrecorded walks every file or symlink under FORK_DIR and
# reports any path that is neither a shared FILES entry, a manifest entry,
# nor an ignored path. Returns 1 if it found one, 0 otherwise; exits 2
# directly if the walk itself could not be completed (for example a find
# that rejects -printf, a GNU extension not available on BSD/macOS find),
# so a broken walk fails the run instead of silently reporting that it
# found nothing.
scan_for_unrecorded() {
  local rc=0 relpath list mtype awk_failed=0
  list="$(mktemp)"
  if ! (cd "$FORK_DIR" && find . \( -type f -o -type l \) -printf '%P\0') >"$list"; then
    rm -f "$list"
    echo "error: could not walk $FORK_DIR (find failed)" >&2
    exit 2
  fi
  while IFS= read -r -d '' relpath; do
    in_array "$relpath" "${IGNORED[@]}" && continue
    in_array "$relpath" "${FILES[@]}" && continue
    # A nonzero exit here is the lookup tool itself failing, not "no
    # manifest entry" (which exits 0 with empty output): treating the two
    # alike would misreport a broken reader as an unrecorded file (exit 4)
    # instead of the environment failure (exit 2) it actually is.
    if ! mtype="$(manifest_type "$relpath")"; then
      echo "error: could not determine manifest entry type for '$relpath' (awk failed)" >&2
      awk_failed=1
      break
    fi
    if [ -n "$mtype" ]; then
      continue
    fi
    echo "unrecorded: $relpath is not in FILES or $MANIFEST" >&2
    rc=1
  done <"$list"
  rm -f "$list"
  [ "$awk_failed" -eq 1 ] && exit 2
  return "$rc"
}

# diff_shared_files clones the recorded upstream tag, normalizes the
# fork's shared files back to upstream package and import names, and
# diffs each one. A file recorded as "patched" in the manifest is not
# diffed against upstream (check_manifest verifies the fork's side
# instead); its upstream side is compared against the recorded
# upstream_sha256. The clone is pinned to UPSTREAM_TAG/UPSTREAM_COMMIT on
# every run, so this comparison cannot detect a crypto/tls security
# release published after that pin; it only catches the manifest's
# recorded value going stale, typically after UPSTREAM_TAG is bumped
# without re-verifying this entry. See UPSTREAM.md's "Checking upstream
# crypto/tls security releases against the fork" for the manual
# procedure. Returns 1 if any unpatched shared file differs or is
# missing, or a patched file's recorded upstream hash has gone stale; 0
# otherwise. Exits 2 directly on an environment failure (network,
# checkout, or normalization).
diff_shared_files() {
  local work norm rc=0 f ptype upstream_file diff_rc
  local -a missing_from_fork=()
  local clone_err=""
  work="$(mktemp -d)"
  # A trap set for INT or TERM runs its command and then, by default,
  # resumes the script rather than terminating it: without an explicit
  # exit, a signal delivered while $work is in use is swallowed, the run
  # continues against the now-removed directory, and the exit code
  # reflects whatever that continuation happens to hit (including 0)
  # instead of the interruption. Separate traps that exit immediately
  # after cleanup, with the conventional 128+signal codes, close that.
  # Each trap also removes clone_err: it is created below, outside
  # $work, so a signal caught mid-clone (before the two unconditional
  # rm -f clone_err lines further down ever run) would otherwise leave
  # it behind. The guard on clone_err being non-empty covers the window
  # before it is assigned.
  trap 'rm -rf "$work"; [ -n "$clone_err" ] && rm -f "$clone_err"' EXIT
  trap 'rm -rf "$work"; [ -n "$clone_err" ] && rm -f "$clone_err"; exit 130' INT
  trap 'rm -rf "$work"; [ -n "$clone_err" ] && rm -f "$clone_err"; exit 143' TERM

  clone_err="$(mktemp)"
  if ! timeout "$CLONE_TIMEOUT_SECS" git clone --quiet --filter=blob:none --sparse \
    --branch "$UPSTREAM_TAG" --depth 1 \
    https://github.com/golang/go "$work/go" >/dev/null 2>"$clone_err"; then
    echo "error: could not clone golang/go at tag $UPSTREAM_TAG within ${CLONE_TIMEOUT_SECS}s (network or tag failure):" >&2
    cat "$clone_err" >&2
    rm -f "$clone_err"
    exit 2
  fi
  rm -f "$clone_err"
  if ! git -C "$work/go" sparse-checkout set src/crypto/tls >/dev/null 2>&1; then
    echo "error: sparse-checkout of src/crypto/tls failed" >&2
    exit 2
  fi

  local got
  got="$(git -C "$work/go" rev-parse HEAD)"
  if [ "$got" != "$UPSTREAM_COMMIT" ]; then
    echo "error: upstream commit mismatch: got $got, want $UPSTREAM_COMMIT" >&2
    exit 2
  fi

  norm="$work/norm"
  mkdir -p "$norm" || {
    echo "error: could not create working directory $norm" >&2
    exit 2
  }

  for f in "${FILES[@]}"; do
    upstream_file="$work/go/src/crypto/tls/$f"
    if [ ! -f "$upstream_file" ]; then
      echo "error: upstream file src/crypto/tls/$f not found after checkout" >&2
      exit 2
    fi

    if ! ptype="$(manifest_type "$f")"; then
      echo "error: could not determine manifest entry type for '$f' (awk failed)" >&2
      exit 2
    fi
    if [ "$ptype" = "patched" ]; then
      local upstream_expected upstream_actual
      if ! upstream_expected="$(manifest_upstream_sha "$f")"; then
        echo "error: could not read recorded upstream_sha256 for '$f' (awk failed)" >&2
        exit 2
      fi
      if [ -z "$upstream_expected" ] || [ "$upstream_expected" = "-" ]; then
        echo "error: manifest entry for patched file '$f' has no recorded upstream_sha256; run sha256sum on $upstream_file and record it" >&2
        exit 2
      fi
      if ! upstream_actual="$(sha256sum "$upstream_file" | cut -d' ' -f1)"; then
        echo "error: could not hash $upstream_file (sha256sum failed)" >&2
        exit 2
      fi
      if [ "$upstream_actual" != "$upstream_expected" ]; then
        echo "drift: upstream $f changed since the patch was recorded (upstream sha256 was $upstream_expected, now $upstream_actual); re-review the patch against the new upstream content" >&2
        rc=1
      fi
      continue
    fi

    if [ ! -f "$FORK_DIR/$f" ]; then
      echo "drift: $f is listed as shared but is missing from the fork" >&2
      rc=1
      # Recorded here so the second pass below can skip it too: that pass
      # never got a $norm/$f (the sed normalization above was never reached
      # for a file with no fork copy), and without this list it mistakes an
      # already-reported missing file for its own internal invariant
      # failure (exit 2) instead of returning the drift already found.
      missing_from_fork+=("$f")
      continue
    fi
    # The dot in "github.com" is escaped so the pattern matches only the
    # literal module path, not any single-character stand-in (for example
    # "github_com"): unescaped, a spoofed import naming a stub package
    # under a lookalike path would be silently rewritten to the same
    # normalized upstream import, hiding the substitution from the diff.
    if ! sed -e 's/^package gotls$/package tls/' \
      -e 's#github\.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/fipstls#crypto/internal/boring/fipstls#' \
      -e 's#github\.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/boring#crypto/internal/boring#' \
      -e 's#github\.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/cpu#internal/cpu#' \
      -e 's#github\.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/godebug#internal/godebug#' \
      "$FORK_DIR/$f" >"$norm/$f"; then
      echo "error: could not normalize $f for comparison (sed failed)" >&2
      exit 2
    fi
  done
  # gofmt failing here (a missing binary is already caught by
  # require_tools; this covers gofmt erroring on a specific file) leaves
  # that file un-gofmt-ed, which can only ADD a spurious diff below, not
  # hide a real one, so it fails toward reporting rather than toward
  # silence and is safe to ignore.
  gofmt -w "$norm"/*.go 2>/dev/null || true

  for f in "${FILES[@]}"; do
    if ! ptype="$(manifest_type "$f")"; then
      echo "error: could not determine manifest entry type for '$f' (awk failed)" >&2
      exit 2
    fi
    [ "$ptype" = "patched" ] && continue
    if [ "${#missing_from_fork[@]}" -gt 0 ] && in_array "$f" "${missing_from_fork[@]}"; then
      continue
    fi
    if [ ! -f "$norm/$f" ]; then
      echo "error: internal: normalized copy of $f not found (this is a script bug, not missing input)" >&2
      exit 2
    fi
    # diff -u exits 1 for "differs" and 2 for its own trouble (permissions,
    # a file it cannot read); only the former is drift. Capturing the exit
    # code explicitly, rather than folding any nonzero into rc=1, keeps a
    # broken diff from being reported as content drift.
    diff_rc=0
    diff -u "$work/go/src/crypto/tls/$f" "$norm/$f" || diff_rc=$?
    if [ "$diff_rc" -eq 2 ]; then
      echo "error: could not compare $f against upstream (diff exited 2)" >&2
      exit 2
    elif [ "$diff_rc" -ne 0 ]; then
      rc=1
    fi
  done

  rm -rf "$work"
  trap - EXIT INT TERM
  return "$rc"
}

main() {
  require_tools

  if [ ! -f "$(pwd)/go.mod" ]; then
    echo "error: run from the repository root (the directory containing go.mod)" >&2
    exit 2
  fi
  if [ ! -f "$MANIFEST" ]; then
    echo "error: manifest not found at $MANIFEST" >&2
    exit 2
  fi

  local status=0
  diff_shared_files || status=$((status | 1))
  check_manifest || status=$((status | 1))
  scan_for_unrecorded || status=$((status | 4))

  exit "$status"
}

main "$@"
