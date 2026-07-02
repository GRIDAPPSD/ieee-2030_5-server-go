#!/usr/bin/env bash
# scripts/stress.sh: IEEE 2030.5 stress test harness for IEEESRV-007.
#
# Usage: DIM=throughput CLIENTS=5 DURATION=30 bash scripts/stress.sh
#
# All parameters are env vars; see DESIGN.md for the full table.
# This script:
#  1. Warns if kernel parameters are under-tuned.
#  2. Generates a run-scoped PKI (CA + N device certs).
#  3. Builds the server and load generator binaries.
#  4. Launches sep2server out-of-process (fresh data dir per run).
#  5. Registers all virtual clients via POST /edev.
#  6. Runs sep2loadgen for the load phase.
#  7. Tears down the server and preserves results/.

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
REAL_SIMS="${REAL_SIMS:-0}"
SCRAPE_INTERVAL="${SCRAPE_INTERVAL:-5}"

# IEEESRV-008 sweep support: pass through when set; leave unset for defaults.
# These are forwarded to sep2server if present in the environment.
SEP2_SUBSCRIPTION_WORKERS="${SEP2_SUBSCRIPTION_WORKERS:-}"
SEP2_SUBSCRIPTION_QUEUE_SIZE="${SEP2_SUBSCRIPTION_QUEUE_SIZE:-}"

# ---- paths ----------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RESULTS_ROOT="${REPO_ROOT}/test/stress/results"
SCRAPE_PID=""
SERVER_PID=""
TS="$(date +%Y%m%d-%H%M%S)"
RUN_ID="${TS}-${DIM}-${CLIENTS}"
RUN_DIR="${RESULTS_ROOT}/${RUN_ID}"
PKI_DIR="${RUN_DIR}/pki"
DATA_DIR="${RUN_DIR}/server-data"
ATBP_DIR="${RUN_DIR}/at-breaking-point"

SERVER_BIN="${REPO_ROOT}/bin/sep2server"
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

FD_LIMIT=$(ulimit -n 2>/dev/null || echo 1024)
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

# ---- build binaries --------------------------------------------------------
log "building binaries..."
cd "$REPO_ROOT"
go build -o "${SERVER_BIN}" ./cmd/sep2server 2>&1 | tee /dev/stderr
go build -o "${LOADGEN_BIN}" ./cmd/sep2loadgen 2>&1 | tee /dev/stderr
go build -o "${SETUP_BIN}" ./test/stress/cmd/sep2stress-setup 2>&1 | tee /dev/stderr
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
  "real_sims": ${REAL_SIMS},
  "scrape_interval_sec": ${SCRAPE_INTERVAL},
  "sub_workers": "${SEP2_SUBSCRIPTION_WORKERS:-default}",
  "sub_queue_size": "${SEP2_SUBSCRIPTION_QUEUE_SIZE:-default}",
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
# If anything is still bound to TARGET_PORT from a prior run, kill it now.
# A stale server from a previous run would otherwise receive our new-PKI clients
# and fail TLS verification because the CA cert has changed.
STALE_PID="$(ss -tlnp 2>/dev/null | awk -v port=":${TARGET_PORT}" '$4 == port {match($0,/pid=([0-9]+)/,a); print a[1]}')"
if [ -n "${STALE_PID}" ]; then
    log "stale process on port ${TARGET_PORT} (pid=${STALE_PID}); sending SIGTERM..."
    kill -TERM "${STALE_PID}" 2>/dev/null || true
    stale_waited=0
    while ss -tlnp 2>/dev/null | grep -q ":${TARGET_PORT}" && [ "$stale_waited" -lt 5 ]; do
        sleep 1
        stale_waited=$(( stale_waited + 1 ))
    done
    if ss -tlnp 2>/dev/null | grep -q ":${TARGET_PORT}"; then
        kill -KILL "${STALE_PID}" 2>/dev/null || true
        sleep 1
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
if [ "${CCM}" = "true" ]; then
    SERVER_ENV+=("SEP2_CCM=true")
fi
if [ -n "${SEP2_SUBSCRIPTION_WORKERS}" ]; then
    SERVER_ENV+=("SEP2_SUBSCRIPTION_WORKERS=${SEP2_SUBSCRIPTION_WORKERS}")
fi
if [ -n "${SEP2_SUBSCRIPTION_QUEUE_SIZE}" ]; then
    SERVER_ENV+=("SEP2_SUBSCRIPTION_QUEUE_SIZE=${SEP2_SUBSCRIPTION_QUEUE_SIZE}")
fi

env "${SERVER_ENV[@]}" "${SERVER_BIN}" serve \
    > "${RUN_DIR}/server.log" 2>&1 &
SERVER_PID=$!
log "sep2server started (pid=${SERVER_PID})"

# Trap: teardown server and capture pprof on exit.
cleanup() {
    local exit_code=$?
    # Stop the metrics scrape loop if it is still running.
    kill "${SCRAPE_PID}" 2>/dev/null || true
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
log "all clients registered"

# ---- metrics scrape loop ---------------------------------------------------
METRICS_FILE="${RUN_DIR}/metrics-series.jsonl"
scrape_loop() {
    while sleep "${SCRAPE_INTERVAL}"; do
        RAW="$(curl -sf "http://127.0.0.1:${METRICS_PORT}/metrics" 2>/dev/null || true)"
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
scrape_loop &
SCRAPE_PID=$!

# ---- load phase ------------------------------------------------------------
log "starting load phase: dim=${DIM} clients=${CLIENTS} duration=${DURATION}s"

export TARGET_HOST TARGET_PORT CLIENTS RAMP_RATE DURATION DIM CCM SEED
export RESULTS_DIR="${RUN_DIR}"
export CA_FILE="${PKI_DIR}/ca.crt"

"${LOADGEN_BIN}" \
    -host "${TARGET_HOST}" \
    -port "${TARGET_PORT}" \
    -clients "${CLIENTS}" \
    -ramp-rate "${RAMP_RATE}" \
    -duration "${DURATION}" \
    -dim "${DIM}" \
    -ccm "${CCM}" \
    -results-dir "${RUN_DIR}" \
    -ca "${PKI_DIR}/ca.crt"

log "load phase complete"

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
