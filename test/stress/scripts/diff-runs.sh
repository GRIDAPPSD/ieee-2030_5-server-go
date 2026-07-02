#!/usr/bin/env bash
# scripts/diff-runs.sh <run-a-dir> <run-b-dir>
# Compares two stress run result directories: params, breaking-point, and
# p99 latency trends. Output is plain text suitable for a terminal or CI log.
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }

[ $# -eq 2 ] || die "usage: diff-runs.sh <run-a> <run-b>"
RUN_A="$1"
RUN_B="$2"
[ -d "$RUN_A" ] || die "not a directory: $RUN_A"
[ -d "$RUN_B" ] || die "not a directory: $RUN_B"

echo "=== diff-runs: $RUN_A vs $RUN_B ==="

py3() {
python3 - "$@" <<'PY'
import sys, json, os

def load(path):
    if os.path.exists(path):
        return json.load(open(path))
    return {}

a_dir, b_dir = sys.argv[1], sys.argv[2]

print("\n-- params --")
pa = load(f"{a_dir}/params.json")
pb = load(f"{b_dir}/params.json")
all_keys = sorted(set(list(pa) + list(pb)))
for k in all_keys:
    va, vb = pa.get(k, "(missing)"), pb.get(k, "(missing)")
    marker = "" if va == vb else "  <-- DIFFERS"
    print(f"  {k}: {va!r} vs {vb!r}{marker}")

print("\n-- breaking-point --")
ba = load(f"{a_dir}/breaking-point.json")
bb = load(f"{b_dir}/breaking-point.json")
bp_keys = ["dimension","criterion","value_at_break","time_elapsed_sec","at_clients","host_limited"]
for k in bp_keys:
    va, vb = ba.get(k, "(missing)"), bb.get(k, "(missing)")
    marker = "" if va == vb else "  <-- DIFFERS"
    print(f"  {k}: {va!r} vs {vb!r}{marker}")

print("\n-- p99 latency trend (from metrics-series.jsonl) --")
import re

def extract_p99_samples(metrics_dir):
    path = f"{metrics_dir}/metrics-series.jsonl"
    if not os.path.exists(path):
        return []
    samples = []
    for line in open(path):
        try:
            obj = json.loads(line)
            raw = obj.get("raw", "")
        except Exception:
            continue
        # Extract sep2_http_request_duration_seconds{...,le="0.5"} and le="1" to approximate p99
        for m in re.finditer(r'sep2_http_request_duration_seconds_bucket\{[^}]*le="([^"]+)"[^}]*\}\s+(\S+)', raw):
            le_val = m.group(1)
            count = m.group(2)
            if le_val in ("+Inf", "1.0", "0.5"):
                samples.append((obj.get("ts_ms", 0), le_val, count))
    return samples

sa = extract_p99_samples(a_dir)
sb = extract_p99_samples(b_dir)
print(f"  run-a p99 samples: {len(sa)}")
print(f"  run-b p99 samples: {len(sb)}")
print("  (use the full metrics-series.jsonl for detailed latency analysis)")
PY
}

py3 "$RUN_A" "$RUN_B"

echo ""
echo "=== done ==="
