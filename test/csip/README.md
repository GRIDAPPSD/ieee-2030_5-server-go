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
├── README.md                   this file
├── handshake_test.go           smoke: in-process spec server + SunSpec V1.2 client
├── testdevice_handshake_test.go  smoke: in-process spec server + self-minted test device PKI (#75)
├── comm_*, core_*, basic_*     named CSIP conformance tests
├── csiptest/                   helper: BootServer + Client + Load (#51, #52, #53)
└── fixtures/
    ├── *.yaml                  topology fixtures (committed)
    └── sunspec/                (gitignored) CSIP §6.11-compliant SunSpec PKI
        ├── cert.pem
        ├── key.pem
        └── roots.pem
```

The self-minted test device PKI lives under `testdata/csip-pki/testdevice/`
(committed) — see that directory's `README.md` for device LFDI/SFDI and
regeneration commands. The test device cert is CSIP §6.11-compliant.

## Running the harness

The Make target invokes the test driver, which boots an in-process spec
server in CCM mode and runs the smoke against it:

```sh
make test-csip-server
```

A companion `make test-csip-client` target exists but is a stub today —
the inverter client cannot yet negotiate `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`
(see [#21](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/21)).

## Fixture provisioning

The SunSpec V1.2 test PKI is **not** committed to this repo. It is
gitignored under `test/csip/fixtures/`. The materials are CSIP §6.11
compliant (critical HardwareModuleName SAN, empty Subject, indefinite
notAfter, etc.) and are stored separately from the source tree.

The harness resolves fixture paths in this order, per file:

1. Env var (`CSIP_SUNSPEC_CERT`, `CSIP_SUNSPEC_KEY`, `CSIP_SUNSPEC_ROOTS`).
2. Default — `test/csip/fixtures/sunspec/{cert,key,roots}.pem`.

If a path resolves to nothing, the SunSpec-backed tests **skip cleanly**
with a pointer back to this README. Fresh clones never fail.

Absence is the only fault that skips. A path that resolves to an empty
file, a directory, or a file carrying no complete PEM block is material
that is present and wrong, and that **fails** whether or not the gate
below is armed: a truncated download must not be able to report the same
green as a clean checkout.

### Demanding the fixtures: `CSIP_SUNSPEC_REQUIRED`

A skip is the right default for a fresh clone and the wrong default for a
run that is supposed to supply the material: absent fixtures would skip
and the run would still report green. Set `CSIP_SUNSPEC_REQUIRED=1` and
absence becomes a hard failure instead:

```sh
CSIP_SUNSPEC_REQUIRED=1 make test-csip
```

Unset and empty are the only values that disarm it. Anything that is not
a boolean is an error rather than a false, so `CSIP_SUNSPEC_REQUIRED=ture`
cannot silently disarm the gate. This is the same contract as
`SEP2_WADL_REQUIRED` (see `test/conformance/README.md`).

`TestCSIPFixtureGateArmed` is the canary to look for in a log: PASS means
the cert and key agree, the leaf is unexpired, every certificate in
`roots.pem` parses, and the leaf verifies against that bundle through the
same hook the server applies, so the SunSpec-backed procedures ran
against material a handshake could use. It scopes that material only;
`TestDeviceHandshake` proves mTLS against the committed test-device PKI
either way.

In CI the material arrives through `.github/actions/supply-csip-pki`,
which reassembles it from a repository secret into `RUNNER_TEMP` outside
the checkout, streams each archive member to a path it names itself so no
member can be a symlink, checks the archive against a pinned digest,
verifies that the cert and key agree, and then exports the three path
variables plus `CSIP_SUNSPEC_REQUIRED=1`.

Arming needs three repository settings and no code change:

| Setting | Kind | Purpose |
| --- | --- | --- |
| `CSIP_SUNSPEC_PKI_TAR_XZ_B64` | secret | `base64(xz(tar of cert.pem key.pem roots.pem))` |
| `CSIP_SUNSPEC_PKI_PROVISIONED` | variable | `true` once the secret exists |
| `CSIP_SUNSPEC_PKI_SHA256` | variable | sha256 of the decoded tar |

The digest is not knowable before the first run, so a run armed without it
fails and reports the digest it observed in the job's step summary; set the
variable to that value and re-run. A secret present while
`CSIP_SUNSPEC_PKI_PROVISIONED` is not `true` is a contradiction and also
fails, which is what keeps the switch from being a silent off-ramp.

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
  - [#21](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/21)
    — move the inverter client onto vendored `gotls` for CCM-8.
  - [#22](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/22)
    — `SEP2_CSIP_STRICT=true` server mode that drops the GCM fallback.
- The 25 SunSpec V1.2 conformance requirements. Those will be added as
  sibling tests once the scaffold lands.

## Related

- ADR-001 — CSIP lives in the base repo as an opt-in profile.
- #7 (PR #16) — cert nil-deref guard + redundant-read removal.
- #17 (PR #18) — CSIP §6.11 cert generator + TLS verify hook.
- #19 (PR #20) — README "Running in CSIP mode" docs.
- #21 — CCM-8-capable inverter client.
- #22 — `SEP2_CSIP_STRICT=true` server mode.
- #23 — this scaffold + smoke.
