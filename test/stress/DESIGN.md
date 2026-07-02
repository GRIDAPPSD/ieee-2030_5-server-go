# IEEE 2030.5 Stress Test Harness: Design of Record

Card: IEEESRV-007
Author: Devi (interoperability/conformance)

## Placement Rationale

The harness lives in `test/stress/` within the server repo (alongside `test/csip`).
Reason: the load generator imports `internal/certs` directly to pre-generate per-client
device certificates without shelling out. Placing it in the client repo would require
publishing the certs package or duplicating the ECDSA/HardwareModuleName SAN logic.
A `test/` subdirectory in the server repo is the existing pattern for harness code that
needs server internals. The `test/stress/` tree lives under the module root
(`github.com/GRIDAPPSD/ieee-2030_5-go`) with no separate `go.mod`; the load generator
binary is at `cmd/sep2loadgen` and the loadgen library is at `test/stress/loadgen`,
both within the same module (see build section below).

## Scope

This harness characterizes the shipped server under load. It is not a certification
test. The goal is to find the breaking point across four dimensions and produce
flat diffable JSON artifacts per run.

## What is Host-Bound vs True Server Limits

The following breaking-point signals are artifacts of the single-host environment,
not inherent server limits:

- **CCM/CPU saturation**: the vendored gotls CCM-8 path uses a non-accelerated
  AES-128-CCM-8 cipher. On a single core shared by server and load generator,
  CPU saturation at high new-connection rates is a host ceiling, not a server
  architectural limit. Distributed load generation would reveal a higher server
  ceiling.
- **fd/socket exhaustion**: Linux default `ulimit -n 1024` is a kernel parameter,
  not a server limit. The harness warns and requires `ulimit -n 65536` pre-run.
  Results from runs with untuned fd limits are labeled `host_limited: true` in
  `breaking-point.json`.
- **Ephemeral port exhaustion**: ~28k ports (default range) limits no-keepalive
  TLS tests. `tcp_tw_reuse` and wider range are required. Same labeling.
- **Load generator goroutine saturation**: above ~500 virtual clients on a single
  host, the driver itself may plateau. Diagnosed via driver self-metrics
  (`loadgen_send_rate_rps` vs `sep2_http_requests_total` rate). When they diverge,
  the driver is the bottleneck.

The following is a true server architectural limit, regardless of host:

- **Subscription queue saturation (`queue_full`)**: the server's
  `coresub.Manager` is initialized with `subscriptionWorkers=4` and
  `subscriptionQueueSize=256` (constants in `internal/server/server.go`).
  A non-zero `sep2_subscription_notifications_total{outcome="queue_full"}`
  counter is a server-side limit that distributed load generation would not
  change. This is the headline finding the fan-out dimension targets.

## Pre-Test Kernel Tuning (opt-in)

`scripts/stress.sh` never mutates system state; it only warns when kernel
parameters are under-tuned and labels affected runs `host_limited: true`.
`scripts/pretest-tune.sh` (card IEEESRV-009) is the explicit, operator-invoked
escape hatch that applies the tuning the connection-heavy dimensions need. Run it
via `make stress-pretest` or by invoking the script directly.

It applies exactly three tunings and nothing else:

1. `net.ipv4.ip_local_port_range = "10000 65535"` (widens the ephemeral port pool
   so the tls dimension does not exhaust ports on new-connection-per-request load).
2. `net.ipv4.tcp_tw_reuse = 1` (lets the kernel reuse sockets in TIME_WAIT for new
   outbound connections, which the same connection-churn pattern needs).
3. fd soft limit raised to at least `65536` (so the server and load generator can
   hold enough concurrent sockets).

The two sysctls are applied with `sudo sysctl -w`, are system-wide until the next
reboot, and re-running is a no-op. If `sudo` is unavailable or a write fails, the
script fails loudly and names what could not be applied; it does not leave a
half-applied state ambiguous.

### When it is REQUIRED vs not needed

REQUIRED before the `throughput` and `tls` (CCM) dimensions: both are
connection-heavy and hit the port-range, TIME_WAIT, and fd ceilings directly.

NOT needed for `fanout` or `soak`: the fan-out breaking point is the server's
subscription queue (a true server limit, not a host limit), and soak runs at a
fixed sub-knee load where the default host limits are not the constraint.

### The ulimit source-vs-execute distinction

`ulimit -n` only affects the invoking shell and its children; a child process
cannot raise its parent's limit. The script handles both modes:

- SOURCED (`. scripts/pretest-tune.sh`): applies the sysctls AND raises `ulimit -n`
  in the caller's shell, so the harness launched from that shell inherits it.
- EXECUTED (`bash scripts/pretest-tune.sh`, including `make stress-pretest`):
  applies the sysctls, then PRINTS the exact `ulimit -n 65536` line for the
  operator to run in their own run shell, plus an optional
  `/etc/security/limits.conf` snippet for a persistent hard-limit bump (which
  needs a re-login to take effect).

A make target runs in a child process, so `make stress-pretest` cannot set the
parent shell's ulimit; run the printed line in the shell you launch the harness
from, or `source` the script there.

When sourced, the script snapshots your shell options up front and restores them
on every exit path, so sourcing it does not leave your shell in `set -euo pipefail`
(where the next non-zero command could otherwise kill an interactive session).

## Architecture

### Server lifecycle (out of process)

Every run launches the real `sep2server` binary (built from the current worktree).
This is mandatory: in-process sharing (via `csiptest.BootServer`) contaminates
CPU and memory attribution between driver and server.

The harness:
1. Generates a run-scoped CA + N device certs in `<run-dir>/pki/` at startup.
2. Writes server cert/key/CA files to `<run-dir>/pki/server.*`.
3. Launches `sep2server serve` with env vars pointing at `<run-dir>/server-data/`
   and `SEP2_METRICS_ADDR=127.0.0.1:<metrics-port>`.
4. Health-probes `http://<metrics-addr>/metrics` until 200 (up to 10s).
5. Runs the load phase.
6. Sends SIGTERM, waits up to 10s for clean exit.
7. Renames `<run-dir>/server-data/` intact for post-mortem.

### PKI generation

The `internal/certs` package generates an ephemeral CA + one device cert per virtual
client. Each cert has a unique `HWSerialNum` of the form `STRESS-<seed>-<index>`.
The CA and device certs are written to `<run-dir>/pki/` before the server starts.
The server is given `SEP2_EXTRA_CLIENT_CAS=<ca.crt path>` so it trusts the generated
device certs. For each virtual client, the harness also registers the device with the
server (POST /edev) before the load phase; this ensures routes resolve.

### Load driver

`cmd/sep2loadgen` (in this repo) is a lean purpose-built driver. Per virtual client:
one `*http.Client` with its own `*tls.Config` presenting the client's device cert.
Client behavior: a seeded per-client `math/rand` instance selects endpoints in a
deterministic round-robin from the dimension's endpoint set. No inverter state
machine, no HMI, no timesync goroutines.

Ramp controller: spawns `RAMP_RATE` clients per second until `CLIENTS` is reached
(or until a breaking-point criterion fires). With `CLIENTS=0` (open-ended), ramp
continues until criterion fires.

### Subscription fan-out (real sim cohort)

For the fan-out dimension, a small cohort of real `inverterclient` binaries (5 per
run, configurable via `REAL_SIMS`) run alongside the lean load driver. They exercise
the full subscription/notification round trip. The lean driver drives throughput;
the real sims drive subscription churn.

## Per-Dimension Parameters

| DIM | Description |
|---|---|
| `throughput` | GET /dcap+/tm+/edev round-robin, zero think time, GCM. Ramp until p99 > 500ms or error rate > 1%. |
| `fanout` | Fix HTTP load at 50% of throughput knee. Ramp subscriptions. Break: queue_full rate > 0. |
| `soak` | Fixed load 2h (CI: 10min). Break: monotonic growth in heap/goroutine/fd across 3 windows. |
| `tls` | CCM-8 mode, new connection per request (no keepalive). Break: handshake error rate > 0.1% or p99 > 1s. |

## Subscription Worker Sweep (IEEESRV-008 dependency)

When `SEP2_SUBSCRIPTION_WORKERS` and `SEP2_SUBSCRIPTION_QUEUE_SIZE` are set in the
harness environment, they are passed through to the `sep2server` process. The harness
does not set them by default (characterize the shipped 4/256 defaults first). To sweep:

```
SEP2_SUBSCRIPTION_WORKERS=8 SEP2_SUBSCRIPTION_QUEUE_SIZE=512 make stress-test DIM=fanout
```

IEEESRV-008 must land and deploy before the sweep runs. The harness wire-up is in
`scripts/stress.sh` (the env vars are forwarded to the server process unconditionally
when present).

## Result Artifacts

```
results/<YYYYMMDD-HHMMSS>-<dim>-<clients>/
  params.json               # all run parameters including SEED, host limits applied
  server.log                # sep2server stdout+stderr
  loadgen.log               # load generator stdout
  metrics-series.jsonl      # one JSON line per /metrics scrape (5s interval)
  client-latency.jsonl      # one JSON line per request: {t_send,t_recv_ms,status,endpoint,client_id}
  breaking-point.json       # verdict: dimension, criterion, value_at_break, time_elapsed_s,
                            #          host_limited (bool), at_clients
  at-breaking-point/        # captured at first criterion breach
    heap.prof
    goroutines.txt
    metrics-final.txt
  pki/                      # generated CA + per-client certs (for re-use / audit)
  server-data/              # sep2server data dir preserved post-teardown
```

## Environment Variables Reference

| Variable | Source | Description |
|---|---|---|
| `CLIENTS` | harness | virtual client count (0 = open-ended, ramp until break) |
| `RAMP_RATE` | harness | clients added per second (default 5) |
| `DURATION` | harness | max run duration in seconds (0 = unlimited) |
| `DIM` | harness | dimension: throughput/fanout/soak/tls |
| `TARGET_HOST` | harness | server host (default 127.0.0.1) |
| `TARGET_PORT` | harness | server port (default 8443) |
| `CCM` | harness | enable CCM-8 on server and clients (default false) |
| `SEED` | harness | deterministic RNG seed (default 42) |
| `REAL_SIMS` | harness | count of real inverterclient instances for fan-out (default 0) |
| `SCRAPE_INTERVAL` | harness | seconds between /metrics scrapes (default 5) |
| `SEP2_SUBSCRIPTION_WORKERS` | IEEESRV-008 | forwarded to server; unset = server default (4) |
| `SEP2_SUBSCRIPTION_QUEUE_SIZE` | IEEESRV-008 | forwarded to server; unset = server default (256) |

## Changes from Initial Design

1. PKI generation happens per-run (not a shared pre-generated set) to keep runs
   hermetic and the worktree clean. The SEED makes cert identities reproducible.
2. Device registration (POST /edev) is done in the harness setup phase before
   load starts, so the server has a valid EndDevice for each virtual client.
   Without registration, /edev/{id} routes 404 and the load would not exercise
   real handler paths.
3. The `REAL_SIMS` cohort is wired at the fan-out dimension only, not throughput
   or soak, to avoid the fan-out from confounding the throughput baseline.
