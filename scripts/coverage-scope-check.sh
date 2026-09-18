#!/usr/bin/env bash
# #387 - fail the coverage build when a CSIP_COVERPKG entry resolves to no
# package, instead of letting `go test -coverpkg` print a warning and
# continue (see scripts/coverage-gate.sh, which only parses the profile
# `go test -coverpkg` produces and cannot see this on its own).
#
# `go list <pattern>` exits nonzero and reports the pattern that matched
# nothing; `go test -coverpkg=<pattern>` does not, so this script re-checks
# each entry the way `go build`/`go vet` would already refuse it.
#
# Usage:
#   scripts/coverage-scope-check.sh <comma-separated-package-patterns>
#
# Exit codes:
#   0  - every entry resolves to at least one package
#   1  - at least one entry resolves to no package
#   2  - usage error

set -euo pipefail

usage() {
    printf 'usage: %s <comma-separated-package-patterns>\n' "$0" >&2
}

check_entries() {
    local coverpkg="$1"
    local -a entries
    IFS=',' read -r -a entries <<<"$coverpkg"

    local failed=0
    local entry
    for entry in "${entries[@]}"; do
        if ! go list "$entry" >/dev/null 2>&1; then
            printf 'coverage-scope-check: pattern resolves to no package: %s\n' "$entry" >&2
            failed=1
        fi
    done

    if [[ "$failed" -ne 0 ]]; then
        return 1
    fi
    printf 'coverage-scope-check: %d patterns all resolve\n' "${#entries[@]}"
}

main() {
    if [[ $# -ne 1 ]]; then
        usage
        exit 2
    fi
    check_entries "$1"
}

main "$@"
