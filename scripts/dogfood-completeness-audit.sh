#!/usr/bin/env bash
#
# dogfood-completeness-audit.sh — do the last N closed beads have their etude
# evidence, and is it pushed?
#
# Three hard checks and one warning. This file replaced a 902-line predecessor
# in bead etude-9uf.4; the checks it dropped were either duplicated elsewhere
# (docs drift, which the verify gate already runs as make docs-check /
# make docs-reality) or bookkeeping about retros rather than evidence about work.
#
#   (a) run ref present     — refs/etude/runs/<id> exists.            [hard]
#   (b) run has gates       — its manifest records at least one gate. [hard]
#   (d) refs pushed         — every refs/etude/{runs,retros}/* ref
#                             matches origin.                         [hard]
#   (c) retro cadence       — a cadence retro every 3 closed beads.   [WARN]
#
# (a) and (b) survive because nothing forces `etude gate` to be used. No hook,
# CLI path or `bd close` guard requires a run ref or a gate, so (a) is the only
# detector of a bead closed with no run at all and (b) the only detector of one
# captured but never gated. Check (d) cannot cover either: it compares local
# refs to origin, so a bead with no ref has nothing to compare and passes.
#
# (c) is a WARNING, not a gap. It counts cadence-retro REFS, while lanes write
# retro MARKDOWN under .etude/retros/ — see bead etude-3xt. A ratchet that fires
# on the wrong evidence trains people to bypass it, so this one only reports.
#
# Usage:
#   scripts/dogfood-completeness-audit.sh [--last <N>]   (default N=9)
#
# Exit codes — the .beads/hooks/pre-push hook branches on these and fails
# CLOSED on anything unexpected, so they are a contract:
#   0  clean (hard checks passed; warnings do not fail)
#   1  one or more hard gaps
#   2  usage / environment error
#
# Allowlist: scripts/dogfood-completeness-allow.txt exempts a bead from (a) and
# (b), AND is check (c)'s denominator. Exempt beads are reported, never hidden.
set -euo pipefail

last_n=9
while [[ $# -gt 0 ]]; do
  case "$1" in
    --last) last_n="${2:-}"; shift 2 || { echo "error: --last needs a value" >&2; exit 2; } ;;
    -h|--help) sed -n '2,37p' "$0"; exit 0 ;;
    *) echo "error: unknown argument '$1' (only --last <N> is supported)" >&2; exit 2 ;;
  esac
done
[[ "$last_n" =~ ^[0-9]+$ && "$last_n" -gt 0 ]] \
  || { echo "error: --last needs a positive integer" >&2; exit 2; }

REPO_ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
# Run every git/bd query against the repo this script lives in. Without this the
# allowlist is resolved from THIS repo while refs come from the ambient cwd, so
# running from inside another checkout would audit that repo's refs against this
# repo's exemptions.
cd "$REPO_ROOT"
ALLOWLIST="$REPO_ROOT/scripts/dogfood-completeness-allow.txt"

gaps=0
# GAP lines go to stderr, as the predecessor's did: the pre-push hook tells the
# operator to "see GAP lines above" after its own stderr message.
gap() { echo "  GAP  [$1] $2 — $3" >&2; gaps=$((gaps + 1)); }

# --- in-scope beads: the N most recently closed ------------------------------
bd_json="$(mktemp)"; trap 'rm -f "$bd_json"' EXIT
# Defense in depth: if this guard were removed the run would still fail closed
# downstream (an empty window exits 2), so it carries no independent test signal.
# It exists to report the ACTUAL cause rather than a misleading empty-window one.
bd list --status closed --json > "$bd_json" 2>/dev/null \
  || { echo "error: 'bd list --status closed --json' failed" >&2; exit 2; }

# Materialise the window through a file, not a process substitution: `mapfile
# < <(...)` discards the producer's exit status, so a python failure mid-loop
# would silently truncate the window and the audit would report "clean" on a
# short list. Same fail-open shape as this bead's round-1 defect.
window="$(mktemp)"; trap 'rm -f "$bd_json" "$window"' EXIT
python3 -c "
import json,sys
beads=json.loads(open(sys.argv[1]).read())
for b in sorted(beads,key=lambda b:b.get('closed_at',''),reverse=True)[:int(sys.argv[2])]:
    print(b['id'])
" "$bd_json" "$last_n" > "$window" \
  || { echo "error: could not read the closed-bead window from bd output" >&2; exit 2; }
mapfile -t in_scope < "$window"
[[ "${#in_scope[@]}" -gt 0 ]] || { echo "error: no closed beads in the window" >&2; exit 2; }

# --- allowlist: exempt from (a)/(b), and excluded from (c)'s denominator ------
declare -A exempt=()
if [[ -f "$ALLOWLIST" ]]; then
  while read -r id reason || [[ -n "$id" ]]; do
    [[ -z "$id" || "$id" == \#* ]] && continue
    exempt["$id"]="${reason#\# }"
  done < "$ALLOWLIST"
fi

active=()
for b in "${in_scope[@]}"; do
  if [[ -v "exempt[$b]" ]]; then
    echo "  bypass: $b — ${exempt[$b]:-no reason given}"
  else
    active+=("$b")
  fi
done

echo "dogfood-audit: ${#in_scope[@]} closed bead(s) in window, ${#active[@]} active"

# --- (a) run ref present, (b) run has gates ----------------------------------
for b in "${active[@]}"; do
  ref="refs/etude/runs/$b"
  if ! git rev-parse --verify --quiet "$ref" >/dev/null 2>&1; then
    gap "missing-run" "$b" "no $ref"
    continue
  fi
  # Read the blob first, so an unreadable manifest is distinguishable from a
  # readable one. Do NOT collapse this into a pipeline: under `set -o pipefail`
  # a failing `git cat-file` makes the whole substitution non-zero even though
  # python printed a sentinel, and an `|| echo -1` fallback then appends a
  # SECOND line — producing a value that matches no case arm and passes clean.
  # That is a fail-open on a hard check, and it is how this was first written.
  manifest=""
  if ! manifest="$(git cat-file -p "$ref:manifest.json" 2>/dev/null)"; then
    gap "bad-manifest" "$b" "$ref has no readable manifest.json"
    continue
  fi
  # `gates` must be a LIST. A string or object would satisfy len() and pass a
  # naive count, so the type is checked rather than assumed.
  n_gates="$(python3 -c "
import json,sys
try:
    g=json.load(sys.stdin).get('gates') or []
    print(len(g) if isinstance(g,list) else -2)
except Exception: print(-1)
" <<< "$manifest" 2>/dev/null)" || n_gates=-1
  case "$n_gates" in
    -1) gap "bad-manifest" "$b" "$ref manifest.json is not readable JSON" ;;
    -2) gap "bad-manifest" "$b" "$ref manifest.json has a non-list 'gates'" ;;
    0)  gap "gateless-run" "$b" "$ref records no gate attempts" ;;
    *[!0-9]*|"") gap "bad-manifest" "$b" "$ref gate count unreadable ('$n_gates')" ;;
    # Any positive integer is a gated run. The catch-all above is deliberate:
    # a `case` with no default arm passes anything unexpected, which is the
    # shape that produced this bead's round-1 fail-open.
  esac
done

# --- (d) refs pushed ---------------------------------------------------------
# An unreachable origin must be an ENVIRONMENT error (exit 2), not a repo-wide
# pile of "absent from origin" gaps (exit 1). Those two mean opposite things to
# the operator the pre-push hook is talking to: one says "push your refs", the
# other says "your network or auth is broken".
ls_remote="$(mktemp)"; trap 'rm -f "$bd_json" "$window" "$ls_remote"' EXIT
if ! git ls-remote origin 'refs/etude/*' > "$ls_remote" 2>/dev/null; then
  echo "error: 'git ls-remote origin' failed — cannot tell pushed from unpushed" >&2
  exit 2
fi
declare -A origin_sha=()
while read -r sha ref; do [[ -n "$ref" ]] && origin_sha["$ref"]="$sha"; done < "$ls_remote"

local_refs="$(mktemp)"
trap 'rm -f "$bd_json" "$window" "$ls_remote" "$local_refs"' EXIT
git for-each-ref 'refs/etude/runs' 'refs/etude/retros' --format='%(objectname) %(refname)' \
  > "$local_refs" \
  || { echo "error: 'git for-each-ref' failed — cannot enumerate local refs" >&2; exit 2; }
while read -r sha ref; do
  if [[ ! -v "origin_sha[$ref]" ]]; then
    gap "unpushed-ref" "$ref" "absent from origin — push it with 'etude sync'"
  elif [[ "${origin_sha[$ref]}" != "$sha" ]]; then
    # Behind and ahead need opposite remedies, so name which one this is when it
    # can be determined. It usually cannot: deciding requires origin's commit to
    # be present locally, which a repo that is behind is exactly the one not to
    # have. So the undecidable case says "compare after fetching" rather than
    # asserting "push", which would be the wrong instruction half the time.
    if ! git cat-file -e "${origin_sha[$ref]}^{commit}" 2>/dev/null; then
      gap "diverged-ref" "$ref" "local $sha, origin ${origin_sha[$ref]} — fetch to compare"
    elif git merge-base --is-ancestor "$sha" "${origin_sha[$ref]}" 2>/dev/null; then
      gap "stale-ref" "$ref" "local $sha is BEHIND origin ${origin_sha[$ref]} — fetch, do not push"
    else
      gap "unpushed-ref" "$ref" "local $sha is AHEAD of origin ${origin_sha[$ref]} — push it with 'etude sync'"
    fi
  fi
done < "$local_refs"

# --- (c) retro cadence — WARN only -------------------------------------------
# A retro that a later one supersedes must not count as coverage — otherwise a
# re-captured retro's subjects are counted twice and the cadence under-reports.
covered="$(mktemp)"; superseded="$(mktemp)"
trap 'rm -f "$bd_json" "$window" "$ls_remote" "$local_refs" "$covered" "$superseded"' EXIT

for ref in $(git for-each-ref 'refs/etude/retros' --format='%(refname)'); do
  manifest="$(git cat-file -p "$ref:manifest.json" 2>/dev/null)" || continue
  python3 -c "
import json,sys
refs=json.load(sys.stdin).get('refs',{})
for k,v in refs.items():
    if k.startswith('supersedes') and v: print(v)
" <<< "$manifest" 2>/dev/null >> "$superseded" || true
done

for ref in $(git for-each-ref 'refs/etude/retros' --format='%(refname)'); do
  grep -qxF "${ref#refs/etude/retros/}" "$superseded" 2>/dev/null && continue
  manifest="$(git cat-file -p "$ref:manifest.json" 2>/dev/null)" || continue
  python3 -c "
import json,sys
refs=json.load(sys.stdin).get('refs',{})
if refs.get('trigger')=='cadence-retro':
    for k,v in refs.items():
        if k.startswith('subject_run.'): print(v)
" <<< "$manifest" 2>/dev/null >> "$covered" || true
done

uncovered=0
for b in "${active[@]}"; do
  grep -qxF "$b" "$covered" 2>/dev/null || uncovered=$((uncovered + 1))
done
if [[ "$uncovered" -ge 3 ]]; then
  echo "  WARN [cadence-overdue] $uncovered active bead(s) not covered by a cadence retro (rule: every 3)"
else
  echo "  ok   cadence: $uncovered uncovered (< 3)"
fi

# --- verdict -----------------------------------------------------------------
if [[ "$gaps" -gt 0 ]]; then
  echo "dogfood-audit: $gaps hard gap(s)."
  exit 1
fi
echo "dogfood-audit: clean."
exit 0
