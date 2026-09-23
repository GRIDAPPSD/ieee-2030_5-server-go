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

  export PATH="$MOCK_UPDATE_BIN:$PATH"
  export CERT_DIR TRUST_ANCHOR_DIR SYSTEM_CA_BUNDLE DEVICE_DEST_DIR NSS_DB_DIR ROOT_BIN WORK
  export TRUST_VERIFY_LEAF="$CERT_DIR/server.crt"
}

teardown() {
  rm -rf "$WORK"
}

run_install_certs() {
  "$SRC_DIR/install-certs.sh" "$@"
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
