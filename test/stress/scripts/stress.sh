#!/usr/bin/env bash
# scripts/stress.sh: IEEE 2030.5 stress test harness.
#
# Usage: DIM=throughput CLIENTS=5 DURATION=30 bash scripts/stress.sh
#
# All parameters are env vars; see DESIGN.md for the full table.
# This script:
#  1. Warns if kernel parameters are under-tuned.
#  2. Generates a run-scoped PKI (CA + N device certs).
#  3. Builds the server and load generator binaries.
#     For DIM=fanout, builds sep2server with -tags csip_test_hooks.
#  4. Launches sep2server out-of-process (fresh data dir per run).
#  5. Registers all virtual clients via POST /edev.
#  6. For DIM=fanout: subscribes each client via POST /edev/{id}/sub.
#  7. Runs sep2loadgen for the load phase.
#  8. Tears down the server and preserves results/.

set -euo pipefail

# ---- parameter defaults ---------------------------------------------------
DIM="${DIM:-throughput}"
CLIENTS="${CLIENTS:-50}"
RAMP_RATE="${RAMP_RATE:-5}"
DURATION="${DURATION:-0}"
TARGET_HOST="${TARGET_HOST:-127.0.0.1}"
TARGET_PORT="${TARGET_PORT:-8443}"
METRICS_PORT="${METRICS_PORT:-9100}"
CCM="${CCM:-false}"
SEED="${SEED:-42}"
SCRAPE_INTERVAL="${SCRAPE_INTERVAL:-5}"

# Fanout-specific parameters.
# MUTATION_TOKEN is the shared secret for /test/mutations/stress-notify.
# It is generated fresh per fanout run when not set (do not commit a value).
MUTATION_TOKEN="${MUTATION_TOKEN:-}"
MUTATION_RATE_HZ="${MUTATION_RATE_HZ:-20}"
RECEIVER_PORT="${RECEIVER_PORT:-0}"
# MUTATION_HREF: derived from edev-manifest.json after registration when not
# explicitly set. Set this to override (e.g. MUTATION_HREF=/edev/some-id/fsa).
MUTATION_HREF="${MUTATION_HREF:-}"

# Subscription-worker sweep support: pass through when set; leave unset for defaults.
# These are forwarded to sep2server if present in the environment.
SEP2_SUBSCRIPTION_WORKERS="${SEP2_SUBSCRIPTION_WORKERS:-}"
SEP2_SUBSCRIPTION_QUEUE_SIZE="${SEP2_SUBSCRIPTION_QUEUE_SIZE:-}"

# ---- paths ----------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RESULTS_ROOT="${REPO_ROOT}/test/stress/results"
SCRAPE_PID=""
SERVER_PID=""
LOADGEN_PID=""
TS="$(date +%Y%m%d-%H%M%S)"
RUN_ID="${TS}-${DIM}-${CLIENTS}"
RUN_DIR="${RESULTS_ROOT}/${RUN_ID}"
PKI_DIR="${RUN_DIR}/pki"
DATA_DIR="${RUN_DIR}/server-data"
ATBP_DIR="${RUN_DIR}/at-breaking-point"

SERVER_BIN="${REPO_ROOT}/bin/sep2server"
# For the fanout dimension we need a server built with -tags csip_test_hooks
# so the /test/mutations/stress-notify endpoint is compiled in.
SERVER_BIN_FANOUT="${REPO_ROOT}/bin/sep2server-fanout"
LOADGEN_BIN="${REPO_ROOT}/bin/sep2loadgen"
SETUP_BIN="${REPO_ROOT}/bin/sep2stress-setup"

# ---- helpers ---------------------------------------------------------------
log() { echo "[stress] $*"; }
die() { echo "[stress] ERROR: $*" >&2; exit 1; }

warn_not_applied() {
    echo "[stress] WARNING: $1 not applied. Results may reflect host limit, not server limit."
    echo "[stress]   To apply: $2"
}

# ---- pre-run kernel checks -------------------------------------------------
log "checking kernel parameters..."

FD_LIMIT_RAW=$(ulimit -n 2>/dev/null || echo "1024")
# Guard against the "unlimited" string that some shells return; treat it as
# a large number so the integer comparison below does not error under set -e.
if [ "${FD_LIMIT_RAW}" = "unlimited" ]; then
    FD_LIMIT=65536
else
    FD_LIMIT="${FD_LIMIT_RAW}"
fi
if [ "$FD_LIMIT" -lt 16384 ]; then
    warn_not_applied "ulimit -n ${FD_LIMIT} (need >=16384)" "ulimit -n 65536"
    HOST_LIMITED="true"
    HOST_LIMIT_REASON="fd_ulimit=${FD_LIMIT}"
else
    HOST_LIMITED="false"
    HOST_LIMIT_REASON=""
fi

PORT_RANGE=$(cat /proc/sys/net/ipv4/ip_local_port_range 2>/dev/null || echo "32768 60999")
PORT_MIN=$(echo "$PORT_RANGE" | awk '{print $1}')
PORT_MAX=$(echo "$PORT_RANGE" | awk '{print $2}')
PORT_COUNT=$(( PORT_MAX - PORT_MIN ))
if [ "$DIM" = "tls" ] && [ "$PORT_COUNT" -lt 40000 ]; then
    warn_not_applied "ip_local_port_range=${PORT_RANGE} (need >=40000 range for TLS dim)" \
        "sysctl -w net.ipv4.ip_local_port_range='10000 65535' && sysctl -w net.ipv4.tcp_tw_reuse=1"
    HOST_LIMITED="true"
    HOST_LIMIT_REASON="${HOST_LIMIT_REASON} port_range=${PORT_COUNT}"
fi

# Goroutine and memory pressure detection: on a single-host run the load
# generator goroutine pool saturates before the server does. CLIENTS > 500
# on a box with <4 cores is a likely driver-side bottleneck.
NPROC=$(nproc 2>/dev/null || echo 4)
if [ "${CLIENTS}" -gt 500 ] && [ "${NPROC}" -lt 4 ]; then
    warn_not_applied "CLIENTS=${CLIENTS} > 500 on ${NPROC}-core host (goroutine saturation likely)" \
        "run on a host with >=4 cores or reduce CLIENTS"
    HOST_LIMITED="true"
    HOST_LIMIT_REASON="${HOST_LIMIT_REASON} goroutine_saturation_likely"
fi

# ---- fanout: generate mutation token if not provided ----------------------
# Use openssl rand (standard on all Linux/macOS with OpenSSL). If openssl is
# absent, fall back to /dev/urandom + od. Either way, an empty token means
# the csip_test_hooks surface silently disables; we FAIL loudly here rather
# than proceeding with a disabled mutation surface.
if [ "${DIM}" = "fanout" ] && [ -z "${MUTATION_TOKEN}" ]; then
    if command -v openssl > /dev/null 2>&1; then
        MUTATION_TOKEN="$(openssl rand -hex 24)"
    elif [ -r /dev/urandom ]; then
        MUTATION_TOKEN="$(od -vAn -N24 -tx1 /dev/urandom | tr -d ' \n')"
    else
        die "cannot generate MUTATION_TOKEN: openssl and /dev/urandom are both unavailable. Set MUTATION_TOKEN explicitly."
    fi
    if [ -z "${MUTATION_TOKEN}" ]; then
        die "MUTATION_TOKEN generation produced an empty string; aborting fanout run."
    fi
    log "fanout: generated mutation token (not committed)"
fi

# ---- build binaries --------------------------------------------------------
log "building binaries..."
cd "$REPO_ROOT"
go build -o "${LOADGEN_BIN}" ./cmd/sep2loadgen 2>&1 | tee /dev/stderr
go build -o "${SETUP_BIN}" ./test/stress/cmd/sep2stress-setup 2>&1 | tee /dev/stderr

if [ "${DIM}" = "fanout" ]; then
    log "fanout: building sep2server with -tags csip_test_hooks..."
    go build -tags csip_test_hooks -o "${SERVER_BIN_FANOUT}" ./cmd/sep2server 2>&1 | tee /dev/stderr
    ACTIVE_SERVER_BIN="${SERVER_BIN_FANOUT}"
else
    go build -o "${SERVER_BIN}" ./cmd/sep2server 2>&1 | tee /dev/stderr
    ACTIVE_SERVER_BIN="${SERVER_BIN}"
fi
log "binaries built"

# ---- create result directories --------------------------------------------
mkdir -p "${PKI_DIR}" "${DATA_DIR}" "${ATBP_DIR}"

# ---- write params.json ----------------------------------------------------
cat > "${RUN_DIR}/params.json" <<PARAMS
{
  "run_id": "${RUN_ID}",
  "dim": "${DIM}",
  "clients": ${CLIENTS},
  "ramp_rate": ${RAMP_RATE},
  "duration_sec": ${DURATION},
  "target_host": "${TARGET_HOST}",
  "target_port": ${TARGET_PORT},
  "ccm": ${CCM},
  "seed": ${SEED},
  "scrape_interval_sec": ${SCRAPE_INTERVAL},
  "sub_workers": "${SEP2_SUBSCRIPTION_WORKERS:-default}",
  "sub_queue_size": "${SEP2_SUBSCRIPTION_QUEUE_SIZE:-default}",
  "mutation_rate_hz": ${MUTATION_RATE_HZ},
  "host_limited": ${HOST_LIMITED},
  "host_limit_reason": "${HOST_LIMIT_REASON}"
}
PARAMS
log "params.json written to ${RUN_DIR}"

# ---- generate PKI ---------------------------------------------------------
log "generating PKI for ${CLIENTS} virtual clients (seed=${SEED})..."
"${SETUP_BIN}" \
    -seed "${SEED}" \
    -count "${CLIENTS}" \
    -out-dir "${PKI_DIR}" \
    -server-cn "SEP2StressServer"
log "PKI generated"

# ---- pre-launch port check -------------------------------------------------
# If a sep2server is still bound to TARGET_PORT from a prior run, kill it now.
# A stale server would receive our new-PKI clients and fail TLS verification
# because the CA cert changes each run.
# Safety: only kill the process if it matches the binary name "sep2server".
# Refusing to kill an unknown process avoids accidentally terminating unrelated
# services on a shared box.
STALE_LINE="$(ss -tlnp 2>/dev/null | awk -v port=":${TARGET_PORT}" '$4 == port {print}')"
if [ -n "${STALE_LINE}" ]; then
    if echo "${STALE_LINE}" | grep -q "sep2server"; then
        STALE_PID="$(echo "${STALE_LINE}" | grep -oP 'pid=\K[0-9]+')"
        log "stale sep2server on port ${TARGET_PORT} (pid=${STALE_PID}); sending SIGTERM..."
        kill -TERM "${STALE_PID}" 2>/dev/null || true
        stale_waited=0
        while ss -tlnp 2>/dev/null | grep -q ":${TARGET_PORT}" && [ "$stale_waited" -lt 5 ]; do
            sleep 1
            stale_waited=$(( stale_waited + 1 ))
        done
        if ss -tlnp 2>/dev/null | grep -q ":${TARGET_PORT}"; then
            log "sep2server did not exit in 5s; sending SIGKILL (pid=${STALE_PID})"
            kill -KILL "${STALE_PID}" 2>/dev/null || true
            sleep 1
        fi
    else
        die "port ${TARGET_PORT} is bound by a non-sep2server process: ${STALE_LINE}; refusing to kill it. Change TARGET_PORT or stop the process manually."
    fi
fi

# ---- launch sep2server ----------------------------------------------------
log "launching sep2server on :${TARGET_PORT}, metrics on :${METRICS_PORT}..."

SERVER_ENV=(
    "SEP2_ADDR=:${TARGET_PORT}"
    "SEP2_CERT=${PKI_DIR}/server.crt"
    "SEP2_KEY=${PKI_DIR}/server.key"
    "SEP2_CA=${PKI_DIR}/ca.crt"
    "SEP2_EXTRA_CLIENT_CAS=${PKI_DIR}/ca.crt"
    "SEP2_METRICS_ADDR=127.0.0.1:${METRICS_PORT}"
    "SEP2_DATA_DIR=${DATA_DIR}"
)
if [ -n "${SEP2_SUBSCRIPTION_WORKERS}" ]; then
    SERVER_ENV+=("SEP2_SUBSCRIPTION_WORKERS=${SEP2_SUBSCRIPTION_WORKERS}")
fi
if [ -n "${SEP2_SUBSCRIPTION_QUEUE_SIZE}" ]; then
    SERVER_ENV+=("SEP2_SUBSCRIPTION_QUEUE_SIZE=${SEP2_SUBSCRIPTION_QUEUE_SIZE}")
fi
# For the fanout dim: supply the mutation token so the server enables
# /test/mutations/stress-notify (csip_test_hooks build).
if [ "${DIM}" = "fanout" ]; then
    SERVER_ENV+=("SEP2_TEST_MUTATION_TOKEN=${MUTATION_TOKEN}")
    # The loadgen notification receiver listens on 127.0.0.1, which the
    # server refuses as a notification destination unless this is set.
    SERVER_ENV+=("SEP2_NOTIFICATION_ALLOW_LOOPBACK=true")
fi

env "${SERVER_ENV[@]}" "${ACTIVE_SERVER_BIN}" serve \
    > "${RUN_DIR}/server.log" 2>&1 &
SERVER_PID=$!
log "sep2server started (pid=${SERVER_PID})"

# Trap: teardown server and capture pprof on exit.
cleanup() {
    local exit_code=$?
    # Stop the metrics scrape loop if it is still running.
    kill "${SCRAPE_PID}" 2>/dev/null || true
    # Stop the background loadgen if it is still running (fanout only).
    kill "${LOADGEN_PID}" 2>/dev/null || true
    log "teardown: sending SIGTERM to sep2server (pid=${SERVER_PID})"
    kill -TERM "${SERVER_PID}" 2>/dev/null || true
    local waited=0
    while kill -0 "${SERVER_PID}" 2>/dev/null && [ "$waited" -lt 10 ]; do
        sleep 1
        waited=$(( waited + 1 ))
    done
    if kill -0 "${SERVER_PID}" 2>/dev/null; then
        log "sep2server did not exit in 10s; sending SIGKILL"
        kill -KILL "${SERVER_PID}" 2>/dev/null || true
    fi
    log "server.log preserved at ${RUN_DIR}/server.log"
    log "server-data preserved at ${DATA_DIR}"
    exit "${exit_code}"
}
trap cleanup EXIT

# ---- health probe /metrics -------------------------------------------------
log "waiting for sep2server health probe..."
for i in $(seq 1 20); do
    if curl -sf "http://127.0.0.1:${METRICS_PORT}/metrics" > /dev/null 2>&1; then
        log "server healthy after ${i}s"
        break
    fi
    if [ "$i" -eq 20 ]; then
        die "sep2server did not become healthy within 20s; check ${RUN_DIR}/server.log"
    fi
    sleep 1
done

# ---- register virtual clients via POST /edev --------------------------------
log "registering ${CLIENTS} virtual clients via POST /edev..."
"${SETUP_BIN}" \
    -register \
    -pki-dir "${PKI_DIR}" \
    -count "${CLIENTS}" \
    -server "https://${TARGET_HOST}:${TARGET_PORT}"
log "all clients registered (edev-manifest.json written)"

# ---- metrics scrape loop (started AFTER registration to avoid registration
#      traffic contaminating the load-phase scrape series) --------------------
METRICS_URL="http://127.0.0.1:${METRICS_PORT}/metrics"
METRICS_FILE="${RUN_DIR}/metrics-series.jsonl"
scrape_metrics() {
    while sleep "${SCRAPE_INTERVAL}"; do
        RAW="$(curl -sf "${METRICS_URL}" 2>/dev/null || true)"
        if [ -n "$RAW" ]; then
            TS_NOW="$(date +%s%3N)"
            echo "{\"ts_ms\":${TS_NOW},\"raw\":$(echo "$RAW" | python3 -c '
import sys, json
lines = [l for l in sys.stdin.read().splitlines() if l and not l.startswith("#")]
print(json.dumps("\n".join(lines)))
')}" >> "${METRICS_FILE}"
        fi
    done
}
scrape_metrics &
SCRAPE_PID=$!

# ---- load phase ------------------------------------------------------------
log "starting load phase: dim=${DIM} clients=${CLIENTS} duration=${DURATION}s"

LOADGEN_COMMON_ARGS=(
    -host "${TARGET_HOST}"
    -port "${TARGET_PORT}"
    -clients "${CLIENTS}"
    -ramp-rate "${RAMP_RATE}"
    -duration "${DURATION}"
    -dim "${DIM}"
    -ccm="${CCM}"
    -results-dir "${RUN_DIR}"
    -ca "${PKI_DIR}/ca.crt"
    -metrics-url "${METRICS_URL}"
)

if [ "${DIM}" = "fanout" ]; then
    # Fanout sequence (unified subscriber/client model):
    # Each of the N virtual clients is both an active GET driver and a
    # subscriber. The loadgen ramps clients at RAMP_RATE/s; as each client
    # starts, it subscribes to /dcap using its own mTLS cert and edev ID
    # (from edev-manifest.json), then immediately starts driving GET traffic.
    # Subscriber count equals active client count (1:1).
    #
    # The notification receiver is started inside loadgen before the ramp
    # begins; its URL is written to notify-receiver-url.txt and printed to
    # stderr. The per-client subscribe func reads edev-manifest.json and
    # issues POST /edev/{id}/sub for each client as it is ramped.
    #
    # Sequence:
    # a) Start loadgen in background; it starts the receiver, writes
    #    notify-receiver-url.txt, and begins ramping+subscribing clients.
    # b) Wait for loadgen to finish (duration or criterion).
    # No separate subscribe step needed: subscriptions happen per-client
    # inside the loadgen ramp.

    FANOUT_EXTRA_ARGS=(
        -mutation-token "${MUTATION_TOKEN}"
        -mutation-rate-hz "${MUTATION_RATE_HZ}"
        -receiver-port "${RECEIVER_PORT}"
    )
    if [ -n "${MUTATION_HREF:-}" ]; then
        FANOUT_EXTRA_ARGS+=(-mutation-href "${MUTATION_HREF}")
    fi

    "${LOADGEN_BIN}" \
        "${LOADGEN_COMMON_ARGS[@]}" \
        "${FANOUT_EXTRA_ARGS[@]}" \
        > "${RUN_DIR}/loadgen-stdout.txt" 2>&1 &
    LOADGEN_PID=$!

    # Wait for the receiver URL file so we can log it, then let loadgen run.
    NOTIFY_URL_FILE="${RUN_DIR}/notify-receiver-url.txt"
    notify_wait=0
    while [ ! -f "${NOTIFY_URL_FILE}" ] && [ "$notify_wait" -lt 10 ]; do
        sleep 1
        notify_wait=$(( notify_wait + 1 ))
    done
    if [ -f "${NOTIFY_URL_FILE}" ]; then
        NOTIFY_URL="$(tr -d '\n' < "${NOTIFY_URL_FILE}")"
        log "fanout: notification receiver at ${NOTIFY_URL}"
    else
        log "fanout: WARNING: notify-receiver-url.txt not found within 10s; loadgen may have failed"
    fi

    # Wait for loadgen to finish.
    wait "${LOADGEN_PID}" || true
    LOADGEN_PID=""
    # Append loadgen stdout to the standard log location.
    cat "${RUN_DIR}/loadgen-stdout.txt" >> "${RUN_DIR}/loadgen.log" 2>/dev/null || true
else
    "${LOADGEN_BIN}" "${LOADGEN_COMMON_ARGS[@]}"
fi

log "load phase complete"

# ---- fanout validity gate (Pike HIGH) --------------------------------------
# The loadgen binary stamps partial_subscription=true and exits with code 2
# when the delivered-per-mutation ratio is below 0.9 x client count. Catch
# that here so the harness exits with a loud error rather than silently
# continuing to the results summary. The check reads breaking-point.json
# (written by loadgen before it exits) rather than the loadgen exit code
# because the fanout run uses 'wait ... || true' above and drops the exit code.
if [ "${DIM}" = "fanout" ] && [ -f "${RUN_DIR}/breaking-point.json" ]; then
    PARTIAL="$(python3 -c "
import json, sys
d = json.load(open('${RUN_DIR}/breaking-point.json'))
print('true' if d.get('partial_subscription') else 'false')
")"
    if [ "${PARTIAL}" = "true" ]; then
        DPM="$(python3 -c "
import json, sys
d = json.load(open('${RUN_DIR}/breaking-point.json'))
print(d.get('delivered_per_mutation', 0))
")"
        die "FANOUT VALIDITY GATE FAILED: partial_subscription=true, delivered_per_mutation=${DPM}; run is not a valid N-wide data point. Check ${RUN_DIR}/loadgen.log."
    fi
fi

# ---- at-breaking-point capture --------------------------------------------
BP_FILE="${RUN_DIR}/breaking-point.json"
if [ -f "${BP_FILE}" ]; then
    CRITERION="$(python3 -c "import json,sys; d=json.load(open('${BP_FILE}')); print(d.get('criterion','none'))")"
    if [ "${CRITERION}" != "none" ] && [ "${CRITERION}" != "duration_elapsed" ]; then
        log "breaking point detected (${CRITERION}): capturing pprof..."
        # pprof is served on the protocol listener via net/http/pprof if registered,
        # but the server currently does not register pprof. Capture goroutine count
        # from /metrics and heap stats instead.
        curl -sf "http://127.0.0.1:${METRICS_PORT}/metrics" \
            > "${ATBP_DIR}/metrics-final.txt" 2>/dev/null || true
        log "at-breaking-point metrics captured"
    fi
fi

# Apply host_limited flag from params to breaking-point.json if set.
if [ "${HOST_LIMITED}" = "true" ] && [ -f "${BP_FILE}" ]; then
    python3 - "${BP_FILE}" "${HOST_LIMIT_REASON}" <<'PY'
import sys, json
path, reason = sys.argv[1], sys.argv[2]
d = json.load(open(path))
d["host_limited"] = True
d["host_limit_reason"] = reason
json.dump(d, open(path, "w"), indent=2)
PY
fi

kill "${SCRAPE_PID}" 2>/dev/null || true

log "run complete: results in ${RUN_DIR}"
log "breaking-point.json:"
cat "${BP_FILE}" 2>/dev/null || echo "(not written)"
# Explicitly exit 0 so the cleanup trap inherits success.
exit 0
