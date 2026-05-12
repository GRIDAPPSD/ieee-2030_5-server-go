# Enphase Test PKI (CSIP V1.2 non-compliant)

Factory certificate material for a real Enphase test microinverter, vendored
here so the server can authenticate an mTLS connection from the device when
the lab swaps to the Enphase LAN. This is **test material** — the leaf is
**not** CSIP §6.11 compliant; see "Strict-mode caveat" below.

The server's runtime profile that consumes this material is the
`make run-enphase` target (IEEE-068). The server must run in non-strict
cert-verification mode (the default; `SEP2_CSIP_STRICT=true` is incompatible
with this device).

## Files

| File                 | Contents                                                                 |
|----------------------|--------------------------------------------------------------------------|
| `Enph_cert_chain.pem`| Full chain: device leaf → MICA (EPRI) → MCA (Enphase) → SERCA (PPL root).|
| `Enph_key.pem`       | Device leaf private key (ECDSA P-256, unencrypted PKCS#8).               |
| `Enph_root.pem`      | Standalone SERCA root, equivalent to chain entry #4. Either is fine for `ClientCAs`. |

The original drop also carried a `dev_info` text file whose LFDI value is
**stale** (does not match the chain leaf). It is intentionally omitted —
use the values in this README, which were computed live from chain[0].

## Device identity

| Field | Value                                       |
|-------|---------------------------------------------|
| LFDI  | `9711948EBF52B988A39728F756AEAA763EFF5540`  |
| SFDI  | `405521881394` (display `405-521-881-394`)  |
| PIN   | `111115`                                    |

LFDI is the first 20 bytes of `SHA-256(chain[0].DER)`, hex-uppercase
(40 chars). SFDI is the top 36 bits of the same hash, formatted as 11
zero-padded decimal digits + a sum-of-digits mod-10 check digit.

The **PIN** is an out-of-band admin-pairing secret. IEEE 2030.5 does not
define a PIN field on `EndDevice` — the value lives only in this README
and as a comment in the fixture YAML. The `Registration` resource carries
a `PIN uint32` on the server side but it is used for the inverter→server
registration handshake, not Enphase device pairing.

## Chain

```
SERCA  (self-signed C=AU CN="Enphase_2030.5-Program", PPL-Test-Root, valid through 9999)
  └── MCA   (Enphase)
        └── MICA  (EPRI)
              └── device leaf (CN=enphase, ECDSA P-256, valid through 9999)
```

All signatures are `ecdsa-with-SHA256`. The leaf carries:

- **no** SubjectAltName extension
- **no** Key Usage extension
- **no** Basic Constraints extension
- Subject = `CN=enphase` (non-empty)

These omissions are why this PKI is not §6.11-compliant.

## Strict-mode caveat

CSIP §6.11 requires the device leaf to carry a critical `HardwareModuleName`
SAN (OID `1.3.6.1.5.5.7.8.4`), an empty Subject, a Key Usage extension, and a
Basic Constraints extension. The Enphase test leaf carries none of these.

Future strict-mode work (gated on IEEE-020 / `SEP2_CSIP_STRICT=true`) is
**incompatible with this device**. The `make run-enphase` target therefore
runs the server in the default non-strict mode. Full §6.11 conformance
against a strict server requires real Enphase production firmware that
ships a compliant device cert.

## Provenance

Copied from
`~/knowledge/projects/ieee-2030_5-go/artifacts/inputs/csip-test-pki/enphase/`
under the Knowledge workspace (where it originally landed from Enphase as
the test cert bundle for the lab device).
