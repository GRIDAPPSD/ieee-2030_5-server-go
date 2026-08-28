#!/usr/bin/env bash
# scripts/ci-local/ci-local-drift-check.sh
#
# Requirement 2 of #410: something must fail when a workflow invokes a
# `make` target scripts/ci-local/ci-local.sh does not run. Extracts the
# make targets a GitHub Actions workflow file (or every workflow file in
# a directory) invokes and fails if any is missing from
# CI_LOCAL_MAKE_TARGETS, the same array ci-local.sh iterates to run the
# gates.
#
# A guard observed only ever passing is not evidence it can fail. Point
# this script at a scratch copy of a workflow with an extra `make` line
# to exercise the failure path, then discard the scratch copy:
#
#   scripts/ci-local/ci-local-drift-check.sh /path/to/scratch-ci.yml
#
# Usage:
#   scripts/ci-local/ci-local-drift-check.sh [workflow-file-or-dir]
# Defaults to .github/workflows/ (every *.yml/*.yaml file in it) under
# this script's own repo.
#
# Exit codes:
#   0 - every make target found is in CI_LOCAL_MAKE_TARGETS
#   1 - at least one is missing (drift)
#   2 - usage or IO error (bad path, no workflow files found, extraction
#       empty across every file scanned)
#   3 - the anchored parser and an unanchored cross-check sweep disagree
#       on how many make invocations a file contains: a form the anchor
#       cannot structurally parse (chained after && or ;, a quoted run:
#       scalar, a flow-mapping one-liner) may be hiding real drift, so
#       this refuses rather than reporting a false OK.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=scripts/ci-local/lib/ci-local-targets.sh
source "${CI_LOCAL_TARGETS_LIB:-${SCRIPT_DIR}/lib/ci-local-targets.sh}"

WORKFLOW_TARGET="${1:-${REPO_ROOT}/.github/workflows}"

if [[ -d "${WORKFLOW_TARGET}" ]]; then
  mapfile -t WORKFLOW_FILES < <(find "${WORKFLOW_TARGET}" -maxdepth 1 -type f \( -name '*.yml' -o -name '*.yaml' \) | sort)
elif [[ -f "${WORKFLOW_TARGET}" ]]; then
  WORKFLOW_FILES=("${WORKFLOW_TARGET}")
else
  printf 'ci-local-drift-check: workflow path not found: %s\n' "${WORKFLOW_TARGET}" >&2
  exit 2
fi

if [[ "${#WORKFLOW_FILES[@]}" -eq 0 ]]; then
  printf 'ci-local-drift-check: no *.yml/*.yaml workflow files found under %s\n' "${WORKFLOW_TARGET}" >&2
  exit 2
fi

# Anchor: matches the whole value of a single-line `run: make X` step
# (the dash-prefixed list-item form included: `- run: make X`), or a bare
# `make X` line inside a `run: |` block. A comment line (first non-blank
# char `#`) never matches. Not structurally parseable by this anchor
# alone: a quoted `run:` scalar, a block scalar other than `run: |`, a
# flow-mapping one-liner, and `make` chained after `&&` or `;`; the
# cross-check below refuses on those rather than silently missing them.
readonly ANCHOR='^[[:space:]]*(-[[:space:]]+)?(run:[[:space:]]*)?make[[:space:]]+[A-Za-z0-9_-]+'

extract_targets() { # extract_targets <file> -- one target name per line
  grep -oE "${ANCHOR}" "$1" | sed -E 's/^[[:space:]]*(-[[:space:]]+)?(run:[[:space:]]*)?make[[:space:]]+//'
}

# count_anchored/count_unanchored back the cross-check: count_unanchored
# sees every `make X` on any non-comment line regardless of what precedes
# it on that line; count_anchored sees only what the anchor can parse. A
# grep with zero matches exits 1, which is an expected outcome here (an
# empty file, or a file with no make lines at all), not an error -- hence
# the `|| true` on both.
count_anchored() { # count_anchored <file> -- number of anchor-matching lines
  grep -cE "${ANCHOR}" "$1" || true
}

count_unanchored() { # count_unanchored <file> -- number of make X occurrences on non-comment lines
  { grep -vE '^[[:space:]]*#' "$1" | grep -oE '\bmake[[:space:]]+[A-Za-z0-9_-]+\b' || true; } | wc -l | tr -d '[:space:]'
}

all_targets=()
blind_spots=()
files_scanned=0

for f in "${WORKFLOW_FILES[@]}"; do
  files_scanned=$((files_scanned + 1))

  anchored_n="$(count_anchored "${f}")"
  unanchored_n="$(count_unanchored "${f}")"
  if [[ "${anchored_n}" != "${unanchored_n}" ]]; then
    blind_spots+=("${f}: anchored parser saw ${anchored_n} make invocation(s), an unanchored sweep saw ${unanchored_n}")
  fi

  file_targets="$(extract_targets "${f}")" || true
  while IFS= read -r t; do
    [[ -z "${t}" ]] && continue
    all_targets+=("${t}")
  done <<<"${file_targets}"
done

if [[ "${#blind_spots[@]}" -gt 0 ]]; then
  printf 'ci-local-drift-check: BLIND SPOT: the anchored parser may have missed a real make invocation:\n' >&2
  for b in "${blind_spots[@]}"; do
    printf '  - %s\n' "${b}" >&2
  done
  printf 'ci-local-drift-check: refusing to report coverage while the anchored and unanchored counts disagree.\n' >&2
  exit 3
fi

unique_targets="$(printf '%s\n' "${all_targets[@]:-}" | grep -v '^$' | sort -u)" || true

if [[ -z "${unique_targets}" ]]; then
  printf 'ci-local-drift-check: extracted zero make targets across %d workflow file(s) under %s\n' \
    "${files_scanned}" "${WORKFLOW_TARGET}" >&2
  printf 'ci-local-drift-check: that is almost certainly the pattern failing to match, not an empty workflow set; refusing to report a pass on zero evidence.\n' >&2
  exit 2
fi

target_count=0
missing=()
while IFS= read -r target; do
  [[ -z "${target}" ]] && continue
  target_count=$((target_count + 1))
  found=0
  for known in "${CI_LOCAL_MAKE_TARGETS[@]}"; do
    if [[ "${known}" == "${target}" ]]; then
      found=1
      break
    fi
  done
  if [[ "${found}" -eq 0 ]]; then
    missing+=("${target}")
  fi
done <<<"${unique_targets}"

if [[ "${#missing[@]}" -gt 0 ]]; then
  printf 'ci-local-drift-check: DRIFT: %d of %d workflow-invoked make target(s) missing from scripts/ci-local/ci-local.sh:\n' \
    "${#missing[@]}" "${target_count}" >&2
  for m in "${missing[@]}"; do
    printf '  - make %s (not run by scripts/ci-local/ci-local.sh)\n' "${m}" >&2
  done
  exit 1
fi

printf 'ci-local-drift-check: OK: all %d make target(s) invoked across %d workflow file(s) under %s are covered by scripts/ci-local/ci-local.sh.\n' \
  "${target_count}" "${files_scanned}" "${WORKFLOW_TARGET}"
