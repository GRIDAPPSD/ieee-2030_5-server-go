#!/usr/bin/env bash
# scripts/ci-local/ci-local-drift-check.sh
#
# Requirement 2 of #410: something must fail when a workflow invokes a
# `make` target scripts/ci-local/ci-local.sh does not run. Extracts the set of
# `make <target>` invocations from a GitHub Actions workflow file and
# fails if any of them is missing from CI_LOCAL_MAKE_TARGETS, the same
# array scripts/ci-local/ci-local.sh iterates to run the gates.
#
# A guard observed only ever passing is not evidence it can fail. Point
# this script at a scratch copy of the workflow with an extra `make` line
# to exercise the failure path, then discard the scratch copy:
#
#   scripts/ci-local/ci-local-drift-check.sh /path/to/scratch-ci.yml
#
# Usage:
#   scripts/ci-local/ci-local-drift-check.sh [workflow-file]
# Defaults to .github/workflows/ci.yml in this script's own repo.
#
# Exit codes:
#   0 - every make target the workflow invokes is in CI_LOCAL_MAKE_TARGETS
#   1 - at least one is missing (drift)
#   2 - usage or IO error (bad path, workflow unreadable, extraction empty)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=scripts/ci-local/lib/ci-local-targets.sh
source "${SCRIPT_DIR}/lib/ci-local-targets.sh"

WORKFLOW_FILE="${1:-${REPO_ROOT}/.github/workflows/ci.yml}"

if [[ ! -f "${WORKFLOW_FILE}" ]]; then
  printf 'ci-local-drift-check: workflow file not found: %s\n' "${WORKFLOW_FILE}" >&2
  exit 2
fi

# Real invocations only: the whole value of a single-line `run: make X`
# step, or a bare `make X` line inside a `run: |` block. Anchored at the
# start of the line (after leading whitespace) so a comment line whose
# first non-blank character is `#` never matches, even when the comment
# names a make target in prose (ci.yml does this, e.g. near its
# "CLAUDE.md-referenced `make test-csip-server`" remark).
extract_targets() {
  grep -oE '^[[:space:]]*(run:[[:space:]]*)?make[[:space:]]+[A-Za-z0-9_-]+' "$1" \
    | sed -E 's/^[[:space:]]*(run:[[:space:]]*)?make[[:space:]]+//' \
    | sort -u
}

# grep exits 1 when a workflow has zero make invocations. That is a real,
# expected outcome this script handles explicitly below (refusing to
# report a pass on zero evidence), not an error to let `set -e` abort on
# before that check ever runs.
workflow_targets="$(extract_targets "${WORKFLOW_FILE}")" || true

if [[ -z "${workflow_targets}" ]]; then
  printf 'ci-local-drift-check: extracted zero make targets from %s\n' "${WORKFLOW_FILE}" >&2
  printf 'ci-local-drift-check: that is almost certainly the pattern failing to match, not an empty workflow; refusing to report a pass on zero evidence.\n' >&2
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
done <<<"${workflow_targets}"

if [[ "${#missing[@]}" -gt 0 ]]; then
  printf 'ci-local-drift-check: DRIFT: %d of %d workflow-invoked make target(s) missing from scripts/ci-local/ci-local.sh:\n' \
    "${#missing[@]}" "${target_count}" >&2
  for m in "${missing[@]}"; do
    printf '  - make %s (invoked by %s, not run by scripts/ci-local/ci-local.sh)\n' "${m}" "${WORKFLOW_FILE}" >&2
  done
  exit 1
fi

printf 'ci-local-drift-check: OK: all %d make target(s) %s invokes are covered by scripts/ci-local/ci-local.sh.\n' \
  "${target_count}" "${WORKFLOW_FILE}"
