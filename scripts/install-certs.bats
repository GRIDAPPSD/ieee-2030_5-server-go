#!/usr/bin/env bats
# Smoke test for install-certs.sh (#656): the dry run writes nothing, a
# missing tool refuses (real, on this host: certutil and pk12util are
# genuinely absent, per the #656 research map), wrong privilege refuses in
# both directions, the trust-mode install/verify/remove round trip against
# scratch stores (never the real system trust store), and the device-mode
# permission convention, including a verify that can actually fail.
#
# Fixtures are minted per test with openssl so the suite needs no network
# access and never reads or writes the operator's real $HOME/tls or NSS DB.

setup() {
  SRC_DIR="$BATS_TEST_DIRNAME"
  WORK="$(mktemp -d)"
  CERT_DIR="$WORK/certdir"
  mkdir -p "$CERT_DIR"

  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -keyout "$WORK/ca.key" -out "$CERT_DIR/ca.crt" -days 2 -sha256 \
    -subj "/CN=install-certs test CA" >/dev/null 2>&1

  for name in admin server device; do
    openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
      -keyout "$CERT_DIR/$name.key" -out "$WORK/$name.csr" \
      -subj "/CN=install-certs test $name" >/dev/null 2>&1
    openssl x509 -req -in "$WORK/$name.csr" -CA "$CERT_DIR/ca.crt" -CAkey "$WORK/ca.key" \
      -CAcreateserial -out "$CERT_DIR/$name.crt" -days 2 -sha256 >/dev/null 2>&1
  done

  # A second, unrelated CA and a cert issued by it: proves a check that is
  # supposed to reject a foreign CA can actually reject one, rather than
  # only ever being exercised against the matching CA.
  UCA_DIR="$WORK/unrelated-ca"
  mkdir -p "$UCA_DIR"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -keyout "$UCA_DIR/uca.key" -out "$UCA_DIR/uca.crt" -days 2 -sha256 \
    -subj "/CN=unrelated test CA" >/dev/null 2>&1

  # A stub `id` that reports uid 0, so a test can exercise "wrong privilege"
  # in the as-root direction without real root. Off PATH by default: only
  # prepended for the single command that needs it, per test.
  ROOT_BIN="$WORK/root-bin"
  mkdir -p "$ROOT_BIN"
  cat >"$ROOT_BIN/id" <<'IDEOF'
#!/usr/bin/env bash
if [ "$1" = "-u" ]; then
  echo 0
  exit 0
fi
exec /usr/bin/id "$@"
IDEOF
  chmod +x "$ROOT_BIN/id"

  # Stub certutil/pk12util: real operator-mode NSS acceptance is
  # UNVERIFIED (#656, certutil and pk12util are genuinely absent on this
  # host), but the shape of the fix for items 1 and 3 (the trap firing
  # exactly once, and two distinct password files reaching the two
  # tools) is mechanical and does not depend on NSS itself. Off PATH by
  # default, like ROOT_BIN.
  NSS_STUB_BIN="$WORK/nss-stub-bin"
  mkdir -p "$NSS_STUB_BIN"
  NSS_STUB_LOG="$WORK/nss-stub.log"
  cat >"$NSS_STUB_BIN/certutil" <<'CERTUTILEOF'
#!/usr/bin/env bash
printf '%s\n' "certutil $*" >>"$NSS_STUB_LOG"
exit 0
CERTUTILEOF
  chmod +x "$NSS_STUB_BIN/certutil"
  cat >"$NSS_STUB_BIN/pk12util" <<'PK12EOF'
#!/usr/bin/env bash
printf '%s\n' "pk12util $*" >>"$NSS_STUB_LOG"
exit 0
PK12EOF
  chmod +x "$NSS_STUB_BIN/pk12util"

  # A second pk12util stub that fails, for the set -e abort path (item 1's
  # MEDIUM: key material surviving an unhappy exit).
  NSS_FAIL_BIN="$WORK/nss-fail-bin"
  mkdir -p "$NSS_FAIL_BIN"
  cp "$NSS_STUB_BIN/certutil" "$NSS_FAIL_BIN/certutil"
  cat >"$NSS_FAIL_BIN/pk12util" <<'PK12FAILEOF'
#!/usr/bin/env bash
echo "pk12util: stub failure" >&2
exit 1
PK12FAILEOF
  chmod +x "$NSS_FAIL_BIN/pk12util"

  # Scratch stand-ins for the two real system paths trust mode targets, so
  # the round-trip test below never touches /usr/local/share/ca-certificates
  # or /etc/ssl/certs/ca-certificates.crt.
  TRUST_ANCHOR_DIR="$WORK/trust-anchors"
  SYSTEM_CA_BUNDLE="$WORK/system-bundle.crt"
  mkdir -p "$TRUST_ANCHOR_DIR"
  : >"$SYSTEM_CA_BUNDLE"

  # A stand-in for update-ca-certificates: rebuilds the scratch bundle from
  # whatever *.crt files currently sit in the scratch anchor dir, the same
  # concatenation the real tool performs against the real paths (map,
  # mode 2).
  MOCK_UPDATE_BIN="$WORK/mock-update-bin"
  mkdir -p "$MOCK_UPDATE_BIN"
  cat >"$MOCK_UPDATE_BIN/update-ca-certificates" <<MOCKEOF
#!/usr/bin/env bash
set -euo pipefail
: >"$SYSTEM_CA_BUNDLE"
for f in "$TRUST_ANCHOR_DIR"/*.crt; do
  [ -e "\$f" ] || continue
  cat "\$f" >>"$SYSTEM_CA_BUNDLE"
done
MOCKEOF
  chmod +x "$MOCK_UPDATE_BIN/update-ca-certificates"

  DEVICE_DEST_DIR="$WORK/device-dest"
  NSS_DB_DIR="$WORK/nssdb"

  # A scratch TMPDIR under WORK, not the shared /tmp: lets a test assert
  # "nothing left behind" by listing a directory this suite owns and
  # teardown already removes, and matches what mktemp -d in the script
  # under test will actually use.
  SCRATCH_TMPDIR="$WORK/scratch-tmp"
  mkdir -p "$SCRATCH_TMPDIR"

  export PATH="$MOCK_UPDATE_BIN:$PATH"
  export CERT_DIR TRUST_ANCHOR_DIR SYSTEM_CA_BUNDLE DEVICE_DEST_DIR NSS_DB_DIR
  export ROOT_BIN NSS_STUB_BIN NSS_FAIL_BIN NSS_STUB_LOG UCA_DIR WORK
  export TMPDIR="$SCRATCH_TMPDIR"
  export TRUST_VERIFY_LEAF="$CERT_DIR/server.crt"
}

teardown() {
  rm -rf "$WORK"
}

run_install_certs() {
  "$SRC_DIR/install-certs.sh" "$@"
}

# Builds a PATH-like directory of symlinks to every executable under
# /usr/bin, /bin and /usr/local/bin except $1, via globs only (no find):
# lets a test make one real tool genuinely absent, rather than merely
# mocked to fail, so a "checks before writing" claim is proven against a
# real absence.
build_path_without() {
  local target="$1" out="$2" dir f name
  mkdir -p "$out"
  for dir in /usr/bin /bin /usr/local/bin; do
    [ -d "$dir" ] || continue
    for f in "$dir"/*; do
      [ -e "$f" ] || continue
      name="$(basename "$f")"
      [ "$name" = "$target" ] && continue
      [ -e "$out/$name" ] && continue
      ln -s "$f" "$out/$name" 2>/dev/null || true
    done
  done
}

@test "dry-run install writes nothing" {
  run run_install_certs device install --dry-run
  [ "$status" -eq 0 ]
  [ ! -e "$DEVICE_DEST_DIR" ]
}

@test "a missing tool refuses before writing anything" {
  # certutil and pk12util are genuinely absent from this host (the #656
  # research map, mode 1); this exercises the refusal for real, no mock.
  if command -v certutil >/dev/null 2>&1; then
    skip "certutil is installed on this host; this control no longer holds"
  fi
  run run_install_certs operator install
  [ "$status" -eq 3 ]
  [[ "$output" == *"certutil"* ]]
  [ ! -d "$NSS_DB_DIR" ]
}

@test "wrong privilege refuses: trust mode without root" {
  run run_install_certs trust install
  [ "$status" -eq 4 ]
  [[ "$output" == *"must run as root"* ]]
  [ -z "$(ls -A "$TRUST_ANCHOR_DIR")" ]
}

@test "wrong privilege refuses: operator and device modes as root" {
  PATH="$ROOT_BIN:$PATH" run run_install_certs operator install
  [ "$status" -eq 4 ]
  [[ "$output" == *"must NOT run as root"* ]]

  PATH="$ROOT_BIN:$PATH" run run_install_certs device install
  [ "$status" -eq 4 ]
  [[ "$output" == *"must NOT run as root"* ]]
  [ ! -e "$DEVICE_DEST_DIR" ]
}

@test "trust mode installs, verifies, removes, and stops verifying" {
  # Before: the leaf does not yet verify against the scratch system bundle.
  # This is the negative control that proves the positive result below is
  # not a check that always passes.
  run run_install_certs trust verify
  [ "$status" -eq 1 ]

  PATH="$ROOT_BIN:$PATH" run run_install_certs trust install
  [ "$status" -eq 0 ]

  run run_install_certs trust verify
  [ "$status" -eq 0 ]

  PATH="$ROOT_BIN:$PATH" run run_install_certs trust remove
  [ "$status" -eq 0 ]
  [[ "$output" == *"anchor removed from"* ]]
  [[ "$output" == *"no longer verifies"* ]]

  # After: removal must leave the leaf unable to verify again, not merely
  # report success.
  run run_install_certs trust verify
  [ "$status" -eq 1 ]
}

@test "device mode installs with the convention's permissions" {
  run run_install_certs device install
  [ "$status" -eq 0 ]
  [ "$(stat -c '%a' "$DEVICE_DEST_DIR")" = "700" ]
  [ "$(stat -c '%a' "$DEVICE_DEST_DIR/device.crt")" = "644" ]
  [ "$(stat -c '%a' "$DEVICE_DEST_DIR/device.key")" = "600" ]
  [ "$(stat -c '%a' "$DEVICE_DEST_DIR/ca.crt")" = "644" ]
}

@test "device verify fails when the installed key does not match the installed cert" {
  run run_install_certs device install
  [ "$status" -eq 0 ]
  # Swap in an unrelated key: proves the pubkey-match check can fail, not
  # only that it can pass.
  install -m 600 "$CERT_DIR/admin.key" "$DEVICE_DEST_DIR/device.key"
  run run_install_certs device verify
  [ "$status" -eq 1 ]
  [[ "$output" == *"does not match"* ]]
}

@test "device install refuses without DEVICE_DEST_DIR" {
  DEVICE_DEST_DIR="" run run_install_certs device install
  [ "$status" -eq 2 ]
  [[ "$output" == *"DEVICE_DEST_DIR"* ]]
}

@test "device remove deletes local files without claiming server-side revocation, and verify fails afterward" {
  run run_install_certs device install
  [ "$status" -eq 0 ]
  run run_install_certs device remove
  [ "$status" -eq 0 ]
  [[ "$output" == *"does NOT revoke"* ]]
  run run_install_certs device verify
  [ "$status" -eq 1 ]
}

# --------------------------------------------------- item 1: the temp trap

@test "operator install reports success on success, instead of an unbound-variable crash" {
  PATH="$NSS_STUB_BIN:$PATH" run run_install_certs operator install
  [ "$status" -eq 0 ]
  [[ "$output" != *"unbound variable"* ]]
  [[ "$output" == *"is present in"* ]]
}

@test "operator install leaves no workdir behind, on success or on a mid-export failure" {
  PATH="$NSS_STUB_BIN:$PATH" run run_install_certs operator install
  [ "$status" -eq 0 ]
  [ -z "$(ls -A "$TMPDIR")" ]

  NSS_DB_DIR="$WORK/nssdb-fail"
  PATH="$NSS_FAIL_BIN:$PATH" NSS_DB_DIR="$NSS_DB_DIR" run run_install_certs operator install
  [ "$status" -ne 0 ]
  # Before the fix this left operator.p12 (the private key bundle) and its
  # password file behind under TMPDIR, because a RETURN trap does not fire
  # on a set -e abort.
  [ -z "$(ls -A "$TMPDIR")" ]
}

# ---------------------------------------------- item 2: live verify -k

@test "device verify's live check does not pass -k, bounds the wait, and survives a curl failure under set -e" {
  run run_install_certs device install
  [ "$status" -eq 0 ]

  MOCK_CURL_BIN="$WORK/mock-curl-bin"
  mkdir -p "$MOCK_CURL_BIN"
  cat >"$MOCK_CURL_BIN/curl" <<'CURLEOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$CURL_ARGS_FILE"
exit 28
CURLEOF
  chmod +x "$MOCK_CURL_BIN/curl"
  CURL_ARGS_FILE="$WORK/curl-args"
  export CURL_ARGS_FILE

  DEVICE_VERIFY_URL="https://example.invalid/" PATH="$MOCK_CURL_BIN:$PATH" \
    run run_install_certs device verify
  # Before the fix, curl's nonzero exit aborted the script under set -e
  # before line :430 could report anything; status here is bats' own
  # report of that abort, and the message below is what proves the abort
  # no longer happens.
  [ "$status" -eq 1 ]
  [[ "$output" == *"request failed"* ]]

  run /usr/bin/grep -Fx -- "-k" "$CURL_ARGS_FILE"
  [ "$status" -ne 0 ]
  run /usr/bin/grep -Fx -- "--connect-timeout" "$CURL_ARGS_FILE"
  [ "$status" -eq 0 ]
  run /usr/bin/grep -Fx -- "--max-time" "$CURL_ARGS_FILE"
  [ "$status" -eq 0 ]
}

# ------------------------------------------- item 4: removal exit/message

@test "trust remove reports what happened even when the confirming verify cannot run" {
  PATH="$ROOT_BIN:$PATH" run run_install_certs trust install
  [ "$status" -eq 0 ]

  # The confirming verify's own precondition (its leaf file) is missing,
  # so before the fix this ran the removal, then exited 2 with the
  # message discarded, indistinguishable from "removal did not run".
  TRUST_VERIFY_LEAF="$WORK/no-such-leaf.crt" PATH="$ROOT_BIN:$PATH" \
    run run_install_certs trust remove
  [ "$status" -eq 2 ]
  [[ "$output" == *"anchor removed from"* ]]
  [[ "$output" == *"could not run"* ]]
  [ -z "$(ls -A "$TRUST_ANCHOR_DIR")" ]
}

@test "device remove reports what happened even when the confirming verify cannot run" {
  run run_install_certs device install
  [ "$status" -eq 0 ]

  NO_OPENSSL_BIN="$WORK/no-openssl-bin"
  build_path_without openssl "$NO_OPENSSL_BIN"
  PATH="$NO_OPENSSL_BIN" run run_install_certs device remove
  [ "$status" -eq 3 ]
  [[ "$output" == *"local files removed"* ]]
  [[ "$output" == *"could not run"* ]]
  [ ! -e "$DEVICE_DEST_DIR/device.crt" ]
}

@test "operator remove reports certutil -D ran and shows the still-present verify message, not just its own final line" {
  # NSS acceptance itself is UNVERIFIED (#656: certutil and pk12util are
  # not installed here); this stubs certutil -L to always report the
  # nickname present, so the "still present" branch is reachable without
  # a real NSS DB, and proves the message operator_verify itself prints
  # (previously discarded by >/dev/null 2>&1) now reaches the operator.
  STILL_PRESENT_BIN="$WORK/certutil-still-present"
  mkdir -p "$STILL_PRESENT_BIN"
  cat >"$STILL_PRESENT_BIN/certutil" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "$STILL_PRESENT_BIN/certutil"

  PATH="$STILL_PRESENT_BIN:$PATH" run run_install_certs operator remove
  [ "$status" -eq 1 ]
  [[ "$output" == *"certutil -D ran for"* ]]
  [[ "$output" == *"is present in"* ]]
  [[ "$output" == *"is still present"*"after remove"* ]]
}

@test "operator remove reports the removal succeeded when the absence check confirms it" {
  ABSENT_BIN="$WORK/certutil-absent"
  mkdir -p "$ABSENT_BIN"
  cat >"$ABSENT_BIN/certutil" <<'EOF'
#!/usr/bin/env bash
case "$1" in
-D) exit 0 ;;
-L) exit 1 ;;
esac
exit 0
EOF
  chmod +x "$ABSENT_BIN/certutil"

  PATH="$ABSENT_BIN:$PATH" run run_install_certs operator remove
  [ "$status" -eq 0 ]
  [[ "$output" == *"certutil -D ran for"* ]]
  [[ "$output" == *"'sep2-admin' removed from"* ]]
}

# --------------------------------------- item 5: unchecked input, no --

@test "trust install and remove refuse a TRUST_ANCHOR_NAME that is not a bare filename" {
  TRUST_ANCHOR_NAME="../escaped.crt" run run_install_certs trust install
  [ "$status" -eq 2 ]
  [[ "$output" == *"bare filename"* ]]

  TRUST_ANCHOR_NAME="../escaped.crt" run run_install_certs trust remove
  [ "$status" -eq 2 ]
  [[ "$output" == *"bare filename"* ]]
}

@test "trust remove cannot delete a file outside the anchor directory via TRUST_ANCHOR_NAME" {
  OUTSIDE="$WORK/outside-victim.crt"
  echo "do not delete me" >"$OUTSIDE"
  TRUST_ANCHOR_NAME="../outside-victim.crt" PATH="$ROOT_BIN:$PATH" \
    run run_install_certs trust remove
  [ "$status" -eq 2 ]
  [ -f "$OUTSIDE" ]
}

@test "device install treats a dash-prefixed --dest value as a path, not an option" {
  cd "$WORK"
  run run_install_certs device install --dest "--looks-like-an-option"
  [ "$status" -eq 0 ]
  [ -d "$WORK/--looks-like-an-option" ]
  [ "$(stat -c '%a' -- "$WORK/--looks-like-an-option")" = "700" ]
}

@test "the Makefile recipe does not let DEST inject a shell command" {
  MARKER="$WORK/injected.txt"
  run make -C "$SRC_DIR/.." install-certs MODE=device VERB=install \
    "DEST=$WORK/dev-inj; touch $MARKER; echo"
  [ ! -e "$MARKER" ]
}

# --------------------------------- item 6: device destroy / verify scope

@test "device remove refuses to delete material it did not install" {
  mkdir -p "$DEVICE_DEST_DIR"
  install -m 644 "$CERT_DIR/ca.crt" "$DEVICE_DEST_DIR/ca.crt"
  run run_install_certs device remove
  [ "$status" -eq 2 ]
  [[ "$output" == *"no install manifest"* ]]
  [ -f "$DEVICE_DEST_DIR/ca.crt" ]
}

@test "device verify fails when the installed CA does not chain to the installed cert" {
  run run_install_certs device install
  [ "$status" -eq 0 ]
  # An unrelated CA, not the one that issued device.crt: proves the check
  # can fail, not only that a real chain passes it.
  install -m 644 "$UCA_DIR/uca.crt" "$DEVICE_DEST_DIR/ca.crt"
  run run_install_certs device verify
  [ "$status" -eq 1 ]
  [[ "$output" == *"does not verify against the installed CA"* ]]
}

@test "device verify fails when the installed CA file is empty" {
  run run_install_certs device install
  [ "$status" -eq 0 ]
  : >"$DEVICE_DEST_DIR/ca.crt"
  run run_install_certs device verify
  [ "$status" -eq 1 ]
}

@test "device install checks for openssl before writing any file" {
  NO_OPENSSL_BIN="$WORK/no-openssl-bin"
  build_path_without openssl "$NO_OPENSSL_BIN"
  PATH="$NO_OPENSSL_BIN" run run_install_certs device install
  [ "$status" -eq 3 ]
  [[ "$output" == *"openssl"* ]]
  [ ! -e "$DEVICE_DEST_DIR" ]
}
