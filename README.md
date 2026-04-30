# ieee-2030_5-go

Go implementation of IEEE 2030.5 (SEP2), the smart energy profile spec for utility-to-DER communication. Ships a server, an inverter simulator client, and an admin dashboard.

## Prerequisites

- Go 1.25 or newer
- GNU make
- `golangci-lint` (optional, for `make lint`)
- Node.js plus `npx` if you intend to run the Playwright E2E suite

The Go module path is `github.com/GRIDAPPSD/ieee-2030_5-go`.

## Build

```bash
make build      # bin/sep2server only
make build-all  # bin/sep2server and bin/inverterclient
```

Binaries land in `bin/`.

## Run

```bash
make run           # builds, generates certs, serves on :8443 (GCM mode)
make run-ccm       # same, with CCM-8 cipher suite (spec compliant)
make run-full      # CCM-8 plus mDNS plus admin dashboard on :8444
make run-inverter  # inverter simulator against https://localhost:8443
```

`make run-scenario SCENARIO=voltvar` runs the simulator with a specific scenario. `make list-scenarios` prints available scenarios.

## Test

```bash
make test        # go test ./...
make test-cover  # writes coverage.out and coverage.html
make test-race   # race detector
make test-e2e    # Playwright dashboard suite (under e2e/)
```

`make lint` runs `golangci-lint`. `make vet` runs `go vet`.

## Cert generation

```bash
make certs
```

Generates the CA, server, admin, and a sample device certificate into `certs/` using the cert subcommands built into the `sep2server` binary.

## Repository tour

- `cmd/sep2server` — server binary entry point and cert subcommands (`serve`, `certs generate-*`, `version`)
- `cmd/inverterclient` — inverter simulator CLI with scenarios and an HMI port
- `internal/` — auth and tickets, cert generation, env config, mDNS discovery, XML codec, per-function-set handlers, simulator engine, paging, router and dashboard, subscription plumbing, and TLS bits
- `pkg/sep2` — public Go types for every IEEE 2030.5 resource (DER, DRLC, FSA, metering, mirror, subscription, etc.)
- `pkg/store` — `Store` interface plus an in-memory implementation
- `e2e/` — Playwright dashboard tests

See `projects/ieee-2030_5-go/CLAUDE.md` in the Knowledge workspace for a deeper layout breakdown.

## Vendored code

Two third-party items live in `internal/tls/`:

- `internal/tls/ccm/` is copied from `pion/dtls/v3/pkg/crypto/ccm` (MIT). It gives us RFC 3610 CCM without pulling in DTLS or WebRTC.
- `internal/tls/gotls/` is a fork of the Go standard library `crypto/tls` (BSD-3) with roughly 30 lines added to register the CCM-8 cipher suite (`TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`, code point 0xc0ae) that IEEE 2030.5 mandates.

`VENDORED.md` carries the full provenance, the diff against upstream, and the rationale.

## Interop

`make test-epri` runs the EPRI C client against a running server in CCM mode. It expects the EPRI client checked out at `~/repos/IEEE-2030.5-Client` and the server already running (`make run-ccm`). `make build-epri` builds the EPRI client locally.

## License

BSD-2-Clause-style (PNNL/Battelle). See `LICENSE`.
