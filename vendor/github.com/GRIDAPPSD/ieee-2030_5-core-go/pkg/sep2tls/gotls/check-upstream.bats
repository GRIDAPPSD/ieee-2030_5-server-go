#!/usr/bin/env bats
# Smoke test for check-upstream.sh: the happy path plus one test per
# documented failure path (missing tool, wrong cwd, missing manifest,
# clone/commit failure, content drift, and an unrecorded file). The
# upstream clone is mocked (see mock_git below) so the suite needs no
# network access and is not sensitive to real upstream content changes.

setup() {
  SRC_DIR="$BATS_TEST_DIRNAME"
  WORK="$(mktemp -d)"
  REPO="$WORK/repo"
  FORK_DIR="$REPO/pkg/sep2tls/gotls"

  mkdir -p "$REPO/pkg/sep2tls"
  : >"$REPO/go.mod"
  cp -r "$SRC_DIR" "$FORK_DIR"

  # A frozen "upstream" fixture, built once from the pristine fork copy
  # above, before any test mutates it. This is what the mocked git
  # presents as the recorded upstream tag, so the happy path diffs clean
  # by construction and a later mutation to $FORK_DIR shows up as drift
  # against this fixture, not against a moving target.
  UPSTREAM_FIXTURE="$WORK/upstream-fixture/src/crypto/tls"
  mkdir -p "$UPSTREAM_FIXTURE"
  for f in "$FORK_DIR"/*.go; do
    base="$(basename "$f")"
    case "$base" in
    cipher_suites_ccm.go | cipher_suites_ccm_order_test.go | ccm_check_test.go | ccm_raw_test.go) continue ;;
    esac
    sed -e 's/^package gotls$/package tls/' \
      -e 's#github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/fipstls#crypto/internal/boring/fipstls#' \
      -e 's#github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/boring#crypto/internal/boring#' \
      -e 's#github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/cpu#internal/cpu#' \
      -e 's#github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/godebug#internal/godebug#' \
      "$f" >"$UPSTREAM_FIXTURE/$base"
  done
  gofmt -w "$UPSTREAM_FIXTURE"/*.go

  MOCKBIN="$WORK/mockbin"
  mkdir -p "$MOCKBIN"
  cat >"$MOCKBIN/git" <<MOCKEOF
#!/usr/bin/env bash
set -euo pipefail
if [ "\$1" = "clone" ]; then
  if [ -n "\${MOCK_GIT_FAIL_CLONE:-}" ]; then
    echo "mock: clone failed" >&2
    exit 1
  fi
  dest="\${!#}"
  mkdir -p "\$dest"
  exit 0
fi
if [ "\$1" = "-C" ]; then
  dir="\$2"
  sub="\$3"
  if [ "\$sub" = "sparse-checkout" ]; then
    mkdir -p "\$dir/src/crypto/tls"
    cp "$UPSTREAM_FIXTURE"/*.go "\$dir/src/crypto/tls/"
    exit 0
  fi
  if [ "\$sub" = "rev-parse" ]; then
    echo "\${MOCK_GIT_COMMIT:-a10e42f219abb9c5bc4e7d86d9464700a42c7d57}"
    exit 0
  fi
fi
echo "mock git: unhandled args: \$*" >&2
exit 1
MOCKEOF
  chmod +x "$MOCKBIN/git"

  PATH="$MOCKBIN:$PATH"
  export PATH REPO FORK_DIR WORK
}

teardown() {
  rm -rf "$WORK"
}

run_check() {
  (cd "$REPO" && bash "$FORK_DIR/check-upstream.sh")
}

@test "clean tree against the recorded upstream fixture exits 0" {
  run run_check
  [ "$status" -eq 0 ]
}

@test "missing required tool exits 2" {
  # A curated PATH holding every external tool check-upstream.sh itself
  # calls, resolved to a real executable path with "type -P" (so a
  # builtin or a shell function with no backing file is never
  # symlinked), except git: git is left out, so "command -v git" fails
  # and the script's own tool-presence guard is what's under test.
  local no_git_bin="$WORK/no-git-bin"
  mkdir -p "$no_git_bin"
  for t in bash sed diff sha256sum gofmt mkdir rm mktemp find awk cut timeout; do
    ln -s "$(type -P "$t")" "$no_git_bin/$t"
  done
  PATH="$no_git_bin" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"required tool 'git'"* ]]
}

@test "running outside the repository root exits 2" {
  run bash -c "cd '$WORK' && bash '$FORK_DIR/check-upstream.sh'"
  [ "$status" -eq 2 ]
  [[ "$output" == *"repository root"* ]]
}

@test "a missing manifest exits 2" {
  rm "$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"manifest not found"* ]]
}

@test "an upstream clone failure exits 2, not 1" {
  export MOCK_GIT_FAIL_CLONE=1
  run run_check
  [ "$status" -eq 2 ]
}

@test "an upstream commit mismatch exits 2, not 1" {
  export MOCK_GIT_COMMIT=deadbeef
  run run_check
  [ "$status" -eq 2 ]
}

@test "a SIGTERM during the upstream clone exits at once instead of continuing" {
  local sig_bin="$WORK/sigterm-git-bin" marker="$WORK/sigterm-clone-started" tmp_under_test pid status
  mkdir -p "$sig_bin"
  cat >"$sig_bin/git" <<GITEOF
#!/usr/bin/env bash
if [ "\$1" = "clone" ]; then
  dest="\${!#}"
  : >"$marker"
  sleep 5
  mkdir -p "\$dest"
  exit 0
fi
echo "mock git: unhandled args: \$*" >&2
exit 1
GITEOF
  chmod +x "$sig_bin/git"
  tmp_under_test="$WORK/tmp-under-test"
  mkdir -p "$tmp_under_test"
  # `exec` inside the subshell replaces it with check-upstream.sh itself,
  # so `$!` names the script's own process. Without it, `$!` is the
  # `(cd ... && ...)` subshell: an untrapped TERM kills that subshell
  # outright (exit status 143 from being killed by the signal, not from
  # the script's own `exit 143`) while the script keeps running
  # underneath it, so the assertion below passes whether or not the
  # script's own trap works. TMPDIR is scoped to a directory this test
  # owns so the emptiness check below can only see temp files this run
  # created, mktemp honors it.
  (cd "$REPO" && TMPDIR="$tmp_under_test" PATH="$sig_bin:$PATH" exec bash "$FORK_DIR/check-upstream.sh") &
  pid=$!
  for _ in $(seq 1 50); do
    [ -f "$marker" ] && break
    sleep 0.1
  done
  [ -f "$marker" ]
  kill -TERM "$pid"
  status=0
  wait "$pid" || status=$?
  [ "$status" -eq 143 ]
  [ -z "$(find "$tmp_under_test" -mindepth 1)" ]
}

@test "a shared file that drifts from the fixture exits 1" {
  printf '\n// test-only marker\n' >>"$FORK_DIR/alert.go"
  run run_check
  [ "$status" -eq 1 ]
}

@test "a shared file deleted from the fork is reported as drift, not an internal error" {
  rm "$FORK_DIR/alert.go"
  run run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"drift: alert.go is listed as shared but is missing from the fork"* ]]
  [[ "$output" != *"internal:"* ]]
}

@test "a spoofed stub import using github_com instead of github.com is not silently normalized away, for each of the four rewrite patterns" {
  # One importing file per stub the normalize pass rewrites (diff_shared_files'
  # four `sed -e` patterns): fipstls only appears in boring.go; boring and cpu
  # both appear in cipher_suites.go; godebug appears in three files, of which
  # common.go is enough to exercise its pattern.
  local -a stubs=(fipstls boring cpu godebug)
  local -a files=(boring.go cipher_suites.go cipher_suites.go common.go)
  local i stub file escaped spoofed
  for i in "${!stubs[@]}"; do
    stub="${stubs[$i]}"
    file="${files[$i]}"
    escaped="github\\.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/${stub}"
    spoofed="github_com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/${stub}"
    sed -i "s#${escaped}#${spoofed}#" "$FORK_DIR/$file"
    run run_check
    [ "$status" -eq 1 ]
    [[ "$output" == *"$file"* ]]
    sed -i "s#${spoofed}#github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls/stubs/${stub}#" "$FORK_DIR/$file"
  done
}

@test "an edited fork-only file exits 1 via the manifest check" {
  sed -i 's/keyLen: 16,/keyLen: 32,/' "$FORK_DIR/cipher_suites_ccm.go"
  run run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"drift: cipher_suites_ccm.go"* ]]
}

@test "an unrecorded new file exits 4" {
  printf 'package gotls\n' >"$FORK_DIR/unrecorded.go"
  run run_check
  [ "$status" -eq 4 ]
  [[ "$output" == *"unrecorded: unrecorded.go"* ]]
}

@test "an unrecorded file nested below the top level is scanned, not skipped by a shallow walk" {
  mkdir -p "$FORK_DIR/stubs/deep"
  printf 'package boring\n' >"$FORK_DIR/stubs/deep/nested.go"
  run run_check
  [ "$status" -eq 4 ]
  [[ "$output" == *"unrecorded: stubs/deep/nested.go"* ]]
}

@test "a symlink under FORK_DIR is scanned, not skipped by -type f" {
  ln -s alert.go "$FORK_DIR/evil-link.go"
  run run_check
  [ "$status" -eq 4 ]
  [[ "$output" == *"unrecorded: evil-link.go"* ]]
}

@test "drift and an unrecorded file combine into exit 5 instead of one overwriting the other" {
  printf '\n// test-only marker\n' >>"$FORK_DIR/alert.go"
  printf 'package gotls\n' >"$FORK_DIR/unrecorded.go"
  run run_check
  [ "$status" -eq 5 ]
  [[ "$output" == *"test-only marker"* ]]
  [[ "$output" == *"unrecorded: unrecorded.go"* ]]
}

@test "a malformed manifest line (missing field) exits 1 with a distinct message" {
  printf 'fork-only\tstubs/godebug/godebug.go\n' >>"$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"malformed manifest line"* ]]
}

@test "a manifest with no final newline still hash-checks its last entry" {
  local last_path
  last_path="$(tail -1 "$FORK_DIR/upstream-manifest.sha256" | cut -f2)"
  printf '\n// test-only marker\n' >>"$FORK_DIR/$last_path"
  # Strip the manifest's own trailing newline: $(...) drops it, printf
  # writes the content back with none, so the last line (the one just
  # corrupted) is what `read`'s EOF-without-newline behavior is tested
  # against.
  printf '%s' "$(cat "$FORK_DIR/upstream-manifest.sha256")" >"$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"drift: $last_path content changed"* ]]
}

@test "a wrongly hashed entry appended with no final newline still hash-checks" {
  printf 'package gotls\n// new fork-only file\n' >"$FORK_DIR/no_newline_new.go"
  printf 'fork-only\tno_newline_new.go\tdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\t-\ttest: wrong hash, no trailing newline' \
    >>"$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"drift: no_newline_new.go content changed"* ]]
}

@test "a patched file whose recorded upstream hash still matches upstream exits 0" {
  local fork_hash upstream_hash
  fork_hash="$(sha256sum "$FORK_DIR/handshake_server.go" | cut -d' ' -f1)"
  upstream_hash="$(sha256sum "$UPSTREAM_FIXTURE/handshake_server.go" | cut -d' ' -f1)"
  printf 'patched\thandshake_server.go\t%s\t%s\ttest: pretend deliberate patch\n' \
    "$fork_hash" "$upstream_hash" >>"$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 0 ]
}

@test "a patched file whose fork content has actually diverged from upstream is exempt from the direct diff" {
  local fork_hash upstream_hash
  printf '\n// deliberate fork patch: diverges from upstream\n' >>"$FORK_DIR/handshake_server.go"
  fork_hash="$(sha256sum "$FORK_DIR/handshake_server.go" | cut -d' ' -f1)"
  upstream_hash="$(sha256sum "$UPSTREAM_FIXTURE/handshake_server.go" | cut -d' ' -f1)"
  printf 'patched\thandshake_server.go\t%s\t%s\ttest: pretend deliberate patch that diverges from upstream\n' \
    "$fork_hash" "$upstream_hash" >>"$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 0 ]
}

@test "a patched file reports drift once its recorded upstream hash no longer matches the pin" {
  local fork_hash
  fork_hash="$(sha256sum "$FORK_DIR/handshake_server.go" | cut -d' ' -f1)"
  printf 'patched\thandshake_server.go\t%s\tdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\ttest: pretend deliberate patch\n' \
    "$fork_hash" >>"$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"drift: upstream handshake_server.go changed since the patch was recorded"* ]]
}

@test "a patched entry with no recorded upstream_sha256 exits 2" {
  local fork_hash
  fork_hash="$(sha256sum "$FORK_DIR/handshake_server.go" | cut -d' ' -f1)"
  printf 'patched\thandshake_server.go\t%s\t-\ttest: pretend deliberate patch\n' \
    "$fork_hash" >>"$FORK_DIR/upstream-manifest.sha256"
  run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"no recorded upstream_sha256"* ]]
}

@test "each of find, awk, cut, mktemp, and timeout is checked before use" {
  local missing no_tool_bin t
  for missing in find awk cut mktemp timeout; do
    no_tool_bin="$WORK/no-$missing-bin"
    mkdir -p "$no_tool_bin"
    for t in bash sed diff sha256sum gofmt mkdir rm mktemp find awk cut timeout git; do
      [ "$t" = "$missing" ] && continue
      ln -sf "$(type -P "$t")" "$no_tool_bin/$t"
    done
    PATH="$no_tool_bin" run run_check
    [ "$status" -eq 2 ]
    [[ "$output" == *"required tool '$missing'"* ]]
  done
}

@test "a find that rejects -printf fails the walk instead of silently finding nothing" {
  local bsdfind_bin="$WORK/bsdfind-bin"
  mkdir -p "$bsdfind_bin"
  cat >"$bsdfind_bin/find" <<'BSDEOF'
#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "-printf" ]; then
    echo "find: -printf: unknown primary or operator" >&2
    exit 1
  fi
done
exec /usr/bin/find "$@"
BSDEOF
  chmod +x "$bsdfind_bin/find"
  printf 'package gotls\n' >"$FORK_DIR/unrecorded_m1.go"
  PATH="$bsdfind_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not walk"* ]]
}

@test "a failing awk reports an environment failure, not an unrecorded file" {
  local broken_awk_bin="$WORK/broken-awk-bin"
  mkdir -p "$broken_awk_bin"
  cat >"$broken_awk_bin/awk" <<'AWKEOF'
#!/usr/bin/env bash
echo "mock: awk is broken" >&2
exit 1
AWKEOF
  chmod +x "$broken_awk_bin/awk"
  PATH="$broken_awk_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"awk failed"* ]]
}

# The four cases below each fail an awk lookup at exactly one of the four
# manifest_type/manifest_upstream_sha call sites, so each is a control on
# its own call site's guard: reverting only that guard's error check (while
# leaving the other three intact) must turn the matching case red and no
# other. A single globally-broken awk (above) cannot tell the four call
# sites apart, since whichever call site runs first wins.

@test "an awk failure resolving alert.go's type on the first diff_shared_files pass exits 2" {
  # Fails only alert.go's very first lookup, then falls through to the real
  # awk: without the first-pass guard, the failure is swallowed by the
  # unchecked assignment and the second pass's own (untouched) guard never
  # sees a failing call to re-catch it, so this can only pass by the
  # first-pass guard itself firing.
  local mock_bin="$WORK/mock-awk-pass1-bin" marker="$WORK/mock-awk-pass1-fired"
  mkdir -p "$mock_bin"
  cat >"$mock_bin/awk" <<AWKEOF
#!/usr/bin/env bash
if [ "\$p" = "alert.go" ] && [ ! -e "$marker" ]; then
  : >"$marker"
  echo "mock: awk fails for alert.go's first lookup" >&2
  exit 1
fi
exec $(type -P awk) "\$@"
AWKEOF
  chmod +x "$mock_bin/awk"
  PATH="$mock_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not determine manifest entry type for 'alert.go' (awk failed)"* ]]
}

@test "an awk failure resolving alert.go's type on the second diff_shared_files pass exits 2" {
  local mock_bin="$WORK/mock-awk-pass2-bin" marker_dir="$WORK/mock-awk-pass2-seen"
  mkdir -p "$mock_bin" "$marker_dir"
  cat >"$mock_bin/awk" <<AWKEOF
#!/usr/bin/env bash
if [ "\$p" = "alert.go" ]; then
  marker="$marker_dir/alert.go"
  if [ -e "\$marker" ]; then
    echo "mock: awk fails for alert.go's second lookup" >&2
    exit 1
  fi
  : >"\$marker"
fi
exec $(type -P awk) "\$@"
AWKEOF
  chmod +x "$mock_bin/awk"
  PATH="$mock_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not determine manifest entry type for 'alert.go' (awk failed)"* ]]
}

@test "an awk failure resolving a patched file's recorded upstream hash exits 2" {
  local fork_hash mock_bin="$WORK/mock-awk-upstreamsha-bin"
  fork_hash="$(sha256sum "$FORK_DIR/handshake_server.go" | cut -d' ' -f1)"
  printf 'patched\thandshake_server.go\t%s\t%s\ttest: pretend deliberate patch\n' \
    "$fork_hash" "$(sha256sum "$UPSTREAM_FIXTURE/handshake_server.go" | cut -d' ' -f1)" \
    >>"$FORK_DIR/upstream-manifest.sha256"
  mkdir -p "$mock_bin"
  cat >"$mock_bin/awk" <<AWKEOF
#!/usr/bin/env bash
if [[ "\$2" == *'\$4'* ]]; then
  echo "mock: awk fails reading the recorded upstream hash" >&2
  exit 1
fi
exec $(type -P awk) "\$@"
AWKEOF
  chmod +x "$mock_bin/awk"
  PATH="$mock_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not read recorded upstream_sha256 for 'handshake_server.go' (awk failed)"* ]]
}

@test "an awk failure resolving an unrecorded file's type exits 2, not misreported as unrecorded" {
  local mock_bin="$WORK/mock-awk-unrecorded-bin"
  printf 'package gotls\n' >"$FORK_DIR/totally-unrecorded-marker.go"
  mkdir -p "$mock_bin"
  cat >"$mock_bin/awk" <<AWKEOF
#!/usr/bin/env bash
if [ "\$p" = "totally-unrecorded-marker.go" ]; then
  echo "mock: awk fails for the unrecorded marker" >&2
  exit 1
fi
exec $(type -P awk) "\$@"
AWKEOF
  chmod +x "$mock_bin/awk"
  PATH="$mock_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not determine manifest entry type for 'totally-unrecorded-marker.go' (awk failed)"* ]]
}

@test "a failing sha256sum hashing a manifest entry's fork file exits 2, not drift" {
  local first_path mock_bin="$WORK/mock-sha256sum-manifest-bin"
  first_path="$(grep -vE '^#|^$' "$FORK_DIR/upstream-manifest.sha256" | head -1 | cut -f2)"
  mkdir -p "$mock_bin"
  cat >"$mock_bin/sha256sum" <<'SHAEOF'
#!/usr/bin/env bash
echo "mock: sha256sum is broken" >&2
exit 1
SHAEOF
  chmod +x "$mock_bin/sha256sum"
  PATH="$mock_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not hash"* ]]
  [[ "$output" == *"$first_path"* ]]
}

@test "a diff that exits 2 comparing a shared file is an environment failure, not drift" {
  local broken_diff_bin="$WORK/broken-diff-bin"
  mkdir -p "$broken_diff_bin"
  cat >"$broken_diff_bin/diff" <<'DIFFEOF'
#!/usr/bin/env bash
echo "mock: diff is broken" >&2
exit 2
DIFFEOF
  chmod +x "$broken_diff_bin/diff"
  PATH="$broken_diff_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not compare"* ]]
}

@test "a failing sha256sum hashing a patched file's upstream copy exits 2, not drift" {
  # Fails only a path under the cloned upstream tree (what diff_shared_files
  # hashes at this call site), not the fork's own copy (what check_manifest
  # hashes for the same manifest entry): without this call site's own guard,
  # the failure is reported as drift instead of exit 2, and check_manifest's
  # unrelated guard on the fork's copy never runs to catch it.
  local fork_hash mock_bin="$WORK/mock-sha256sum-upstream-bin"
  fork_hash="$(sha256sum "$FORK_DIR/handshake_server.go" | cut -d' ' -f1)"
  printf 'patched\thandshake_server.go\t%s\t%s\ttest: pretend deliberate patch\n' \
    "$fork_hash" "$(sha256sum "$UPSTREAM_FIXTURE/handshake_server.go" | cut -d' ' -f1)" \
    >>"$FORK_DIR/upstream-manifest.sha256"
  mkdir -p "$mock_bin"
  cat >"$mock_bin/sha256sum" <<AWKEOF
#!/usr/bin/env bash
case "\$1" in
*/src/crypto/tls/*)
  echo "mock: sha256sum is broken" >&2
  exit 1
  ;;
esac
exec $(type -P sha256sum) "\$@"
AWKEOF
  chmod +x "$mock_bin/sha256sum"
  PATH="$mock_bin:$PATH" run run_check
  [ "$status" -eq 2 ]
  [[ "$output" == *"could not hash"* ]]
  [[ "$output" == *"handshake_server.go"* ]]
}

# gawk's `-v var=value` runs backslash-escape processing on value; an
# unrecognized escape (`\.`) is silently dropped, so a literal backslash in
# a filename can collapse onto a different, recorded manifest path. mawk
# does not collapse it, so the two awks disagree unless the comparison
# treats the path as a literal string throughout (see manifest_type).
@test "an unrecorded file named with a backslash under gawk exits 4, not silently matched" {
  command -v gawk >/dev/null 2>&1 || skip "gawk not installed"
  local gawk_bin="$WORK/gawk-only-bin" t
  mkdir -p "$gawk_bin"
  for t in bash sed diff sha256sum gofmt mkdir rm cp mktemp find cut timeout; do
    ln -sf "$(type -P "$t")" "$gawk_bin/$t"
  done
  ln -sf "$MOCKBIN/git" "$gawk_bin/git"
  ln -sf "$(type -P gawk)" "$gawk_bin/awk"
  printf 'package gotls\n' >"$FORK_DIR"/'cipher_suites_ccm\.go'
  PATH="$gawk_bin" run run_check
  [ "$status" -eq 4 ]
  [[ "$output" == *'unrecorded: cipher_suites_ccm\.go'* ]]
}

@test "an unrecorded file named with a backslash under mawk exits 4, not silently matched" {
  command -v mawk >/dev/null 2>&1 || skip "mawk not installed"
  local mawk_bin="$WORK/mawk-only-bin" t
  mkdir -p "$mawk_bin"
  for t in bash sed diff sha256sum gofmt mkdir rm cp mktemp find cut timeout; do
    ln -sf "$(type -P "$t")" "$mawk_bin/$t"
  done
  ln -sf "$MOCKBIN/git" "$mawk_bin/git"
  ln -sf "$(type -P mawk)" "$mawk_bin/awk"
  printf 'package gotls\n' >"$FORK_DIR"/'cipher_suites_ccm\.go'
  PATH="$mawk_bin" run run_check
  [ "$status" -eq 4 ]
  [[ "$output" == *'unrecorded: cipher_suites_ccm\.go'* ]]
}
