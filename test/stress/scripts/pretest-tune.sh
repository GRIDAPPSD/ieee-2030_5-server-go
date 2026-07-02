#!/usr/bin/env bash
# scripts/pretest-tune.sh: opt-in kernel tuning for the IEEE 2030.5 stress harness.
# Card: IEEESRV-009.
#
# The stress harness (scripts/stress.sh) deliberately never mutates system state;
# it only warns when kernel parameters are under-tuned. This script is the explicit,
# operator-invoked escape hatch that applies the tuning the connection-heavy
# dimensions need (throughput and tls/ccm). It is NOT needed for fanout or soak.
#
# It applies exactly three tunings and nothing else:
#   1. net.ipv4.ip_local_port_range = "10000 65535"   (sudo sysctl -w)
#   2. net.ipv4.tcp_tw_reuse        = 1               (sudo sysctl -w)
#   3. fd soft limit                >= 65536          (see the source-vs-execute note)
#
# Modes:
#   SOURCED  (. scripts/pretest-tune.sh):  applies the sysctls AND raises ulimit -n
#            in the caller's shell, so the harness inherits the higher fd limit.
#   EXECUTED (bash scripts/pretest-tune.sh): applies the sysctls, then PRINTS the exact
#            ulimit -n line for the operator to run in their own run shell, because a
#            child process cannot raise its parent's ulimit. It also prints an optional
#            /etc/security/limits.conf snippet for a persistent hard-limit bump.
#
# The sysctls persist system-wide once applied (until the next reboot), so re-running
# is a no-op. The ulimit only sticks in the shell that ran the ulimit line.

set -euo pipefail

# ---- desired values (keep in lockstep with the warnings in stress.sh) -------
readonly PORT_RANGE_DESIRED="10000 65535"
readonly TW_REUSE_DESIRED="1"
readonly FD_LIMIT_DESIRED="65536"

# ---- sourced-vs-executed detection -----------------------------------------
# When sourced, $0 is the caller's shell (bash/-bash), not this file, and
# BASH_SOURCE[0] differs from $0. That distinction drives the ulimit UX below.
SOURCED="false"
if [ "${BASH_SOURCE[0]}" != "${0}" ]; then
    SOURCED="true"
fi

# ---- output helpers --------------------------------------------------------
log() { echo "[pretest-tune] $*"; }
warn() { echo "[pretest-tune] WARNING: $*" >&2; }

# fail: emit a clear message naming what could not be applied and the
# consequence, then stop. When sourced we cannot exit the caller's shell, so
# return instead of exit to avoid killing the operator's interactive session.
fail() {
    echo "[pretest-tune] ERROR: $*" >&2
    echo "[pretest-tune] Consequence: the harness will run host-limited; tune manually or run on a tuned host." >&2
    if [ "${SOURCED}" = "true" ]; then
        return 1
    fi
    exit 1
}

# ---- sudo preflight --------------------------------------------------------
# We need sudo for the sysctl writes. Detect its absence up front rather than
# leaving a half-applied state if the first write succeeds and a later one is
# blocked. sysctl reads (via /proc) do not need sudo.
SUDO=""
if [ "$(id -u)" -eq 0 ]; then
    SUDO=""
elif command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
else
    fail "sudo not found and not running as root; cannot apply sysctls (ip_local_port_range, tcp_tw_reuse)."
fi

# read_sysctl KEY: print the current value of a sysctl, space-collapsed so the
# port range compares cleanly against the desired "min max" form.
read_sysctl() {
    local key="$1"
    sysctl -n "${key}" 2>/dev/null | tr -s '[:space:]' ' ' | sed 's/^ *//; s/ *$//'
}

# apply_sysctl KEY DESIRED: idempotently set KEY to DESIRED via sudo sysctl -w,
# printing BEFORE and AFTER. A no-op when already at the desired value.
apply_sysctl() {
    local key="$1"
    local desired="$2"
    local before
    before="$(read_sysctl "${key}")"
    log "${key}: before = '${before}'"
    if [ "${before}" = "${desired}" ]; then
        log "${key}: already '${desired}', no change"
        return 0
    fi
    if ! ${SUDO} sysctl -w "${key}=${desired}" >/dev/null 2>&1; then
        fail "failed to set ${key} to '${desired}' (sudo sysctl -w). This dimension needs it to avoid ephemeral-port or time-wait exhaustion."
        return 1
    fi
    local after
    after="$(read_sysctl "${key}")"
    log "${key}: after  = '${after}'"
    if [ "${after}" != "${desired}" ]; then
        fail "${key} did not take the desired value '${desired}' after write (got '${after}')."
        return 1
    fi
}

# ---- 1 + 2: sysctls --------------------------------------------------------
log "applying sysctl tunings (sudo required)..."
apply_sysctl "net.ipv4.ip_local_port_range" "${PORT_RANGE_DESIRED}"
apply_sysctl "net.ipv4.tcp_tw_reuse" "${TW_REUSE_DESIRED}"

# ---- 3: fd soft limit ------------------------------------------------------
# ulimit -n only affects the invoking shell and its children. A child process
# cannot raise its parent's limit, so an EXECUTED run cannot make the limit
# stick in the operator's run shell. Handle both modes honestly.
FD_BEFORE="$(ulimit -n 2>/dev/null || echo unknown)"
log "ulimit -n: before = '${FD_BEFORE}'"

fd_at_or_above_target() {
    # true when the current soft limit is unlimited or >= the desired target.
    local cur="$1"
    [ "${cur}" = "unlimited" ] && return 0
    case "${cur}" in
        (*[!0-9]*) return 1 ;;  # non-numeric, unknown: treat as below target
    esac
    [ "${cur}" -ge "${FD_LIMIT_DESIRED}" ]
}

if [ "${SOURCED}" = "true" ]; then
    if fd_at_or_above_target "${FD_BEFORE}"; then
        log "ulimit -n: already >= ${FD_LIMIT_DESIRED}, no change"
    elif ulimit -n "${FD_LIMIT_DESIRED}" 2>/dev/null; then
        log "ulimit -n: after  = '$(ulimit -n)' (raised in this shell; the harness will inherit it)"
    else
        warn "could not raise ulimit -n to ${FD_LIMIT_DESIRED} in this shell."
        warn "The soft cap is likely the hard limit. Raise the hard limit via the limits.conf snippet below, re-login, then re-source this script."
    fi
else
    if fd_at_or_above_target "${FD_BEFORE}"; then
        log "ulimit -n: already >= ${FD_LIMIT_DESIRED} in this shell"
        log "If your RUN shell differs, run this line there to be sure:"
    else
        log "This was EXECUTED, so it cannot raise the fd limit in your run shell."
        log "Run this exact line in the shell you will launch the harness from:"
    fi
    echo
    echo "    ulimit -n ${FD_LIMIT_DESIRED}"
    echo
    log "Alternatively, source this script to raise it here:  . ${BASH_SOURCE[0]}"
    echo
    log "Optional persistent hard-limit bump (needs a re-login to take effect):"
    echo
    echo "    # /etc/security/limits.conf"
    echo "    # <user>  soft  nofile  ${FD_LIMIT_DESIRED}"
    echo "    # <user>  hard  nofile  ${FD_LIMIT_DESIRED}"
    echo
fi

log "sysctl tunings applied and idempotent; ulimit handled per the mode above."
log "Tuning complete for the throughput and tls/ccm dimensions."
