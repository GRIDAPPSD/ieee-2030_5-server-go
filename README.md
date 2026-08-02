# ieee-2030_5-server

[![ci](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/ci.yml)
[![CodeQL](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/codeql.yml)
[![Go](https://img.shields.io/badge/go-1.26.3-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Battelle%20BSD-blue)](LICENSE)

This repo is private: the workflow badges above render for viewers with
repository access and show nothing for anonymous visitors. No release
badge yet; this repo has not cut a tagged release.

Go implementation of IEEE 2030.5 (SEP2), the smart energy profile spec for utility-to-DER communication. Ships a server binary (`sep2server`) with TLS/mTLS, CSIP V1.2 cipher-suite support (GCM and CCM-8), an admin dashboard, and cert-generation subcommands.

> **Server of record for the IEEE 2030.5 (SEP2) Go implementation.** The original public reference is at `github.com/GRIDAPPSD/ieee-2030_5-go`; that GitHub repo remains accessible but active development lands here on GitLab. The `ieee-2030_5-core` library is consumed via a local-path `replace` directive in `go.mod` during the pre-1.0 window; CI clones core into the expected path before building.

## Documentation

- **[docs/2030_5.md](docs/2030_5.md)**: full IEEE 2030.5 (SEP2) reference and function-set support table.
- **[docs/csip.md](docs/csip.md)**: CSIP V1.2 profile: cert profile, cipher, conformance harness, operator profiles.
- **[docs/admin.md](docs/admin.md)**: admin surface: dashboard, login flow, auth model, mTLS cert flow, admin features.
- **[docs/admin-listener.md](docs/admin-listener.md)**: admin listener TLS posture matrix (plain HTTP behind Caddy, direct HTTPS, self-signed fallback).
- **[docs/glossary.md](docs/glossary.md)**: acronyms and protocol terms (GCM, CCM-8, SFDI/LFDI, FSA, PEN, etc.).
- **[VENDORED.md](VENDORED.md)**: provenance for the vendored CCM AEAD and the `crypto/tls` fork that registers the CCM-8 cipher.

## Prerequisites

- Go 1.26 or newer
- GNU make
- `golangci-lint` (optional, for `make lint`)
- Node.js plus `npx` if you intend to run the Playwright E2E suite

The Go module path is `github.com/GRIDAPPSD/ieee-2030_5-go`.

## Build

```bash
make build      # bin/sep2server
```

Binaries land in `bin/`.

## Run

```bash
make run           # builds, generates certs, serves on :8443; admin on 127.0.0.1:8444 (loopback)
make run-ccm       # same, with CCM-8 cipher suite (CSIP-conformant)
make run-full      # CCM-8 plus mDNS plus admin dashboard
```

[CCM-8](docs/glossary.md) vs [GCM](docs/glossary.md) and the operator
profiles (`run-testdevice`, `run-sunspec`) are covered in
[docs/csip.md](docs/csip.md). `make run` enables the
admin dashboard on `127.0.0.1:8444` (loopback) by default; for off-box
access set `SEP2_ADMIN_LISTEN=0.0.0.0:8444`. See
[docs/admin-listener.md](docs/admin-listener.md) for the bind matrix and
[docs/admin.md](docs/admin.md) for the dashboard.

The `SEP2_USE_CORE_ROUTER` env var selects the router at boot time. The
default (unset) uses the in-tree protocol router. Set `SEP2_USE_CORE_ROUTER=1`
to opt into the router provided by `ieee-2030_5-core`; the startup log records
which value was read and which path was taken.

## Test

```bash
make test        # go test ./...
make test-cover  # writes coverage.out and coverage.html
make test-race   # race detector
make test-e2e    # Playwright dashboard suite (under e2e/)
```

`make lint` runs `golangci-lint`. `make vet` runs `go vet`. The CSIP
conformance harness (`make test-csip`, `make test-csip-hooks`,
`make test-csip-cover`, `make coverage-gate`) is described in
[docs/csip.md](docs/csip.md).

## Cert generation

```bash
make certs
```

Generates the CA, server, admin, and a sample device certificate into
`certs/` using the cert subcommands built into the `sep2server` binary.
The four artifacts and what each is for are described in
[docs/admin.md](docs/admin.md). The CSIP-compliant device-cert profile
(`HardwareModuleName` SAN, ECDSA P-256, indefinite `notAfter`, etc.) is
described in [docs/csip.md](docs/csip.md).

## Repository tour

- `cmd/sep2server`: server binary entry point and cert subcommands (`serve`, `certs generate-*`, `version`)
- `internal/`: auth and tickets, cert generation, env config, mDNS discovery, XML codec, per-function-set handlers, paging, router and dashboard, subscription plumbing, and TLS bits
- `pkg/sep2`: public Go types for every IEEE 2030.5 resource (DER, FSA, metering, mirror, subscription, etc.)
- `pkg/store`: `Store` interface plus an in-memory implementation
- `e2e/`: Playwright dashboard tests
- `docs/`: protocol, CSIP, admin, and listener references (see above)

## Interop

`make test-epri` runs the EPRI C client against a running server in CCM
mode. It expects the EPRI client checked out at
`~/repos/IEEE-2030.5-Client` and the server already running
(`make run-ccm`). `make build-epri` builds the EPRI client locally.

## License

Battelle BSD (modified BSD with a Battelle name-use clause and DOE disclaimer). Copyright Battelle Memorial Institute, operated by Battelle for the U.S. Department of Energy under Contract DE-AC05-76RL01830. See `LICENSE`.
