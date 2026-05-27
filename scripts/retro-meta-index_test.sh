#!/usr/bin/env bash
#
# retro-meta-index_test.sh — fixture-based tests for scripts/retro-meta-index.sh.
#
# Creates a throwaway git repo (no origin needed — retro-meta-index.sh is
# read-only and never checks remote refs), seeds cadence-retro refs with
# retro-meta sidecars, and asserts aggregation output.
#
# Run directly:
#   bash scripts/retro-meta-index_test.sh
#
# Or via make:
#   make retro-index-test
#
# Requires: bash 4+ (associative arrays), git, python3.
# Does NOT mutate any real repo data.
#
# Test cases:
#   (1) Two current cadence retros with sidecars — both aggregated.
#   (2) Superseded retro excluded — its content does NOT appear.
#   (3) Non-cadence trigger (process-retro) excluded.
#   (4) No-sidecar cadence retro excluded (no stage, no error).
#   (5) Follow-up bead named by 2 retros — count=2 / MULTI-RETRO.
#   (6) Durable-changes timeline ordered by original_event_date.
#   (7) Deterministic output — two runs produce identical output.
#   (8) --json mode — valid JSON, correct retros_indexed.
#   (9) Empty corpus — exits 0, zero retros.
#  (10) Dangling supersedes (names non-existent id) — no crash.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INDEX="$SCRIPT_DIR/retro-meta-index.sh"

# ---------------------------------------------------------------------------
# Test harness helpers (mirroring dogfood-completeness-audit_test.sh style)
# ---------------------------------------------------------------------------
pass_count=0
fail_count=0
current_test=""

t_start() {  # <test-name>
  current_test="$1"
  echo "--- TEST: $current_test"
}
t_pass() {
  (( pass_count++ )) || true
  echo "    PASS: $current_test"
}
t_fail() {  # <reason>
  (( fail_count++ )) || true
  echo "    FAIL: $current_test — $1" >&2
}

assert_exit() {  # <expected-exit> <actual-exit> [<extra-context>]
  local expected="$1" actual="$2" ctx="${3:-}"
  if [[ "$actual" -eq "$expected" ]]; then
    t_pass
  else
    t_fail "expected exit $expected, got $actual${ctx:+ ($ctx)}"
  fi
}

assert_output_contains() {  # <pattern> <output>
  if grep -qE "$1" <<< "$2"; then
    t_pass
  else
    t_fail "expected pattern '$1' not found in output"
  fi
}

assert_output_not_contains() {  # <pattern> <output>
  if ! grep -qE "$1" <<< "$2"; then
    t_pass
  else
    t_fail "pattern '$1' found but should be absent"
  fi
}

# ---------------------------------------------------------------------------
# Fixture setup — throwaway git repo (no remote needed)
# ---------------------------------------------------------------------------
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

work_repo="$tmpdir/work"
git init "$work_repo" --quiet
cd "$work_repo"
git config user.email "test@test.com"
git config user.name "Test"
touch README.md
git add README.md
git commit -m "init" --quiet
INITIAL_SHA="$(git rev-parse HEAD)"

# ---------------------------------------------------------------------------
# Seed helper: cadence retro WITH retro-meta sidecar.
# Mirrors seed_cadence_retro_with_meta from dogfood-completeness-audit_test.sh.
#
# Usage: seed_cadence_retro_with_meta <retro-id> <created> <sidecar-json> [subject-run ...]
# ---------------------------------------------------------------------------
seed_cadence_retro_with_meta() {
  local retro_id="$1"; shift
  local created="$1"; shift
  local sidecar_json="$1"; shift

  local refs_json='{"scope":"cohort","trigger":"cadence-retro"'
  local i=1
  for run in "$@"; do
    refs_json="${refs_json},\"subject_run.$i\":\"$run\""
    (( i++ )) || true
  done
  refs_json="${refs_json}}"

  # Write sidecar blob and compute content-addressed path
  local sidecar_blob_sha
  sidecar_blob_sha="$(git hash-object -w --stdin <<< "$sidecar_json")"
  local sidecar_path="artifacts/sha256/${sidecar_blob_sha:0:2}/$sidecar_blob_sha"

  # Build manifest with retro stage + retro-meta stage
  local manifest
  manifest="$(python3 -c "
import json
sidecar_path = '$sidecar_path'
sidecar_sha  = '$sidecar_blob_sha'
m = {
    'manifest_version': 2,
    'run_id': '$retro_id',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$created',
    'refs': json.loads('''$refs_json'''),
    'stages': [
        {
            'stage': 'retro',
            'produced_by': 'retro',
            'git_sha': '',
            'inputs': [],
            'output': {
                'role': 'retro',
                'artifact': sidecar_sha,
                'path': 'artifacts/sha256/' + sidecar_sha[:2] + '/placeholder',
                'media_type': 'text/markdown; charset=utf-8',
                'storage': 'content',
                'size': 0
            }
        },
        {
            'stage': 'retro-meta',
            'produced_by': 'retro',
            'git_sha': '',
            'inputs': [],
            'output': {
                'role': 'retro-meta',
                'artifact': sidecar_sha,
                'path': sidecar_path,
                'media_type': 'application/json',
                'storage': 'content',
                'size': len('$sidecar_json'.encode())
            }
        }
    ]
}
print(json.dumps(m))
")"

  local manifest_blob_sha
  manifest_blob_sha="$(git hash-object -w --stdin <<< "$manifest")"

  # Build tree: manifest.json + sidecar at content-addressed path
  local tree_sha
  tree_sha="$(python3 -c "
import subprocess
manifest_blob = '$manifest_blob_sha'
sidecar_blob  = '$sidecar_blob_sha'
sidecar_path  = '$sidecar_path'
parts = sidecar_path.split('/')
level3_in = '100644 blob {}\t{}\n'.format(sidecar_blob, parts[3])
r3 = subprocess.run(['git','mktree'], input=level3_in, capture_output=True, text=True)
tree3 = r3.stdout.strip()
level2_in = '040000 tree {}\t{}\n'.format(tree3, parts[2])
r2 = subprocess.run(['git','mktree'], input=level2_in, capture_output=True, text=True)
tree2 = r2.stdout.strip()
level1_in = '040000 tree {}\t{}\n'.format(tree2, parts[1])
r1 = subprocess.run(['git','mktree'], input=level1_in, capture_output=True, text=True)
tree1 = r1.stdout.strip()
level0_in  = '040000 tree {}\t{}\n'.format(tree1, parts[0])
level0_in += '100644 blob {}\tmanifest.json\n'.format(manifest_blob)
r0 = subprocess.run(['git','mktree'], input=level0_in, capture_output=True, text=True)
print(r0.stdout.strip())
")"

  local commit_sha
  commit_sha="$(git commit-tree "$tree_sha" -m "retro: $retro_id" <<< "")"
  git update-ref "refs/etude/retros/$retro_id" "$commit_sha"
}

# ---------------------------------------------------------------------------
# Seed helper: superseding retro WITH sidecar.
# Mirrors seed_superseding_retro from dogfood-completeness-audit_test.sh.
#
# Usage: seed_superseding_retro <new-id> <supersedes-id> <created> <trigger> <sidecar-json> [subject-run ...]
# ---------------------------------------------------------------------------
seed_superseding_retro() {
  local new_id="$1"; shift
  local supersedes_id="$1"; shift
  local created="$1"; shift
  local trigger="$1"; shift
  local sidecar_json="$1"; shift

  local refs_json
  refs_json="$(python3 -c "
import json, sys
refs = {'scope': 'cohort', 'trigger': '$trigger', 'supersedes': '$supersedes_id'}
i = 1
for run in sys.argv[1:]:
    refs['subject_run.' + str(i)] = run
    i += 1
print(json.dumps(refs))
" "$@")"

  if [[ -n "$sidecar_json" ]]; then
    local sidecar_blob_sha
    sidecar_blob_sha="$(git hash-object -w --stdin <<< "$sidecar_json")"
    local sidecar_path="artifacts/sha256/${sidecar_blob_sha:0:2}/$sidecar_blob_sha"

    local manifest
    manifest="$(python3 -c "
import json
sidecar_path = '$sidecar_path'
sidecar_sha  = '$sidecar_blob_sha'
m = {
    'manifest_version': 2,
    'run_id': '$new_id',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$created',
    'refs': json.loads('''$refs_json'''),
    'stages': [
        {
            'stage': 'retro-meta',
            'produced_by': 'retro',
            'git_sha': '',
            'inputs': [],
            'output': {
                'role': 'retro-meta',
                'artifact': sidecar_sha,
                'path': sidecar_path,
                'media_type': 'application/json',
                'storage': 'content',
                'size': len('$sidecar_json'.encode())
            }
        }
    ]
}
print(json.dumps(m))
")"

    local manifest_blob_sha
    manifest_blob_sha="$(git hash-object -w --stdin <<< "$manifest")"

    local tree_sha
    tree_sha="$(python3 -c "
import subprocess
manifest_blob = '$manifest_blob_sha'
sidecar_blob  = '$sidecar_blob_sha'
sidecar_path  = '$sidecar_path'
parts = sidecar_path.split('/')
level3_in = '100644 blob {}\t{}\n'.format(sidecar_blob, parts[3])
r3 = subprocess.run(['git','mktree'], input=level3_in, capture_output=True, text=True)
tree3 = r3.stdout.strip()
level2_in = '040000 tree {}\t{}\n'.format(tree3, parts[2])
r2 = subprocess.run(['git','mktree'], input=level2_in, capture_output=True, text=True)
tree2 = r2.stdout.strip()
level1_in = '040000 tree {}\t{}\n'.format(tree2, parts[1])
r1 = subprocess.run(['git','mktree'], input=level1_in, capture_output=True, text=True)
tree1 = r1.stdout.strip()
level0_in  = '040000 tree {}\t{}\n'.format(tree1, parts[0])
level0_in += '100644 blob {}\tmanifest.json\n'.format(manifest_blob)
r0 = subprocess.run(['git','mktree'], input=level0_in, capture_output=True, text=True)
print(r0.stdout.strip())
")"

    local commit_sha
    commit_sha="$(git commit-tree "$tree_sha" -m "retro: $new_id" <<< "")"
    git update-ref "refs/etude/retros/$new_id" "$commit_sha"
  else
    # No sidecar — simple manifest, no stages
    local manifest
    manifest="$(python3 -c "
import json
m = {
    'manifest_version': 2,
    'run_id': '$new_id',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$created',
    'refs': json.loads('''$refs_json'''),
    'stages': []
}
print(json.dumps(m))
")"
    local blob_sha tree_sha commit_sha
    blob_sha="$(git hash-object -w --stdin <<< "$manifest")"
    tree_sha="$(printf '100644 blob %s\tmanifest.json\n' "$blob_sha" | git mktree)"
    commit_sha="$(git commit-tree "$tree_sha" -m "retro: $new_id" <<< "")"
    git update-ref "refs/etude/retros/$new_id" "$commit_sha"
  fi
}

# Seed helper: cadence retro WITHOUT a retro-meta stage (no sidecar)
seed_cadence_retro_no_sidecar() {  # <retro-id> <created>
  local retro_id="$1" created="$2"
  local manifest
  manifest="$(python3 -c "
import json
m = {
    'manifest_version': 2,
    'run_id': '$retro_id',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$created',
    'refs': {'scope': 'cohort', 'trigger': 'cadence-retro'},
    'stages': []
}
print(json.dumps(m))
")"
  local blob_sha tree_sha commit_sha
  blob_sha="$(git hash-object -w --stdin <<< "$manifest")"
  tree_sha="$(printf '100644 blob %s\tmanifest.json\n' "$blob_sha" | git mktree)"
  commit_sha="$(git commit-tree "$tree_sha" -m "retro: $retro_id" <<< "")"
  git update-ref "refs/etude/retros/$retro_id" "$commit_sha"
}

# Seed helper: non-cadence retro (process-retro) with sidecar
seed_process_retro_with_sidecar() {  # <retro-id> <created> <sidecar-json>
  local retro_id="$1" created="$2" sidecar_json="$3"
  local sidecar_blob_sha sidecar_path
  sidecar_blob_sha="$(git hash-object -w --stdin <<< "$sidecar_json")"
  sidecar_path="artifacts/sha256/${sidecar_blob_sha:0:2}/$sidecar_blob_sha"
  local manifest
  manifest="$(python3 -c "
import json
sidecar_path = '$sidecar_path'
sidecar_sha  = '$sidecar_blob_sha'
m = {
    'manifest_version': 2,
    'run_id': '$retro_id',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$created',
    'refs': {'scope': 'cohort', 'trigger': 'process-retro'},
    'stages': [
        {
            'stage': 'retro-meta',
            'produced_by': 'retro',
            'git_sha': '',
            'inputs': [],
            'output': {
                'role': 'retro-meta',
                'artifact': sidecar_sha,
                'path': sidecar_path,
                'media_type': 'application/json',
                'storage': 'content',
                'size': 0
            }
        }
    ]
}
print(json.dumps(m))
")"
  local manifest_blob_sha
  manifest_blob_sha="$(git hash-object -w --stdin <<< "$manifest")"
  local tree_sha
  tree_sha="$(python3 -c "
import subprocess
manifest_blob = '$manifest_blob_sha'
sidecar_blob  = '$sidecar_blob_sha'
sidecar_path  = '$sidecar_path'
parts = sidecar_path.split('/')
level3_in = '100644 blob {}\t{}\n'.format(sidecar_blob, parts[3])
r3 = subprocess.run(['git','mktree'], input=level3_in, capture_output=True, text=True)
tree3 = r3.stdout.strip()
level2_in = '040000 tree {}\t{}\n'.format(tree3, parts[2])
r2 = subprocess.run(['git','mktree'], input=level2_in, capture_output=True, text=True)
tree2 = r2.stdout.strip()
level1_in = '040000 tree {}\t{}\n'.format(tree2, parts[1])
r1 = subprocess.run(['git','mktree'], input=level1_in, capture_output=True, text=True)
tree1 = r1.stdout.strip()
level0_in  = '040000 tree {}\t{}\n'.format(tree1, parts[0])
level0_in += '100644 blob {}\tmanifest.json\n'.format(manifest_blob)
r0 = subprocess.run(['git','mktree'], input=level0_in, capture_output=True, text=True)
print(r0.stdout.strip())
")"
  local commit_sha
  commit_sha="$(git commit-tree "$tree_sha" -m "retro: $retro_id" <<< "")"
  git update-ref "refs/etude/retros/$retro_id" "$commit_sha"
}

# Helper: run the index script and capture output + exit code
run_index() {  # [args...]
  local rc=0
  output="$(bash "$INDEX" "$@" 2>&1)" || rc=$?
  echo "$output"
  return "$rc"
}

echo ""
echo "=== retro-meta-index_test.sh ==="
echo ""

# ===========================================================================
# TEST (9): Empty corpus — zero refs, exits 0
# ===========================================================================
t_start "empty-corpus exits 0"
rc=0; out="$(run_index)" || rc=$?
assert_exit 0 "$rc"

t_start "empty-corpus zero retros"
assert_output_contains "Retros indexed.*0" "$out"

t_start "empty-corpus no failure modes"
assert_output_contains "_No failure modes recorded._" "$out"

# ===========================================================================
# Seed fixtures
# ===========================================================================

# Retro A — date 2026-05-10 — shared failure mode + unique content
SIDECAR_A='{"retro_type":"cadence","original_event_date":"2026-05-10","failure_modes":["shared failure mode","only in A"],"root_causes":["shared root cause","unique root cause A"],"follow_up_beads":["etude-shared-bead","etude-bead-a"],"decisions":["decision A"],"durable_changes":["durable change A1","durable change A2"]}'
seed_cadence_retro_with_meta \
  "retro-test-a" "2026-05-10T10:00:00Z" "$SIDECAR_A" "run-a1"

# Retro B — date 2026-05-15 — shared failure mode + unique content
SIDECAR_B='{"retro_type":"cadence","original_event_date":"2026-05-15","failure_modes":["shared failure mode","only in B"],"root_causes":["shared root cause","unique root cause B"],"follow_up_beads":["etude-shared-bead","etude-bead-b"],"decisions":["decision B"],"durable_changes":["durable change B1"]}'
seed_cadence_retro_with_meta \
  "retro-test-b" "2026-05-15T10:00:00Z" "$SIDECAR_B" "run-b1"

# OLD retro (superseded) — same date as A but will be excluded
SIDECAR_OLD='{"retro_type":"cadence","original_event_date":"2026-05-10","failure_modes":["OLD ONLY failure mode"],"root_causes":[],"follow_up_beads":["etude-old-bead"],"decisions":[],"durable_changes":["OLD durable change"]}'
seed_cadence_retro_with_meta \
  "retro-old-superseded" "2026-05-09T00:00:00Z" "$SIDECAR_OLD"

# NEW retro that supersedes OLD — date 2026-05-20
SIDECAR_NEW='{"retro_type":"cadence","original_event_date":"2026-05-20","failure_modes":["NEW superseding failure mode"],"root_causes":[],"follow_up_beads":["etude-new-bead"],"decisions":[],"durable_changes":["NEW durable change"]}'
seed_superseding_retro \
  "retro-new-supersedes-old" "retro-old-superseded" "2026-05-20T10:00:00Z" "cadence-retro" "$SIDECAR_NEW"

# Process-retro (non-cadence) with sidecar — should be excluded
SIDECAR_PROC='{"retro_type":"process","original_event_date":"2026-05-12","failure_modes":["process retro failure"],"root_causes":[],"follow_up_beads":[],"decisions":[],"durable_changes":["process durable"]}'
seed_process_retro_with_sidecar \
  "retro-process-noncadence" "2026-05-12T10:00:00Z" "$SIDECAR_PROC"

# Cadence retro without sidecar — should be excluded (no error)
seed_cadence_retro_no_sidecar "retro-no-sidecar" "2026-05-11T10:00:00Z"

# Retro with dangling supersedes (names a non-existent id) — must not crash
SIDECAR_DANGLING='{"retro_type":"cadence","original_event_date":"2026-05-22","failure_modes":["dangling superseder failure"],"root_causes":[],"follow_up_beads":[],"decisions":[],"durable_changes":[]}'
seed_superseding_retro \
  "retro-dangling-supersedes" "retro-nonexistent-id-xyz" "2026-05-22T10:00:00Z" "cadence-retro" "$SIDECAR_DANGLING"

echo ""
echo "--- Seeded fixtures:"
git for-each-ref refs/etude/retros --format='  %(refname)' 2>/dev/null
echo ""

# ===========================================================================
# TEST (1): Two current retros aggregated (A + B, not OLD)
# ===========================================================================
t_start "two-current-retros retros-indexed line"
rc=0; out="$(run_index)" || rc=$?
assert_exit 0 "$rc"
# A, B, NEW, DANGLING = 4 current cadence retros with sidecars
assert_output_contains "Retros indexed.*4" "$out"

t_start "two-current-retros failure-mode from A appears"
assert_output_contains "only in A" "$out"

t_start "two-current-retros failure-mode from B appears"
assert_output_contains "only in B" "$out"

t_start "two-current-retros durable-change from A appears"
assert_output_contains "durable change A1" "$out"

t_start "two-current-retros durable-change from B appears"
assert_output_contains "durable change B1" "$out"

# ===========================================================================
# TEST (2): Superseded retro excluded
# ===========================================================================
t_start "superseded-excluded OLD failure mode absent"
assert_output_not_contains "OLD ONLY failure mode" "$out"

t_start "superseded-excluded OLD bead absent"
assert_output_not_contains "etude-old-bead" "$out"

t_start "superseded-excluded OLD durable change absent"
assert_output_not_contains "OLD durable change" "$out"

t_start "superseded-excluded NEW failure mode present"
assert_output_contains "NEW superseding failure mode" "$out"

t_start "superseded-excluded NEW bead present"
assert_output_contains "etude-new-bead" "$out"

# ===========================================================================
# TEST (3): Non-cadence (process-retro) excluded
# ===========================================================================
t_start "non-cadence process-retro failure mode absent"
assert_output_not_contains "process retro failure" "$out"

t_start "non-cadence process-retro durable change absent"
assert_output_not_contains "process durable" "$out"

# ===========================================================================
# TEST (4): No-sidecar cadence retro excluded (no error)
# ===========================================================================
t_start "no-sidecar exits 0"
assert_exit 0 "$rc"

# ===========================================================================
# TEST (5): Follow-up bead named by 2 retros — MULTI-RETRO
# ===========================================================================
t_start "multi-retro bead MULTI-RETRO marker"
assert_output_contains "MULTI-RETRO.*etude-shared-bead" "$out"

t_start "multi-retro bead count=2 retros listed"
# etude-shared-bead is in A and B
assert_output_contains "retro-test-a" "$out"
assert_output_contains "retro-test-b" "$out"

# ===========================================================================
# TEST (5b): Failure mode and root cause recurring (count ≥2)
# ===========================================================================
t_start "recurring-failure-mode count=2"
assert_output_contains "count=2.*shared failure mode" "$out"

t_start "recurring-failure-mode RECURRING marker"
assert_output_contains "\[RECURRING\].*shared failure mode" "$out"

t_start "recurring-root-cause count=2"
assert_output_contains "count=2.*shared root cause" "$out"

t_start "summary line reports recurring failure modes"
assert_output_contains "1 recurring" "$out"

# ===========================================================================
# TEST (6): Durable-changes timeline ordered by original_event_date
# ===========================================================================
t_start "timeline A before B (dates 2026-05-10 before 2026-05-15)"
# A's durable changes (2026-05-10) must appear before B's (2026-05-15)
pos_a="$(grep -n "durable change A" <<< "$out" | head -1 | cut -d: -f1)"
pos_b="$(grep -n "durable change B" <<< "$out" | head -1 | cut -d: -f1)"
if [[ -n "$pos_a" && -n "$pos_b" && "$pos_a" -lt "$pos_b" ]]; then
  t_pass
else
  t_fail "A changes (line $pos_a) not before B changes (line $pos_b)"
fi

t_start "timeline date headings present"
assert_output_contains "### 2026-05-10" "$out"
assert_output_contains "### 2026-05-15" "$out"

# ===========================================================================
# TEST (7): Deterministic output — two runs produce identical output
# ===========================================================================
t_start "deterministic output"
out1="$(run_index)"
out2="$(run_index)"
if [[ "$out1" == "$out2" ]]; then
  t_pass
else
  t_fail "two runs produced different output"
fi

# ===========================================================================
# TEST (8): --json mode — valid JSON, correct retros_indexed
# ===========================================================================
t_start "json-mode exits 0"
rc=0; json_out="$(run_index --json)" || rc=$?
assert_exit 0 "$rc"

t_start "json-mode valid JSON"
if python3 -c "import json,sys; json.loads(sys.stdin.read())" <<< "$json_out"; then
  t_pass
else
  t_fail "output is not valid JSON"
fi

t_start "json-mode retros_indexed=4"
indexed="$(python3 -c "import json,sys; print(json.loads(sys.stdin.read())['retros_indexed'])" <<< "$json_out")"
if [[ "$indexed" == "4" ]]; then
  t_pass
else
  t_fail "expected retros_indexed=4, got $indexed"
fi

t_start "json-mode follow_up_beads sorted"
bead_order="$(python3 -c "
import json,sys
d=json.loads(sys.stdin.read())
beads=[b['bead'] for b in d['follow_up_beads']]
print('sorted' if beads==sorted(beads) else 'unsorted')
" <<< "$json_out")"
if [[ "$bead_order" == "sorted" ]]; then
  t_pass
else
  t_fail "follow_up_beads not sorted"
fi

t_start "json-mode failure_modes count-desc sorted"
order_check="$(python3 -c "
import json,sys
d=json.loads(sys.stdin.read())
fms=d['failure_modes']
counts=[f['count'] for f in fms]
ok=all(counts[i]>=counts[i+1] for i in range(len(counts)-1))
print('ok' if ok else 'not-sorted')
" <<< "$json_out")"
if [[ "$order_check" == "ok" ]]; then
  t_pass
else
  t_fail "failure_modes not sorted count-desc"
fi

# ===========================================================================
# TEST (10): Dangling supersedes — no crash
# ===========================================================================
t_start "dangling-supersedes no crash"
rc=0; out_dangle="$(run_index 2>&1)" || rc=$?
assert_exit 0 "$rc"

t_start "dangling-supersedes retro itself included"
assert_output_contains "dangling superseder failure" "$out_dangle"

# ===========================================================================
# Summary
# ===========================================================================
echo ""
echo "=== RESULTS: $pass_count passed, $fail_count failed ==="
if [[ "$fail_count" -gt 0 ]]; then
  exit 1
fi
echo "ALL TESTS PASSED"
