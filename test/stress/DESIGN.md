# IEEE 2030.5 Stress Test Harness: Design of Record

Cards: IEEESRV-007, IEEESRV-010
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

## Architecture

### Server lifecycle (out of process)

Every run launches the real `sep2server` binary (built from the current worktree).
This is mandatory: in-process sharing (via `csiptest.BootServer`) contaminates
CPU and memory attribution between driver and server.

For the **fanout dimension**, the server is built with `-tags csip_test_hooks` so
the `/test/mutations/stress-notify` endpoint is compiled in. This tagged binary is
written to `bin/sep2server-fanout` and is not committed. The token
(`SEP2_TEST_MUTATION_TOKEN`) is generated fresh per fanout run and is never committed.

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
server (POST /edev) before the load phase; this ensures routes resolve. The setup binary
writes `edev-manifest.json` recording the assigned edev IDs (extracted from the Location
header of each POST /edev response) for use in the subscribe step.

### Load driver

`cmd/sep2loadgen` (in this repo) is a lean purpose-built driver. Per virtual client:
one `*http.Client` with its own `*tls.Config` presenting the client's device cert.
Client behavior: a seeded per-client `math/rand` instance selects endpoints in a
deterministic round-robin from the dimension's endpoint set. No inverter state
machine, no HMI, no timesync goroutines.

Ramp controller: spawns `RAMP_RATE` clients per second until `CLIENTS` is reached
(or until a breaking-point criterion fires). With `CLIENTS=0` (open-ended), ramp
continues until criterion fires.

### Subscription fan-out (IEEESRV-010)

For the fan-out dimension, the harness exercises the full subscription/notification
round trip using only the lean load generator (no separate inverterclient binary is
needed or launched). The sequence is:

1. **Notification receiver**: `sep2loadgen` starts a plain-HTTP listener on an
   auto-assigned port. The server POSTs outbound notifications here. The URL is
   written to `<run-dir>/notify-receiver-url.txt`.

2. **Subscription registration**: after the receiver URL is known, the setup binary
   (`sep2stress-setup -subscribe`) POSTs to `POST /edev/{id}/sub` for each
   registered device, setting `notificationURI` to the receiver URL and
   `subscribedResource` to `/dcap` (the server-wide DeviceCapability resource,
   accessible to every device cert). All N subscribers watch this single shared
   resource so one mutation call fans to all N in a single `notifier.Notify()` call.

3. **Notification injection**: a goroutine inside `sep2loadgen` fires
   `POST /test/mutations/stress-notify` at `MUTATION_RATE_HZ` calls/second (default
   20 Hz). The mutation token authenticates via `X-CSIP-Test-Token`. Each call
   invokes `notifier.Notify(ctx, href, Changed)` inside the server, which enqueues
   work onto the 4-worker/256-queue pool and dispatches notification POSTs to all
   subscribers of that href.

4. **Measurement**: the criteria goroutine scrapes
   `sep2_subscription_notifications_total{outcome="queue_full"}` every 2 seconds.
   When that counter goes non-zero, the break criterion fires. The receiver count
   (`notify_delivered`) and queue-full count are both stamped into
   `breaking-point.json` at break time.

5. **Validity**: a valid fanout result requires `notify_delivered` to be roughly
   `S * mutation_count` at break time (where S is the subscriber count and
   `mutation_count = mutation_rate_hz * elapsed_seconds`). This is the N-wide
   fan-out invariant: each mutation call fans to all S subscribers, so total
   deliveries scale linearly with subscriber count. A `notify_delivered` near zero
   at break time means a misconfigured subscription (wrong href or receiver URL).
   A `notify_delivered` near `mutation_count` (not `S * mutation_count`) means
   subscriptions were on per-device hrefs rather than the shared `/dcap` resource.

**Build tag discipline**: only the fanout run builds and uses the
`csip_test_hooks`-tagged binary. All other dimensions use the standard (untagged)
binary. The `Makefile` `stress-test` target passes the tag only when `DIM=fanout`.
The token is generated fresh each run via `openssl rand -hex 24` (fallback:
`/dev/urandom + od`) and is never committed anywhere.

**REAL_SIMS removed**: an earlier design planned a real `inverterclient` binary
cohort controlled by `REAL_SIMS`. That parameter was accepted by the script but
never wired to any binary invocation. It has been removed. If a real-world client
fidelity cohort is wanted in the future, that belongs in a separate card.

## Per-Dimension Parameters

| DIM | Description |
|---|---|
| `throughput` | GET /dcap+/tm+/edev round-robin, zero think time, GCM. Ramp until p99 > 500ms or error rate > 1%. |
| `fanout` | Ramp subscribers. Inject notifications at 20 Hz via stress-notify mutation. Break: queue_full counter goes non-zero. |
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
  loadgen-stdout.txt        # raw loadgen stdout (fanout only; appended to loadgen.log)
  notify-receiver-url.txt   # notification receiver URL (fanout only)
  metrics-series.jsonl      # one JSON line per /metrics scrape (5s interval)
  client-latency.jsonl      # one JSON line per request: {t_send,t_recv_ms,status,endpoint,client_id}
  breaking-point.json       # verdict: dimension, criterion, value_at_break, time_elapsed_s,
                            #          host_limited (bool), at_clients,
                            #          notify_delivered, notify_queue_full (fanout only)
  at-breaking-point/        # captured at first criterion breach
    metrics-final.txt
  pki/
    edev-manifest.json      # assigned edev IDs from POST /edev Location headers
    ca.crt, ca.key
    server.crt, server.key
    device-00000.crt, ...   # per-client certs
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
| `SCRAPE_INTERVAL` | harness | seconds between /metrics scrapes (default 5) |
| `MUTATION_RATE_HZ` | fanout | stress-notify calls per second (default 20) |
| `MUTATION_TOKEN` | fanout | pre-set token; auto-generated when empty |
| `RECEIVER_PORT` | fanout | notification receiver port (0 = auto-assign) |
| `SEP2_SUBSCRIPTION_WORKERS` | IEEESRV-008 | forwarded to server; unset = server default (4) |
| `SEP2_SUBSCRIPTION_QUEUE_SIZE` | IEEESRV-008 | forwarded to server; unset = server default (256) |

## Changes from Initial Design

1. PKI generation happens per-run (not a shared pre-generated set) to keep runs
   hermetic and the worktree clean. The SEED makes cert identities reproducible.
2. Device registration (POST /edev) is done in the harness setup phase before
   load starts, so the server has a valid EndDevice for each virtual client.
   Without registration, /edev/{id} routes 404 and the load would not exercise
   real handler paths.
3. The `REAL_SIMS` variable (planned cohort of real inverterclient binaries) was
   never implemented and has been removed. IEEESRV-010 closes the fanout workload
   gap using the lean loadgen itself: subscription registration, a notification
   receiver, and mutation injection via the csip_test_hooks surface.
4. The fanout dimension now builds sep2server with `-tags csip_test_hooks` for the
   `/test/mutations/stress-notify` endpoint. The token is ephemeral per run.
