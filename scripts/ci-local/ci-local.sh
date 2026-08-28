#!/usr/bin/env bash
# scripts/ci-local/ci-local.sh
#
# Local gate: runs the same make targets .github/workflows/ci.yml invokes,
# from one entry point, so a change can be verified before push without
# approximating the workflow by hand. #410.
#
# Sources scripts/ci-local/lib/ci-local-targets.sh for the target list, and runs
# scripts/ci-local/ci-local-drift-check.sh first: the same array that check
# compares against ci.yml's extracted set is the array this script
# iterates to invoke `make`, so an added CI step and an added local gate
# are the same edit rather than two lists that can go out of sync.
#
# Prerequisite-dependent gates report SKIPPED distinctly from PASSED
# rather than being run against an absent prerequisite. The CSIP
# conformance suite needs a SunSpec V1.2 test PKI that is gitignored and
# provisioned out of band (see test/csip/README.md); the frontend drift
# check needs a Node toolchain; golangci-lint is documented as optional
# in README.md.
#
# Exit codes:
#   0 - every gate PASSED, zero SKIPPED: a genuinely clean run
#   1 - the drift guard failed, or at least one gate FAILED (fail-fast:
#       stops at the first failure and names it)
#   2 - every gate PASSED or SKIPPED, but at least one SKIPPED: partial
#       coverage, deliberately distinguishable from both a clean pass
#       and a failure by exit code alone, not by output text
#
# Usage:
#   make ci-local
#   scripts/ci-local/ci-local.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=scripts/ci-local/lib/ci-local-targets.sh
source "${SCRIPT_DIR}/lib/ci-local-targets.sh"

cd "${REPO_ROOT}"

GATE_LOG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ci-local.XXXXXX")"
trap 'rm -rf "${GATE_LOG_DIR}"' EXIT INT TERM

GATE_ORDER=()
declare -A GATE_STATUS=()
declare -A GATE_DETAIL=()

record() { # record <gate> <status> [detail]
  local gate="$1" status="$2" detail="${3:-}"
  GATE_ORDER+=("${gate}")
  GATE_STATUS["${gate}"]="${status}"
  GATE_DETAIL["${gate}"]="${detail}"
}

csip_fixtures_present() {
  [[ -f test/csip/fixtures/sunspec/cert.pem \
    && -f test/csip/fixtures/sunspec/key.pem \
    && -f test/csip/fixtures/sunspec/roots.pem ]]
}

node_toolchain_present() {
  command -v npm >/dev/null 2>&1 && command -v node >/dev/null 2>&1
}

golangci_lint_present() {
  command -v golangci-lint >/dev/null 2>&1
}

skip_gate() { # skip_gate <gate> <reason>
  local gate="$1" reason="$2"
  record "${gate}" SKIPPED "${reason}"
  printf 'SKIPPED %s (%s)\n' "${gate}" "${reason}"
}

run_make_gate() { # run_make_gate <gate>
  local gate="$1"
  local log="${GATE_LOG_DIR}/${gate}.log"
  local rc=0
  make "${gate}" >"${log}" 2>&1 || rc=$?
  if [[ "${rc}" -eq 0 ]]; then
    record "${gate}" PASSED
    printf 'PASSED  %s\n' "${gate}"
    return 0
  fi
  record "${gate}" FAILED "exit ${rc}, log ${log}"
  printf 'FAILED  %s (exit %s)\n' "${gate}" "${rc}"
  printf -- '--- last 40 lines of %s output (%s) ---\n' "${gate}" "${log}"
  tail -n 40 "${log}"
  return 1
}

run_ui_check_gate() {
  local gate="ui-check"
  local log="${GATE_LOG_DIR}/${gate}.log"
  local dist_path="internal/server/web/dist"
  local rc=0
  make "${gate}" >"${log}" 2>&1 || rc=$?
  # make ui-check's own job is exactly this rebuild-and-diff, so rc above
  # already carries the real verdict. The checkout+clean below only
  # undoes this run's rebuild afterward, scoped to the single directory
  # ui-check writes, so it never lingers as tree state for the next tool
  # or the next run (#410 invariants: ui-check can leave the tree dirty).
  git -C "${REPO_ROOT}" checkout --quiet -- "${dist_path}"
  git -C "${REPO_ROOT}" clean --quiet -fd -- "${dist_path}"
  if [[ "${rc}" -eq 0 ]]; then
    record "${gate}" PASSED
    printf 'PASSED  %s\n' "${gate}"
    return 0
  fi
  record "${gate}" FAILED "exit ${rc}, log ${log}"
  printf 'FAILED  %s (exit %s)\n' "${gate}" "${rc}"
  printf -- '--- last 40 lines of %s output (%s) ---\n' "${gate}" "${log}"
  tail -n 40 "${log}"
  return 1
}

check_toolchain() {
  local pinned local_version
  pinned="$(awk '/^go[[:space:]]+[0-9]+\.[0-9]+(\.[0-9]+)?/ {print $2; exit}' "${REPO_ROOT}/go.mod")"
  local_version="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  if [[ -z "${pinned}" || -z "${local_version}" ]]; then
    printf 'TOOLCHAIN: could not determine the pinned (%s) or local (%s) Go version; skipping the comparison.\n' "${pinned:-?}" "${local_version:-?}"
    return
  fi
  if [[ "${local_version}" == "${pinned}" ]]; then
    printf 'TOOLCHAIN: local go%s matches the go.mod pin ci.yml resolves via go-version-file (go%s).\n' "${local_version}" "${pinned}"
  else
    printf 'TOOLCHAIN: MISMATCH - local go%s differs from the go.mod pin ci.yml resolves via go-version-file (go%s). Every gate above ran against a different compiler than CI would use.\n' "${local_version}" "${pinned}"
  fi
}

print_not_covered() {
  cat <<'EOF'
NOT COVERED by this local run (documented gaps, not silent ones):
  - CodeQL static analysis (.github/workflows/codeql.yml): a GitHub-hosted
    analysis service; there is no local CodeQL CLI wired into this repo.
  - core-freshness.yml: tests this server against ieee-2030_5-core-go's
    main branch on a daily schedule, not against this change; it is not
    part of the per-change gate this script mirrors.
  - Clean-room checkout: this runs against the current working tree, not
    a fresh clone; stray local state is not reproduced or ruled out the
    way a CI runner starting from actions/checkout is.
  - Network isolation: the CI runner's network posture is not reproduced.
EOF
}

print_gate_plan() {
  printf 'Gates to run (%d), matching the make targets .github/workflows/ci.yml invokes, in order:\n' "${#CI_LOCAL_MAKE_TARGETS[@]}"
  local i=1
  local gate note
  for gate in "${CI_LOCAL_MAKE_TARGETS[@]}"; do
    note="${CI_LOCAL_ONLY_TARGETS[${gate}]:-}"
    if [[ -n "${note}" ]]; then
      printf '  %2d. %-18s (local addition: %s)\n' "${i}" "${gate}" "${note}"
    else
      printf '  %2d. %-18s\n' "${i}" "${gate}"
    fi
    i=$((i + 1))
  done
}

print_summary() {
  local gate status detail
  local passed=0 skipped=0 failed=0
  printf '\n=== ci-local summary ===\n'
  for gate in "${GATE_ORDER[@]}"; do
    status="${GATE_STATUS[${gate}]}"
    detail="${GATE_DETAIL[${gate}]:-}"
    printf '%-8s %-18s %s\n' "${status}" "${gate}" "${detail}"
    case "${status}" in
    PASSED) passed=$((passed + 1)) ;;
    SKIPPED) skipped=$((skipped + 1)) ;;
    FAILED) failed=$((failed + 1)) ;;
    esac
  done
  printf '%d PASSED, %d SKIPPED, %d FAILED (of %d gates)\n' \
    "${passed}" "${skipped}" "${failed}" "${#GATE_ORDER[@]}"
  echo
  check_toolchain
  echo
  print_not_covered
}

main() {
  echo "=== Drift guard ==="
  if ! "${SCRIPT_DIR}/ci-local-drift-check.sh"; then
    echo "ci-local: refusing to run the gates; the drift guard above must pass first." >&2
    exit 1
  fi
  echo

  print_gate_plan
  echo

  local gate prereq skip_reason
  for gate in "${CI_LOCAL_MAKE_TARGETS[@]}"; do
    prereq="${CI_LOCAL_TARGET_PREREQ[${gate}]:-none}"
    skip_reason=""
    case "${prereq}" in
    csip-fixtures)
      csip_fixtures_present || skip_reason="SunSpec CSIP test PKI absent under test/csip/fixtures/sunspec/ (gitignored, provisioned out of band; see test/csip/README.md)"
      ;;
    node-toolchain)
      node_toolchain_present || skip_reason="npm/node not found on PATH"
      ;;
    golangci-lint)
      golangci_lint_present || skip_reason="golangci-lint not found on PATH (README.md lists it as optional)"
      ;;
    esac

    if [[ -n "${skip_reason}" ]]; then
      skip_gate "${gate}" "${skip_reason}"
      continue
    fi

    if [[ "${gate}" == "ui-check" ]]; then
      run_ui_check_gate || {
        print_summary
        exit 1
      }
    else
      run_make_gate "${gate}" || {
        print_summary
        exit 1
      }
    fi
  done

  print_summary

  local skipped_count=0
  for gate in "${GATE_ORDER[@]}"; do
    [[ "${GATE_STATUS[${gate}]}" == "SKIPPED" ]] && skipped_count=$((skipped_count + 1))
  done
  if [[ "${skipped_count}" -gt 0 ]]; then
    exit 2
  fi
  exit 0
}

main "$@"
