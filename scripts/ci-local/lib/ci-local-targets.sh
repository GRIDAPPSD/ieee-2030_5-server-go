# shellcheck shell=bash
#
# This file is sourced only, never executed directly, so it carries no
# shebang; the directive above tells shellcheck which shell to check it
# as when it is analyzed on its own rather than pulled in via `-x` from
# scripts/ci-local/ci-local.sh or scripts/ci-local/ci-local-drift-check.sh.
#
# Single source of truth for scripts/ci-local/ci-local.sh (the local gate) and
# scripts/ci-local/ci-local-drift-check.sh (the drift guard). #410.
#
# CI_LOCAL_MAKE_TARGETS is the run order. scripts/ci-local/ci-local.sh iterates
# this exact array to invoke `make <target>`; scripts/ci-local/ci-local-drift-check.sh
# checks the make targets extracted from ci.yml against this same array.
# One list, not two hand-kept copies that could drift from each other.
#
# CI_LOCAL_TARGET_PREREQ classes each target's external prerequisite, so
# ci-local.sh can report SKIPPED before invoking `make` rather than
# running it against an absent prerequisite and risking a misleading
# PASS from a test that skips cleanly on its own:
#   none            - no external prerequisite beyond the Go toolchain
#   csip-fixtures   - needs test/csip/fixtures/sunspec/{cert,key,roots}.pem
#                     (gitignored, provisioned out of band; see
#                     test/csip/README.md)
#   node-toolchain  - needs npm and node on PATH
#   golangci-lint   - needs the golangci-lint binary on PATH (README.md
#                     lists it as optional, so its absence is a SKIP,
#                     not a failure)
#
# CI_LOCAL_ONLY_TARGETS names gates ci-local.sh runs that ci.yml does not
# invoke via a bare `make <target>` line, so they are never silently
# folded into the count as if the workflow named them.

CI_LOCAL_MAKE_TARGETS=(
  vet
  build
  lint
  test
  test-race
  test-cover
  test-csip
  test-csip-server
  test-csip-hooks
  test-csip-race
  test-csip-cover
  coverage-gate
  ui-check
)

declare -A CI_LOCAL_TARGET_PREREQ=(
  [vet]=none
  [build]=none
  [lint]=golangci-lint
  [test]=none
  [test-race]=none
  [test-cover]=none
  [test-csip]=csip-fixtures
  [test-csip-server]=csip-fixtures
  [test-csip-hooks]=csip-fixtures
  [test-csip-race]=csip-fixtures
  [test-csip-cover]=csip-fixtures
  [coverage-gate]=csip-fixtures
  [ui-check]=node-toolchain
)

declare -A CI_LOCAL_ONLY_TARGETS=(
  [lint]="covers ci.yml's Lint step (golangci-lint-action) and its standalone gofmt job (an inlined gofmt -l check) in one target, since make lint already runs golangci-lint followed by gofmt-check"
)
