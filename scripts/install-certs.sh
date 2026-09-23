#!/usr/bin/env bash
# install-certs.sh - install, verify, or remove IEEE 2030.5 certificate
# material in one of three destinations, per GRIDAPPSD/ieee-2030_5-server-go#656.
#
# Invoked directly or from `make install-certs-<mode>` (Make exports the
# mode-specific env vars; the script does the work, the same split
# new-device.sh uses).
#
# Modes (mirrors the research map for #656):
#   operator  an admin-auth client certificate into this host's browser
#             certificate stores (NSS shared DB). Needs no root; must NOT
#             run as root.
#   trust     the serving CA into the system trust store, via
#             update-ca-certificates. The one mode that needs root; must
#             run as root.
#   device    a device cert/key/CA onto a client host's plain-file
#             convention (cert 644, key 600, dir 700). Needs no root; must
#             NOT run as root.
#
# Verbs: install, verify, remove. Every install ends by running its own
# verify; every remove ends by running its own verify and expecting it to
# fail. `verify` never writes, so --dry-run changes nothing there beyond
# what a normal verify already does; on install and remove, --dry-run
# prints the plan (paths, tool presence, the verify command) and returns
# before any write.
#
# Common env:
#   CERT_DIR        source material (default: SEP2_CERT_DIR, else $HOME/tls,
#                    same default chain as `make certs` and new-device.sh, #598)
#
# Mode operator:
#   NSS_DB_DIR        target NSS DB (default $HOME/.pki/nssdb)
#   OPERATOR_CERT     default $CERT_DIR/admin.crt
#   OPERATOR_KEY      default $CERT_DIR/admin.key
#   OPERATOR_CA       default $CERT_DIR/ca.crt
#   OPERATOR_NICKNAME default sep2-admin
#   OPERATOR_P12_LEGACY  when "1", export the PKCS#12 bundle with -legacy
#                     (RC2/3DES) instead of OpenSSL 3's modern default
#                     (AES-256-CBC/PBKDF2). Which mode this host's NSS
#                     needs is UNVERIFIED (#656): certutil and pk12util are
#                     both absent here, so neither mode has been imported
#                     against a real NSS DB. Defaults to modern.
#   OPERATOR_NSS_PASSFILE  path to a file holding the NSS DB's own master
#                     password, for a pre-existing DB that has one. Unset
#                     by default, which authenticates with an empty
#                     password: the no-master-password convention
#                     Chrome/Chromium's own NSS DB uses. This is separate
#                     from the PKCS#12 export password, which is always a
#                     fresh one-time secret: the two are not the same
#                     value (INFERRED, #656: certutil and pk12util are
#                     absent here, so this is reasoned from NSS's
#                     documented password handling, not observed).
#
# Mode trust:
#   TRUST_ANCHOR_DIR   default /usr/local/share/ca-certificates
#   SYSTEM_CA_BUNDLE   default /etc/ssl/certs/ca-certificates.crt
#   TRUST_CA_SRC       default $CERT_DIR/ca.crt
#   TRUST_ANCHOR_NAME  default ieee-2030_5-dev-ca.crt
#   TRUST_VERIFY_LEAF  default $CERT_DIR/server.crt (the leaf verify checks)
#
# Mode device:
#   DEVICE_DEST_DIR   required (or --dest DIR); no default. The 2030.5
#                     client's own default search path lives in
#                     ieee-2030_5-client-go, outside this script's scope,
#                     so this refuses rather than guess (#656 map, mode 3).
#   DEVICE_SRC_CERT   default $CERT_DIR/device.crt
#   DEVICE_SRC_KEY    default $CERT_DIR/device.key
#   DEVICE_SRC_CA     default $CERT_DIR/ca.crt
#   DEVICE_VERIFY_URL optional. When set, verify also makes a live mTLS
#                     request through curl, with server certificate
#                     verification on (curl's -k is never used: that
#                     disabled it entirely, so any server, impostor
#                     included, verified). When unset, verify checks that
#                     the installed key's derived public key matches the
#                     installed cert, and that the cert chains to the
#                     installed CA (proves the set is internally coherent,
#                     not that the server currently admits it).
#
# Exit codes:
#   0  success
#   1  a verify check ran and found the material not usable (or, for
#      remove, found it still usable)
#   2  usage or precondition failure: bad args, missing required input,
#      missing source material, missing target store
#   3  a required external tool is not installed
#   4  the invoking privilege is wrong for this mode
#
# This script never prints, logs, or copies a private key outside the
# destination the invoked mode names: it reads keys only to derive a
# public key for a match check, and it derives export passwords with
# openssl rand into a private temp file removed by an EXIT/INT/TERM trap
# that covers both the success path and every abort, rather than a
# literal in the script.

set -euo pipefail

PROG="$(basename "$0")"
DRY_RUN=0

# Not local to operator_install: a trap fired by a set -e abort from inside
# a function runs after bash has already torn down that function's own
# locals, so an EXIT trap referencing a local workdir reports it unbound
# instead of cleaning it up. Script-scope avoids that; nothing else reads
# this variable.
workdir=""

err() { printf 'ERROR: %s\n' "$*" >&2; }
info() { printf '%s\n' "$*"; }

usage() {
  cat <<EOF
Usage: $PROG <mode> <verb> [--dry-run] [--dest DIR]

Modes: operator | trust | device
Verbs: install | verify | remove

  --dry-run   print the plan (paths, tools, the verify command) for
              install or remove, and write nothing. verify never writes,
              so this changes nothing further there.
  --dest DIR  shorthand for DEVICE_DEST_DIR (device mode only)

Examples:
  $PROG trust install
  $PROG device install --dest /home/operator/.sep2/device
  $PROG operator verify
EOF
}

require_tool() {
  local tool="$1" package="$2"
  if ! command -v "$tool" >/dev/null 2>&1; then
    err "required tool '$tool' is not installed (package: $package)"
    exit 3
  fi
}

tool_present() {
  command -v "$1" >/dev/null 2>&1
}

# Reads privilege through `id -u` rather than the bash builtin $EUID, so a
# bats suite can stub privilege by prepending a fake `id` to PATH: $EUID is
# a read-only shell variable and cannot be overridden without real root.
effective_uid() {
  id -u
}

require_root() {
  local mode="$1"
  if [[ "$(effective_uid)" != "0" ]]; then
    err "mode '$mode' must run as root: it writes to a root-owned system store"
    exit 4
  fi
}

require_not_root() {
  local mode="$1"
  if [[ "$(effective_uid)" == "0" ]]; then
    err "mode '$mode' must NOT run as root: a root-owned entry under another user's \$HOME is unusable by that user's own processes, and fails closed later rather than loudly here"
    exit 4
  fi
}

require_file() {
  local path="$1" what="$2"
  if [[ ! -f "$path" ]]; then
    err "$what not found: $path"
    exit 2
  fi
}

# check_perm stats the path AFTER writing and compares against the wanted
# mode, rather than trusting that chmod (or install -m, under whatever
# umask was in effect) landed the mode it asked for.
check_perm() {
  local path="$1" want="$2" got
  got="$(stat -c '%a' -- "$path")"
  if [[ "$got" != "$want" ]]; then
    err "$path has mode $got after writing, expected $want"
    exit 1
  fi
}

# require_bare_filename rejects a caller-supplied name that is not a plain
# filename: a path separator lets the name walk out of the directory it is
# joined into (TRUST_ANCHOR_NAME concatenated into an install/rm target),
# and a leading '-' lets it be read as an option by the tool it reaches.
require_bare_filename() {
  local name="$1" what="$2"
  case "$name" in
  '' | . | .. | */*)
    err "$what must be a bare filename with no path separators: '$name'"
    exit 2
    ;;
  -*)
    err "$what must not begin with '-': '$name'"
    exit 2
    ;;
  esac
}

resolve_cert_dir() {
  if [[ -n "${CERT_DIR:-}" ]]; then
    return 0
  elif [[ -n "${SEP2_CERT_DIR:-}" ]]; then
    CERT_DIR="$SEP2_CERT_DIR"
  elif [[ -n "${HOME:-}" ]]; then
    CERT_DIR="$HOME/tls"
  else
    err "CERT_DIR is unset, SEP2_CERT_DIR is unset, and HOME could not be determined"
    exit 2
  fi
}

# ---------------------------------------------------------------- operator

operator_defaults() {
  NSS_DB_DIR="${NSS_DB_DIR:-$HOME/.pki/nssdb}"
  OPERATOR_CERT="${OPERATOR_CERT:-$CERT_DIR/admin.crt}"
  OPERATOR_KEY="${OPERATOR_KEY:-$CERT_DIR/admin.key}"
  OPERATOR_CA="${OPERATOR_CA:-$CERT_DIR/ca.crt}"
  OPERATOR_NICKNAME="${OPERATOR_NICKNAME:-sep2-admin}"
}

operator_plan() {
  local db_state tool_state
  db_state="absent"
  [[ -d "$NSS_DB_DIR" ]] && db_state="present"
  tool_state="present"
  { tool_present certutil && tool_present pk12util; } || tool_state="absent (need certutil and pk12util, package libnss3-tools)"
  info "mode: operator"
  info "  NSS DB: $NSS_DB_DIR ($db_state)"
  info "  tools: $tool_state"
  info "  source: $OPERATOR_CERT, $OPERATOR_KEY, $OPERATOR_CA"
  info "  would build a PKCS#12 bundle and import it as nickname '$OPERATOR_NICKNAME'"
  info "  verify command: certutil -L -d sql:$NSS_DB_DIR -n $OPERATOR_NICKNAME"
}

operator_install() {
  operator_defaults
  if [[ "$DRY_RUN" == "1" ]]; then
    operator_plan
    return 0
  fi
  require_not_root operator
  require_tool certutil libnss3-tools
  require_tool pk12util libnss3-tools
  require_tool openssl openssl
  require_file "$OPERATOR_CERT" "operator certificate"
  require_file "$OPERATOR_KEY" "operator key"
  require_file "$OPERATOR_CA" "operator CA"

  local nss_passfile p12_passfile p12file legacy_flag=()
  workdir="$(mktemp -d)"
  # An EXIT/INT/TERM trap, not RETURN: RETURN fires again on every later
  # function return in the whole call stack (including main's own), which
  # is fatal under set -u once this scope is gone; and RETURN never fires
  # at all on a set -e abort, which is exactly the path that leaves the
  # bundle and its password on disk. The trap is cleared once the workdir
  # is removed on the normal path, below.
  trap 'rm -rf "$workdir"' EXIT INT TERM

  # Two distinct secrets, not one reused: the DB's own master password
  # (empty by default, matching the no-master-password convention
  # Chrome/Chromium's NSS DB uses; OPERATOR_NSS_PASSFILE overrides it for a
  # pre-existing protected DB) and the PKCS#12 bundle's one-time export
  # password. Reusing one random value for both, then deleting it, left a
  # freshly created DB with a password nobody holds (INFERRED, #656).
  nss_passfile="${OPERATOR_NSS_PASSFILE:-$workdir/nss.pass}"
  [[ -f "$nss_passfile" ]] || : >"$nss_passfile"
  p12_passfile="$workdir/p12.pass"
  p12file="$workdir/operator.p12"
  ( umask 077 && openssl rand -base64 24 >"$p12_passfile" )
  [[ "${OPERATOR_P12_LEGACY:-}" == "1" ]] && legacy_flag=(-legacy)

  openssl pkcs12 -export "${legacy_flag[@]}" \
    -in "$OPERATOR_CERT" -inkey "$OPERATOR_KEY" -certfile "$OPERATOR_CA" \
    -name "$OPERATOR_NICKNAME" -passout "file:$p12_passfile" -out "$p12file"

  if [[ ! -d "$NSS_DB_DIR" ]]; then
    install -d -m 700 -- "$NSS_DB_DIR"
    certutil -N -d "sql:$NSS_DB_DIR" -f "$nss_passfile"
  fi
  pk12util -i "$p12file" -d "sql:$NSS_DB_DIR" -k "$nss_passfile" -w "$p12_passfile"

  rm -rf "$workdir"
  workdir=""
  trap - EXIT INT TERM

  operator_verify
}

operator_verify() {
  operator_defaults
  if [[ "$DRY_RUN" == "1" ]]; then
    operator_plan
    return 0
  fi
  require_tool certutil libnss3-tools
  if certutil -L -d "sql:$NSS_DB_DIR" -n "$OPERATOR_NICKNAME" >/dev/null 2>&1; then
    info "operator: '$OPERATOR_NICKNAME' is present in $NSS_DB_DIR"
    return 0
  fi
  err "operator: '$OPERATOR_NICKNAME' is not present in $NSS_DB_DIR"
  return 1
}

operator_remove() {
  operator_defaults
  if [[ "$DRY_RUN" == "1" ]]; then
    info "mode: operator (remove)"
    info "  would run: certutil -D -d sql:$NSS_DB_DIR -n $OPERATOR_NICKNAME"
    return 0
  fi
  require_not_root operator
  require_tool certutil libnss3-tools
  # -f is only meaningful when OPERATOR_NSS_PASSFILE names a real DB
  # password; certutil -D does not require one for an empty-password DB
  # (INFERRED, #656: reasoned from NSS's documented password handling,
  # certutil is absent here so this has not been run against a real DB).
  if [[ -n "${OPERATOR_NSS_PASSFILE:-}" ]]; then
    certutil -D -d "sql:$NSS_DB_DIR" -n "$OPERATOR_NICKNAME" -f "$OPERATOR_NSS_PASSFILE"
  else
    certutil -D -d "sql:$NSS_DB_DIR" -n "$OPERATOR_NICKNAME"
  fi
  if operator_verify >/dev/null 2>&1; then
    err "operator: '$OPERATOR_NICKNAME' is still present in $NSS_DB_DIR after remove"
    return 1
  fi
  info "operator: '$OPERATOR_NICKNAME' removed from $NSS_DB_DIR"
}

# ------------------------------------------------------------------ trust

trust_defaults() {
  TRUST_ANCHOR_DIR="${TRUST_ANCHOR_DIR:-/usr/local/share/ca-certificates}"
  SYSTEM_CA_BUNDLE="${SYSTEM_CA_BUNDLE:-/etc/ssl/certs/ca-certificates.crt}"
  TRUST_CA_SRC="${TRUST_CA_SRC:-$CERT_DIR/ca.crt}"
  TRUST_ANCHOR_NAME="${TRUST_ANCHOR_NAME:-ieee-2030_5-dev-ca.crt}"
  TRUST_VERIFY_LEAF="${TRUST_VERIFY_LEAF:-$CERT_DIR/server.crt}"
}

trust_plan() {
  local dir_state tool_state
  dir_state="absent"
  [[ -d "$TRUST_ANCHOR_DIR" ]] && dir_state="present"
  tool_state="present"
  tool_present update-ca-certificates || tool_state="absent (package: ca-certificates)"
  info "mode: trust"
  info "  anchor dir: $TRUST_ANCHOR_DIR ($dir_state)"
  info "  tool: $tool_state"
  info "  would copy $TRUST_CA_SRC to $TRUST_ANCHOR_DIR/$TRUST_ANCHOR_NAME (mode 644), then run update-ca-certificates"
  info "  verify command: openssl verify -CAfile $SYSTEM_CA_BUNDLE $TRUST_VERIFY_LEAF"
}

trust_install() {
  trust_defaults
  require_bare_filename "$TRUST_ANCHOR_NAME" "TRUST_ANCHOR_NAME"
  if [[ "$DRY_RUN" == "1" ]]; then
    trust_plan
    return 0
  fi
  require_root trust
  require_tool update-ca-certificates ca-certificates
  require_file "$TRUST_CA_SRC" "trust CA"
  [[ -d "$TRUST_ANCHOR_DIR" ]] || { err "trust anchor directory does not exist: $TRUST_ANCHOR_DIR"; exit 2; }

  local dest="$TRUST_ANCHOR_DIR/$TRUST_ANCHOR_NAME"
  install -m 644 -- "$TRUST_CA_SRC" "$dest"
  check_perm "$dest" 644
  update-ca-certificates

  trust_verify
}

trust_verify() {
  trust_defaults
  if [[ "$DRY_RUN" == "1" ]]; then
    trust_plan
    return 0
  fi
  require_tool openssl openssl
  require_file "$TRUST_VERIFY_LEAF" "trust verify leaf"
  if openssl verify -CAfile "$SYSTEM_CA_BUNDLE" "$TRUST_VERIFY_LEAF" >/dev/null 2>&1; then
    info "trust: $TRUST_VERIFY_LEAF verifies against $SYSTEM_CA_BUNDLE"
    return 0
  fi
  err "trust: $TRUST_VERIFY_LEAF does not verify against $SYSTEM_CA_BUNDLE"
  return 1
}

trust_remove() {
  trust_defaults
  require_bare_filename "$TRUST_ANCHOR_NAME" "TRUST_ANCHOR_NAME"
  if [[ "$DRY_RUN" == "1" ]]; then
    info "mode: trust (remove)"
    info "  would delete $TRUST_ANCHOR_DIR/$TRUST_ANCHOR_NAME, then run update-ca-certificates"
    return 0
  fi
  require_root trust
  require_tool update-ca-certificates ca-certificates
  rm -f -- "$TRUST_ANCHOR_DIR/$TRUST_ANCHOR_NAME"
  update-ca-certificates
  info "trust: anchor removed from $TRUST_ANCHOR_DIR"

  # Run in a subshell: trust_verify's own preconditions (require_tool,
  # require_file) call exit, not return, so calling it directly here would
  # terminate the whole script with its message discarded by the redirect
  # this used to carry. A subshell confines that exit to itself and lets
  # the real exit status, and the message, reach the operator.
  local verify_status=0
  ( trust_verify ) || verify_status=$?
  case "$verify_status" in
  0)
    err "trust: $TRUST_VERIFY_LEAF still verifies against $SYSTEM_CA_BUNDLE after remove"
    return 1
    ;;
  1)
    info "trust: $TRUST_VERIFY_LEAF no longer verifies"
    return 0
    ;;
  *)
    err "trust: anchor removed, but the verify step that would confirm it could not run (exit $verify_status)"
    return "$verify_status"
    ;;
  esac
}

# ----------------------------------------------------------------- device

# Recorded at install time and consulted at remove time, so remove only
# ever deletes files this script itself wrote to DEVICE_DEST_DIR: a
# destination that already held unrelated or hand-placed material (the
# same directory as CERT_DIR, by misconfiguration; a manually copied CA)
# keeps that material rather than losing it to a fixed-filename rm -f.
DEVICE_MANIFEST_NAME=".install-certs.manifest"

device_defaults() {
  DEVICE_SRC_CERT="${DEVICE_SRC_CERT:-$CERT_DIR/device.crt}"
  DEVICE_SRC_KEY="${DEVICE_SRC_KEY:-$CERT_DIR/device.key}"
  DEVICE_SRC_CA="${DEVICE_SRC_CA:-$CERT_DIR/ca.crt}"
}

require_device_dest() {
  if [[ -z "${DEVICE_DEST_DIR:-}" ]]; then
    err "DEVICE_DEST_DIR (or --dest DIR) is required: the 2030.5 client's own default cert-file path is not established for this installer (see #656's research map, mode 3), so this refuses to guess it"
    exit 2
  fi
  # Anchored to an absolute path so a relative destination that happens to
  # start with '-' (a plain typo, or --dest '-foo') is never mistaken for
  # an option by a tool downstream: every $DEVICE_DEST_DIR/... path built
  # from here on starts with '/', never '-'. A -- guard on each individual
  # call is not enough, because not every tool's own arg parser (openssl
  # verify's, notably) honors --.
  if [[ "$DEVICE_DEST_DIR" != /* ]]; then
    DEVICE_DEST_DIR="$PWD/$DEVICE_DEST_DIR"
  fi
}

device_plan() {
  device_defaults
  local dest_state tool_state verify_line
  dest_state="absent"
  [[ -n "${DEVICE_DEST_DIR:-}" && -d "${DEVICE_DEST_DIR:-}" ]] && dest_state="present"
  tool_state="present"
  tool_present openssl || tool_state="absent (package: openssl)"
  info "mode: device"
  info "  dest dir: ${DEVICE_DEST_DIR:-<unset, required>} ($dest_state)"
  info "  tool: $tool_state"
  info "  source: $DEVICE_SRC_CERT, $DEVICE_SRC_KEY, $DEVICE_SRC_CA"
  info "  would copy device.crt (644), device.key (600), ca.crt (644) into the dest dir (mode 700), and record them in $DEVICE_MANIFEST_NAME"
  verify_line="openssl pubkey match between the installed cert and key, and a chain check against the installed CA"
  [[ -n "${DEVICE_VERIFY_URL:-}" ]] && verify_line="$verify_line, plus curl --cacert/--cert/--key against $DEVICE_VERIFY_URL"
  info "  verify: $verify_line"
}

device_install() {
  if [[ "$DRY_RUN" == "1" ]]; then
    device_plan
    return 0
  fi
  require_not_root device
  require_device_dest
  device_defaults
  # Checked before any write, the same order operator mode already uses:
  # writing cert, key and CA and only then discovering openssl is missing
  # (inside device_verify) left key material on disk for a refusal.
  require_tool openssl openssl
  require_file "$DEVICE_SRC_CERT" "device certificate"
  require_file "$DEVICE_SRC_KEY" "device key"
  require_file "$DEVICE_SRC_CA" "device CA"

  install -d -m 700 -- "$DEVICE_DEST_DIR"
  check_perm "$DEVICE_DEST_DIR" 700
  install -m 644 -- "$DEVICE_SRC_CERT" "$DEVICE_DEST_DIR/device.crt"
  check_perm "$DEVICE_DEST_DIR/device.crt" 644
  install -m 600 -- "$DEVICE_SRC_KEY" "$DEVICE_DEST_DIR/device.key"
  check_perm "$DEVICE_DEST_DIR/device.key" 600
  install -m 644 -- "$DEVICE_SRC_CA" "$DEVICE_DEST_DIR/ca.crt"
  check_perm "$DEVICE_DEST_DIR/ca.crt" 644
  ( umask 077 && printf 'device.crt\ndevice.key\nca.crt\n' >"$DEVICE_DEST_DIR/$DEVICE_MANIFEST_NAME" )

  device_verify
}

device_verify() {
  if [[ "$DRY_RUN" == "1" ]]; then
    device_plan
    return 0
  fi
  require_device_dest
  require_tool openssl openssl
  local cert="$DEVICE_DEST_DIR/device.crt" key="$DEVICE_DEST_DIR/device.key" ca="$DEVICE_DEST_DIR/ca.crt"
  if [[ ! -f "$cert" || ! -f "$key" || ! -f "$ca" ]]; then
    err "device: $cert, $key or $ca not found in $DEVICE_DEST_DIR"
    return 1
  fi

  # openssl pkey -pubout derives ONLY the public key from the private key
  # file; it never prints private key material. This proves the pair is
  # usable (works for both RSA and EC keys, unlike an RSA-only modulus
  # check) without ever reading key bytes onto the terminal or into a log.
  local cert_pub key_pub
  cert_pub="$(openssl x509 -in "$cert" -noout -pubkey | openssl sha256)"
  key_pub="$(openssl pkey -in "$key" -pubout | openssl sha256)"
  if [[ "$cert_pub" != "$key_pub" ]]; then
    err "device: $key does not match the public key in $cert"
    return 1
  fi

  # The pubkey match alone says nothing about the installed CA: an
  # unrelated or empty ca.crt passed here before. openssl verify actually
  # chains the cert to it.
  if ! openssl verify -CAfile "$ca" "$cert" >/dev/null 2>&1; then
    err "device: $cert does not verify against the installed CA $ca"
    return 1
  fi

  if [[ -n "${DEVICE_VERIFY_URL:-}" ]]; then
    require_tool curl curl
    local code curl_status=0
    # No -k: the whole point of this check is that the server's certificate
    # chains to the CA just installed, so disabling that check made it pass
    # against any server, impostor included. --connect-timeout/--max-time
    # bound the wait so a install/verify run cannot hang indefinitely on an
    # unreachable endpoint.
    code="$(curl -s --cacert "$ca" --cert "$cert" --key "$key" \
      --connect-timeout 10 --max-time 30 \
      -o /dev/null -w '%{http_code}' "$DEVICE_VERIFY_URL")" || curl_status=$?
    if [[ "$curl_status" -ne 0 ]]; then
      err "device: $DEVICE_VERIFY_URL request failed before a response was received (curl exit $curl_status)"
      return 1
    fi
    if [[ "$code" != "200" ]]; then
      err "device: $DEVICE_VERIFY_URL returned $code, not 200"
      return 1
    fi
    info "device: cert/key pair matches and $DEVICE_VERIFY_URL admitted the request (200)"
    return 0
  fi

  info "device: cert/key pair in $DEVICE_DEST_DIR matches and verifies against $ca (no DEVICE_VERIFY_URL set, protocol admission not checked)"
}

device_remove() {
  if [[ "$DRY_RUN" == "1" ]]; then
    info "mode: device (remove)"
    info "  would delete device.crt, device.key, ca.crt from ${DEVICE_DEST_DIR:-<unset, required>}, if this script's own install manifest is present there"
    return 0
  fi
  require_not_root device
  require_device_dest
  local manifest="$DEVICE_DEST_DIR/$DEVICE_MANIFEST_NAME"
  if [[ ! -f "$manifest" ]]; then
    err "device: no install manifest at $manifest; this script did not record installing material in $DEVICE_DEST_DIR, so it refuses to delete anything there"
    exit 2
  fi
  local f
  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    require_bare_filename "$f" "manifest entry"
    rm -f -- "$DEVICE_DEST_DIR/$f"
  done <"$manifest"
  rm -f -- "$manifest"
  info "device: local files removed from $DEVICE_DEST_DIR"
  info "device: this only stops the client from presenting them; it does NOT revoke the server's trust in the identity (remove the EndDevice/Registration entry separately)"

  # Run in a subshell for the same reason trust_remove does: device_verify's
  # own preconditions call exit, not return, and the removal's own message
  # above must not be lost behind a discarded redirect on that exit.
  local verify_status=0
  ( device_verify ) || verify_status=$?
  case "$verify_status" in
  0)
    err "device: cert/key pair in $DEVICE_DEST_DIR still verifies after remove"
    return 1
    ;;
  1)
    return 0
    ;;
  *)
    err "device: local files removed, but the verify step that would confirm it could not run (exit $verify_status)"
    return "$verify_status"
    ;;
  esac
}

# -------------------------------------------------------------------- main

main() {
  local mode="" verb=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --dest)
      [[ $# -ge 2 ]] || { err "--dest requires a value"; exit 2; }
      DEVICE_DEST_DIR="$2"
      shift 2
      ;;
    operator | trust | device)
      mode="$1"
      shift
      ;;
    install | verify | remove)
      verb="$1"
      shift
      ;;
    *)
      err "unrecognized argument: $1"
      usage
      exit 2
      ;;
    esac
  done

  if [[ -z "$mode" || -z "$verb" ]]; then
    err "a mode (operator|trust|device) and a verb (install|verify|remove) are both required"
    usage
    exit 2
  fi

  resolve_cert_dir
  "${mode}_${verb}"
}

main "$@"
