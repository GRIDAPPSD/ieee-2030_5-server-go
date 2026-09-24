# Vendored Dependencies

Code copied directly into this repo rather than imported as Go modules.
Each entry includes the source, version, license, and rationale.

No hand-copied dependencies are currently vendored this way. The two entries
that once lived here, a pion CCM AEAD primitive and a crypto/tls fork adding
the CCM cipher suite, were removed when the server started consuming the
equivalent sep2tls package from ieee-2030_5-core-go instead: `pkg/sep2tls/ccm`
and `pkg/sep2tls/gotls`, mod-vendored at
`vendor/github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/{ccm,gotls}`.

## Quality gates and a `go mod vendor` tree

A `go mod vendor` tree is not listed above: it is generated from `go.mod`, not
hand-copied. This section covers only the three `ci.yml` gates below (gofmt,
golangci-lint, go vet); `core-freshness.yml` is out of scope here and its
vendor-tree behavior is unmeasured. How each covered gate behaves when a
top-level `vendor/` is present, all confirmed
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

## Bumping the core-go pin

`make bump-core` (default `TARGET=main`, or `make bump-core TARGET=vX.Y.Z` for an
explicit tag) is the supported path for moving the `ieee-2030_5-core-go` pin by
hand: it runs `go get`, `go mod tidy`, and `go mod vendor` in that order, so a
human doing the bump manually cannot skip the vendor step. `core-freshness.yml`
follows the same order in its automated `chore/bump-core` PR.

`ci.yml`'s `vendor-integrity` job enforces the other direction: it runs
`go mod vendor` and then `git status --porcelain -- vendor/`, so a content
change, an added or removed file, or a mode change in the regenerated tree
against the committed one fails the job. `git status`, not `git diff`: a
deleted vendored file regenerates as an untracked path, which `git diff`
does not report (the same reason `ui-check` uses `git status --porcelain`
rather than `git diff` on `dist/`, at `Makefile:54-58`). A hand-edited
vendored file, not just a missed `make vendor` after a pin bump, fails
this gate.

## Toolchain caveat: vendoring removes the module fetch, not the toolchain fetch

Vendoring `ieee-2030_5-core-go` and its dependencies removes the need to fetch
those MODULES from the network on a fresh `GOMODCACHE`. It does not remove the
Go toolchain fetch: `go.mod` pins `go 1.26.3` with no `toolchain` line, so a
host whose installed `go` is older (e.g. 1.24.4) and whose `GOMODCACHE` has
never cached a 1.26.x toolchain will still try to download one under
`GOTOOLCHAIN=auto`, and that download fails closed under `GOPROXY=off`. An
air-gapped host needs a matching 1.26.x toolchain provisioned separately from
vendoring.
