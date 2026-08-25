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
hand-copied. This section covers only the three `ci.yml` gates below (gofmt,
golangci-lint, go vet); `codeql.yml` and `core-freshness.yml` are out of scope
here and their vendor-tree behavior is unmeasured (tracked as IEEESRV-090). How
each covered gate behaves when a top-level `vendor/` is present, all confirmed
by observation against a real generated tree on Go 1.26.3:

- `gofmt -l .` walks `vendor/`, so `make gofmt-check` and the CI formatting
  step filter its drift results by the `^vendor/` path prefix. That filter
  applies to drift only: a gofmt run that fails outright (a file it cannot
  parse, exit 2; the binary missing, exit 127; killed by a signal, exit 137)
  fails the gate regardless of whether the file sits inside `vendor/`.
- `golangci-lint run ./...` reports nothing from `vendor/`: its built-in
  default directory exclusions already skip it.
- `go vet ./...` prints no diagnostics for a `vendor/` package's own code, but
  this is a general property of the analysis driver, not a vendor-specific
  exclusion: vet computes type and fact information for every package in the
  import closure, and prints diagnostics only for packages matching the
  requested pattern (`./...` resolves to first-party packages only; measured:
  0 of 42 under `vendor/`). Facts derived from a non-requested package still
  feed analysis of requested callers: a printf-style wrapper defined in a
  vendored package, called with a mismatched argument from `internal/config`,
  was flagged at the first-party call site. Four planted baits (printf,
  copylocks, structtag, unusedresult) were tested; printf, copylocks, and
  unusedresult each fired from a first-party package and none fired from
  identical source placed in a vendored one. structtag is bait-form
  dependent: a duplicate-key struct tag did not trip it in either location,
  while a malformed tag (`json:name`, missing quotes) fired from the
  first-party package and not the vendored one, matching the other three.
- Narrowing a red `go vet ./...` gate to a single package by hand
  (`go vet <pkg>`) can read a stale warm-cache result and report exit 0 when
  the full cold run reports exit 1; pass `-a` to force a fresh analysis. CI
  is unaffected: it runs the single `./...` gate cold.

A `vendor/` path in `go vet` output is therefore a type-check error rather than
a lint finding, and `go build ./...` fails on the same input. Do not filter it
out and do not narrow the vet package list to suppress it: an explicit
first-party package list still surfaces it, because it arrives through the
dependency closure rather than the requested set. Filtering reports a green
gate over a broken build.
