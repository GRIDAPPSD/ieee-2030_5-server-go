# Glossary

Acronyms, ciphers, and protocol terms used across the IEEE 2030.5
references. Linked from the docs on first use.

## Standards and profiles

- **IEEE 2030.5 / SEP2** - *IEEE Standard for Smart Energy Profile
  Application Protocol*, also known as Smart Energy Profile 2 (SEP2).
  HTTPS+XML protocol for utility-to-DER communication. See
  [`2030_5.md`](2030_5.md).
- **CSIP** - *Common Smart Inverter Profile*, SunSpec V1.2. The IEEE
  2030.5 profile California utilities and most current US deployments
  target. See [`csip.md`](csip.md).
- **DER** - *Distributed Energy Resource*. Solar inverters, batteries,
  EV chargers, etc. The thing on the other end of the protocol.
- **DRLC** - *Demand Response and Load Control*. IEEE 2030.5 function
  set for utility-driven load shaping events.
- **FSA** - *FunctionSetAssignments*. The IEEE 2030.5 resource that
  binds a set of programs (e.g. DERPrograms) to a group of EndDevices.
- **PEN** - *Private Enterprise Number*. IANA-assigned OID arc that
  identifies the manufacturer in the CSIP `HardwareModuleName` SAN.

## Identifiers

- **SFDI** - *Short Form Device Identifier*. 11-digit decimal,
  derived from LFDI per IEEE 2030.5 section 6.3.
- **LFDI** - *Long Form Device Identifier*. First 160 bits of
  `SHA-256(DER-encoded device cert)`.

## Ciphers

- **GCM** - *Galois/Counter Mode*. The AEAD (Authenticated Encryption
  with Associated Data) that ships in the Go standard library
  `crypto/tls` by default. Used by the `make run` profile here. NOT
  CSIP-conformant on its own - CSIP V1.2 mandates CCM-8.
- **CCM-8** - *Counter with CBC-MAC, 8-byte authentication tag*.
  Specifically `TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8`, code point
  `0xc0ae`. The cipher CSIP V1.2 mandates. Registered in this server
  by the vendored `internal/tls/gotls` fork. See [`csip.md`](csip.md).
- **AEAD** - *Authenticated Encryption with Associated Data*. A class
  of cipher modes (GCM, CCM, ChaCha20-Poly1305, etc.) that produce
  ciphertext and an authentication tag in one pass.

## TLS and PKI

- **mTLS** - *mutual TLS*. Both peers present and verify
  X.509 certificates during the handshake. Required on the IEEE 2030.5
  protocol listener; optional on the admin listener.
- **PKI** - *Public Key Infrastructure*. The CA + leaf-cert hierarchy
  rooted at `certs/ca.crt` after `make certs`.
- **OID** - *Object Identifier*. Globally unique dotted-decimal name
  (e.g. the IEEE 2030.5 admin policy OID `1.3.6.1.4.1.40732.2.5`).
- **SAN** - *Subject Alternative Name*. X.509 cert extension that
  carries identity beyond the Subject DN. CSIP encodes the device's
  PEN OID + hardware serial in a critical `HardwareModuleName` SAN.

## Operator surface

- **Admin policy OID** - `1.3.6.1.4.1.40732.2.5`. Marks an X.509
  client cert as belonging to an operator authorized for the admin
  surface. See [`admin.md`](admin.md).
- **Bearer token** - Opaque credential sent in
  `Authorization: Bearer ...` headers (or the `key` form field on
  `POST /auth/login`). Configured via `SEP2_ADMIN_KEY`.

## Tools and components

- **EXI** - *Efficient XML Interchange*. Binary XML codec the IEEE
  2030.5 spec lists as an optional alternative to plain XML. Removed
  from this server in #2; XML is the only supported wire
  encoding.
- **mDNS** - *Multicast DNS*. Zero-config service discovery
  (`_smartenergy._tcp`). Opt-in via `SEP2_MDNS=true`.
- **EPRI client** - Reference IEEE 2030.5 C client maintained by the
  Electric Power Research Institute. Used for interop testing
  (`make test-epri`).

## Cross-references

- [README](../README.md)
- [`2030_5.md`](2030_5.md) - protocol surface
- [`csip.md`](csip.md) - CSIP V1.2 profile
- [`admin.md`](admin.md) - operator surface
- [`admin-listener.md`](admin-listener.md) - admin listener TLS posture
