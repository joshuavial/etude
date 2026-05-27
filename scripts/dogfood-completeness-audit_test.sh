#!/usr/bin/env bash
#
# dogfood-completeness-audit_test.sh — fixture-based tests for
# scripts/dogfood-completeness-audit.sh.
#
# Creates a throwaway git repo with a bare-repo origin (so the pushed-ref
# check is real), a PATH `bd` shim emitting canned closed-bead JSON, and
# seeded etude refs. Runs the audit script in several configurations and
# asserts exit codes + output patterns.
#
# Run directly:
#   bash scripts/dogfood-completeness-audit_test.sh
#
# Or via make:
#   make dogfood-audit-test
#
# Requires: bash 4+ (associative arrays), git, go, python3.
# Does NOT mutate any real repo data.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
AUDIT="$SCRIPT_DIR/dogfood-completeness-audit.sh"

# ---------------------------------------------------------------------------
# Pre-build etude once from the real repo root; inject via DOGFOOD_AUDIT_ETUDE_BIN
# so the audit script does not try to run 'go build' inside the throwaway repo.
# ---------------------------------------------------------------------------
PREBUILT_ETUDE_DIR="$(mktemp -d)"
trap 'rm -rf "$PREBUILT_ETUDE_DIR"' EXIT
PREBUILT_ETUDE="$PREBUILT_ETUDE_DIR/etude"
echo "building etude for tests..."
(cd "$REPO_ROOT" && go build -o "$PREBUILT_ETUDE" ./cmd/etude)
export DOGFOOD_AUDIT_ETUDE_BIN="$PREBUILT_ETUDE"
echo "etude built: $PREBUILT_ETUDE"
echo ""

# ---------------------------------------------------------------------------
# Test harness helpers
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
# Fixture setup
# ---------------------------------------------------------------------------
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir" "$PREBUILT_ETUDE_DIR"' EXIT

bare_origin="$tmpdir/origin.git"
work_repo="$tmpdir/work"

# Create bare origin + working clone
git init --bare "$bare_origin" --quiet
git clone "$bare_origin" "$work_repo" --quiet 2>/dev/null

cd "$work_repo"

# Seed an initial commit so the repo is valid
git config user.email "test@test.com"
git config user.name "Test"
touch README.md
git add README.md
git commit -m "init" --quiet

# Make a docs/ directory for docs-drift test
mkdir -p docs
echo "# docs" > docs/README.md
git add docs/
git commit -m "add docs" --quiet
DOCS_COMMIT="$(git rev-parse HEAD)"

# A commit that touches ONLY a non-docs file (not README.md, not docs/) — used to
# assert the docs-drift check does NOT false-positive on code-only changes.
echo "package x" > code.go
git add code.go
git commit -m "add non-docs file" --quiet
NODOCS_COMMIT="$(git rev-parse HEAD)"

git push origin main --quiet

# ---------------------------------------------------------------------------
# Manifest builder helper
# ---------------------------------------------------------------------------
# Creates a minimal manifest.json in the git object store and writes it to
# refs/etude/runs/<id>. Accepts a list of gate IDs (empty for gateless).
seed_run_ref() {  # <bead-id> <git-sha> <gate-id1> [<gate-id2> ...]
  local bead="$1"; shift
  local git_sha="$1"; shift
  local gates_json="[]"
  if [[ $# -gt 0 ]]; then
    local gate_entries=""
    for gid in "$@"; do
      gate_entries="${gate_entries},{\"gate_id\":\"$gid\",\"phase\":\"implement\",\"round\":1,\"tier\":1,\"status\":\"pass\",\"reviewed_stages\":[],\"seats\":[],\"decision\":{},\"timestamp\":\"2026-05-25T00:00:00Z\"}"
    done
    gates_json="[${gate_entries#,}]"
  fi

  local manifest
  manifest="$(python3 -c "
import json
m = {
    'manifest_version': 3,
    'run_id': '$bead',
    'workflow': 'default',
    'workflow_version': 'v1',
    'created': '2026-05-25T00:00:00Z',
    'refs': {'bead': '$bead'},
    'stages': [{'stage': 'plan', 'produced_by': 'original', 'git_sha': '$git_sha', 'inputs': [], 'output': {}}],
    'gates': $gates_json
}
print(json.dumps(m))
")"

  # Write tree with manifest.json blob
  local blob_sha
  blob_sha="$(git hash-object -w --stdin <<< "$manifest")"
  local tree_sha
  tree_sha="$(printf '100644 blob %s\tmanifest.json\n' "$blob_sha" | git mktree)"
  local commit_sha
  commit_sha="$(git commit-tree "$tree_sha" -m "run: $bead" <<< "")"
  git update-ref "refs/etude/runs/$bead" "$commit_sha"
}

# Seed a minimal cadence-retro ref covering the given subject runs
seed_cadence_retro() {  # <retro-id> <subject-run1> [<subject-run2> ...]
  local retro_id="$1"; shift
  local refs_json='{"scope":"cohort","trigger":"cadence-retro"'
  local i=1
  for run in "$@"; do
    refs_json="${refs_json},\"subject_run.$i\":\"$run\""
    (( i++ )) || true
  done
  refs_json="${refs_json}}"

  local manifest
  manifest="$(python3 -c "
import json
m = {
    'manifest_version': 2,
    'run_id': '$retro_id',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '2026-05-26T00:00:00Z',
    'refs': json.loads('$refs_json'),
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

# Seed a cadence-retro ref WITH a retro-meta sidecar blob.
# The sidecar JSON is written as a content-addressed blob at
# artifacts/sha256/<2>/<hash> and referenced from the manifest stages[].
#
# Usage: seed_cadence_retro_with_meta <retro-id> <created> <sidecar-json> <subject-run1> [...]
#   <created>     full ISO-8601 timestamp for manifest.created
#   <sidecar-json> the JSON string to store as the retro-meta blob
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

  # Build manifest with a retro stage and a retro-meta stage
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

  # Write the manifest blob and tree with both the manifest and the sidecar artifact path
  local manifest_blob_sha
  manifest_blob_sha="$(git hash-object -w --stdin <<< "$manifest")"

  # Build tree: manifest.json + the sidecar at its content-addressed path
  local tree_sha
  tree_sha="$(python3 -c "
import subprocess, sys

manifest_blob = '$manifest_blob_sha'
sidecar_blob  = '$sidecar_blob_sha'
sidecar_path  = '$sidecar_path'

# Build a tree with nested directories for the artifact path
# e.g. artifacts/sha256/ab/<hash>
# We need to create sub-trees; use git mktree hierarchically.
# Simpler: use fast-import-style git mktree with the full path entries.
# git mktree only accepts flat entries; we need to build sub-trees bottom-up.

parts = sidecar_path.split('/')
# parts = ['artifacts','sha256','<2-char>','<hash>']

# Level 3 tree: just the blob
level3_in = '100644 blob {}\t{}\n'.format(sidecar_blob, parts[3])
r3 = subprocess.run(['git','mktree'], input=level3_in, capture_output=True, text=True)
tree3 = r3.stdout.strip()

# Level 2 tree: the 2-char dir
level2_in = '040000 tree {}\t{}\n'.format(tree3, parts[2])
r2 = subprocess.run(['git','mktree'], input=level2_in, capture_output=True, text=True)
tree2 = r2.stdout.strip()

# Level 1 tree: sha256 dir
level1_in = '040000 tree {}\t{}\n'.format(tree2, parts[1])
r1 = subprocess.run(['git','mktree'], input=level1_in, capture_output=True, text=True)
tree1 = r1.stdout.strip()

# Level 0 tree: artifacts dir + manifest.json
level0_in  = '040000 tree {}\t{}\n'.format(tree1, parts[0])
level0_in += '100644 blob {}\tmanifest.json\n'.format(manifest_blob)
r0 = subprocess.run(['git','mktree'], input=level0_in, capture_output=True, text=True)
print(r0.stdout.strip())
")"

  local commit_sha
  commit_sha="$(git commit-tree "$tree_sha" -m "retro: $retro_id" <<< "")"
  git update-ref "refs/etude/retros/$retro_id" "$commit_sha"
}

# bd shim: returns canned closed-bead JSON; accepts being replaced with a
# custom $BD_SHIM_JSON env var.
BD_SHIM="$tmpdir/bd"
cat > "$BD_SHIM" <<'BDSHIM'
#!/usr/bin/env bash
# Minimal bd shim for audit tests.
# Outputs $BD_JSON_FILE content for "bd list --status closed --json".
if [[ "$*" == *"list"* && "$*" == *"closed"* && "$*" == *"--json"* ]]; then
  cat "${BD_JSON_FILE:-/dev/null}"
elif [[ "$*" == *"show"* ]]; then
  echo "✓ ${BD_SHOW_ID:-test-bead} · Test bead title [● P1 · CLOSED]"
else
  echo "[]"
fi
BDSHIM
chmod +x "$BD_SHIM"

# Prepend shim dir to PATH for the duration
export PATH="$tmpdir:$PATH"

# ---------------------------------------------------------------------------
# Helper: write the bd JSON file for given beads
# closed_beads_json <id1> [<id2> ...]
write_bd_json() {  # <bead-id> ...
  local entries=""
  local now="2026-05-25T10:00:00Z"
  for bid in "$@"; do
    entries="${entries},{\"id\":\"$bid\",\"status\":\"closed\",\"closed_at\":\"$now\",\"title\":\"Test bead $bid\"}"
  done
  echo "[${entries#,}]" > "$tmpdir/bd_beads.json"
  export BD_JSON_FILE="$tmpdir/bd_beads.json"
}

# Helper: run audit and capture exit code without set -e killing the test
run_audit() {  # [args...]
  local rc=0
  # Build etude is expensive; reuse a pre-built binary if available
  output="$(bash "$AUDIT" "$@" 2>&1)" || rc=$?
  echo "$output"
  return "$rc"
}

# Helper: run audit capturing exit + output separately
run_audit_split() {  # [args...] -> sets $AUDIT_OUT and $AUDIT_RC
  AUDIT_RC=0
  AUDIT_OUT="$(bash "$AUDIT" "$@" 2>&1)" || AUDIT_RC=$?
}

# ---------------------------------------------------------------------------
# Seed the initial commit SHA (used for manifest git_sha)
# ---------------------------------------------------------------------------
INITIAL_SHA="$(git rev-parse HEAD)"

# ===========================================================================
# TEST 1: Complete fixture — 2 beads, runs with gates, cadence retro with valid
# 7-key sidecar, all pushed.  Uses a pre-cutoff created date; sidecar present
# → PASS for check (f) (no WARN, no gap).
# ===========================================================================
t_start "complete fixture exits 0"

# Valid 7-key sidecar for TEST 1
VALID_SIDECAR='{"retro_type":"cadence","original_event_date":"2026-05-26","failure_modes":[],"root_causes":[],"follow_up_beads":[],"decisions":[],"durable_changes":[]}'

write_bd_json "test-aaa" "test-bbb"
seed_run_ref "test-aaa" "$INITIAL_SHA" "plan.r1"
seed_run_ref "test-bbb" "$INITIAL_SHA" "plan.r1"
seed_cadence_retro_with_meta "retro-cohort-test-bbb" "2026-05-26T00:00:00Z" \
  "$VALID_SIDECAR" "test-aaa" "test-bbb"

# Push all refs to bare origin
git push origin 'refs/etude/*:refs/etude/*' --quiet 2>/dev/null

run_audit_split --last 9
assert_exit 0 "$AUDIT_RC" "complete fixture"

t_start "complete fixture output says OK"
assert_output_contains "audit: OK" "$AUDIT_OUT"

# ===========================================================================
# TEST 2: Missing run ref — bead with no run -> exit 1
# ===========================================================================
t_start "missing run ref causes exit 1"

write_bd_json "test-aaa" "test-bbb" "test-ccc"
# test-ccc has no run ref
run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "missing run"

t_start "missing run output has missing-run finding"
assert_output_contains "missing-run.*test-ccc" "$AUDIT_OUT"

# ===========================================================================
# TEST 3: Gateless run — run with empty gates -> exit 1
# ===========================================================================
t_start "gateless run causes exit 1"

write_bd_json "test-aaa" "test-bbb" "test-ddd"
seed_run_ref "test-ddd" "$INITIAL_SHA"   # no gate IDs -> gates=[]
git push origin "refs/etude/runs/test-ddd" --quiet 2>/dev/null

run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "gateless run"

t_start "gateless run output has gateless-run finding"
assert_output_contains "gateless-run.*test-ddd" "$AUDIT_OUT"

# Clean up test-ddd ref
git update-ref -d "refs/etude/runs/test-ddd"

# ===========================================================================
# TEST 4: Unpushed ref — local ref not on origin -> exit 1
# ===========================================================================
t_start "unpushed ref causes exit 1"

write_bd_json "test-aaa" "test-bbb"
# Create a local ref not pushed to origin
seed_run_ref "test-local-only" "$INITIAL_SHA" "plan.r1"
# Do NOT push it

run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "unpushed ref"

t_start "unpushed ref output has unpushed-ref finding"
assert_output_contains "unpushed-ref.*test-local-only" "$AUDIT_OUT"

# Clean up
git update-ref -d "refs/etude/runs/test-local-only"

# ===========================================================================
# TEST 5: Bypassed bead — allowlisted bead with no run -> exit 0 + bypass reported
# ===========================================================================
t_start "bypassed bead exits 0"

# Use a bead that IS in the repo allowlist (it won't have a run ref since we
# haven't seeded it). We'll create our own tmp allowlist for isolation.
ORIG_ALLOW="$REPO_ROOT/scripts/dogfood-completeness-allow.txt"

# Write a temp allowlist into the work repo's scripts/ dir
mkdir -p "$work_repo/scripts"
cat > "$work_repo/scripts/dogfood-completeness-allow.txt" <<'ALLOWEOF'
test-exempt  # test exemption reason
ALLOWEOF

write_bd_json "test-aaa" "test-bbb" "test-exempt"

run_audit_split --last 9
assert_exit 0 "$AUDIT_RC" "bypassed bead"

t_start "bypassed bead output shows bypass line"
assert_output_contains "bypass:.*test-exempt" "$AUDIT_OUT"

t_start "bypassed bead output does NOT show missing-run for exempt bead"
assert_output_not_contains "missing-run.*test-exempt" "$AUDIT_OUT"

# Remove the temp allowlist (reset to empty so subsequent tests use real allowlist)
rm "$work_repo/scripts/dogfood-completeness-allow.txt"

# ===========================================================================
# TEST 6: Cadence overdue — 3+ beads not covered by any cadence retro -> WARN + exit 0
# ===========================================================================
t_start "cadence overdue is WARN only (exit 0)"

# Delete existing cadence retro so none exist
git update-ref -d "refs/etude/retros/retro-cohort-test-bbb" 2>/dev/null || true
git push origin --delete "refs/etude/retros/retro-cohort-test-bbb" --quiet 2>/dev/null || true

write_bd_json "test-aaa" "test-bbb" "test-eee"
seed_run_ref "test-eee" "$INITIAL_SHA" "plan.r1"
git push origin "refs/etude/runs/test-eee" --quiet 2>/dev/null

run_audit_split --last 9
assert_exit 0 "$AUDIT_RC" "cadence overdue is warn only"

t_start "cadence overdue output shows WARN"
assert_output_contains "cadence-overdue" "$AUDIT_OUT"

# Restore cadence retro for subsequent tests
seed_cadence_retro "retro-cohort-test-bbb" "test-aaa" "test-bbb" "test-eee"
git push origin "refs/etude/retros/retro-cohort-test-bbb" --quiet 2>/dev/null

# Clean up test-eee
git update-ref -d "refs/etude/runs/test-eee"
git push origin --delete "refs/etude/runs/test-eee" --quiet 2>/dev/null || true

# ===========================================================================
# TEST 7: --bead mode, complete bead -> exit 0
# ===========================================================================
t_start "--bead mode on complete bead exits 0"

write_bd_json "test-aaa" "test-bbb"
run_audit_split --bead "test-aaa"
assert_exit 0 "$AUDIT_RC" "--bead mode complete"

t_start "--bead mode on complete bead output says OK"
assert_output_contains "audit: OK" "$AUDIT_OUT"

# ===========================================================================
# TEST 8: --bead mode, missing run -> exit 1
# ===========================================================================
t_start "--bead mode on missing run exits 1"

# test-zzz is not in bd json AND has no run ref
write_bd_json "test-zzz"
run_audit_split --bead "test-zzz"
assert_exit 1 "$AUDIT_RC" "--bead mode missing run"

t_start "--bead mode missing run output has missing-run finding"
assert_output_contains "missing-run.*test-zzz" "$AUDIT_OUT"

# ===========================================================================
# TEST 9: --bead mode does NOT run cadence check (c)
# ===========================================================================
t_start "--bead mode does not run cadence check"

# Delete all cadence retros
git for-each-ref refs/etude/retros --format='%(refname)' | while IFS= read -r r; do
  git update-ref -d "$r" 2>/dev/null || true
done

write_bd_json "test-aaa"
run_audit_split --bead "test-aaa"
assert_exit 0 "$AUDIT_RC" "--bead mode no cadence check"

t_start "--bead mode output has no cadence-overdue warning"
assert_output_not_contains "cadence-overdue" "$AUDIT_OUT"

# ===========================================================================
# TEST 10: --json flag emits parseable JSON
# ===========================================================================
t_start "--json flag produces valid JSON"

write_bd_json "test-aaa" "test-bbb"
seed_cadence_retro "retro-cohort-test-bbb2" "test-aaa" "test-bbb"
git push origin "refs/etude/retros/retro-cohort-test-bbb2" --quiet 2>/dev/null

run_audit_split --last 9 --json
# The output is text+JSON; look for '"exit": 0' in the output (from the JSON block)
if grep -q '"exit": 0' <<< "$AUDIT_OUT"; then
  t_pass
else
  t_fail "expected '\"exit\": 0' in JSON output, not found. output: $AUDIT_OUT"
fi

# ===========================================================================
# TEST 11: --since mode filters beads by date
# ===========================================================================
t_start "--since mode includes only beads closed on or after date"

# Write beads with different close dates
python3 -c "
import json
beads = [
    {'id': 'test-new', 'status': 'closed', 'closed_at': '2026-05-25T10:00:00Z', 'title': 'new bead'},
    {'id': 'test-old', 'status': 'closed', 'closed_at': '2026-05-10T10:00:00Z', 'title': 'old bead'},
]
print(json.dumps(beads))
" > "$tmpdir/bd_beads.json"
export BD_JSON_FILE="$tmpdir/bd_beads.json"

# Ensure test-new has a run ref with gates, pushed to origin
# (test-old is before the since cutoff so it won't be checked even if it's missing a run)
seed_run_ref "test-new" "$INITIAL_SHA" "plan.r1"
git push origin "refs/etude/runs/test-new" --quiet 2>/dev/null
seed_cadence_retro "retro-cohort-test-new" "test-new"
git push origin "refs/etude/retros/retro-cohort-test-new" --quiet 2>/dev/null

run_audit_split --since "2026-05-20"

# test-old is outside the window, so only test-new is checked; it has run+gates+pushed
assert_exit 0 "$AUDIT_RC" "--since date filtering"

t_start "--since mode output shows only 1 in-scope bead"
assert_output_contains "in-scope=1" "$AUDIT_OUT"

# ===========================================================================
# TEST: docs-drift WARN — --bead whose manifest git_sha touched docs/ warns,
# but exit stays 0 (docs is WARN-only). Guards the grep -c '0\n0' regression.
# ===========================================================================
t_start "docs-drift WARN on --bead does not fail (exit 0)"
write_bd_json "test-docs"
# git_sha = DOCS_COMMIT (the commit that added docs/README.md), with a gate, pushed.
seed_run_ref "test-docs" "$DOCS_COMMIT" "plan.r1"
git push origin "refs/etude/runs/test-docs" --quiet 2>/dev/null
run_audit_split --bead test-docs
assert_exit 0 "$AUDIT_RC" "docs-drift bead is WARN, not a hard gap"

t_start "docs-drift WARN finding is reported"
assert_output_contains "docs-drift.*test-docs" "$AUDIT_OUT"

t_start "no docs-drift WARN when git_sha did not touch docs/"
write_bd_json "test-nodocs"
seed_run_ref "test-nodocs" "$NODOCS_COMMIT" "plan.r1"   # NODOCS_COMMIT = code-only, no README/docs
git push origin "refs/etude/runs/test-nodocs" --quiet 2>/dev/null
run_audit_split --bead test-nodocs
assert_exit 0 "$AUDIT_RC" "non-docs bead clean"

t_start "non-docs bead does NOT report docs-drift"
assert_output_not_contains "docs-drift.*test-nodocs" "$AUDIT_OUT"

# ===========================================================================
# TEST: mutually-exclusive mode flags -> exit 2 (usage error)
# ===========================================================================
t_start "combining --last and --since exits 2"
run_audit_split --last 9 --since 2026-05-20
assert_exit 2 "$AUDIT_RC" "mutually exclusive mode flags"

t_start "combining --bead and --last exits 2"
run_audit_split --bead test-docs --last 9
assert_exit 2 "$AUDIT_RC" "bead + last mutually exclusive"

# ===========================================================================
# TEST GROUP: check (f) cadence-sidecar
# All these tests need a clean ref state; delete all cadence retros first,
# then seed specifically for each sub-test.
# ===========================================================================

# Shared sidecar constants
VALID_SIDECAR_JSON='{"retro_type":"cadence","original_event_date":"2026-05-27","failure_modes":[],"root_causes":[],"follow_up_beads":[],"decisions":[],"durable_changes":[]}'
POST_CUTOFF_TS="2026-05-27T01:00:00Z"
PRE_CUTOFF_TS="2026-05-26T12:00:00Z"
FRAC_CUTOFF_TS="2026-05-27T00:00:00.000001Z"

# Helper: delete all retro refs
delete_all_retros() {
  git for-each-ref refs/etude/retros --format='%(refname)' | while IFS= read -r r; do
    git update-ref -d "$r" 2>/dev/null || true
    git push origin --delete "$r" --quiet 2>/dev/null || true
  done
}

# Helper: ensure at least one run ref exists and is pushed so the audit runs
ensure_run_refs() {
  write_bd_json "test-aaa" "test-bbb"
  # test-aaa and test-bbb were seeded + pushed in TEST 1; just make sure bd sees them
}

# ===========================================================================
# TEST (f.1): post-cutoff cadence retro WITH a valid 7-key sidecar → PASS (exit 0, no gap)
# ===========================================================================
t_start "(f.1) post-cutoff cadence retro with valid sidecar exits 0"

delete_all_retros
ensure_run_refs
seed_cadence_retro_with_meta "retro-f1-post-valid" "$POST_CUTOFF_TS" \
  "$VALID_SIDECAR_JSON" "test-aaa" "test-bbb"
git push origin "refs/etude/retros/retro-f1-post-valid" --quiet 2>/dev/null

run_audit_split --last 9
assert_exit 0 "$AUDIT_RC" "(f.1) post-cutoff valid sidecar"

t_start "(f.1) output shows no cadence-sidecar gap"
assert_output_not_contains "cadence-sidecar.*GAP\|GAP.*cadence-sidecar" "$AUDIT_OUT"

# ===========================================================================
# TEST (f.2): post-cutoff cadence retro MISSING retro-meta stage → BLOCK (exit 1)
# ===========================================================================
t_start "(f.2) post-cutoff cadence retro missing sidecar stage exits 1"

delete_all_retros
ensure_run_refs
# seed_cadence_retro creates a retro with NO retro-meta stage, post-cutoff date
python3 -c "
import json
m = {
    'manifest_version': 2,
    'run_id': 'retro-f2-post-nosidecar',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$POST_CUTOFF_TS',
    'refs': {'scope':'cohort','trigger':'cadence-retro','subject_run.1':'test-aaa'},
    'stages': []
}
print(json.dumps(m))
" | {
  manifest_data=$(cat)
  blob_sha=$(git hash-object -w --stdin <<< "$manifest_data")
  tree_sha=$(printf '100644 blob %s\tmanifest.json\n' "$blob_sha" | git mktree)
  commit_sha=$(git commit-tree "$tree_sha" -m "retro: retro-f2-post-nosidecar" <<< "")
  git update-ref "refs/etude/retros/retro-f2-post-nosidecar" "$commit_sha"
  git push origin "refs/etude/retros/retro-f2-post-nosidecar" --quiet 2>/dev/null
}

run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "(f.2) post-cutoff missing sidecar → hard gap"

t_start "(f.2) output contains cadence-sidecar gap"
assert_output_contains "cadence-sidecar" "$AUDIT_OUT"

# ===========================================================================
# TEST (f.3): post-cutoff sidecar MISSING one required key → BLOCK (exit 1)
# ===========================================================================
t_start "(f.3) post-cutoff sidecar missing required key exits 1"

delete_all_retros
ensure_run_refs
# Drop 'durable_changes' from the sidecar
MISSING_KEY_SIDECAR='{"retro_type":"cadence","original_event_date":"2026-05-27","failure_modes":[],"root_causes":[],"follow_up_beads":[],"decisions":[]}'
seed_cadence_retro_with_meta "retro-f3-missing-key" "$POST_CUTOFF_TS" \
  "$MISSING_KEY_SIDECAR" "test-aaa"
git push origin "refs/etude/retros/retro-f3-missing-key" --quiet 2>/dev/null

run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "(f.3) post-cutoff missing key → hard gap"

t_start "(f.3) output contains cadence-sidecar gap"
assert_output_contains "cadence-sidecar" "$AUDIT_OUT"

# ===========================================================================
# TEST (f.4): post-cutoff sidecar with WRONG TYPE (failure_modes is string not array) → BLOCK
# ===========================================================================
t_start "(f.4) post-cutoff sidecar wrong type exits 1"

delete_all_retros
ensure_run_refs
WRONG_TYPE_SIDECAR='{"retro_type":"cadence","original_event_date":"2026-05-27","failure_modes":"not-an-array","root_causes":[],"follow_up_beads":[],"decisions":[],"durable_changes":[]}'
seed_cadence_retro_with_meta "retro-f4-wrong-type" "$POST_CUTOFF_TS" \
  "$WRONG_TYPE_SIDECAR" "test-aaa"
git push origin "refs/etude/retros/retro-f4-wrong-type" --quiet 2>/dev/null

run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "(f.4) post-cutoff wrong type → hard gap"

t_start "(f.4) output contains cadence-sidecar gap"
assert_output_contains "cadence-sidecar" "$AUDIT_OUT"

# ===========================================================================
# TEST (f.5): fractional-seconds boundary — created="2026-05-27T00:00:00.000001Z",
# no sidecar → BLOCK (exit 1).
# This locks in the r3 datetime-parse fix: a lexical compare would wrongly WARN
# because '.' (0x2E) < 'Z' (0x5A).
# ===========================================================================
t_start "(f.5) fractional-seconds post-cutoff missing sidecar exits 1 (datetime-parse fix)"

delete_all_retros
ensure_run_refs
python3 -c "
import json
m = {
    'manifest_version': 2,
    'run_id': 'retro-f5-frac-nosidecar',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$FRAC_CUTOFF_TS',
    'refs': {'scope':'cohort','trigger':'cadence-retro','subject_run.1':'test-aaa'},
    'stages': []
}
print(json.dumps(m))
" | {
  manifest_data=$(cat)
  blob_sha=$(git hash-object -w --stdin <<< "$manifest_data")
  tree_sha=$(printf '100644 blob %s\tmanifest.json\n' "$blob_sha" | git mktree)
  commit_sha=$(git commit-tree "$tree_sha" -m "retro: retro-f5-frac-nosidecar" <<< "")
  git update-ref "refs/etude/retros/retro-f5-frac-nosidecar" "$commit_sha"
  git push origin "refs/etude/retros/retro-f5-frac-nosidecar" --quiet 2>/dev/null
}

run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "(f.5) fractional-seconds boundary is post-cutoff → hard gap"

t_start "(f.5) output contains cadence-sidecar gap (not WARN)"
assert_output_contains "cadence-sidecar" "$AUDIT_OUT"
assert_output_not_contains "pre-convention.*$FRAC_CUTOFF_TS\|$FRAC_CUTOFF_TS.*pre-convention" "$AUDIT_OUT"

# ===========================================================================
# TEST (f.6): pre-cutoff cadence retro MISSING sidecar → WARN only (exit 0),
# appears in the single summarizing WARN line.
# ===========================================================================
t_start "(f.6) pre-cutoff cadence retro missing sidecar is WARN only (exit 0)"

delete_all_retros
ensure_run_refs
python3 -c "
import json
m = {
    'manifest_version': 2,
    'run_id': 'retro-f6-pre-nosidecar',
    'workflow': 'retro',
    'workflow_version': 'retro-v1',
    'created': '$PRE_CUTOFF_TS',
    'refs': {'scope':'cohort','trigger':'cadence-retro','subject_run.1':'test-aaa'},
    'stages': []
}
print(json.dumps(m))
" | {
  manifest_data=$(cat)
  blob_sha=$(git hash-object -w --stdin <<< "$manifest_data")
  tree_sha=$(printf '100644 blob %s\tmanifest.json\n' "$blob_sha" | git mktree)
  commit_sha=$(git commit-tree "$tree_sha" -m "retro: retro-f6-pre-nosidecar" <<< "")
  git update-ref "refs/etude/retros/retro-f6-pre-nosidecar" "$commit_sha"
  git push origin "refs/etude/retros/retro-f6-pre-nosidecar" --quiet 2>/dev/null
}

run_audit_split --last 9
assert_exit 0 "$AUDIT_RC" "(f.6) pre-cutoff missing sidecar is WARN, not hard gap"

t_start "(f.6) output contains cadence-sidecar WARN"
assert_output_contains "cadence-sidecar" "$AUDIT_OUT"

t_start "(f.6) WARN summary line mentions retro-f6-pre-nosidecar"
assert_output_contains "retro-f6-pre-nosidecar" "$AUDIT_OUT"

# ===========================================================================
# TEST (f.7): post-cutoff retro with UNREADABLE/GARBLED sidecar blob → BLOCK not crash
# (The manifest references a blob path that git cat-file returns non-JSON for.)
# ===========================================================================
t_start "(f.7) post-cutoff garbled sidecar blob → BLOCK (exit 1), not crash"

delete_all_retros
ensure_run_refs
# Write a non-JSON blob and use it as the sidecar
GARBLED_SIDECAR="this is not JSON {{{"
seed_cadence_retro_with_meta "retro-f7-garbled" "$POST_CUTOFF_TS" \
  "$GARBLED_SIDECAR" "test-aaa"
git push origin "refs/etude/retros/retro-f7-garbled" --quiet 2>/dev/null

run_audit_split --last 9
assert_exit 1 "$AUDIT_RC" "(f.7) garbled sidecar blob → hard gap, not crash"

t_start "(f.7) output contains cadence-sidecar gap"
assert_output_contains "cadence-sidecar" "$AUDIT_OUT"

# Clean up all check-f retro refs so subsequent tests don't see them
delete_all_retros

# ===========================================================================
# Summary
# ===========================================================================
echo ""
echo "==========================================="
echo "Test results: $pass_count passed, $fail_count failed"
echo "==========================================="

if [[ $fail_count -gt 0 ]]; then
  exit 1
fi
exit 0
