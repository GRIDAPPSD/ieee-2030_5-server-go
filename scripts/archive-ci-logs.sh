#!/usr/bin/env bash
# IEEE-109 — Mirror CI artifacts from a GitHub Actions workflow run into the
# Knowledge workspace for long-term retention.
#
# Why:
#   GitHub Actions caps artifact retention at 90 days. Plan-2 Phase 9 needs
#   archive-grade evidence (≥3 years) anchored to the attestation `main` SHA.
#   This script downloads the `csip-logs-<hooks>-<sha>` and `coverage-*`
#   artifacts produced by `.github/workflows/ci.yml` and lays them out under
#   the Knowledge repo at a structured, year/month-bucketed path.
#
# Layout (mirror destination):
#
#   <knowledge>/projects/ieee-2030_5-go/artifacts/outputs/ci-archive/
#     <YYYY>/<MM>/<workflow-run-id>/
#       run.json            workflow-run metadata snapshot (gh run view --json)
#       csip-logs-off/      contents of the csip-logs-off-<sha> artifact
#       csip-logs-on/       contents of the csip-logs-on-<sha> artifact
#       coverage/           contents of the coverage-<run>-<attempt> artifact
#
# Usage:
#   archive-ci-logs.sh <workflow-run-id> [--knowledge-root <path>]
#
# Defaults:
#   --knowledge-root defaults to $KNOWLEDGE_ROOT or $HOME/knowledge.
#
# Dependencies:
#   gh (GitHub CLI), authenticated against the GRIDAPPSD/ieee-2030_5-go repo.
#   The script uses `gh --jq` for JSON parsing (embedded in gh); no external
#   jq binary is required.
#
# Re-run procedure (per Phase 9 retention policy):
#   1. Pick an attestation SHA on main.
#   2. Trigger a CI run on that SHA: `gh workflow run ci.yml --ref <SHA>` or
#      re-run the latest push run.
#   3. Wait for the run to complete (gh run watch <id>).
#   4. archive-ci-logs.sh <id>.
#   5. Update INDEX.md (script does this automatically for new rows).
#
# Idempotency:
#   If <year>/<month>/<run-id>/ already exists, the script exits non-zero
#   with a clear message — refuse to silently overwrite. Pass --force to
#   replace the directory.

set -euo pipefail

REPO="GRIDAPPSD/ieee-2030_5-go"
KNOWLEDGE_ROOT="${KNOWLEDGE_ROOT:-$HOME/knowledge}"
FORCE=0
RUN_ID=""

usage() {
  cat <<EOF
Usage: $(basename "$0") <workflow-run-id> [--knowledge-root <path>] [--force]

Mirror CI artifacts from a GitHub Actions workflow run into the Knowledge
workspace at:
  <knowledge-root>/projects/ieee-2030_5-go/artifacts/outputs/ci-archive/<YYYY>/<MM>/<run-id>/

Options:
  --knowledge-root <path>  Override the Knowledge workspace root (default:
                           \$KNOWLEDGE_ROOT or \$HOME/knowledge).
  --force                  Overwrite an existing destination directory.
  -h, --help               Show this help.

Environment:
  KNOWLEDGE_ROOT  Default Knowledge workspace path if --knowledge-root unset.

Example:
  $(basename "$0") 25782404605
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --knowledge-root)
      KNOWLEDGE_ROOT="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    -*)
      echo "error: unknown flag: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      if [[ -z "$RUN_ID" ]]; then
        RUN_ID="$1"
      else
        echo "error: extra positional arg: $1" >&2
        usage >&2
        exit 2
      fi
      shift
      ;;
  esac
done

if [[ -z "$RUN_ID" ]]; then
  echo "error: workflow-run-id is required" >&2
  usage >&2
  exit 2
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh (GitHub CLI) not found on PATH" >&2
  exit 1
fi

archive_root="$KNOWLEDGE_ROOT/projects/ieee-2030_5-go/artifacts/outputs/ci-archive"
if [[ ! -d "$archive_root" ]]; then
  echo "error: archive root does not exist: $archive_root" >&2
  echo "       create it (and INDEX.md / README.md) before running this script." >&2
  exit 1
fi

echo "==> Fetching run metadata for $REPO run $RUN_ID"
# Single round-trip for the pretty-printed metadata snapshot we persist.
fields="databaseId,headSha,headBranch,event,status,conclusion,workflowName,createdAt,updatedAt,url"
run_json=$(gh run view "$RUN_ID" --repo "$REPO" --json "$fields")

# Pull individual fields via gh's embedded --jq so no external jq is needed.
view_field() {
  gh run view "$RUN_ID" --repo "$REPO" --json "$fields" --jq "$1"
}
created_at=$(view_field '.createdAt')
head_sha=$(view_field '.headSha')
head_branch=$(view_field '.headBranch')
conclusion=$(view_field '.conclusion')
run_url=$(view_field '.url')

# Year and month buckets are derived from createdAt (UTC) for stable sort.
year=$(date -u -d "$created_at" +%Y 2>/dev/null || echo "$created_at" | cut -c1-4)
month=$(date -u -d "$created_at" +%m 2>/dev/null || echo "$created_at" | cut -c6-7)
short_sha="${head_sha:0:7}"

dest="$archive_root/$year/$month/$RUN_ID"
if [[ -e "$dest" ]]; then
  if [[ "$FORCE" -eq 1 ]]; then
    echo "==> --force set; removing existing $dest"
    rm -rf -- "$dest"
  else
    echo "error: destination already exists: $dest" >&2
    echo "       pass --force to overwrite." >&2
    exit 1
  fi
fi

mkdir -p "$dest"
# Persist the raw metadata as-is. gh emits compact JSON; pretty-print only if
# python3 is around so the file diffs cleanly in code review.
if command -v python3 >/dev/null 2>&1; then
  printf '%s' "$run_json" | python3 -c 'import json,sys; json.dump(json.load(sys.stdin), sys.stdout, indent=2, sort_keys=True); sys.stdout.write("\n")' > "$dest/run.json"
else
  printf '%s\n' "$run_json" > "$dest/run.json"
fi

# Download every artifact attached to the run. `gh run download` places each
# artifact into a directory named after the artifact under -D.
echo "==> Downloading artifacts into $dest"
if ! gh run download "$RUN_ID" --repo "$REPO" -D "$dest" 2>&1; then
  echo "warning: gh run download returned non-zero — artifacts may be missing or expired" >&2
fi

# Normalize artifact directory names so the layout is stable across runs.
# The CSIP harness uploads `csip-logs-<hooks>-<sha>`; coverage uploads
# `coverage-<run>-<attempt>`. Rename to canonical short names.
shopt -s nullglob
for d in "$dest"/csip-logs-off-*; do
  mv -- "$d" "$dest/csip-logs-off"
done
for d in "$dest"/csip-logs-on-*; do
  mv -- "$d" "$dest/csip-logs-on"
done
for d in "$dest"/coverage-*; do
  mv -- "$d" "$dest/coverage"
done
shopt -u nullglob

# Sanity check: surface what landed.
echo "==> Archive contents:"
( cd "$dest" && find . -maxdepth 2 -mindepth 1 -printf '    %P\n' )

# INDEX.md update — append a new row if not already present. Keep table
# format stable so reviewers (Dutch / Leon) can diff.
index="$archive_root/INDEX.md"
if [[ ! -f "$index" ]]; then
  echo "warning: $index missing — skipping table update (create it first)" >&2
else
  if grep -q "| $RUN_ID |" "$index"; then
    echo "==> INDEX.md already has row for run $RUN_ID; not duplicating"
  else
    rel_dest="${year}/${month}/${RUN_ID}"
    row="| $RUN_ID | $created_at | $short_sha | $head_branch | $conclusion | [\`$rel_dest/\`]($rel_dest/) | [run]($run_url) |"
    # Append the row at end-of-file. The table header is in INDEX.md; we
    # only ever append data rows here.
    printf '%s\n' "$row" >> "$index"
    echo "==> Appended row to $index"
  fi
fi

echo "==> Done. Archive: $dest"
