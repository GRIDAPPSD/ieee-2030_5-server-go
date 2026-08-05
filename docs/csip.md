# CSIP V1.2 reference

CSIP (Common Smart Inverter Profile, SunSpec V1.2) is the IEEE 2030.5
profile that California utilities and most current US deployments target.
This document describes how the server runs in CSIP mode, what conforms
today, and what does not. For the broader IEEE 2030.5 surface see
[`2030_5.md`](2030_5.md).

CSIP mode here is a property of how you start the binaries, not a feature
flag. A `sep2server` plus client pair is operating in CSIP mode when:

1. Device certs carry a critical `HardwareModuleName` SAN encoding the
   manufacturer's PEN OID and the hardware serial (RFC 4108 OID
   `1.3.6.1.5.5.7.8.4`).
2. The TLS stack negotiates
   [`TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`](glossary.md) (code point `0xc0ae`).
3. Mutual TLS is enforced; both peers present CSIP-compliant device or
   server certs.

## Function-set support (CSIP-required subset)

CSIP V1.2 §10 mandates a subset of IEEE 2030.5 function sets. This table
covers that subset. For the full IEEE 2030.5 surface this server
implements (including non-CSIP-mandated function sets) see
[`2030_5.md`](2030_5.md).

Status vocabulary: **Supported / Partial / Planned / Not Started.**
"Wire" means routes registered and handler returns the spec resource.
"Persistence" means writes survive a restart when `SEP2_DATA_DIR` is
set. "Admin visibility" means the resource is reachable from the
operator dashboard or admin API. CSIP does not require admin visibility
for protocol conformance, but operators usually do.

| Function set | Wire | Persistence | Admin visibility | Source |
|---|---|---|---|---|
| DeviceCapability (DCAP) | Supported | n/a (static) | — | [`handler/dcap.go`](../internal/handler/dcap.go) |
| Time | Supported | n/a (clock) | — | [`handler/time.go`](../internal/handler/time.go) |
| EndDevice + Registration | Supported | Supported (`SEP2_DATA_DIR`, #165) | Supported (`POST /api/devices`, #159; dashboard "Add End Device" form) | [`handler/edev.go`](../internal/handler/edev.go), [`handler/registration.go`](../internal/handler/registration.go) |
| FunctionSetAssignments | Supported | Supported | Supported (FSA management API + dashboard tree, #163) | [`handler/fsa.go`](../internal/handler/fsa.go), [`handler/admin_fsa.go`](../internal/handler/admin_fsa.go) |
| DER (DER, DERCapability, DERSettings, DERStatus, DERAvailability) | Supported | Supported | Partial (DER list visible on dashboard via the EndDevice rollup; no per-DER edit UI) | [`handler/der.go`](../internal/handler/der.go) |
| DERProgram + DERControl + DefaultDERControl | Supported | Supported (DERProgram disk-backed) | Partial (Send-DER-Control form is wired in HTML but the POST is a stub — see `dashboard_html.go` "TODO: POST to /api/der/controls") | [`handler/der.go`](../internal/handler/der.go) |
| DERCurve | Supported | In-memory only | — | [`handler/der.go`](../internal/handler/der.go) |
| Mirror UsagePoint | Supported | In-memory | Supported (dashboard MUP count) | [`handler/mirror.go`](../internal/handler/mirror.go) |
| Metering (UsagePoint, MeterReading, Reading, ReadingType) | Supported | In-memory | — | [`handler/metering.go`](../internal/handler/metering.go) |
| Subscription / Notification | Supported | Supported (`SEP2_SUBSCRIPTION_STORE_PATH` or `SEP2_DATA_DIR`) | — | [`handler/subscription.go`](../internal/handler/subscription.go), [`internal/subscription/`](../internal/subscription/) |
| LogEvent | Supported | In-memory | — | [`handler/log_event.go`](../internal/handler/log_event.go) |
| Response | Supported | In-memory | — | [`handler/messaging.go`](../internal/handler/messaging.go) |
| FlowReservation | Supported | In-memory | — | [`handler/flow_reservation.go`](../internal/handler/flow_reservation.go) |
| DRLC | Planned | — | — | No handler wired. `FunctionSetDRLC` constant exists in [`pkg/sep2/log_event.go`](../pkg/sep2/log_event.go); no open ticket yet. |
| Mode coverage gap (LVRT / HVRT / LFRT / HFRT, VoltWatt, FreqWatt, setGradW / setSoftGradW) | Partial | Partial | — | Tracked in [#140](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/140). Affects DERSettings + DefaultDERControl. |

## Cert profile (CSIP §6.11 / IEEE 2030.5 §6.11)

`make certs` generates a CSIP-compliant device cert by passing
`--hw-serial` and `--hw-type` to `sep2server certs generate-device`. Both
flags are required; the command exits non-zero with no files written if
either is missing.

```bash
bin/sep2server certs generate-device \
  --ca certs/ca.crt --ca-key certs/ca.key \
  --hw-serial INV-001 \
  --hw-type 1.3.6.1.4.1.<PEN> \
  --name device --out certs
```

Resulting profile:

- ECDSA P-256 keys, `ecdsa-with-SHA256` signature
- Indefinite `notAfter` (`9999-12-31 23:59:59 GMT`)
- Critical `keyUsage` of `digitalSignature` + `keyAgreement`
- Critical `certificatePolicies` from the IEEE 2030.5 arc
  `1.3.6.1.4.1.40732.1`
- Critical SAN carrying a single `otherName` of type
  `id-on-hardwareModuleName` (`1.3.6.1.5.5.7.8.4`) bearing the PEN OID
  and serial
- Empty Subject (LFDI is the first 160 bits of `SHA-256(DER-cert)`;
  SFDI is derived from LFDI per §6.3)

### Device-type policy OID (§6.11.7.2)

The `certificatePolicies` extension carries one of three IEEE 2030.5
device-type OIDs under the arc `1.3.6.1.4.1.40732.1`. The value is pure
spec metadata identifying the device class — no server code in this
implementation branches on it at runtime. Pick at issuance time and live
with it for the cert's lifetime.

| `--device-type` | OID | Constant | Meaning |
|---|---|---|---|
| `1` (default) | `1.3.6.1.4.1.40732.1.1` | `OIDDeviceTypeGeneric` | Fixed-install equipment provisioned at manufacture or commissioning (inverters, meters, gateways, EV chargers). The catch-all for assets that stay with one unit for its operating life. |
| `2` | `1.3.6.1.4.1.40732.1.2` | `OIDDeviceTypeMobile` | Equipment that may roam between utility service territories (mobile EVs, portable test rigs). The policy signals revocation/renewal handling may differ from a fixed asset. |
| `3` | `1.3.6.1.4.1.40732.1.3` | `OIDDeviceTypePostMfg` | Cert provisioned AFTER the device left the factory floor (field-installed certs, retrofits, replacements). Distinguishes "made it with this cert baked in" from "shipped without one and added later." |

For the typical operator workflow (issue a cert for a fixed inverter or
meter and register it through the admin dashboard), leave `--device-type`
at its default `1`. Pick `2` only when the device physically moves
between territories, and `3` only when issuing certs after the
manufacturing line.

### Hardware type — the manufacturer PEN (`--hw-type`)

CSIP V1.2 §6.2 mandates that every device cert carry a critical Subject
Alternative Name `otherName` of type `id-on-hardwareModuleName`
(RFC 4108, OID `1.3.6.1.5.5.7.8.4`). The SAN value is an ASN.1 SEQUENCE
of two fields:

```
HardwareModuleName ::= SEQUENCE {
    hwType   OBJECT IDENTIFIER,   -- the manufacturer PEN
    hwSerial OCTET STRING          -- the per-unit serial number
}
```

`--hw-type` supplies the first field: the manufacturer's
**IANA Private Enterprise Number (PEN)** as a dotted-decimal OID under
the arc `1.3.6.1.4.1.<PEN>`. Every organization that issues device
certificates registers its own PEN with IANA; registration is free and
permanent. The PEN is the equivalent of "vendor namespace" for ASN.1
OIDs — two devices from different vendors with the same serial number
get distinct identities because their hwType OIDs differ.

The SFDI and LFDI are SHA-256 hashes over the entire DER-encoded
certificate (§6.3), so the PEN bytes are part of the input. The PEN
does not appear in the SFDI/LFDI directly — it just makes the inputs
distinct, so a serial-number collision across vendors does not collide
on the wire.

| Setting | Value | When to use |
|---|---|---|
| **Default** | `1.3.6.1.4.1.40732.99` | Simulator, development, interop tests, PR demos. The `.99` suffix under the IEEE 2030.5 PEN is a project convention for "test/dev" certs — no IANA reservation, just a marker that the PEN is not a real vendor. `make certs` and `make new-device` both use it. |
| **Production** | Your organization's IANA-assigned PEN | Required for any cert that ships on real hardware. Look up at <https://www.iana.org/assignments/enterprise-numbers/> or apply for a new one (free, processed within ~1 week). Most large vendors registered theirs decades ago for SNMP. |
| **Multi-vendor fleet** | Each vendor uses its own PEN | The server treats all PENs identically — no per-vendor wiring, no allowlist. CSIP only requires the SAN exists and is well-formed. |

The server does NOT branch on the PEN at runtime: no validation against
an allowlist, no per-vendor handling. CSIP §6.2 mandates the SAN is
present and well-formed; that's it. The PEN is metadata, parallel to
the device-type policy OID above.

Inspect with:

```bash
openssl x509 -in certs/device.crt -text -noout
```

OpenSSL 3 prints the inner serial as `<unsupported>` — that is cosmetic;
the otherName OID is the RFC 4108 `1.3.6.1.5.5.7.8.4`.

Cert generation lives in [`internal/certs/`](../internal/certs/). The
LFDI/SFDI derivation and the CCM-8 cipher registration live in
[`internal/tls/`](../internal/tls/) (see also
[`VENDORED.md`](../VENDORED.md)).

## Cipher

Server-side TLS stacks:

| Target | TLS stack | Cipher list | CSIP-conformant on the wire? |
|---|---|---|---|
| `make run` | Go stdlib | [GCM](glossary.md) only | No |
| `make run-ccm`, `make run-full` | Vendored `internal/tls/gotls` | [CCM-8](glossary.md) first, GCM fallback (see footnote) | Conditional |

> **GCM-fallback footnote:** under `run-ccm` the server still accepts a
> non-CSIP client that lands on GCM. Strict-mode (handshake fails when
> the peer can't negotiate CCM-8) is tracked under
> [#22](https://github.com/GRIDAPPSD/ieee-2030_5-go/issues/22).
> Until that lands, conformance must be confirmed in the server's
> handshake log, not assumed from the target name.

Client-side stacks (paired with a running `run-ccm` server):

| Target | TLS stack | CSIP-conformant on the wire? |
|---|---|---|
| `make test-epri` | EPRI C client (CCM-capable) | Yes (negotiates CCM-8) |

The cipher list is built in
[`internal/tls/ccmserver.go`](../internal/tls/ccmserver.go); the cipher
itself is registered in [`internal/tls/gotls/`](../internal/tls/gotls/),
with the diff against upstream Go documented in
[`internal/tls/gotls/DIFF.md`](../internal/tls/gotls/DIFF.md).

## Conformance harness

The CSIP V1.2 conformance suite lives under [`test/csip/`](../test/csip/).
Two run modes:

- `make test-csip` — production code path (no build tag).
- `make test-csip-hooks` — same suite under the `csip_test_hooks` build
  tag, which compiles in the #27 mutation surface and #28
  time-advance hook. The mutation surface is an `httptest`-only mux
  registered by `RegisterMutationHandlers` in
  [`internal/server/test_mutations.go`](../internal/server/test_mutations.go);
  it is a no-op in production builds.

Coverage gate: `make test-csip-cover` writes `coverage-csip.out` over a
pinned `-coverpkg` list (the `CSIP_COVERPKG` Make variable);
`make coverage-gate` enforces the floor (currently 80%, ratcheted at
#212).

## Operator profiles

Two ready-made CSIP-flavored boot profiles ship under Make targets:

- `make run-testdevice` — server bound to `127.0.0.1:8888` with the
  self-minted test device root appended to `ClientCAs` and a pre-seeded
  EndDevice matching the test device's LFDI/SFDI. The test device cert is
  CSIP §6.11-compliant. `TESTDEVICE_ADDR=host:port` overrides the bind.
- `make run-sunspec` — CCM-8 server trusting the SunSpec CSIP test PKI
  roots via `SEP2_EXTRA_CLIENT_CAS`. Override `SUNSPEC_ROOTS=...` to
  relocate the trust bundle (default points at the operator's Knowledge
  workspace, which is gitignored).

Both targets print a connection-details banner at boot (#206).

## Cross-references

- [README](../README.md)
- [`2030_5.md`](2030_5.md) — full IEEE 2030.5 surface
- [`admin.md`](admin.md) — operator surface
- [`admin-listener.md`](admin-listener.md) — admin TLS posture
- [`glossary.md`](glossary.md) — acronyms and protocol terms
- [`VENDORED.md`](../VENDORED.md) — CCM AEAD + `gotls` fork provenance
