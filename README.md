# ieee-2030_5-server

[![ci](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/ci.yml)
[![CodeQL](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/github-code-scanning/codeql/badge.svg)](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/github-code-scanning/codeql)
[![Go](https://img.shields.io/badge/go-1.26.3-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Battelle%20BSD-blue)](LICENSE)

No release badge yet; this repo has not cut a tagged release.

Go implementation of IEEE 2030.5 (SEP2), the smart energy profile spec for utility-to-DER communication. Ships a server binary (`sep2server`) with TLS/mTLS, CSIP V1.2 cipher-suite support (GCM and CCM-8), an admin dashboard, and cert-generation subcommands.

> **Server of record for the IEEE 2030.5 (SEP2) Go implementation.** The original public reference is at `github.com/GRIDAPPSD/ieee-2030_5-go`; that GitHub repo remains accessible but active development lands here on GitLab. The `ieee-2030_5-core` library is consumed via a local-path `replace` directive in `go.mod` during the pre-1.0 window; CI clones core into the expected path before building.

> **EndDevice access control.** Every `/edev/{id}` route answers only the device whose certificate LFDI is stored on that EndDevice, or an aggregator provisioned to manage it, and `GET /edev` lists only those devices. Manager pairs are provisioned on the utility side; the server binary wires none yet, so aggregators have self access only until admin-plane provisioning lands ([#440](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/issues/440)). Embedders that relied on any certificate reaching any EndDevice must change. See [docs/enddevice-access.md](docs/enddevice-access.md).

## Documentation

- **[docs/2030_5.md](docs/2030_5.md)**: full IEEE 2030.5 (SEP2) reference and function-set support table.
- **[docs/enddevice-access.md](docs/enddevice-access.md)**: who may reach an EndDevice, what an aggregator's manager pair grants, denial statuses, and wiring `Stores.EndDeviceManagers`.
- **[docs/csip.md](docs/csip.md)**: CSIP V1.2 profile: cert profile, cipher, conformance harness, operator profiles.
- **[docs/admin.md](docs/admin.md)**: admin surface: dashboard, the browser procedure for reaching the admin UI from another machine, login flow, auth model, mTLS cert flow, admin features.
- **[docs/operator-guide.md](docs/operator-guide.md)**: operator walkthrough of the admin dashboard: screenshots of each screen and the steps to reach them.
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
admin dashboard on `127.0.0.1:8444` (loopback) by default. Off-box access
needs both `SEP2_ADMIN_LISTEN=0.0.0.0:8444` and
`SEP2_ADMIN_ALLOW_NON_LOOPBACK=true`; the listen address alone is refused at
startup. See
[docs/admin-listener.md](docs/admin-listener.md) for the bind matrix and
[docs/admin.md](docs/admin.md) for the dashboard.

The `SEP2_USE_CORE_ROUTER` env var selects the router at boot time. The
default (unset) uses the in-tree protocol router. Set `SEP2_USE_CORE_ROUTER=1`
to opt into the router provided by `ieee-2030_5-core`; the startup log records
which value was read and which path was taken.

Subscription `notificationURI`s are checked when a Subscription is created and
again on every delivery. Loopback, link-local, unspecified, local multicast, and
cloud metadata destinations are refused; private ranges are allowed. `SEP2_NOTIFICATION_ALLOW_LOOPBACK=true`
allows loopback for a test receiver on the same host and must not be set in
production. See [docs/csip.md](docs/csip.md#notification-destinations).

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

## Local CI gate

```bash
make ci-local                # human entry point: run the gates, in order
scripts/ci-local/ci-local.sh # same thing, invoked directly (see below)
make ci-local-drift-check    # fail if a workflow invokes a make target ci-local does not run
```

`scripts/ci-local/ci-local.sh` runs vet, build, lint, the test suite, and
the CSIP conformance harness from one entry point, in a tri-state exit
contract: `0` every gate passed with nothing skipped, `1` a precondition
refused to run or a gate failed (fail-fast: stops at the first failure and
names it), `2` every gate passed or skipped but at least one skipped
(partial coverage). A prerequisite-dependent gate (the CSIP suite resolves
its SunSpec V1.2 test PKI env-var-first, then from
`test/csip/fixtures/sunspec/`, gitignored and provisioned out of band; see
[test/csip/README.md](test/csip/README.md); the frontend drift check needs
a Node toolchain; `golangci-lint` is optional) reports `SKIPPED` rather
than running against an absent prerequisite.

**`make ci-local` cannot carry that tri-state.** GNU Make maps any
non-zero recipe exit to make's own exit `2`, so a real gate failure and a
routine skip both surface to `make` as the same code; `make ci-local exit
2` does not tell you which happened. `make ci-local` stays the convenient
human entry point, but a script or CI job that needs the real tri-state
either invokes `scripts/ci-local/ci-local.sh` directly, or reads the final
`CI_LOCAL_RESULT=PASS|FAIL|PARTIAL` line the script prints on every path,
which `make` passes through on stdout unchanged regardless of which exit
code it maps to.

`make ci-local-drift-check` extracts the `make` targets every workflow file
under `.github/workflows/` invokes and fails if any of them is not in
`scripts/ci-local/lib/ci-local-targets.sh`, the same list `ci-local` runs;
`ci-local` runs this check first and refuses to proceed if it fails. A
second cross-check compares the anchored extraction against an unanchored
sweep and refuses (exit `3`) if they disagree, so a form the anchor cannot
structurally parse (chained after `&&`, a quoted `run:` scalar, a
flow-mapping one-liner) is a detected gap, not a silent one.

**A passing local run is not equivalent to a passing CI run.** Every run
prints what it does not cover: static analysis comes from the GRIDAPPSD
organization's [default code scanning
setup](https://github.com/GRIDAPPSD/ieee-2030_5-server-go/actions/workflows/github-code-scanning/codeql),
not a repository workflow, and has no local CLI wired into this repo;
`core-freshness.yml` tests against
`ieee-2030_5-core-go`'s main branch on a schedule, not against your change;
this runs against the current working tree, not a clean-room checkout from
`actions/checkout`; and the CI runner's network isolation is not
reproduced. Treat a clean `ci-local` run as a strong pre-push check, not as
proof CI will also pass.

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
