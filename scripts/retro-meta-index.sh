#!/usr/bin/env bash
#
# retro-meta-index.sh — read-only cross-retro failure-mode / root-cause index.
#
# Aggregates the 7-key retro-meta sidecars from all CURRENT (non-superseded)
# cadence-retro refs into a markdown report (stdout) or machine-readable JSON
# (--json mode). Mutates nothing: no git writes, no ref changes, no bd calls.
#
# Usage:
#   bash scripts/retro-meta-index.sh [--json] [--quiet] [--help]
#
# Options:
#   --json    Emit machine-readable JSON instead of markdown.
#   --quiet   Suppress informational lines; only emit the report.
#   --help    Show this help and exit 0.
#
# Exit codes:
#   0  success (report emitted; zero retros found is still a success)
#   1  unexpected error (git unavailable, python3 unavailable)
#   2  usage error
#
# NOTE on exact-string tallying:
#   failure_modes and root_causes are free-text prose sentences. "Recurrence"
#   means the identical string appears verbatim in >=2 retro sidecars. Given
#   that human-written sentences rarely match exactly, most entries today have
#   count=1 (singletons). The report presents all entries honestly: a summary
#   line states the count of recurring (>=2) entries, then lists ALL entries
#   attributed to their source retro(s).
#
# Style mirrors scripts/dogfood-completeness-audit.sh:
#   set -euo pipefail, repo-root cd, git for-each-ref + git cat-file + python3.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
emit_json=false
quiet=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --json)   emit_json=true; shift ;;
    --quiet)  quiet=true; shift ;;
    --help|-h)
      sed -n '2,/^set -/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *)
      echo "retro-meta-index: unknown option: $1" >&2
      echo "Usage: bash scripts/retro-meta-index.sh [--json] [--quiet] [--help]" >&2
      exit 2 ;;
  esac
done

# ---------------------------------------------------------------------------
# Temp files — cleaned up on exit
# ---------------------------------------------------------------------------
retro_jsonl_file="$(mktemp)"
py_script_file="$(mktemp)"
trap 'rm -f "$retro_jsonl_file" "$py_script_file"' EXIT

# ---------------------------------------------------------------------------
# Build superseded-set — identical pattern to dogfood-completeness-audit.sh:380-395.
# One pass over refs/etude/retros: collect every non-empty refs.supersedes value.
# ---------------------------------------------------------------------------
declare -A superseded_set   # bare-retro-id -> 1

while IFS= read -r retro_ref; do
  sup_val="$(git cat-file -p "$retro_ref:manifest.json" 2>/dev/null | python3 -c "
import json,sys
m=json.load(sys.stdin)
v=m.get('refs',{}).get('supersedes','')
if v:
    print(v)
" 2>/dev/null || true)"
  if [[ -n "$sup_val" ]]; then
    superseded_set["$sup_val"]=1
  fi
done < <(git for-each-ref refs/etude/retros --format='%(refname)' 2>/dev/null || true)

# ---------------------------------------------------------------------------
# Collect all current cadence-retro sidecars.
# For each ref:
#   1. SKIP unconditionally if its bare id is in superseded_set (mirror audit :413-414).
#      Applied BEFORE the trigger filter.
#   2. Read manifest; keep only trigger==cadence-retro (mirror audit :491).
#   3. Find output.role==retro-meta stage; read the blob (mirror audit :524-554).
# ---------------------------------------------------------------------------
# Accumulate sidecar data as JSONL; aggregate in one python3 pass below.

while IFS= read -r retro_ref; do
  retro_short="${retro_ref#refs/etude/retros/}"

  # Skip unconditionally if superseded — mirror check (c) at audit:413-414,
  # NOT the pre-cutoff-conditional skip from check (f) at audit:519.
  [[ -v "superseded_set[$retro_short]" ]] && continue

  manifest_data="$(git cat-file -p "$retro_ref:manifest.json" 2>/dev/null)" || continue

  # Keep only cadence-retro trigger (mirror audit :491)
  trigger="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
print(m.get('refs',{}).get('trigger',''))
" <<< "$manifest_data" 2>/dev/null || echo "")"
  [[ "$trigger" == "cadence-retro" ]] || continue

  # Find retro-meta stage and read sidecar blob (mirror audit :524-554)
  sidecar_line="$(python3 -c "
import json,sys,subprocess
ref='$retro_ref'
rid='$retro_short'
m=json.load(sys.stdin)
stages=m.get('stages',[])
meta_stage=None
for s in stages:
    if s.get('output',{}).get('role','')=='retro-meta':
        meta_stage=s
        break
if meta_stage is None:
    sys.exit(0)
blob_path=meta_stage.get('output',{}).get('path','')
if not blob_path:
    sys.exit(0)
try:
    result=subprocess.run(
        ['git','cat-file','-p',ref+':'+blob_path],
        capture_output=True,text=True,timeout=10
    )
    if result.returncode!=0:
        sys.exit(0)
    sidecar=json.loads(result.stdout)
except Exception:
    sys.exit(0)
if not isinstance(sidecar,dict):
    sys.exit(0)
out={'retro_id':rid,'sidecar':sidecar}
print(json.dumps(out))
" <<< "$manifest_data" 2>/dev/null || true)"

  if [[ -n "$sidecar_line" ]]; then
    printf '%s\n' "$sidecar_line" >> "$retro_jsonl_file"
  fi
done < <(git for-each-ref refs/etude/retros --format='%(refname)' 2>/dev/null || true)

# ---------------------------------------------------------------------------
# Write the aggregation+emission python script to a temp file, then run it
# with the JSONL file on stdin. This avoids the heredoc vs stdin-redirect
# conflict (a heredoc always wins over < redirection).
# ---------------------------------------------------------------------------
cat > "$py_script_file" << 'PYEOF'
import json, sys, collections

emit_json_arg = sys.argv[1].lower() == "true"

# Read JSONL from stdin
entries = []
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        entries.append(json.loads(line))
    except json.JSONDecodeError:
        continue

# Sort entries by (original_event_date, retro_id) for deterministic ordering
def sort_key(e):
    s = e.get("sidecar", {})
    return (s.get("original_event_date", ""), e.get("retro_id", ""))

entries.sort(key=sort_key)

retros_indexed = len(entries)

# Collect date range
dates = [
    e["sidecar"]["original_event_date"]
    for e in entries
    if e.get("sidecar", {}).get("original_event_date")
]
date_from = min(dates) if dates else ""
date_to   = max(dates) if dates else ""

# ---------------------------------------------------------------------------
# Tally failure_modes by exact string
# ---------------------------------------------------------------------------
fm_tally  = collections.Counter()
fm_retros = collections.defaultdict(list)
for e in entries:
    rid = e["retro_id"]
    for val in e.get("sidecar", {}).get("failure_modes", []):
        if isinstance(val, str) and val:
            fm_tally[val] += 1
            fm_retros[val].append(rid)

# ---------------------------------------------------------------------------
# Tally root_causes by exact string
# ---------------------------------------------------------------------------
rc_tally  = collections.Counter()
rc_retros = collections.defaultdict(list)
for e in entries:
    rid = e["retro_id"]
    for val in e.get("sidecar", {}).get("root_causes", []):
        if isinstance(val, str) and val:
            rc_tally[val] += 1
            rc_retros[val].append(rid)

# ---------------------------------------------------------------------------
# Follow-up beads index: bead_id -> [retro_id, ...]
# ---------------------------------------------------------------------------
bead_retros = collections.defaultdict(list)
for e in entries:
    rid = e["retro_id"]
    for val in e.get("sidecar", {}).get("follow_up_beads", []):
        if isinstance(val, str) and val:
            bead_retros[val].append(rid)

# ---------------------------------------------------------------------------
# Durable changes timeline: ordered by (original_event_date, retro_id)
# Inherits sort order from entries.
# ---------------------------------------------------------------------------
durable_timeline = []
for e in entries:
    rid = e["retro_id"]
    oed = e.get("sidecar", {}).get("original_event_date", "")
    for val in e.get("sidecar", {}).get("durable_changes", []):
        if isinstance(val, str) and val:
            durable_timeline.append({"value": val, "original_event_date": oed, "retro": rid})

# ---------------------------------------------------------------------------
# Sort helpers: count desc, then lexical asc (deterministic)
# ---------------------------------------------------------------------------
def sorted_tally(tally, retros_map):
    items = sorted(tally.items(), key=lambda kv: (-kv[1], kv[0]))
    return [{"value": v, "count": c, "retros": sorted(retros_map[v])} for v, c in items]

sorted_fm    = sorted_tally(fm_tally, fm_retros)
sorted_rc    = sorted_tally(rc_tally, rc_retros)
sorted_beads = sorted(
    [{"bead": b, "retros": sorted(rs)} for b, rs in bead_retros.items()],
    key=lambda x: x["bead"]
)

# ---------------------------------------------------------------------------
# JSON output
# ---------------------------------------------------------------------------
if emit_json_arg:
    out = {
        "retros_indexed": retros_indexed,
        "date_range": {"from": date_from, "to": date_to},
        "failure_modes": sorted_fm,
        "root_causes": sorted_rc,
        "follow_up_beads": sorted_beads,
        "durable_changes": durable_timeline,
    }
    print(json.dumps(out, indent=2, ensure_ascii=False))
    sys.exit(0)

# ---------------------------------------------------------------------------
# Markdown output
# ---------------------------------------------------------------------------
SEPARATOR = "-" * 72

def recurring_count(tally):
    return sum(1 for c in tally.values() if c >= 2)

fm_recurring  = recurring_count(fm_tally)
rc_recurring  = recurring_count(rc_tally)
total_fm      = len(fm_tally)
total_rc      = len(rc_tally)
total_beads   = len(bead_retros)
total_durable = len(durable_timeline)

# Header
print("# Cross-Retro Meta-Index")
print("")
print("> NOTE: Recurrence is tallied by **exact string match** (verbatim prose).")
print("> Human-written sentences rarely match identically across retros, so most")
print("> entries today have count=1 (singletons). This is expected and honest.")
print("> Fuzzy / semantic grouping is a future enhancement if a signal appears.")
print("")
print(f"**Retros indexed:** {retros_indexed}")
if date_from and date_to:
    print(f"**Date range:** {date_from} – {date_to}")
print(f"**Failure modes:** {total_fm} distinct across {retros_indexed} retros; "
      f"{fm_recurring} recurring (count ≥2)")
print(f"**Root causes:** {total_rc} distinct; {rc_recurring} recurring (count ≥2)")
print(f"**Follow-up beads:** {total_beads} distinct bead(s) named")
print(f"**Durable changes:** {total_durable} entries in timeline")
print("")

# ---------------------------------------------------------------------------
# Failure modes
# ---------------------------------------------------------------------------
print(SEPARATOR)
print("## Failure Modes")
print("")
print(f"{total_fm} distinct failure modes across {retros_indexed} retros; "
      f"{fm_recurring} recurring (count ≥2).")
print("")
if not sorted_fm:
    print("_No failure modes recorded._")
else:
    for item in sorted_fm:
        marker = "**[RECURRING]** " if item["count"] >= 2 else ""
        retro_list = ", ".join(f"`{r}`" for r in item["retros"])
        print(f"- {marker}(count={item['count']}) {item['value']}")
        print(f"  - retros: {retro_list}")
print("")

# ---------------------------------------------------------------------------
# Root causes
# ---------------------------------------------------------------------------
print(SEPARATOR)
print("## Root Causes")
print("")
print(f"{total_rc} distinct root causes across {retros_indexed} retros; "
      f"{rc_recurring} recurring (count ≥2).")
print("")
if not sorted_rc:
    print("_No root causes recorded._")
else:
    for item in sorted_rc:
        marker = "**[RECURRING]** " if item["count"] >= 2 else ""
        retro_list = ", ".join(f"`{r}`" for r in item["retros"])
        print(f"- {marker}(count={item['count']}) {item['value']}")
        print(f"  - retros: {retro_list}")
print("")

# ---------------------------------------------------------------------------
# Follow-up beads
# ---------------------------------------------------------------------------
print(SEPARATOR)
print("## Follow-Up Beads Index")
print("")
print(f"{total_beads} distinct bead(s) named across retros.")
print("")
if not sorted_beads:
    print("_No follow-up beads recorded._")
else:
    for item in sorted_beads:
        count = len(item["retros"])
        marker = "**[MULTI-RETRO]** " if count >= 2 else ""
        retro_list = ", ".join(f"`{r}`" for r in item["retros"])
        print(f"- {marker}`{item['bead']}` (named by {count} retro(s)): {retro_list}")
print("")

# ---------------------------------------------------------------------------
# Durable changes timeline
# ---------------------------------------------------------------------------
print(SEPARATOR)
print("## Durable Changes Timeline")
print("")
print(f"{total_durable} durable change(s), ordered by original_event_date.")
print("")
if not durable_timeline:
    print("_No durable changes recorded._")
else:
    prev_date = None
    for item in durable_timeline:
        d = item["original_event_date"]
        if d != prev_date:
            print(f"### {d or '(no date)'}")
            prev_date = d
        print(f"- [{item['retro']}] {item['value']}")
    print("")
PYEOF

python3 "$py_script_file" "$emit_json" < "$retro_jsonl_file"
