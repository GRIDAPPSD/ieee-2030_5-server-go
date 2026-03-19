# Vendored Dependencies

Code copied directly into this repo rather than imported as Go modules.
Each entry includes the source, version, license, and rationale.

## internal/tls/ccm/

- **Source:** github.com/pion/dtls/v3/pkg/crypto/ccm
- **Version:** v3 (latest as of 2026-03-19)
- **License:** MIT (see internal/tls/ccm/LICENSE)
- **Files:** ccm.go
- **Why vendored:** We only need the CCM AEAD primitive (~200 lines), not the full
  pion/dtls library. This avoids pulling in DTLS, WebRTC, and dozens of transitive
  dependencies. The CCM code implements RFC 3610 and satisfies Go's cipher.AEAD
  interface with zero external dependencies.

## internal/tls/gotls/

- **Source:** Go standard library crypto/tls
- **Version:** (will be pinned to specific Go release, recorded in internal/tls/gotls/VERSION)
- **License:** BSD-3-Clause (Go license)
- **Why vendored:** Go's crypto/tls does not support CCM cipher suites and has no
  plugin API for adding them. We fork the package with minimal changes (~30 lines)
  to register TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8 (0xc0ae) as required by
  IEEE 2030.5-2018. Changes are documented in internal/tls/gotls/DIFF.md.
