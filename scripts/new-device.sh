#!/usr/bin/env bash
# new-device.sh - mint a CSIP-conformant device certificate for the admin
# dashboard's "Add End Device" form.
#
# Invoked from `make new-device` with all parameters passed as exported env
# vars (NOT positional args). Make-level dispatch keeps the Makefile target
# tiny and pushes input validation here, where bash regex testing rejects
# shell-metachar / path-traversal payloads BEFORE any value reaches a
# command substitution.
#
# Required env (set by Make from DEVICE_NAME=... SERIAL=... on the command line):
#   DEVICE_NAME    output filename prefix (creates ${CERT_DIR}/<name>.crt + .key)
#   SERIAL         hardware serial for the CSIP section 6.2 HardwareModuleName SAN
#
# Optional env:
#   HW_TYPE        manufacturer PEN OID (default 1.3.6.1.4.1.40732.99)
#   DEVICE_TYPE    1=Generic (default), 2=Mobile, 3=PostMfg
#   FORCE          when "1", overwrite an existing cert/key with the same name
#   CERT_DIR       output directory (default: SEP2_CERT_DIR, or $HOME/tls -
#                  the same default the server and `make certs` use, #598)
#   SERVER_BIN     server binary path (default "bin/sep2server")
#
# Exit codes:
#   0  success
#   1  refusing to overwrite existing cert (FORCE not set)
#   2  validation / pre-flight failure (missing input, malformed input,
#      missing CA)
#   non-zero from $SERVER_BIN itself if cert generation fails

set -euo pipefail

err() { printf 'ERROR: %s\n' "$*" >&2; }
example() {
  printf '  example: make new-device DEVICE_NAME=device-2 SERIAL=DEV-002\n' >&2
}

# Default matches the server and `make certs` (#598): SEP2_CERT_DIR if the
# operator set it, else $HOME/tls. Invoked directly (not through `make
# new-device`, which always exports CERT_DIR), this used to fall back to
# "certs" in the working directory and write a device private key there.
if [[ -z "${CERT_DIR:-}" ]]; then
  if [[ -n "${SEP2_CERT_DIR:-}" ]]; then
    CERT_DIR="$SEP2_CERT_DIR"
  elif [[ -n "${HOME:-}" ]]; then
    CERT_DIR="$HOME/tls"
  else
    err "CERT_DIR is unset, SEP2_CERT_DIR is unset, and HOME could not be determined: set one of them explicitly"
    exit 2
  fi
fi
SERVER_BIN="${SERVER_BIN:-bin/sep2server}"

DEVICE_NAME="${DEVICE_NAME:-}"
SERIAL="${SERIAL:-}"
HW_TYPE="${HW_TYPE:-1.3.6.1.4.1.40732.99}"
DEVICE_TYPE="${DEVICE_TYPE:-1}"
FORCE="${FORCE:-}"

# --- Required ---------------------------------------------------------------
if [[ -z "$DEVICE_NAME" ]]; then
  err "DEVICE_NAME=<slug> is required (output prefix; creates ${CERT_DIR}/<DEVICE_NAME>.crt + .key)"
  example
  exit 2
fi
if [[ -z "$SERIAL" ]]; then
  err "SERIAL=<hw-serial> is required (CSIP section 6.2 HardwareModuleName SAN)"
  example
  exit 2
fi

# --- Validation --------------------------------------------------------------
# DEVICE_NAME flows into a filesystem path AND a shell command line. Restrict
# to a strict allowlist so neither path-traversal (../) nor shell metachars
# (`; | & $ \` etc.) can leak through. The allowlist matches what real
# device naming conventions need: ASCII letters, digits, dot, hyphen,
# underscore. No leading dot (rejects `.hidden` and `..`).
if [[ ! "$DEVICE_NAME" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$ ]]; then
  err "DEVICE_NAME must match ^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}\$ (got: ${DEVICE_NAME})"
  err "Allowed: ASCII letters, digits, '.', '-', '_'. No leading dot. No '/' or shell metachars. Max 64 chars."
  exit 2
fi

# SERIAL ends up inside a CSIP HardwareModuleName SAN (an OCTET STRING) and
# is also passed to the cert-generation command line. Spec is permissive
# about content but ASCII-only is the realistic operator pattern. Allow
# letters, digits, hyphen, underscore, dot, colon, slash (some vendors use
# slash in serials) - but reject shell metachars and quotes that would
# break the command line.
if [[ ! "$SERIAL" =~ ^[A-Za-z0-9._:/-]{1,128}$ ]]; then
  err "SERIAL must match ^[A-Za-z0-9._:/-]{1,128}\$ (got: ${SERIAL})"
  err "Allowed: ASCII letters, digits, '.', '-', '_', ':', '/'. No spaces, quotes, or shell metachars."
  exit 2
fi

# HW_TYPE is a dotted-decimal OID. Strict: digits and dots only.
if [[ ! "$HW_TYPE" =~ ^[0-9]+(\.[0-9]+)+$ ]]; then
  err "HW_TYPE must be a dotted-decimal OID (e.g. 1.3.6.1.4.1.<PEN>); got: ${HW_TYPE}"
  exit 2
fi

# DEVICE_TYPE is the IEEE 2030.5 section 6.11.7.2 device-type policy index.
case "$DEVICE_TYPE" in
  1|2|3) ;;
  *)
    err "DEVICE_TYPE must be 1 (Generic), 2 (Mobile), or 3 (PostMfg); got: ${DEVICE_TYPE}"
    err "See docs/csip.md section 6.11.7.2 for the policy meanings."
    exit 2
    ;;
esac

# --- Pre-flight ---------------------------------------------------------------
if [[ ! -f "${CERT_DIR}/ca.crt" || ! -f "${CERT_DIR}/ca.key" ]]; then
  err "CA missing under ${CERT_DIR}/ - run 'make certs' first to bootstrap the CA"
  exit 2
fi

CERT_PATH="${CERT_DIR}/${DEVICE_NAME}.crt"
KEY_PATH="${CERT_DIR}/${DEVICE_NAME}.key"

if [[ -e "$CERT_PATH" && "$FORCE" != "1" ]]; then
  err "${CERT_PATH} already exists."
  printf '  Pick a different DEVICE_NAME, or pass FORCE=1 to overwrite (this destroys the old key - any deployed device using it loses access).\n' >&2
  exit 1
fi

if [[ ! -x "$SERVER_BIN" ]]; then
  err "${SERVER_BIN} missing or not executable - run 'make build' first"
  exit 2
fi

# --- Issue ---------------------------------------------------------------
"$SERVER_BIN" certs generate-device \
  --ca "${CERT_DIR}/ca.crt" --ca-key "${CERT_DIR}/ca.key" \
  --hw-serial "$SERIAL" \
  --hw-type "$HW_TYPE" \
  --device-type "$DEVICE_TYPE" \
  --name "$DEVICE_NAME" --out "$CERT_DIR"

# --- Render PEM block for paste -------------------------------------------
RULE="$(printf '%.0s=' {1..79})"
printf '\n%s\n' "$RULE"
printf ' Paste the PEM block below into the admin dashboard'\''s "Add End Device" textarea\n'
printf ' (http://localhost:8444/ - Path 0 loopback bypass admits without creds).\n'
printf ' Then fill in Description and PIN, and click Add Device.\n'
printf '%s\n' "$RULE"
cat "$CERT_PATH"
printf '%s\n' "$RULE"
printf ' Private key kept LOCAL: %s (do NOT paste; do NOT commit)\n' "$KEY_PATH"
printf '%s\n' "$RULE"
