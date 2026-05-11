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

The device cert is generated with the CSIP-required `HardwareModuleName` SAN; see `Running in CSIP mode` below for the flags involved and how to issue device certs outside the `make certs` path.

## Running in CSIP mode

"CSIP mode" today is a property of how you start the binaries, not a feature flag. A `sep2server` plus `inverterclient` pair is operating in CSIP mode when:

1. Device certs carry a critical `HardwareModuleName` SAN encoding the manufacturer's PEN OID and the hardware serial (RFC 4108 OID `1.3.6.1.5.5.7.8.4`).
2. The TLS stack negotiates `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8` (code point `0xc0ae`).
3. Mutual TLS is enforced, with both peers presenting CSIP-compliant device or server certs.

### 1. Generate CSIP-compliant device certs

`sep2server certs generate-device` requires two flags with no defaults:

- `--hw-serial <serial>` — the device's hardware serial number.
- `--hw-type <pen-oid>` — the manufacturer's IANA Private Enterprise Number as a dot-separated OID (`1.3.6.1.4.1.<PEN>`, optionally with additional model arcs).

Example (replace `<PEN>` with your registered IANA PEN):

```bash
bin/sep2server certs generate-device \
  --ca certs/ca.crt --ca-key certs/ca.key \
  --hw-serial INV-001 \
  --hw-type 1.3.6.1.4.1.<PEN> \
  --name device --out certs
```

Omitting either flag fails fast — the command exits non-zero with `error: -hw-serial is required (CSIP §6.2 HardwareModuleName SAN)` (or the matching `-hw-type` message) and writes no files. `make certs` already passes both flags, so the in-repo path is unaffected.

The resulting cert profile matches IEEE 2030.5-2018 §6.11 / CSIP V1.2:

- ECDSA P-256 keys, `ecdsa-with-SHA256` signature
- Indefinite `notAfter` (`9999-12-31 23:59:59 GMT`)
- Critical `keyUsage` of `digitalSignature` + `keyAgreement`
- Critical `certificatePolicies` from the IEEE 2030.5 arc `1.3.6.1.4.1.40732.1`
- Critical SAN carrying a single `otherName` of type `id-on-hardwareModuleName` (`1.3.6.1.5.5.7.8.4`) bearing the PEN OID and serial
- Empty Subject (LFDI is the first 160 bits of `SHA-256(DER-cert)`; SFDI is derived from the LFDI per §6.3)

### 2. Start the server with the CSIP-mandated cipher

```bash
make run-ccm    # CCM-8 enabled (with GCM fallback for compat)
make run-full   # same plus mDNS plus admin dashboard
```

Both targets build the server against the vendored `internal/tls/gotls` fork, which registers the CCM-8 cipher suite that upstream Go does not ship, then call `make certs` and serve on `:8443` with mutual TLS required.

The CCM server's cipher list is **CCM-8 first, then GCM as a fallback** so connections from clients that do not implement CCM-8 still complete the handshake. See `internal/tls/ccmserver.go` (the `CipherSuites` slice in `NewCCMServerConfig`). The cipher that is actually negotiated depends on what the client offers:

- A CCM-capable client (e.g., the EPRI IEEE-2030.5-Client invoked by `make test-epri`) will negotiate `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`.
- A stdlib-TLS client (including `make run-inverter` — see section 3) will negotiate the GCM fallback, not CCM-8.

So `make run-ccm` on its own does **not** guarantee a CSIP-compliant cipher on the wire. CSIP cipher conformance is conditional on (a) the client supporting CCM-8 and (b) verifying the handshake actually negotiated CCM-8 — check the server log line that prints the negotiated cipher on every accepted handshake (see section 4).

`make run` uses GCM only (no CCM-8 offered at all). Fine for local testing; not spec-compliant.

A future `csip-strict` server mode that drops the GCM fallback — so any non-CCM client fails the handshake instead of silently falling back — is on the backlog but not implemented.

### 3. Run the inverter client against a CSIP server

```bash
make run-inverter
```

`make run-inverter` points `bin/inverterclient` at `https://localhost:8443` with the device cert and CA from `certs/`. The client presents its CSIP-compliant device cert; the server's TLS verifier acknowledges the critical `HardwareModuleName` SAN automatically. (The verifier acknowledges the SAN so the handshake succeeds; it does not yet enforce a full CSIP function-set profile — see `Future: CSIP enforcement` below.)

**Cipher caveat — `run-inverter` is not a CCM-8 conformance demo.** The inverter client uses the Go standard library `crypto/tls` package (see `internal/inverter/client.go`), which does not implement the CCM-8 cipher suite. Against `make run-ccm`, the inverter client will silently land on the server's GCM fallback (and against a hypothetical strict-CCM server with no fallback, it would fail the handshake outright). What `run-inverter` does exercise honestly:

- The full IEEE 2030.5 protocol path: registration, DER capability/settings/status reporting, DefaultDERControl retrieval, metering via mirror usage points, scenario-driven behavior.
- Mutual TLS with the CSIP `HardwareModuleName` SAN profile.
- The server-side verifier and handler stack.

What it does **not** demonstrate: CSIP cipher conformance (CCM-8 on the wire).

For an end-to-end test that actually negotiates CCM-8 against the server, use the EPRI IEEE-2030.5-Client via `make test-epri` (driver: `scripts/test-epri-client.sh`). That client implements CCM-8 and will negotiate it as the primary cipher.

Moving the inverter client onto the vendored `internal/tls/gotls` stack so it can negotiate CCM-8 itself is on the backlog but not implemented.

`make run-scenario SCENARIO=<name>` runs the simulator with a non-default scenario (`voltvar`, `freqdroop`, `ridethrough`, etc.); `make list-scenarios` prints the full set.

### 4. Confirming you are in CSIP mode

**Cert profile.** Inspect the device cert:

```bash
openssl x509 -in certs/device.crt -text -noout
```

You should see:

- `Signature Algorithm: ecdsa-with-SHA256`
- `NIST CURVE: P-256`
- `Not After : Dec 31 23:59:59 9999 GMT`
- `X509v3 Key Usage: critical` with `Digital Signature, Key Agreement`
- `X509v3 Certificate Policies: critical` with a policy under `1.3.6.1.4.1.40732.1`
- `X509v3 Subject Alternative Name: critical` containing `othername: Hardware Module Name` (OpenSSL 3 prints the inner serial as `<unsupported>` — that is cosmetic; the otherName OID is the RFC 4108 `1.3.6.1.5.5.7.8.4`)

**Cipher.** The server logs the negotiated cipher on every accepted handshake. Look for `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`.

### Future: CSIP enforcement

ADR-001 (in the upstream Knowledge workspace) calls for an `SEP2_CSIP=true` env flag and an `internal/csip/` package that enforces required function sets, cert profile, and cipher at runtime, plus a `test/csip/` directory carrying the 25 SunSpec V1.2 conformance cases. **That work has not landed.** Until it does, CSIP compliance is a runtime property of the commands above, not a flag you toggle.

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
