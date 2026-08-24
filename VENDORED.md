# Vendored Dependencies

Code copied directly into this repo rather than imported as Go modules.
Each entry includes the source, version, license, and rationale.

No hand-copied dependencies are currently vendored this way. The two entries
that once lived here, internal/tls/ccm/ (a pion CCM AEAD primitive) and
internal/tls/gotls/ (a crypto/tls fork adding the CCM cipher suite), were
removed when the server started consuming the equivalent sep2tls package
from ieee-2030_5-core-go instead.
