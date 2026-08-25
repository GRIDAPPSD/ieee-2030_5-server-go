# Vendored Dependencies

Code copied directly into this repo rather than imported as Go modules.
Each entry includes the source, version, license, and rationale.

No hand-copied dependencies are currently vendored this way. The two entries
that once lived here, internal/tls/ccm/ (a pion CCM AEAD primitive) and
internal/tls/gotls/ (a crypto/tls fork adding the CCM cipher suite), were
removed when the server started consuming the equivalent sep2tls package
from ieee-2030_5-core-go instead.

## Quality gates and a `go mod vendor` tree

A `go mod vendor` tree is not listed above: it is generated from `go.mod`, not
hand-copied. How each gate behaves when a top-level `vendor/` is present, all
confirmed by observation against a real generated tree on Go 1.26.3:

- `gofmt -l .` walks `vendor/`, so `make gofmt-check` and the CI formatting
  step filter results by the `^vendor/` path prefix.
- `golangci-lint run ./...` reports nothing from `vendor/`: its built-in
  default directory exclusions already skip it.
- `go vet ./...` reports no analyzer findings from `vendor/`. The `./...`
  pattern resolves to first-party packages only (measured: 0 of 42 under
  `vendor/`), and vet suppresses analyzer diagnostics for packages it loads
  only as dependencies. Four planted baits (printf, copylocks, structtag,
  unusedresult) each fired from a first-party package and none fired from
  identical source placed in a vendored one.

A `vendor/` path in `go vet` output is therefore a type-check error rather than
a lint finding, and `go build ./...` fails on the same input. Do not filter it
out and do not narrow the vet package list to suppress it: an explicit
first-party package list still surfaces it, because it arrives through the
dependency closure rather than the requested set. Filtering reports a green
gate over a broken build.
