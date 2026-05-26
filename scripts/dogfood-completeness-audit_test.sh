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
# TEST 1: Complete fixture — 2 beads, runs with gates, cadence retro, all pushed
# ===========================================================================
t_start "complete fixture exits 0"

write_bd_json "test-aaa" "test-bbb"
seed_run_ref "test-aaa" "$INITIAL_SHA" "plan.r1"
seed_run_ref "test-bbb" "$INITIAL_SHA" "plan.r1"
seed_cadence_retro "retro-cohort-test-bbb" "test-aaa" "test-bbb"

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
