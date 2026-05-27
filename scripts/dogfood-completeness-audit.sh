#!/usr/bin/env bash
#
# dogfood-completeness-audit.sh — audit whether closed beads have their etude
# dogfood artifacts (run refs, gate records, pushed refs).
#
# Modes:
#   --bead <id>        Audit exactly ONE bead (the mode etude-8hq.1 gates close/push on).
#                      Hard checks: (a) refs/etude/runs/<id> exists,
#                                   (b) its manifest has gates (unless allowlisted),
#                                   (d) that bead's run ref is pushed to origin.
#                      DOES NOT run the window, cadence (c), or repo-wide ref sweep.
#                      Exit 1 only on a hard gap for THAT bead.
#
#   --last <N>         Audit the N most-recently-closed in-scope beads (default: 9).
#   --since <date>     Audit beads closed on/after the ISO date (YYYY-MM-DD).
#                      --bead, --last, and --since are mutually exclusive (exit 2 if combined).
#
#   --quiet            Suppress per-check PASS lines; still print gaps + summary.
#   --json             Emit machine-readable {"checks":N,"gaps":[...],"exit":N}.
#
# Exit codes:
#   0  complete (all hard checks passed; warnings are fine)
#   1  one or more hard gaps
#   2  usage / environment error (bad flags, build failure, no closed beads)
#
# Checks (batch = --last/--since; bead = --bead):
#   (a) run-ref present — refs/etude/runs/<id> exists.         [hard; both modes]
#   (b) gated run has gates — manifest.gates non-empty.        [hard; both modes]
#   (c) cadence retro not overdue — every 3 closed beads.      [WARN only; batch only]
#   (d) refs pushed — every refs/etude/{runs,retros}/* matches origin.
#                                                              [hard batch (repo-wide);
#                                                               hard bead (that ref only)]
#   (e) docs drift — make docs-check/docs-reality if docs touched.[WARN only; both modes]
#   (f) cadence-sidecar — every cadence-retro ref on/after     [hard post-cutoff; batch only]
#       CADENCE_SIDECAR_CUTOFF must have a valid 7-key retro-meta
#       sidecar; pre-cutoff refs without one are WARN only.
#   (g) retro subject consistency — for each cadence-retro whose [WARN only; batch only]
#       body title contains a parseable id-list parenthetical,
#       every claimed id must appear in the manifest's
#       subject_run.* or bead.* refs. Where-possible: retros
#       with no parseable id-list are silently skipped.
#   bypass report — print allowlisted beads + reasons.         [info; never affects exit]
#
# Allowlist: scripts/dogfood-completeness-allow.txt
#   Format: <bead-id>  # reason
#   Bypassed beads are reported but do NOT cause exit 1.
#
# Style follows scripts/docs-reality-check.sh and scripts/dogfood-capture.sh.
set -euo pipefail

# Convention-adoption instant (UTC): cadence retros captured on/after this
# instant MUST carry a valid 7-key retro-meta sidecar (hard gap if missing).
# Pre-cutoff retros without a sidecar are WARN-only (backfill worklist, etude-8hq.5).
# Value chosen empirically: the latest existing cadence-retro ref was captured at
# 2026-05-26T23:43:35Z (~16 min before this cutoff), so the entire backlog is pre-convention.
CADENCE_SIDECAR_CUTOFF="2026-05-27T00:00:00Z"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

allow="scripts/dogfood-completeness-allow.txt"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
mode=""
bead_arg=""
last_n=9
since_date=""
quiet=false
json_out=false

usage() {
  cat >&2 <<'USAGE'
usage: scripts/dogfood-completeness-audit.sh [--bead <id>]
                                              [--last <N>] [--since <date>]
                                              [--quiet] [--json]
USAGE
  exit 2
}

mode_flags=0   # count of mutually-exclusive mode flags (--bead/--last/--since)
while [[ $# -gt 0 ]]; do
  case "$1" in
    --bead)
      [[ $# -ge 2 ]] || { echo "error: --bead requires an argument" >&2; usage; }
      bead_arg="$2"; mode="bead"; mode_flags=$((mode_flags+1)); shift 2 ;;
    --last)
      [[ $# -ge 2 ]] || { echo "error: --last requires an argument" >&2; usage; }
      last_n="$2"; mode="last"; mode_flags=$((mode_flags+1)); shift 2 ;;
    --since)
      [[ $# -ge 2 ]] || { echo "error: --since requires an argument" >&2; usage; }
      since_date="$2"; mode="since"; mode_flags=$((mode_flags+1)); shift 2 ;;
    --quiet)  quiet=true;    shift ;;
    --json)   json_out=true; shift ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done

# --bead, --last, --since are mutually exclusive (one mode per invocation).
if [[ "$mode_flags" -gt 1 ]]; then
  echo "error: --bead, --last, and --since are mutually exclusive" >&2
  exit 2
fi

# Default to --last 9 when no mode given
[[ -z "$mode" ]] && mode="last"

# Validate --last argument
if [[ "$mode" == "last" ]]; then
  if ! [[ "$last_n" =~ ^[0-9]+$ ]] || [[ "$last_n" -lt 1 ]]; then
    echo "error: --last requires a positive integer, got: $last_n" >&2
    exit 2
  fi
fi

# ---------------------------------------------------------------------------
# Finding accumulators
# ---------------------------------------------------------------------------
findings=()   # hard gaps:  "<category>|<id>|<message>"
warnings=()   # warn-only:  "<category>|<id>|<message>"
bypasses=()   # bypasses:   "<bead>|<reason>"
check_count=0

log_pass() {   # <message>
  $quiet || echo "  PASS $1"
  (( check_count++ )) || true
}
log_gap() {    # <category> <id> <message>
  findings+=("$1|$2|$3")
  echo "  GAP  [$1] $2 — $3" >&2
  (( check_count++ )) || true
}
log_warn() {   # <category> <id> <message>
  warnings+=("$1|$2|$3")
  echo "  WARN [$1] $2 — $3"
}
log_bypass() { # <bead> <reason>
  bypasses+=("$1|$2")
  echo "  bypass: $1 — $2"
}

# ---------------------------------------------------------------------------
# Load allowlist
# ---------------------------------------------------------------------------
declare -A allowlist_reason   # bead_id -> reason
if [[ -f "$allow" ]]; then
  while IFS= read -r line; do
    [[ "$line" =~ ^[[:space:]]*$  ]] && continue
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    bead_part="${line%%#*}"
    reason_part="${line#*#}"
    # Trim leading/trailing whitespace from each part
    bead_part="$(echo "$bead_part" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
    reason_part="$(echo "$reason_part" | sed 's/^[[:space:]]*//')"
    [[ -n "$bead_part" ]] && allowlist_reason["$bead_part"]="$reason_part"
  done < "$allow"
fi

is_allowlisted() {
  [[ -v "allowlist_reason[$1]" ]]
}

# ---------------------------------------------------------------------------
# Build etude fresh (like all other dogfood scripts)
# Allow DOGFOOD_AUDIT_ETUDE_BIN override so tests can inject a pre-built binary
# and avoid rebuilding in a throwaway repo that has no Go source.
# ---------------------------------------------------------------------------
bindir="$(mktemp -d)"
trap 'rm -rf "$bindir"' EXIT
if [[ -n "${DOGFOOD_AUDIT_ETUDE_BIN:-}" ]]; then
  bin="$DOGFOOD_AUDIT_ETUDE_BIN"
  echo "dogfood-completeness-audit: using pre-built etude: $bin"
else
  bin="$bindir/etude"
  echo "dogfood-completeness-audit: building etude from source..."
  if ! go build -o "$bin" ./cmd/etude 2>&1; then
    echo "error: go build failed" >&2
    exit 2
  fi
fi

# ---------------------------------------------------------------------------
# Load closed beads from bd
# ---------------------------------------------------------------------------
bd_json_file="$(mktemp)"
# mktemp file is cleaned up by trap via bindir; add it separately
trap 'rm -rf "$bindir" "$bd_json_file"' EXIT

if ! bd list --status closed --json > "$bd_json_file" 2>/dev/null; then
  echo "error: 'bd list --status closed --json' failed" >&2
  exit 2
fi

total_closed="$(python3 -c "
import json, sys
data = open(sys.argv[1]).read()
print(len(json.loads(data)))
" "$bd_json_file")"

if [[ "$total_closed" -eq 0 ]]; then
  echo "dogfood-completeness-audit: no closed beads found" >&2
  exit 2
fi

# ---------------------------------------------------------------------------
# Determine in-scope bead set
# ---------------------------------------------------------------------------
in_scope_ids=()

if [[ "$mode" == "bead" ]]; then
  in_scope_ids=("$bead_arg")

elif [[ "$mode" == "last" ]]; then
  while IFS= read -r line; do
    [[ -n "$line" ]] && in_scope_ids+=("$line")
  done < <(python3 -c "
import json, sys
beads = json.loads(open(sys.argv[1]).read())
sorted_beads = sorted(beads, key=lambda b: b.get('closed_at',''), reverse=True)
for b in sorted_beads[:int(sys.argv[2])]:
    print(b['id'])
" "$bd_json_file" "$last_n")

elif [[ "$mode" == "since" ]]; then
  while IFS= read -r line; do
    [[ -n "$line" ]] && in_scope_ids+=("$line")
  done < <(python3 -c "
import json, sys
beads = json.loads(open(sys.argv[1]).read())
since = sys.argv[2]
for b in sorted(beads, key=lambda b: b.get('closed_at',''), reverse=True):
    ca = b.get('closed_at','')[:10]
    if ca >= since:
        print(b['id'])
" "$bd_json_file" "$since_date")
fi

if [[ "${#in_scope_ids[@]}" -eq 0 ]]; then
  echo "dogfood-completeness-audit: no in-scope beads for the given window" >&2
  exit 2
fi

echo ""
echo "dogfood-completeness-audit: mode=$mode, in-scope=${#in_scope_ids[@]} bead(s)"
echo ""

# ---------------------------------------------------------------------------
# (f) Bypass report — always shown first so exceptions are visible
# ---------------------------------------------------------------------------
echo "=== Bypass report ==="
for bead in "${in_scope_ids[@]}"; do
  if is_allowlisted "$bead"; then
    log_bypass "$bead" "${allowlist_reason[$bead]}"
  fi
done
[[ "${#bypasses[@]}" -eq 0 ]] && { $quiet || echo "  (none)"; }
echo ""

# ---------------------------------------------------------------------------
# Active beads (non-allowlisted)
# ---------------------------------------------------------------------------
active_ids=()
for bead in "${in_scope_ids[@]}"; do
  is_allowlisted "$bead" || active_ids+=("$bead")
done

# ---------------------------------------------------------------------------
# (a) run-ref present
# ---------------------------------------------------------------------------
echo "=== Check (a): run-ref present ==="
for bead in "${active_ids[@]}"; do
  ref="refs/etude/runs/$bead"
  if git rev-parse --verify --quiet "$ref" >/dev/null 2>&1; then
    log_pass "(a) run ref exists: $ref"
  else
    log_gap "missing-run" "$bead" "no refs/etude/runs/$bead"
  fi
done
echo ""

# ---------------------------------------------------------------------------
# (b) gated runs have gate records
# ---------------------------------------------------------------------------
echo "=== Check (b): gated run has gate records ==="
for bead in "${active_ids[@]}"; do
  ref="refs/etude/runs/$bead"
  # Skip if no run ref (already flagged by (a))
  git rev-parse --verify --quiet "$ref" >/dev/null 2>&1 || continue

  gates_count="$(git cat-file -p "$ref:manifest.json" 2>/dev/null | python3 -c "
import json, sys
m = json.load(sys.stdin)
print(len(m.get('gates', [])))
" 2>/dev/null)" || gates_count="error"

  if [[ "$gates_count" == "error" || -z "$gates_count" ]]; then
    log_gap "gateless-run" "$bead" "could not read manifest.json from $ref"
  elif [[ "$gates_count" -gt 0 ]]; then
    log_pass "(b) gates present ($gates_count): $bead"
  else
    log_gap "gateless-run" "$bead" "manifest.gates is empty (0 gates)"
  fi
done
echo ""

# ---------------------------------------------------------------------------
# (d) refs pushed
# bead mode: check only that bead's run ref
# batch mode: repo-wide sweep of refs/etude/{runs,retros}/*
# ---------------------------------------------------------------------------
echo "=== Check (d): refs pushed to origin ==="

# Local ref->sha
declare -A local_sha
while IFS=$'\t' read -r sha refname; do
  local_sha["$refname"]="$sha"
done < <(git for-each-ref 'refs/etude/runs' 'refs/etude/retros' --format='%(objectname)%09%(refname)')

# Origin ref->sha
declare -A origin_sha
while IFS=$'\t' read -r sha refname; do
  origin_sha["$refname"]="$sha"
done < <(git ls-remote origin 'refs/etude/*' 2>/dev/null | awk '{print $1"\t"$2}' || true)

if [[ "$mode" == "bead" ]]; then
  # Only check this bead's own run ref
  ref="refs/etude/runs/$bead_arg"
  if [[ -v "local_sha[$ref]" ]]; then
    lsha="${local_sha[$ref]}"
    if [[ -v "origin_sha[$ref]" ]]; then
      osha="${origin_sha[$ref]}"
      if [[ "$lsha" == "$osha" ]]; then
        log_pass "(d) pushed: $ref ($lsha)"
      else
        log_gap "unpushed-ref" "$ref" "local $lsha, origin $osha (diverged)"
      fi
    else
      log_gap "unpushed-ref" "$ref" "local $lsha, not found on origin"
    fi
  fi
  # No run ref is already caught by (a); don't double-report here
else
  # Batch: sweep all local refs/etude/{runs,retros}/*
  for refname in "${!local_sha[@]}"; do
    lsha="${local_sha[$refname]}"
    if [[ -v "origin_sha[$refname]" ]]; then
      osha="${origin_sha[$refname]}"
      if [[ "$lsha" == "$osha" ]]; then
        log_pass "(d) pushed: $refname"
      else
        log_gap "unpushed-ref" "$refname" "local $lsha, origin $osha (diverged)"
      fi
    else
      log_gap "unpushed-ref" "$refname" "local $lsha, not found on origin"
    fi
  done
fi
echo ""

# ---------------------------------------------------------------------------
# (c) Cadence retro check — WARN ONLY, batch mode only
# ---------------------------------------------------------------------------
if [[ "$mode" != "bead" ]]; then
  echo "=== Check (c): cadence retro not overdue (WARN only) ==="

  # Collect subject runs from all cadence-retro refs
  covered_file="$(mktemp)"
  trap 'rm -rf "$bindir" "$bd_json_file" "$covered_file"' EXIT
  latest_cadence_retro=""
  latest_cadence_time=""

  while IFS= read -r retro_ref; do
    manifest_data="$(git cat-file -p "$retro_ref:manifest.json" 2>/dev/null)" || continue
    trigger="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
print(m.get('refs',{}).get('trigger',''))
" <<< "$manifest_data" 2>/dev/null || echo "")"

    if [[ "$trigger" == "cadence-retro" ]]; then
      # Collect covered run ids
      python3 -c "
import json,sys
m=json.load(sys.stdin)
refs=m.get('refs',{})
for k,v in refs.items():
    if k.startswith('subject_run.'):
        print(v)
" <<< "$manifest_data" 2>/dev/null >> "$covered_file" || true

      # Track most-recent cadence retro
      retro_time="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
print(m.get('created',''))
" <<< "$manifest_data" 2>/dev/null || echo "")"
      if [[ "$retro_time" > "$latest_cadence_time" ]]; then
        latest_cadence_time="$retro_time"
        latest_cadence_retro="$retro_ref"
      fi
    fi
  done < <(git for-each-ref refs/etude/retros --format='%(refname)')

  # Count in-scope active beads not covered by any cadence retro
  uncovered_count=0
  for bead in "${active_ids[@]}"; do
    if ! grep -qxF "$bead" "$covered_file" 2>/dev/null; then
      (( uncovered_count++ )) || true
    fi
  done

  if [[ $uncovered_count -ge 3 ]]; then
    last_retro_label="${latest_cadence_retro:-none}"
    log_warn "cadence-overdue" "window" \
      "$uncovered_count in-scope bead(s) not covered by any cadence retro (rule: every 3); last cadence retro: $last_retro_label"
  else
    $quiet || echo "  PASS (c) cadence coverage: $uncovered_count uncovered in-scope bead(s) (< 3 threshold)"
  fi
  echo ""
fi

# ---------------------------------------------------------------------------
# (f) Cadence-sidecar check — hard for post-cutoff, WARN for pre-cutoff, batch only
# For each cadence-retro ref:
#   - compare manifest `created` to CADENCE_SIDECAR_CUTOFF using datetime parse
#     (NOT lexical compare — fractional seconds require chronological comparison)
#   - post-cutoff AND missing/malformed sidecar → hard gap (exit 1)
#   - pre-cutoff AND missing/malformed sidecar → WARN (backfill worklist, etude-8hq.5)
# Required sidecar keys (presence+type; values never checked; arrays may be empty):
#   retro_type(str), original_event_date(str), failure_modes(arr), root_causes(arr),
#   follow_up_beads(arr), decisions(arr), durable_changes(arr)
# ---------------------------------------------------------------------------
if [[ "$mode" != "bead" ]]; then
  echo "=== Check (f): cadence-sidecar (hard post-cutoff; WARN pre-cutoff) ==="

  # Accumulate pre-cutoff refs with missing/malformed sidecars for a single WARN line
  pre_cutoff_warn_refs=()

  while IFS= read -r retro_ref; do
    manifest_data="$(git cat-file -p "$retro_ref:manifest.json" 2>/dev/null)" || continue

    # Check trigger
    trigger="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
print(m.get('refs',{}).get('trigger',''))
" <<< "$manifest_data" 2>/dev/null || echo "")"

    [[ "$trigger" == "cadence-retro" ]] || continue

    # Parse created timestamp and compare to cutoff (datetime parse, NOT lexical)
    is_post_convention="$(python3 -c "
import json,sys
from datetime import datetime,timezone
def _parse(ts):
    return datetime.fromisoformat(ts.replace('Z', '+00:00'))
m=json.load(sys.stdin)
created=m.get('created','')
cutoff='$CADENCE_SIDECAR_CUTOFF'
if not created:
    print('pre')
    sys.exit(0)
try:
    created_dt=_parse(created)
    cutoff_dt=_parse(cutoff)
    print('post' if created_dt >= cutoff_dt else 'pre')
except Exception:
    print('pre')
" <<< "$manifest_data" 2>/dev/null || echo "pre")"

    # Find retro-meta stage and validate sidecar
    sidecar_status="$(python3 -c "
import json,sys,subprocess
m=json.load(sys.stdin)
stages=m.get('stages',[])
meta_stage=None
for s in stages:
    if s.get('output',{}).get('role','')=='retro-meta':
        meta_stage=s
        break
if meta_stage is None:
    print('missing-stage')
    sys.exit(0)
blob_path=meta_stage.get('output',{}).get('path','')
if not blob_path:
    print('missing-path')
    sys.exit(0)
try:
    result=subprocess.run(
        ['git','cat-file','-p','$retro_ref:'+blob_path],
        capture_output=True,text=True,timeout=10
    )
    if result.returncode!=0:
        print('unreadable-blob')
        sys.exit(0)
    sidecar=json.loads(result.stdout)
except json.JSONDecodeError:
    print('invalid-json')
    sys.exit(0)
except Exception:
    print('unreadable-blob')
    sys.exit(0)
if not isinstance(sidecar,dict):
    print('not-object')
    sys.exit(0)
required=[
    ('retro_type',str),
    ('original_event_date',str),
    ('failure_modes',list),
    ('root_causes',list),
    ('follow_up_beads',list),
    ('decisions',list),
    ('durable_changes',list),
]
for key,typ in required:
    if key not in sidecar:
        print('missing-key:'+key)
        sys.exit(0)
    if not isinstance(sidecar[key],typ):
        print('wrong-type:'+key)
        sys.exit(0)
print('ok')
" <<< "$manifest_data" 2>/dev/null || echo "parse-error")"

    if [[ "$sidecar_status" == "ok" ]]; then
      $quiet || echo "  PASS (f) cadence-sidecar valid: $retro_ref"
    elif [[ "$is_post_convention" == "post" ]]; then
      log_gap "cadence-sidecar" "$retro_ref" \
        "post-convention cadence retro missing or malformed sidecar ($sidecar_status); required 7-key retro-meta sidecar"
    else
      # Pre-cutoff: accumulate for a single summarizing WARN line
      pre_cutoff_warn_refs+=("$retro_ref")
    fi
  done < <(git for-each-ref refs/etude/retros --format='%(refname)')

  # Emit a single summarizing WARN for all pre-cutoff refs without sidecars
  if [[ "${#pre_cutoff_warn_refs[@]}" -gt 0 ]]; then
    warn_refs_list="${pre_cutoff_warn_refs[*]}"
    log_warn "cadence-sidecar" "pre-cutoff" \
      "${#pre_cutoff_warn_refs[@]} pre-convention cadence retro(s) lack the required sidecar (backfill worklist, etude-8hq.5): $warn_refs_list"
  fi
  echo ""
fi

# ---------------------------------------------------------------------------
# (g) Retro subject consistency — WARN ONLY, batch mode only
# For each cadence-retro ref, parse the subject id-list from the BODY title
# (the retro-role stage output) and compare against manifest subject_run.* +
# bead.* refs. Where-possible: retros with no parseable id-list are skipped.
#
# Parse algorithm (validated against live corpus, zero false positives):
#   1. Find all (...) groups in the title; take the LAST one.
#   2. Split on ' + ' and keep only the FIRST chunk (drops "defer" suffixes).
#   3. Require the chunk to contain '/' and split into >=2 tokens (split on /).
#      Also accept ',' as a delimiter (handles "(6j8, kig, nm6)" style).
#   4. Strip whitespace from each token.
#   5. All-tokens-valid-id rule: every token must match ^[a-z0-9]+(\.[a-z0-9]+)*$
#      anchored end-to-end. If ANY token fails (e.g. date "2026-05-25" has '-'),
#      SKIP the entire retro — the parenthetical is not an id-list.
#   6. Prefix each surviving token with "etude-" and check against manifest refs.
#
# Inline allowlist for known legitimate divergences (keyed by retro ref id):
# B16's body title legitimately names nm6 (part of the 6j8/kig/nm6 cadence cohort),
# but nm6 is a data-backfill bead with NO run ref (see dogfood-completeness-allow.txt),
# so it could not be a --subject-run. Annotated here pending etude-8hq.5, which
# re-captures B16 with --bead etude-nm6 + a sidecar in one convention-compliant pass
# (a sidecar-less supersede now would be a post-cutoff cadence retro and fail check (f)).
# Add entries here if a retro genuinely cannot be repaired:
#   SUBJECT_CONSISTENCY_ALLOW=("retro-cohort-some-id-TIMESTAMP")
SUBJECT_CONSISTENCY_ALLOW=("retro-cohort-etude-6j8-20260526T215942Z")

if [[ "$mode" != "bead" ]]; then
  echo "=== Check (g): retro subject consistency (WARN only) ==="

  while IFS= read -r retro_ref; do
    manifest_data="$(git cat-file -p "$retro_ref:manifest.json" 2>/dev/null)" || continue

    # Only process cadence-retro refs
    trigger="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
print(m.get('refs',{}).get('trigger',''))
" <<< "$manifest_data" 2>/dev/null || echo "")"
    [[ "$trigger" == "cadence-retro" ]] || continue

    # Check inline allowlist
    retro_short="${retro_ref#refs/etude/retros/}"
    skip_allowed=false
    for allowed_id in "${SUBJECT_CONSISTENCY_ALLOW[@]+"${SUBJECT_CONSISTENCY_ALLOW[@]}"}"; do
      if [[ "$retro_short" == "$allowed_id" ]]; then
        skip_allowed=true
        break
      fi
    done
    $skip_allowed && continue

    # Find the retro-role stage path and read the body
    body_path="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
for s in m.get('stages',[]):
    if s.get('output',{}).get('role','')=='retro':
        print(s.get('output',{}).get('path',''))
        break
" <<< "$manifest_data" 2>/dev/null || echo "")"

    if [[ -z "$body_path" ]]; then
      # No retro-role stage: no body to parse, skip silently
      continue
    fi

    # Read the body and extract the title line (first non-empty line)
    title_line="$(git cat-file -p "$retro_ref:$body_path" 2>/dev/null | grep -m1 '.' || true)"
    if [[ -z "$title_line" ]]; then
      continue
    fi

    # Parse the subject id-list from the title using Python
    # Returns: "SKIP" | "WARN:<missing1>,<missing2>" | "PASS"
    check_result="$(python3 -c "
import sys, re, json, subprocess

title = sys.argv[1]
manifest_json = sys.argv[2]
retro_ref = sys.argv[3]

# Find all (...) groups in the title; take the LAST one
parens = re.findall(r'\(([^)]+)\)', title)
if not parens:
    print('SKIP')
    sys.exit(0)

last_paren = parens[-1]

# Split on ' + ' and keep only the first chunk
first_chunk = last_paren.split(' + ')[0].strip()

# Split on '/' or ',' to get tokens
tokens_raw = re.split(r'[/,]', first_chunk)
tokens = [t.strip() for t in tokens_raw]

# Must have at least 2 tokens (and the original chunk must contain '/' or ',')
if len(tokens) < 2 or not re.search(r'[/,]', first_chunk):
    print('SKIP')
    sys.exit(0)

# All-tokens-valid-id rule: every token must match ^[a-z0-9]+(\.[a-z0-9]+)*$
id_pattern = re.compile(r'^[a-z0-9]+(\.[a-z0-9]+)*$')
for tok in tokens:
    if not id_pattern.fullmatch(tok):
        print('SKIP')
        sys.exit(0)

# Read manifest and collect subject_run.* and bead.* VALUES
try:
    m = json.loads(manifest_json)
except Exception:
    print('SKIP')
    sys.exit(0)

refs = m.get('refs', {})
subject_values = set()
for k, v in refs.items():
    if k.startswith('subject_run.') or k.startswith('bead.'):
        # Normalize: strip 'etude-' prefix for bare comparison
        bare = v[len('etude-'):] if v.startswith('etude-') else v
        subject_values.add(bare)

# Check each token against the subject values
missing = []
for tok in tokens:
    if tok not in subject_values:
        missing.append(tok)

if missing:
    print('WARN:' + ','.join(missing))
else:
    print('PASS')
" "$title_line" "$manifest_data" "$retro_ref" 2>/dev/null || echo "SKIP")"

    if [[ "$check_result" == "SKIP" || "$check_result" == "PASS" ]]; then
      $quiet || echo "  PASS (g) subject consistency: $retro_ref"
    elif [[ "$check_result" == WARN:* ]]; then
      missing_ids="${check_result#WARN:}"
      log_warn "subject-consistency" "$retro_ref" \
        "title claims subject(s) absent from manifest refs: $missing_ids"
    fi
  done < <(git for-each-ref refs/etude/retros --format='%(refname)')
  echo ""
fi

# ---------------------------------------------------------------------------
# (e) Docs drift — WARN ONLY, both modes
# bead mode: warn only if manifest git_sha actually touched docs/
# batch mode: run make docs-check + docs-reality, warn on failure
# ---------------------------------------------------------------------------
echo "=== Check (e): docs drift (WARN only) ==="
if [[ "$mode" == "bead" ]]; then
  ref="refs/etude/runs/$bead_arg"
  if git rev-parse --verify --quiet "$ref" >/dev/null 2>&1; then
    git_sha="$(git cat-file -p "$ref:manifest.json" 2>/dev/null | python3 -c "
import json,sys
m=json.load(sys.stdin)
for s in m.get('stages',[]):
    sha=s.get('git_sha','')
    if sha:
        print(sha)
        break
" 2>/dev/null || echo "")"
    if [[ -n "$git_sha" ]] && git rev-parse --verify --quiet "${git_sha}^{commit}" >/dev/null 2>&1; then
      docs_changed="$(git diff-tree --no-commit-id -r --name-only "$git_sha" 2>/dev/null \
        | grep -cE '^(README\.md|docs/)' || true)"
      docs_changed="${docs_changed:-0}"
      if [[ "$docs_changed" -gt 0 ]]; then
        log_warn "docs-drift" "$bead_arg" \
          "manifest git_sha $git_sha touched docs/; consider running 'make docs-check' + 'make docs-reality'"
      else
        $quiet || echo "  PASS (e) docs: git_sha $git_sha did not touch docs/"
      fi
    else
      $quiet || echo "  PASS (e) docs: no usable git_sha in manifest; skipping"
    fi
  else
    $quiet || echo "  PASS (e) docs: no run ref; skipping"
  fi
else
  # Batch: try make targets
  if make -n docs-check >/dev/null 2>&1; then
    if ! make docs-check >/dev/null 2>&1; then
      log_warn "docs-drift" "repo" "make docs-check failed — generated docs may be stale"
    else
      $quiet || echo "  PASS (e) make docs-check: OK"
    fi
  else
    $quiet || echo "  PASS (e) make docs-check: target not available; skipping"
  fi
  if make -n docs-reality >/dev/null 2>&1; then
    if ! make docs-reality >/dev/null 2>&1; then
      log_warn "docs-drift" "repo" "make docs-reality failed — hand-written docs may drift from shipped CLI"
    else
      $quiet || echo "  PASS (e) make docs-reality: OK"
    fi
  else
    $quiet || echo "  PASS (e) make docs-reality: target not available; skipping"
  fi
fi
echo ""

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
gap_count="${#findings[@]}"
warn_count="${#warnings[@]}"
bypass_count="${#bypasses[@]}"
active_count="${#active_ids[@]}"
scope_count="${#in_scope_ids[@]}"

echo "=== Summary ==="
if [[ $gap_count -gt 0 ]]; then
  echo "audit: $gap_count hard gap(s) across $active_count active (of $scope_count in-scope) bead(s); $bypass_count bypass(es); $warn_count warning(s)."
  echo ""
  echo "Hard gaps:"
  for f in "${findings[@]}"; do
    cat="$(cut -d'|' -f1 <<< "$f")"
    id="$(cut -d'|' -f2 <<< "$f")"
    msg="$(cut -d'|' -f3- <<< "$f")"
    echo "  - [$cat] $id — $msg"
  done
else
  echo "audit: OK — $active_count active bead(s) complete (of $scope_count in-scope); $bypass_count bypass(es); $warn_count warning(s)."
fi

if [[ $warn_count -gt 0 ]]; then
  echo ""
  echo "Warnings (informational only):"
  for w in "${warnings[@]}"; do
    cat="$(cut -d'|' -f1 <<< "$w")"
    id="$(cut -d'|' -f2 <<< "$w")"
    msg="$(cut -d'|' -f3- <<< "$w")"
    echo "  - [$cat] $id — $msg"
  done
fi

# ---------------------------------------------------------------------------
# JSON output
# ---------------------------------------------------------------------------
if $json_out; then
  exit_code=0
  [[ $gap_count -gt 0 ]] && exit_code=1

  # Write findings/warnings/bypasses to temp files for python to read
  findings_file="$(mktemp)"
  warnings_file="$(mktemp)"
  bypasses_file="$(mktemp)"
  trap 'rm -rf "$bindir" "$bd_json_file" "${covered_file:-}" "$findings_file" "$warnings_file" "$bypasses_file"' EXIT

  printf '%s\n' "${findings[@]+"${findings[@]}"}" > "$findings_file"
  printf '%s\n' "${warnings[@]+"${warnings[@]}"}" > "$warnings_file"
  printf '%s\n' "${bypasses[@]+"${bypasses[@]}"}" > "$bypasses_file"

  python3 - "$findings_file" "$warnings_file" "$bypasses_file" \
    "$check_count" "$scope_count" "$active_count" "$exit_code" <<'PYJSON'
import json, sys

def parse_pipe3(path):
    out = []
    with open(path) as f:
        for line in f:
            line = line.rstrip('\n')
            if not line:
                continue
            parts = line.split('|', 2)
            out.append({'category': parts[0],
                        'id': parts[1] if len(parts) > 1 else '',
                        'message': parts[2] if len(parts) > 2 else ''})
    return out

def parse_pipe2(path):
    out = []
    with open(path) as f:
        for line in f:
            line = line.rstrip('\n')
            if not line:
                continue
            parts = line.split('|', 1)
            out.append({'bead': parts[0],
                        'reason': parts[1] if len(parts) > 1 else ''})
    return out

findings_file, warnings_file, bypasses_file = sys.argv[1], sys.argv[2], sys.argv[3]
checks, in_scope, active, exit_code = int(sys.argv[4]), int(sys.argv[5]), int(sys.argv[6]), int(sys.argv[7])

print(json.dumps({
    'checks':   checks,
    'in_scope': in_scope,
    'active':   active,
    'gaps':     parse_pipe3(findings_file),
    'warnings': parse_pipe3(warnings_file),
    'bypasses': parse_pipe2(bypasses_file),
    'exit':     exit_code,
}, indent=2))
PYJSON
fi

# ---------------------------------------------------------------------------
# Exit
# ---------------------------------------------------------------------------
[[ $gap_count -gt 0 ]] && exit 1
exit 0
