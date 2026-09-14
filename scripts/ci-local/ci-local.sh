#!/usr/bin/env bash
# scripts/ci-local/ci-local.sh
#
# Local gate: runs the same make targets .github/workflows/ invokes, from
# one entry point, so a change can be verified before push without
# approximating the workflow by hand. #410.
#
# Sources scripts/ci-local/lib/ci-local-targets.sh for the target list, and
# runs scripts/ci-local/ci-local-drift-check.sh first: the same array that
# check compares against the workflows' extracted set is the array this
# script iterates to invoke `make`, so an added CI step and an added local
# gate are the same edit rather than two lists that can go out of sync.
#
# Prerequisite-dependent gates report SKIPPED distinctly from PASSED
# rather than being run against an absent prerequisite. The CSIP
# conformance suite needs a SunSpec V1.2 test PKI resolved env-var-first
# then from test/csip/fixtures/sunspec/ (gitignored, provisioned out of
# band; see test/csip/README.md); the frontend drift check needs a Node
# toolchain; golangci-lint is documented as optional in README.md.
#
# Exit codes:
#   0 - every gate PASSED, zero SKIPPED: a genuinely clean run
#   1 - a precondition refused to run (an incomplete prerequisite map, the
#       drift guard, a Go toolchain mismatch against the go.mod pin), or
#       at least one gate FAILED (fail-fast: stops at the first failure
#       and names it)
#   2 - every gate PASSED or SKIPPED, but at least one SKIPPED: partial
#       coverage, deliberately distinguishable from both a clean pass and
#       a failure by exit code alone, not by output text
#
# `make ci-local` cannot carry this tri-state: GNU Make maps ANY non-zero
# recipe exit to make's own exit 2, so a real gate failure and a routine
# skip both surface to `make` as exit 2. `make ci-local` is the human
# entry point; a machine consumer that needs the real tri-state invokes
# this script directly, or reads the final `CI_LOCAL_RESULT=` line this
# script prints on every path (PASS/FAIL/PARTIAL), which `make` passes
# through on stdout regardless of which exit code it maps to.
#
# Usage:
#   make ci-local
#   scripts/ci-local/ci-local.sh
#
# Test-only overrides (see scripts/ci-local/ci_local_e2e_test.go), not for
# routine use:
#   CI_LOCAL_REPO_ROOT     working directory to cd into and run make from
#   CI_LOCAL_TARGETS_LIB   path to the sourced target-list lib
#   CI_LOCAL_WORKFLOW_FILE workflow file/dir passed to the drift guard

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${CI_LOCAL_REPO_ROOT:-$(cd "${SCRIPT_DIR}/../.." && pwd)}"
# shellcheck source=scripts/ci-local/lib/ci-local-targets.sh
source "${CI_LOCAL_TARGETS_LIB:-${SCRIPT_DIR}/lib/ci-local-targets.sh}"

cd "${REPO_ROOT}"

GATE_LOG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ci-local.XXXXXX")"
KEEP_GATE_LOGS=0
# Invoked indirectly via 'trap cleanup EXIT INT TERM' below, not by a direct
# call; confirmed the SC2329 below is a false positive by isolating the
# identical trap-by-name pattern in a standalone file, where shellcheck does
# not raise it there.
# shellcheck disable=SC2329
cleanup() {
  if [[ "${KEEP_GATE_LOGS}" -eq 1 ]]; then
    printf 'ci-local: a gate failed; preserving its logs for inspection: %s\n' "${GATE_LOG_DIR}" >&2
    return
  fi
  rm -rf "${GATE_LOG_DIR}"
}
trap cleanup EXIT INT TERM

GATE_ORDER=()
declare -A GATE_STATUS=()
declare -A GATE_DETAIL=()

record() { # record <gate> <status> [detail]
  local gate="$1" status="$2" detail="${3:-}"
  GATE_ORDER+=("${gate}")
  GATE_STATUS["${gate}"]="${status}"
  GATE_DETAIL["${gate}"]="${detail}"
}

# csip_fixture_path <env-var-name> <default-leaf> -- the harness resolves
# each of the three SunSpec PKI files env-var-first (test/csip/fixture_gate_test.go),
# so detection here must honour the same three env vars or it can decide
# "absent" while the harness would have found and run against real
# material supplied purely by environment.
csip_fixture_path() {
  local env_name="$1" leaf="$2" val
  val="${!env_name:-}"
  if [[ -n "${val}" ]]; then
    printf '%s\n' "${val}"
  else
    printf '%s\n' "test/csip/fixtures/sunspec/${leaf}"
  fi
}

csip_fixtures_present() {
  local cert key roots
  cert="$(csip_fixture_path CSIP_SUNSPEC_CERT cert.pem)"
  key="$(csip_fixture_path CSIP_SUNSPEC_KEY key.pem)"
  roots="$(csip_fixture_path CSIP_SUNSPEC_ROOTS roots.pem)"
  [[ -f "${cert}" && -f "${key}" && -f "${roots}" ]]
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

# report_gate_result <gate> <rc> <log> -- the one place run_make_gate and
# run_ui_check_gate report PASSED/FAILED, so the two never drift apart on
# what "reporting a result" means (they used to share a copy-pasted tail
# instead of this call).
report_gate_result() {
  local gate="$1" rc="$2" log="$3"
  if [[ "${rc}" -eq 0 ]]; then
    record "${gate}" PASSED
    printf 'PASSED  %s\n' "${gate}"
    return 0
  fi
  KEEP_GATE_LOGS=1
  record "${gate}" FAILED "exit ${rc}, log ${log}"
  printf 'FAILED  %s (exit %s)\n' "${gate}" "${rc}"
  printf -- '--- last 40 lines of %s output (%s) ---\n' "${gate}" "${log}"
  tail -n 40 "${log}"
  return 1
}

run_make_gate() { # run_make_gate <gate> [env-assignment ...]
  local gate="$1"
  shift
  local extra_env=("$@")
  local log="${GATE_LOG_DIR}/${gate}.log"
  local rc=0
  env "${extra_env[@]}" make "${gate}" >"${log}" 2>&1 || rc=$?
  report_gate_result "${gate}" "${rc}" "${log}"
}

run_ui_check_gate() {
  local gate="ui-check"
  local log="${GATE_LOG_DIR}/${gate}.log"
  local dist_path="internal/server/web/dist"

  # Snapshot BEFORE running: make ui-check rebuilds this directory, so any
  # dirt found afterward is only safely attributable to this run if there
  # was none beforehand. A dirty-before tree means a developer has real
  # uncommitted work there (e.g. `make ui-build` not yet committed); this
  # refuses to rebuild over it rather than reverting it, and rather than
  # running the rebuild and reporting FAILED while quietly healing on the
  # next run once the "fix" is that this gate erased the evidence.
  local before
  before="$(git -C "${REPO_ROOT}" status --porcelain -- "${dist_path}")"
  if [[ -n "${before}" ]]; then
    record "${gate}" FAILED "internal/server/web/dist has pre-existing uncommitted changes; refusing to rebuild over them. Commit, stash, or run 'git checkout -- ${dist_path}' yourself, then re-run."
    printf 'FAILED  %s (pre-existing uncommitted changes under %s; refusing to rebuild over them)\n' "${gate}" "${dist_path}"
    return 1
  fi

  local rc=0
  make "${gate}" >"${log}" 2>&1 || rc=$?

  # Reached only when `before` was clean, so anything dirty now is
  # entirely this run's own rebuild; safe to unconditionally restore.
  # Both git commands are checked explicitly rather than left bare: this
  # function is called as `run_ui_check_gate || {...}` in main, which
  # suspends errexit for everything inside the function, so an unguarded
  # `git checkout`/`git clean` failure here would silently leave the
  # bundle rebuilt while still reporting whatever `rc` said above.
  local restore_rc=0
  git -C "${REPO_ROOT}" checkout --quiet -- "${dist_path}" || restore_rc=$?
  git -C "${REPO_ROOT}" clean --quiet -fd -- "${dist_path}" || restore_rc=$?
  if [[ "${restore_rc}" -ne 0 ]]; then
    KEEP_GATE_LOGS=1
    record "${gate}" FAILED "exit ${rc}, log ${log}; ALSO failed to restore ${dist_path} (exit ${restore_rc}); the tree may still be rebuilt, check git status yourself"
    printf 'FAILED  %s (restore of %s failed, exit %s; tree may be left rebuilt)\n' "${gate}" "${dist_path}" "${restore_rc}"
    return 1
  fi

  report_gate_result "${gate}" "${rc}" "${log}"
}

# assert_prereq_map_complete refuses to start if any target in the run
# list has no entry in CI_LOCAL_TARGET_PREREQ, rather than letting a
# missing key silently degrade to "none" (via a `:-none` default) and run
# with no prerequisite check at all. A CSIP target added to
# CI_LOCAL_MAKE_TARGETS without a matching CI_LOCAL_TARGET_PREREQ entry
# would otherwise run against absent fixtures and report PASSED -- the
# exact misleading pass the prerequisite map exists to prevent.
assert_prereq_map_complete() {
  local gate
  local missing=()
  for gate in "${CI_LOCAL_MAKE_TARGETS[@]}"; do
    if [[ -z "${CI_LOCAL_TARGET_PREREQ[${gate}]+_}" ]]; then
      missing+=("${gate}")
    fi
  done
  if [[ "${#missing[@]}" -gt 0 ]]; then
    printf 'ci-local: refusing to run: CI_LOCAL_TARGET_PREREQ has no entry for: %s\n' "${missing[*]}" >&2
    printf 'ci-local: every target in CI_LOCAL_MAKE_TARGETS needs a prerequisite class in scripts/ci-local/lib/ci-local-targets.sh, even "none", so a target can never silently run unclassified.\n' >&2
    return 1
  fi
  return 0
}

# check_toolchain returns 0 (matches the go.mod pin), 1 (mismatch: this is
# folded into the run's verdict, not decoration -- a warning nothing can
# fail is worth printing but not worth trusting), or 2 (indeterminate:
# neither pin nor local version could be read, so there is nothing to
# compare).
check_toolchain() {
  local pinned local_version
  pinned="$(awk '/^go[[:space:]]+[0-9]+\.[0-9]+(\.[0-9]+)?/ {print $2; exit}' "${REPO_ROOT}/go.mod")"
  local_version="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  if [[ -z "${pinned}" || -z "${local_version}" ]]; then
    printf 'TOOLCHAIN: could not determine the pinned (%s) or local (%s) Go version; skipping the comparison.\n' "${pinned:-?}" "${local_version:-?}"
    return 2
  fi
  if [[ "${local_version}" == "${pinned}" ]]; then
    printf 'TOOLCHAIN: local go%s matches the go.mod pin ci.yml resolves via go-version-file (go%s).\n' "${local_version}" "${pinned}"
    return 0
  fi
  printf 'TOOLCHAIN: MISMATCH - local go%s differs from the go.mod pin ci.yml resolves via go-version-file (go%s). Refusing to run the gates: every result would be against a different compiler than CI would use.\n' "${local_version}" "${pinned}"
  return 1
}

print_not_covered() {
  cat <<'EOF'
NOT COVERED by this local run (documented gaps, not silent ones):
  - Static analysis: comes from the GRIDAPPSD organization's default code
    scanning setup, not a repository workflow; there is no local CLI wired
    into this repo for it.
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
  printf 'Gates to run (%d), matching the make targets .github/workflows/ invokes, in order:\n' "${#CI_LOCAL_MAKE_TARGETS[@]}"
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
  printf '%d PASSED, %d SKIPPED, %d FAILED (%d of %d gates attempted)\n' \
    "${passed}" "${skipped}" "${failed}" "${#GATE_ORDER[@]}" "${#CI_LOCAL_MAKE_TARGETS[@]}"
  echo
  print_not_covered
}

# finish <exit-code> prints the machine-readable tri-state line on every
# exit path, then exits with it. `make ci-local` collapses any non-zero
# recipe exit to make's own exit 2 (GNU Make maps 0->0, anything else->2),
# so a wrapper that needs the real tri-state through `make` cannot read it
# from the exit code; it reads this stdout line instead, which `make`
# passes through unchanged.
finish() {
  local code="$1"
  case "${code}" in
  0) printf 'CI_LOCAL_RESULT=PASS\n' ;;
  2) printf 'CI_LOCAL_RESULT=PARTIAL\n' ;;
  *) printf 'CI_LOCAL_RESULT=FAIL\n' ;;
  esac
  exit "${code}"
}

main() {
  if ! assert_prereq_map_complete; then
    finish 1
  fi

  echo "=== Drift guard ==="
  if ! "${SCRIPT_DIR}/ci-local-drift-check.sh" ${CI_LOCAL_WORKFLOW_FILE:+"${CI_LOCAL_WORKFLOW_FILE}"}; then
    echo "ci-local: refusing to run the gates; the drift guard above must pass first." >&2
    finish 1
  fi
  echo

  echo "=== Toolchain check ==="
  local toolchain_rc=0
  check_toolchain || toolchain_rc=$?
  if [[ "${toolchain_rc}" -eq 1 ]]; then
    echo "ci-local: refusing to run the gates; the toolchain mismatch above must be resolved first." >&2
    finish 1
  fi
  echo

  print_gate_plan
  echo

  local gate prereq skip_reason
  for gate in "${CI_LOCAL_MAKE_TARGETS[@]}"; do
    prereq="${CI_LOCAL_TARGET_PREREQ[${gate}]}"
    skip_reason=""
    case "${prereq}" in
    csip-fixtures)
      csip_fixtures_present || skip_reason="SunSpec CSIP test PKI absent (checked CSIP_SUNSPEC_CERT/KEY/ROOTS env vars, then test/csip/fixtures/sunspec/; gitignored, provisioned out of band; see test/csip/README.md)"
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
        finish 1
      }
    elif [[ "${prereq}" == "csip-fixtures" ]]; then
      # The fixtures were just confirmed present above; arm
      # CSIP_SUNSPEC_REQUIRED=1 so the suite fails loudly instead of
      # silently self-skipping if that resolution turns out to be
      # unusable by the time the harness itself re-resolves it (see
      # HIGH-2: detection deciding "run" must not be undercut by the
      # harness quietly skipping anyway).
      run_make_gate "${gate}" CSIP_SUNSPEC_REQUIRED=1 || {
        print_summary
        finish 1
      }
    else
      run_make_gate "${gate}" || {
        print_summary
        finish 1
      }
    fi
  done

  print_summary

  local skipped_count=0
  for gate in "${GATE_ORDER[@]}"; do
    [[ "${GATE_STATUS[${gate}]}" == "SKIPPED" ]] && skipped_count=$((skipped_count + 1))
  done
  if [[ "${skipped_count}" -gt 0 ]]; then
    finish 2
  fi
  finish 0
}

main "$@"
