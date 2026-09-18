#!/usr/bin/env bash
# #193 - CSIP coverage-gate ratchet.
#
# Phase 7 (#191) shipped CI coverage as warn-not-fail. Phase 8
# (#193) ratchets it to fail-at-achieved-threshold. The gate
# operates on a Go coverage profile (`go test -coverprofile=...`) and
# computes the total-statement percentage by parsing the profile
# directly (no `go tool cover` invocation - keeps the gate runnable
# without a working Go toolchain and without needing the module
# source on disk).
#
# Profile format (https://pkg.go.dev/cmd/cover):
#
#   mode: set|count|atomic
#   <file>:<startLine>.<startCol>,<endLine>.<endCol> <numStmts> <count>
#
# Total percentage = sum(numStmts where count>0) / sum(numStmts) * 100.
#
# Why a shell script (not a Go test): the gate runs after the test
# matrix in CI and consumes the produced `coverage.out` artifact. It
# is intentionally minimal, dependency-free, and inspectable. The
# package-list scoping (CSIP-reachable production code) is encoded in
# the `make test-cover` target that produced the profile - this
# script just enforces the floor.
#
# Floor rationale: Phase 8 matrix walk identified the CSIP-reachable
# production code as `./test/csip/...` + `./internal/...` minus the
# vendored `internal/tls/gotls/` fork and its stubs. The achieved
# threshold under that scope at #192 merge was 79.1%; this script
# floors at 78% (1pp below for measurement noise) per Phase 8 doc
# Deliverable 3.
#
# Usage:
#   scripts/coverage-gate.sh <profile-path> [threshold-percent]
#
# Defaults:
#   profile-path:        coverage.out (current working dir)
#   threshold-percent:   78
#
# Exit codes:
#   0  - coverage at or above threshold
#   1  - coverage below threshold
#   2  - usage / IO / parse error
#
# Output: prints the parsed total and threshold to stdout in a stable
# format suitable for CI log greps:
#
#   coverage-gate: profile=<path> total=<pct>% threshold=<pct>% result=<PASS|FAIL>

set -euo pipefail

usage() {
    printf 'usage: %s <profile-path> [threshold-percent]\n' "$0" >&2
}

if [[ $# -lt 1 ]]; then
    usage
    exit 2
fi

profile="$1"
threshold="${2:-78}"

if [[ ! -f "$profile" ]]; then
    printf 'coverage-gate: profile not found: %s\n' "$profile" >&2
    exit 2
fi

# Validate threshold is a non-negative integer.
if ! [[ "$threshold" =~ ^[0-9]+$ ]]; then
    printf 'coverage-gate: threshold must be a non-negative integer, got: %s\n' "$threshold" >&2
    exit 2
fi

# Parse the coverage profile directly. Skip the `mode:` header line.
# Each data row has whitespace-separated trailing fields
# `<numStmts> <count>`. The key (`<file>:<startLine>.<startCol>,<endLine>.<endCol>`)
# uniquely identifies a covered block; when `-coverpkg` is in play the
# same block appears once per test package, so deduplicate by key
# before summing. For mode=set we take the OR of counts (>0 from any
# row marks the block covered); for mode=count/atomic we take the max
# count seen. Either way: max(count) is correct.
#
# Total percentage = sum(numStmts where dedup-count>0) / sum(numStmts) * 100.
total_pct="$(awk '
    NR == 1 && $1 == "mode:" { next }
    NF >= 3 {
        key = $1
        stmts = $(NF-1)
        count = $NF + 0
        if (!(key in seen) || count > seen[key]) {
            seen[key] = count
        }
        stmts_map[key] = stmts
    }
    END {
        total = 0
        covered = 0
        for (k in seen) {
            total += stmts_map[k]
            if (seen[k] > 0) {
                covered += stmts_map[k]
            }
        }
        if (total == 0) {
            print "ERR_EMPTY"
            exit
        }
        printf "%.1f", (covered / total) * 100
    }
' "$profile")"

if [[ "$total_pct" == "ERR_EMPTY" ]]; then
    printf 'coverage-gate: profile contains no statement rows: %s\n' "$profile" >&2
    exit 2
fi

# Sanity: ensure parseable as a number.
if ! [[ "$total_pct" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
    printf 'coverage-gate: could not parse total percentage (got %s)\n' "$total_pct" >&2
    exit 2
fi

# Floor-compare: `total_pct >= threshold`. Use awk for float math
# because bash arithmetic is integer-only.
result="$(awk -v t="$total_pct" -v th="$threshold" 'BEGIN {print (t+0 >= th+0) ? "PASS" : "FAIL"}')"

printf 'coverage-gate: profile=%s total=%s%% threshold=%s%% result=%s\n' \
    "$profile" "$total_pct" "$threshold" "$result"

if [[ "$result" == "PASS" ]]; then
    exit 0
fi
exit 1
