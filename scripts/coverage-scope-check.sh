#!/usr/bin/env bash
# #387 - fail the coverage build when a CSIP_COVERPKG entry resolves to no
# package, instead of letting `go test -coverpkg` print a warning and
# continue (see scripts/coverage-gate.sh, which only parses the profile
# `go test -coverpkg` produces and cannot see this on its own).
#
# `go list <pattern>` exits 0, with only a stderr warning, when a `/...`
# wildcard matches no package but its directory still exists; it exits
# nonzero only when the directory itself is missing. So this script judges
# an entry by what `go list` printed to stdout (the resolved import paths),
# never by exit status alone, and reports the underlying `go list` message
# when a resolved entry still fails for an unrelated reason (a broken
# import elsewhere in the module, a vendor inconsistency).
#
# Usage:
#   scripts/coverage-scope-check.sh <comma-separated-package-patterns>
#
# Exit codes:
#   0  - every entry resolves to at least one package
#   1  - the pattern list is empty or whitespace, or an entry resolves to no
#        package, or go list otherwise failed to list an entry
#   2  - usage error

set -euo pipefail

usage() {
    printf 'usage: %s <comma-separated-package-patterns>\n' "$0" >&2
}

# split_entries populates the global array `entries` with each comma-
# separated field of "$1", in order. `read -a` on a here-string keeps a
# leading or interior empty field (already reported downstream as a pattern
# that resolves to no package), but drops a trailing one: bash treats a
# trailing delimiter as no further input, the same as normal word
# splitting. Restore it by comparing the parsed field count against the
# comma count, so a trailing comma cannot silently shrink the list.
split_entries() {
    local coverpkg="$1"
    IFS=',' read -r -a entries <<<"$coverpkg"
    local stripped="${coverpkg//,/}"
    local comma_count=$(( ${#coverpkg} - ${#stripped} ))
    if [[ "${#entries[@]}" -le "$comma_count" ]]; then
        entries+=('')
    fi
}

# resolve_entry writes the import paths `go list` prints for pattern "$1"
# to "$2" (one per line, possibly empty) and its stderr text to "$3", then
# returns go list's exit status.
resolve_entry() {
    local entry="$1" outfile="$2" errfile="$3" rc=0
    go list "$entry" >"$outfile" 2>"$errfile" || rc=$?
    return "$rc"
}

check_entries() {
    local coverpkg="$1"

    if [[ -z "${coverpkg//[[:space:]]/}" ]]; then
        printf 'coverage-scope-check: pattern list is empty\n' >&2
        return 1
    fi

    local -a entries=()
    split_entries "$coverpkg"

    local outfile errfile
    outfile=$(mktemp)
    errfile=$(mktemp)
    # shellcheck disable=SC2064 # deliberate: expand now, not at signal time.
    # By the time this EXIT trap runs, check_entries has returned and its
    # locals (including $outfile/$errfile themselves) no longer exist to
    # expand; a single-quoted trap would then fail under `set -u`.
    trap "rm -f '$outfile' '$errfile'" EXIT INT TERM

    local failed=0
    local entry
    for entry in "${entries[@]}"; do
        local rc=0
        resolve_entry "$entry" "$outfile" "$errfile" || rc=$?

        local -a pkgs=()
        mapfile -t pkgs <"$outfile"

        if [[ "${#pkgs[@]}" -eq 0 ]]; then
            printf 'coverage-scope-check: pattern resolves to no package: %s\n' "$entry" >&2
            if [[ -s "$errfile" ]]; then
                printf 'coverage-scope-check:   go list: %s\n' "$(cat "$errfile")" >&2
            fi
            failed=1
        elif [[ "$rc" -ne 0 ]]; then
            printf 'coverage-scope-check: go list failed for %s: %s\n' "$entry" "$(cat "$errfile")" >&2
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
