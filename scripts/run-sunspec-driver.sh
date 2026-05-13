#!/usr/bin/env bash
# IEEE-110 — Run the SunSpec CSIP conformance-test driver against the
# `ieee-2030_5-go` server and emit a populated copy of the IEEE-110 report
# template into a structured output directory.
#
# Why:
#   Plan-2 Phase 9 (CSIP V1.2 conformance attestation) bundles three
#   deliverables. IEEE-108 is the self-attestation letter, IEEE-109 is the
#   internal-CI log archive, and IEEE-110 is the independent SunSpec-driver
#   execution record. This script is the harness that turns a one-off
#   driver invocation into a reproducible, archived artifact pinned to a
#   specific `main` SHA.
#
# What this script DOES NOT do:
#   - It is not the SunSpec driver itself. The driver is a vendor tool
#     (QualityLogic or equivalent reference implementation per Phase 9
#     risks section). The script invokes whichever driver path the
#     operator passes via --driver.
#   - It does not boot the server. The operator runs `make run-ccm`
#     (or equivalent) in a separate shell before launching this script.
#     The script's --target argument names the running server.
#
# Usage:
#   run-sunspec-driver.sh \
#       --driver <PATH>          path to the SunSpec driver executable
#       --target <URL>           server under test (e.g. https://localhost:8443)
#       --out <DIR>              output directory for driver logs + report
#       --sha <ATTEST-SHA>       `main` SHA the server is built from
#       [--report-template <P>]  override the report template path
#       [--dry-run]              run a no-op stub instead of the real driver
#                                (useful for harness smoke tests)
#
# Output layout (--out <DIR>):
#   report.md            populated copy of the IEEE-110 template with
#                        placeholders filled from driver output
#   driver-stdout.log    captured driver stdout
#   driver-stderr.log    captured driver stderr
#   driver-exit-code     plaintext exit code
#   metadata.json        run metadata (server SHA, target URL, timestamps,
#                        driver path, operator)
#
# Dependencies:
#   bash >= 4 (for `mapfile`).
#   Standard POSIX tools: cat, sed, date, mkdir, mv, cp, tee, printf.
#   The SunSpec driver itself (vendor-supplied) when --dry-run is not set.
#
# Reproduction:
#   See IEEE-110 report §6 "Reproducing This Report" in
#   <knowledge>/projects/ieee-2030_5-go/artifacts/outputs/csip-v1.2-sunspec-execution-report.md.
#
# Exit codes:
#   0   driver ran cleanly and report.md was written.
#   1   bad usage / missing args.
#   2   driver path not executable.
#   3   target URL unreachable (driver exit suggests no-connect).
#   4   destination already populated and --force not passed.
#   5   report template not found.
#  >=10 driver-reported failure (forwarded; subtract 10 for the driver's
#       own exit code).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULT_REPORT_TEMPLATE="${KNOWLEDGE_ROOT:-$HOME/knowledge}/projects/ieee-2030_5-go/artifacts/outputs/csip-v1.2-sunspec-execution-report.md"

DRIVER=""
TARGET=""
OUT_DIR=""
SHA=""
REPORT_TEMPLATE="$DEFAULT_REPORT_TEMPLATE"
DRY_RUN=0
FORCE=0

usage() {
  cat <<EOF
Usage: $(basename "$0") --driver <PATH> --target <URL> --out <DIR> --sha <SHA>
                       [--report-template <PATH>] [--dry-run] [--force]

Run the SunSpec CSIP conformance-test driver against the ieee-2030_5-go
server and populate the IEEE-110 report template.

Required arguments:
  --driver <PATH>          Path to the SunSpec driver executable.
  --target <URL>           Server under test (https://host:port).
  --out <DIR>              Output directory for driver logs + report.
  --sha <ATTEST-SHA>       \`main\` SHA the server is built from.

Optional arguments:
  --report-template <P>    Override the report template path.
                           Default: \$KNOWLEDGE_ROOT/projects/ieee-2030_5-go/
                                    artifacts/outputs/csip-v1.2-sunspec-execution-report.md
  --dry-run                Run a no-op stub instead of the real driver
                           (harness smoke test; emits a stub driver log
                           and a populated report template anyway).
  --force                  Overwrite an existing --out directory.
  -h, --help               Show this help.

Environment:
  KNOWLEDGE_ROOT  Default Knowledge workspace path if --report-template unset.

See \`projects/ieee-2030_5-go/artifacts/outputs/csip-v1.2-sunspec-execution-report.md\`
§6 in the Knowledge workspace for the full reproduction procedure.
EOF
}

# ---------- argument parsing -----------------------------------------------

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --driver) DRIVER="${2:-}"; shift 2 ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --out) OUT_DIR="${2:-}"; shift 2 ;;
    --sha) SHA="${2:-}"; shift 2 ;;
    --report-template) REPORT_TEMPLATE="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --force) FORCE=1; shift ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
done

require_arg() {
  local name="$1" value="$2"
  if [[ -z "$value" ]]; then
    echo "missing required argument: $name" >&2
    usage >&2
    exit 1
  fi
}

require_arg --driver "$DRIVER"
require_arg --target "$TARGET"
require_arg --out "$OUT_DIR"
require_arg --sha "$SHA"

if [[ ! -f "$REPORT_TEMPLATE" ]]; then
  echo "report template not found: $REPORT_TEMPLATE" >&2
  echo "pass --report-template <PATH> or set KNOWLEDGE_ROOT." >&2
  exit 5
fi

if [[ "$DRY_RUN" -eq 0 && ! -x "$DRIVER" ]]; then
  echo "driver not executable: $DRIVER" >&2
  echo "pass --dry-run to run the harness against a no-op stub." >&2
  exit 2
fi

if [[ -e "$OUT_DIR" && "$FORCE" -eq 0 ]]; then
  echo "output directory already exists: $OUT_DIR" >&2
  echo "pass --force to overwrite." >&2
  exit 4
fi

# ---------- prep output directory ------------------------------------------

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

START_TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
START_EPOCH="$(date -u +"%s")"

OPERATOR="${SUDO_USER:-${USER:-unknown}}"

# ---------- run the driver -------------------------------------------------

STDOUT_LOG="$OUT_DIR/driver-stdout.log"
STDERR_LOG="$OUT_DIR/driver-stderr.log"
EXIT_FILE="$OUT_DIR/driver-exit-code"

set +e
if [[ "$DRY_RUN" -eq 1 ]]; then
  # No-op stub: emit a synthetic run record that exercises the placeholder
  # substitution path. Useful for harness smoke tests where the real
  # driver isn't available.
  {
    echo "[dry-run] SunSpec driver stub — no real conformance run"
    echo "[dry-run] target: $TARGET"
    echo "[dry-run] sha:    $SHA"
    echo "[dry-run] driver: $DRIVER (not invoked)"
    echo "[dry-run] all per-procedure results are placeholders"
  } > "$STDOUT_LOG"
  : > "$STDERR_LOG"
  DRIVER_EXIT=0
else
  "$DRIVER" --target "$TARGET" --report "$OUT_DIR" \
      > "$STDOUT_LOG" 2> "$STDERR_LOG"
  DRIVER_EXIT=$?
fi
set -e
printf "%s\n" "$DRIVER_EXIT" > "$EXIT_FILE"

END_TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
END_EPOCH="$(date -u +"%s")"
DURATION=$(( END_EPOCH - START_EPOCH ))
DURATION_HMS="$(printf "%02d:%02d:%02d" $(( DURATION / 3600 )) $(( (DURATION % 3600) / 60 )) $(( DURATION % 60 )))"

# ---------- metadata.json --------------------------------------------------

cat > "$OUT_DIR/metadata.json" <<EOF
{
  "ticket": "IEEE-110",
  "attest_sha": "$SHA",
  "target": "$TARGET",
  "driver_path": "$DRIVER",
  "driver_exit_code": $DRIVER_EXIT,
  "dry_run": $([[ "$DRY_RUN" -eq 1 ]] && echo "true" || echo "false"),
  "operator": "$OPERATOR",
  "start_utc": "$START_TS",
  "end_utc": "$END_TS",
  "duration_seconds": $DURATION,
  "duration_hms": "$DURATION_HMS"
}
EOF

# ---------- populate report template ---------------------------------------

REPORT_OUT="$OUT_DIR/report.md"
cp "$REPORT_TEMPLATE" "$REPORT_OUT"

# Substitute placeholders. We replace the most-anchored fields; per-procedure
# placeholders remain `<RESULT>` because they require driver-output parsing
# that is driver-specific and out of scope for the v1 harness. The bundle
# operator fills the per-procedure cells from `driver-stdout.log` at
# editorial-last-mile time per Phase 9 step 3.
RUN_DATE="$(date -u +"%Y-%m-%d")"

# Use a sentinel to avoid shell escaping headaches with sed -i across BSD/GNU.
# Each substitution is a literal pair: marker -> value.
substitute() {
  local marker="$1" value="$2"
  # Escape forward slashes and ampersands for sed.
  local escaped
  escaped="$(printf "%s" "$value" | sed -e 's/[\/&]/\\&/g')"
  sed -i.bak "s/$marker/$escaped/g" "$REPORT_OUT"
  rm -f "$REPORT_OUT.bak"
}

substitute "<ATTEST-SHA — locked at bundle assembly>" "$SHA"
substitute "<ATTEST-SHA>" "$SHA"
substitute "<RUN-DATE — filled at bundle assembly>" "$RUN_DATE"
substitute "<RUN-DATE>" "$RUN_DATE"

# ---------- final summary --------------------------------------------------

cat <<EOF
[IEEE-110] SunSpec driver run complete.
  attest SHA:     $SHA
  target:         $TARGET
  driver:         $DRIVER$([[ "$DRY_RUN" -eq 1 ]] && echo " (dry-run; not invoked)")
  duration:       $DURATION_HMS
  driver exit:    $DRIVER_EXIT
  output dir:     $OUT_DIR
  report:         $REPORT_OUT

Next steps (per Phase 9 bundle-assembly procedure):
  1. Populate the per-procedure <RESULT> placeholders in report.md by
     parsing driver-stdout.log row-by-row.
  2. Cross-walk §5 reconciliation table against the IEEE-109 archive run
     pinned to the same SHA.
  3. Copy report.md into the Knowledge bundle path:
     <knowledge>/projects/ieee-2030_5-go/artifacts/outputs/csip-v1.2-sunspec-execution-report.md
  4. Update IEEE-108 letter to cite this report's run timestamp.
EOF

# Forward the driver's exit code, biased into the >=10 range so callers can
# distinguish harness failures (1-9) from driver-internal failures.
if [[ "$DRIVER_EXIT" -ne 0 ]]; then
  exit $(( 10 + (DRIVER_EXIT % 200) ))
fi
exit 0
