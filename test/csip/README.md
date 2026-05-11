# CSIP Conformance Harness

This directory holds the CSIP (SunSpec Common Smart Inverter Profile V1.2)
conformance test suite for `ieee-2030_5-go`. Today the suite is one
end-to-end smoke test; the 25 SunSpec V1.2 conformance requirements land
incrementally on top of this scaffold.

Placement decision: see [ADR-001](../../README.md) — CSIP lives in the
base repo as an opt-in profile.

## Layout

```
test/csip/
├── README.md            this file
├── handshake_test.go    smoke: in-process spec server + SunSpec V1.2 client
└── fixtures/
    └── sunspec/         (gitignored) CSIP §6.11-compliant device PKI
        ├── cert.pem
        ├── key.pem
        └── roots.pem
```

## Running the harness

The Make target invokes the test driver, which boots an in-process spec
server in CCM mode and runs the smoke against it:

```sh
make test-csip-server
```

A companion `make test-csip-client` target exists but is a stub today —
the inverter client cannot yet negotiate `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`
(see [#21 / IEEE-019](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/21)).

## Fixture provisioning

The SunSpec V1.2 test PKI is **not** committed to this repo. It is
gitignored under `test/csip/fixtures/`. The materials are CSIP §6.11
compliant (critical HardwareModuleName SAN, empty Subject, indefinite
notAfter, etc.) and are stored separately from the source tree.

The harness resolves fixture paths in this order, per file:

1. Env var (`CSIP_SUNSPEC_CERT`, `CSIP_SUNSPEC_KEY`, `CSIP_SUNSPEC_ROOTS`).
2. Default — `test/csip/fixtures/sunspec/{cert,key,roots}.pem`.

If a path resolves to a non-existent file, the smoke test **skips
cleanly** with a pointer back to this README. Fresh clones never fail.

### Option A — environment variables

Point the env vars at an external location:

```sh
CSIP_SUNSPEC_CERT=/path/to/sunspec/cert.pem \
CSIP_SUNSPEC_KEY=/path/to/sunspec/key.pem \
CSIP_SUNSPEC_ROOTS=/path/to/sunspec/roots.pem \
make test-csip-server
```

### Option B — symlink fixtures under `test/csip/fixtures/sunspec/`

Drop the three PEM files at the default location:

```sh
mkdir -p test/csip/fixtures/sunspec
cp /path/to/sunspec/{cert,key,roots}.pem test/csip/fixtures/sunspec/
make test-csip-server
```

Either path works; pick whichever fits your workflow.

## What the smoke test asserts

`TestCSIPHandshakeWithSunSpecDeviceCert`:

- **Cert path:** the server's `verifyClientCertWithHardwareModuleSAN`
  hook accepts the SunSpec leaf's critical HardwareModuleName SAN
  (RFC 4108 OID `1.3.6.1.5.5.7.8.4`).
- **Chain walk:** the leaf validates against `roots.pem` (loaded into
  `ClientCAs` via `NewCCMServerConfig`).
- **Handshake:** mTLS handshake completes (`HandshakeComplete == true`).
- **Application layer:** `GET /dcap` returns HTTP 200 and a parseable
  `DeviceCapability` XML body.

### What it does NOT assert (yet)

- **Cipher suite.** The test logs the negotiated cipher but does not
  require CCM-8. The stdlib `http.Client` used here cannot offer the
  CSIP-mandatory cipher; tightening this assertion is gated on:
  - [IEEE-019 / #21](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/21)
    — move the inverter client onto vendored `gotls` for CCM-8.
  - [IEEE-020 / #22](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/22)
    — `SEP2_CSIP_STRICT=true` server mode that drops the GCM fallback.
- The 25 SunSpec V1.2 conformance requirements. Those will be added as
  sibling tests once the scaffold lands.

## Related

- ADR-001 — CSIP lives in the base repo as an opt-in profile.
- IEEE-008 (PR #16) — cert nil-deref guard + redundant-read removal.
- IEEE-017 (PR #18) — CSIP §6.11 cert generator + TLS verify hook.
- IEEE-018 (PR #20) — README "Running in CSIP mode" docs.
- IEEE-019 (#21) — CCM-8-capable inverter client.
- IEEE-020 (#22) — `SEP2_CSIP_STRICT=true` server mode.
- IEEE-021 (#23) — this scaffold + smoke.
