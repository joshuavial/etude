#!/usr/bin/env bash
#
# dogfood-close_test.sh — fixture-based tests for scripts/dogfood-close.sh
# and the .beads/hooks/pre-push dogfood enforcement block.
#
# Reuses the harness pattern from dogfood-completeness-audit_test.sh:
#   - throwaway git repo + bare-repo origin
#   - bd PATH shim emitting canned closed-bead JSON
#   - DOGFOOD_AUDIT_ETUDE_BIN injection to avoid rebuilding etude
#   - DOGFOOD_CLOSE_CAPTURE_SCRIPT / DOGFOOD_CLOSE_GATE_CAPTURE_SCRIPT /
#     DOGFOOD_CLOSE_AUDIT_SCRIPT env vars to inject stubs into dogfood-close.sh
#
# Run directly:
#   bash scripts/dogfood-close_test.sh
#
# Or via make:
#   make dogfood-close-test
#
# Requires: bash 4+ (associative arrays), git, go, python3.
# Does NOT mutate any real repo data.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
CLOSE_SCRIPT="$SCRIPT_DIR/dogfood-close.sh"
AUDIT_SCRIPT="$SCRIPT_DIR/dogfood-completeness-audit.sh"
PRE_PUSH_HOOK="$REPO_ROOT/.beads/hooks/pre-push"

# ---------------------------------------------------------------------------
# Pre-build etude once from the real repo root
# ---------------------------------------------------------------------------
PREBUILT_ETUDE_DIR="$(mktemp -d)"
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

t_start() {
  current_test="$1"
  echo "--- TEST: $current_test"
}
t_pass() {
  (( pass_count++ )) || true
  echo "    PASS: $current_test"
}
t_fail() {
  (( fail_count++ )) || true
  echo "    FAIL: $current_test — $1" >&2
}

assert_exit() {
  local expected="$1" actual="$2" ctx="${3:-}"
  if [[ "$actual" -eq "$expected" ]]; then
    t_pass
  else
    t_fail "expected exit $expected, got $actual${ctx:+ ($ctx)}"
  fi
}

assert_output_contains() {
  if grep -qE "$1" <<< "$2"; then
    t_pass
  else
    t_fail "expected pattern '$1' not found in output"
  fi
}

assert_output_not_contains() {
  if ! grep -qE "$1" <<< "$2"; then
    t_pass
  else
    t_fail "pattern '$1' found but should be absent"
  fi
}

# ---------------------------------------------------------------------------
# Global fixture: throwaway git repo + bare origin
# ---------------------------------------------------------------------------
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir" "$PREBUILT_ETUDE_DIR"' EXIT

bare_origin="$tmpdir/origin.git"
work_repo="$tmpdir/work"

git init --bare "$bare_origin" --quiet
git clone "$bare_origin" "$work_repo" --quiet 2>/dev/null
cd "$work_repo"

git config user.email "test@test.com"
git config user.name "Test"
touch README.md
git add README.md
git commit -m "init" --quiet
git push origin main --quiet
INITIAL_SHA="$(git rev-parse HEAD)"

# ---------------------------------------------------------------------------
# bd shim: returns canned closed-bead JSON
# ---------------------------------------------------------------------------
BD_SHIM="$tmpdir/bd"
cat > "$BD_SHIM" <<'BDSHIM'
#!/usr/bin/env bash
if [[ "$*" == *"list"* && "$*" == *"closed"* && "$*" == *"--json"* ]]; then
  cat "${BD_JSON_FILE:-/dev/null}"
elif [[ "$*" == *"show"* ]]; then
  echo "bead-id   Test bead [● P1 · CLOSED]"
else
  echo "[]"
fi
BDSHIM
chmod +x "$BD_SHIM"
export PATH="$tmpdir:$PATH"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
write_bd_json() {
  local entries=""
  local now="2026-05-25T10:00:00Z"
  for bid in "$@"; do
    entries="${entries},{\"id\":\"$bid\",\"status\":\"closed\",\"closed_at\":\"$now\",\"title\":\"Test bead $bid\"}"
  done
  echo "[${entries#,}]" > "$tmpdir/bd_beads.json"
  export BD_JSON_FILE="$tmpdir/bd_beads.json"
}

# Seed a run ref directly into the current git repo (no capture script)
seed_run_ref() {
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
  local blob_sha tree_sha commit_sha
  blob_sha="$(git hash-object -w --stdin <<< "$manifest")"
  tree_sha="$(printf '100644 blob %s\tmanifest.json\n' "$blob_sha" | git mktree)"
  commit_sha="$(git commit-tree "$tree_sha" -m "run: $bead" <<< "")"
  git update-ref "refs/etude/runs/$bead" "$commit_sha"
}

run_close_split() {
  CLOSE_RC=0
  CLOSE_OUT="$(bash "$CLOSE_SCRIPT" "$@" 2>&1)" || CLOSE_RC=$?
}

# ---------------------------------------------------------------------------
# make_capture_stubs: create injectable stub scripts for the close wrapper.
# dogfood-close.sh supports DOGFOOD_CLOSE_CAPTURE_SCRIPT,
# DOGFOOD_CLOSE_GATE_CAPTURE_SCRIPT, and DOGFOOD_CLOSE_AUDIT_SCRIPT env vars.
# ---------------------------------------------------------------------------
make_capture_stubs() {
  local stub_dir="$1"
  local order_log="$2"
  local create_ref="${3:-true}"  # whether the capture stub creates+pushes the run ref

  mkdir -p "$stub_dir"

  # dogfood-capture.sh stub: logs call + optionally creates the run ref
  cat > "$stub_dir/dogfood-capture.sh" <<CAPSTUB
#!/usr/bin/env bash
echo "stub-capture \$@" >> "$order_log"
bead="\$1"
if [ "$create_ref" = "true" ]; then
  # Create a minimal run ref (empty gates — gate-capture stub will add them)
  manifest=\$(python3 -c "
import json
m = {
  'manifest_version': 3, 'run_id': '\$bead', 'workflow': 'default',
  'workflow_version': 'v1', 'created': '2026-05-25T00:00:00Z',
  'refs': {'bead': '\$bead'},
  'stages': [{'stage': 'plan', 'produced_by': 'original', 'git_sha': '$INITIAL_SHA', 'inputs': [], 'output': {}}],
  'gates': []
}
print(json.dumps(m))
")
  blob=\$(git hash-object -w --stdin <<< "\$manifest")
  tree=\$(printf '100644 blob %s\tmanifest.json\n' "\$blob" | git mktree)
  cmt=\$(git commit-tree "\$tree" -m "run: \$bead" <<< "")
  git update-ref "refs/etude/runs/\$bead" "\$cmt"
  git push "$bare_origin" "refs/etude/runs/\$bead" --quiet 2>/dev/null
fi
CAPSTUB
  chmod +x "$stub_dir/dogfood-capture.sh"

  # dogfood-gate-capture.sh stub: logs call + appends a gate to the manifest
  cat > "$stub_dir/dogfood-gate-capture.sh" <<GATESTUB
#!/usr/bin/env bash
echo "stub-gate-capture \$@" >> "$order_log"
bead="\$1"; gate_file="\$2"
ref="refs/etude/runs/\$bead"
old_manifest=\$(git cat-file -p "\$ref:manifest.json" 2>/dev/null)
gate_id=\$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['gate_id'])" "\$gate_file" 2>/dev/null || echo "stub-gate-id")
new_manifest=\$(python3 -c "
import json, sys
m = json.loads(sys.argv[1])
m['gates'].append({'gate_id': sys.argv[2], 'phase': 'plan', 'round': 1, 'tier': 1,
  'status': 'pass', 'reviewed_stages': [], 'seats': [], 'decision': {},
  'timestamp': '2026-05-25T00:00:00Z'})
print(json.dumps(m))
" "\$old_manifest" "\$gate_id")
blob=\$(git hash-object -w --stdin <<< "\$new_manifest")
tree=\$(printf '100644 blob %s\tmanifest.json\n' "\$blob" | git mktree)
old_cmt=\$(git rev-parse "\$ref")
cmt=\$(git commit-tree "\$tree" -p "\$old_cmt" -m "gate: \$bead" <<< "")
git update-ref "\$ref" "\$cmt"
git push "$bare_origin" "\$ref" --quiet 2>/dev/null
GATESTUB
  chmod +x "$stub_dir/dogfood-gate-capture.sh"

  export DOGFOOD_CLOSE_CAPTURE_SCRIPT="$stub_dir/dogfood-capture.sh"
  export DOGFOOD_CLOSE_GATE_CAPTURE_SCRIPT="$stub_dir/dogfood-gate-capture.sh"
  # Audit script remains the real one (uses DOGFOOD_AUDIT_ETUDE_BIN)
  unset DOGFOOD_CLOSE_AUDIT_SCRIPT 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Allowlist helpers
# ---------------------------------------------------------------------------
write_allowlist() {
  mkdir -p "$work_repo/scripts"
  printf '%s\n' "$@" > "$work_repo/scripts/dogfood-completeness-allow.txt"
}

remove_allowlist() {
  rm -f "$work_repo/scripts/dogfood-completeness-allow.txt"
}

# Common fixture files
echo "verify output" > "$tmpdir/verify.md"
echo "review output" > "$tmpdir/review.md"

# Common gate dir with one gate file
gate_dir_common="$tmpdir/gates_common"
mkdir -p "$gate_dir_common"
cat > "$gate_dir_common/plan.r1.json" <<'GATEJSON'
{"gate_id":"plan.r1","phase":"plan","round":1,"tier":1,"status":"pass","reviewed_stages":[],"seats":[],"decision":{},"timestamp":"2026-05-25T00:00:00Z"}
GATEJSON

# ===========================================================================
# SECTION A: Wrapper propagation and sequencing tests
# ===========================================================================
echo "=== Section A: Wrapper tests ==="

# ---------------------------------------------------------------------------
# A1: Happy path — run + gates + push present → exit 0
# The capture stub creates the ref and pushes it; gate stub appends gates.
# The wrapper preflights "ref must NOT exist", so stubs must create in sequence.
# ---------------------------------------------------------------------------
t_start "A1: happy path (complete bead) exits 0"

stub_dir_a1="$tmpdir/stubs_a1"
order_log_a1="$tmpdir/order_a1.log"
> "$order_log_a1"
make_capture_stubs "$stub_dir_a1" "$order_log_a1" "true"

write_bd_json "test-close-a1"

run_close_split "test-close-a1" "$INITIAL_SHA" "$tmpdir/verify.md" "$tmpdir/review.md" "$gate_dir_common"
assert_exit 0 "$CLOSE_RC" "happy path"

t_start "A1: happy path output says OK"
assert_output_contains "OK" "$CLOSE_OUT"

# Clean up
git update-ref -d "refs/etude/runs/test-close-a1" 2>/dev/null || true
git push "$bare_origin" --delete "refs/etude/runs/test-close-a1" --quiet 2>/dev/null || true

# ---------------------------------------------------------------------------
# A2: No gate-dir → exit 1 with gateless-run
# ---------------------------------------------------------------------------
t_start "A2: no gate-dir causes exit 1 with gateless-run"

stub_dir_a2="$tmpdir/stubs_a2"
order_log_a2="$tmpdir/order_a2.log"
> "$order_log_a2"
make_capture_stubs "$stub_dir_a2" "$order_log_a2" "true"

write_bd_json "test-close-a2"

run_close_split "test-close-a2" "$INITIAL_SHA" "$tmpdir/verify.md" "$tmpdir/review.md"
assert_exit 1 "$CLOSE_RC" "no gate-dir"

t_start "A2: no gate-dir output contains gateless-run"
assert_output_contains "gateless-run" "$CLOSE_OUT"

# Clean up
git update-ref -d "refs/etude/runs/test-close-a2" 2>/dev/null || true
git push "$bare_origin" --delete "refs/etude/runs/test-close-a2" --quiet 2>/dev/null || true

# ---------------------------------------------------------------------------
# A3: Unpushed ref → exit 1 with unpushed-ref
# Seed the ref directly (without push) and audit it directly.
# (We can't pass it through the wrapper because the wrapper preflights
# "ref must NOT exist" before capture.)
# ---------------------------------------------------------------------------
t_start "A3: unpushed ref causes exit 1"

seed_run_ref "test-close-a3" "$INITIAL_SHA" "plan.r1"   # has gates, NOT pushed
write_bd_json "test-close-a3"

AUDIT_RC_A3=0
AUDIT_OUT_A3="$(bash "$AUDIT_SCRIPT" --bead "test-close-a3" 2>&1)" || AUDIT_RC_A3=$?
assert_exit 1 "$AUDIT_RC_A3" "unpushed ref via audit"

t_start "A3: unpushed ref output contains unpushed-ref"
assert_output_contains "unpushed-ref" "$AUDIT_OUT_A3"

# Clean up
git update-ref -d "refs/etude/runs/test-close-a3" 2>/dev/null || true

# ---------------------------------------------------------------------------
# A4: Allowlisted bead → exit 0 with bypass notice in output
# ---------------------------------------------------------------------------
t_start "A4: allowlisted bead exits 0"

stub_dir_a4="$tmpdir/stubs_a4"
order_log_a4="$tmpdir/order_a4.log"
> "$order_log_a4"
make_capture_stubs "$stub_dir_a4" "$order_log_a4" "true"

write_allowlist "test-close-a4  # test exemption"
write_bd_json "test-close-a4"

run_close_split "test-close-a4" "$INITIAL_SHA" "$tmpdir/verify.md" "$tmpdir/review.md" "$gate_dir_common"
assert_exit 0 "$CLOSE_RC" "allowlisted bead"

t_start "A4: allowlisted bead output contains bypass"
assert_output_contains "bypass:.*test-close-a4" "$CLOSE_OUT"

remove_allowlist
git update-ref -d "refs/etude/runs/test-close-a4" 2>/dev/null || true
git push "$bare_origin" --delete "refs/etude/runs/test-close-a4" --quiet 2>/dev/null || true

# ---------------------------------------------------------------------------
# A5: Sequencing — capture invoked before gate-capture before audit
# ---------------------------------------------------------------------------
t_start "A5: capture invoked before gate-capture before audit"

stub_dir_a5="$tmpdir/stubs_a5"
order_log_a5="$tmpdir/order_a5.log"
> "$order_log_a5"
make_capture_stubs "$stub_dir_a5" "$order_log_a5" "true"

# Wrap the real audit with a stub that also logs to the order file
audit_stub_a5="$tmpdir/audit_stub_a5.sh"
cat > "$audit_stub_a5" <<AUDITSTUB
#!/usr/bin/env bash
echo "stub-audit \$@" >> "$order_log_a5"
bash "$AUDIT_SCRIPT" "\$@"
AUDITSTUB
chmod +x "$audit_stub_a5"
export DOGFOOD_CLOSE_AUDIT_SCRIPT="$audit_stub_a5"

gate_dir_a5="$tmpdir/gates_a5"
mkdir -p "$gate_dir_a5"
cp "$gate_dir_common/plan.r1.json" "$gate_dir_a5/"

write_bd_json "test-close-a5"

run_close_split "test-close-a5" "$INITIAL_SHA" "$tmpdir/verify.md" "$tmpdir/review.md" "$gate_dir_a5"

# Check invocation order: capture → gate-capture → audit
capture_line=$(grep -n "stub-capture" "$order_log_a5" 2>/dev/null | head -1 | cut -d: -f1 || echo "")
gate_line=$(grep -n "stub-gate-capture" "$order_log_a5" 2>/dev/null | head -1 | cut -d: -f1 || echo "")
audit_line=$(grep -n "stub-audit" "$order_log_a5" 2>/dev/null | head -1 | cut -d: -f1 || echo "")

if [[ -n "$capture_line" && -n "$gate_line" && -n "$audit_line" \
      && "$capture_line" -lt "$gate_line" && "$gate_line" -lt "$audit_line" ]]; then
  t_pass
else
  t_fail "wrong order: capture=$capture_line gate=$gate_line audit=$audit_line (log: $(cat "$order_log_a5" 2>/dev/null))"
fi

unset DOGFOOD_CLOSE_AUDIT_SCRIPT
git update-ref -d "refs/etude/runs/test-close-a5" 2>/dev/null || true
git push "$bare_origin" --delete "refs/etude/runs/test-close-a5" --quiet 2>/dev/null || true

# ---------------------------------------------------------------------------
# A6: Preflight guard — run ref already exists → exit 1
# ---------------------------------------------------------------------------
t_start "A6: run ref already exists causes exit 1 (no double-capture)"

seed_run_ref "test-close-a6" "$INITIAL_SHA" "plan.r1"

stub_dir_a6="$tmpdir/stubs_a6"
order_log_a6="$tmpdir/order_a6.log"
> "$order_log_a6"
make_capture_stubs "$stub_dir_a6" "$order_log_a6" "true"
write_bd_json "test-close-a6"

run_close_split "test-close-a6" "$INITIAL_SHA" "$tmpdir/verify.md" "$tmpdir/review.md" "$gate_dir_common"
assert_exit 1 "$CLOSE_RC" "run ref already exists"

t_start "A6: preflight error message mentions already exists"
assert_output_contains "already exists" "$CLOSE_OUT"

git update-ref -d "refs/etude/runs/test-close-a6" 2>/dev/null || true

# ===========================================================================
# SECTION B: pre-push hook classification tests
# ===========================================================================
echo ""
echo "=== Section B: pre-push hook tests ==="

# We test the hook by running it directly with synthetic stdin.
# The hook uses scripts/dogfood-completeness-audit.sh from the repo root;
# we inject a sentinel via DOGFOOD_CLOSE_AUDIT_SCRIPT is not applicable here,
# but we can shadow it by placing a sentinel EARLIER on PATH.

# Create audit sentinel (exit 0 = clean)
audit_sentinel="$tmpdir/audit_sentinel.sh"
audit_called_file="$tmpdir/audit_called"
rm -f "$audit_called_file"

cat > "$audit_sentinel" <<SENTINEL
#!/usr/bin/env bash
touch "$audit_called_file"
exit 0
SENTINEL
chmod +x "$audit_sentinel"

run_hook_split() {
  local stdin_content="$1"
  HOOK_RC=0
  HOOK_OUT="$(
    export DOGFOOD_HOOK_AUDIT_SCRIPT="$audit_sentinel"
    cd "$work_repo"
    printf '%s\n' "$stdin_content" | bash "$PRE_PUSH_HOOK" origin "$bare_origin" 2>&1
  )" || HOOK_RC=$?
}

# ---------------------------------------------------------------------------
# B1: stdin of only refs/etude/... → exit 0, audit NOT called
# ---------------------------------------------------------------------------
t_start "B1: etude-only push is exempt (exit 0, no audit)"
rm -f "$audit_called_file"

run_hook_split "refs/etude/runs/test-bead abc123def456 refs/etude/runs/test-bead 0000000000000000000000000000000000000000"
assert_exit 0 "$HOOK_RC" "etude-only push"

t_start "B1: audit not called for etude-only push"
if [ ! -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was called but should not have been (etude-only push)"
fi

# ---------------------------------------------------------------------------
# B2: refs/heads/main with clean window → exit 0, audit called
# ---------------------------------------------------------------------------
t_start "B2: code push with clean audit passes (exit 0)"
rm -f "$audit_called_file"

run_hook_split "refs/heads/main abc123def456 refs/heads/main 0000000000000000000000000000000000000000"
assert_exit 0 "$HOOK_RC" "code push clean window"

t_start "B2: audit was called for code push"
if [ -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was NOT called but should have been (code push)"
fi

# ---------------------------------------------------------------------------
# B3: refs/heads/main with a gap → exit 1 (rejected)
# Use a failing audit sentinel
# ---------------------------------------------------------------------------
t_start "B3: code push with gap is blocked (exit 1)"

audit_fail="$tmpdir/audit_fail.sh"
cat > "$audit_fail" <<'FAILSENTINEL'
#!/usr/bin/env bash
echo "  GAP  [missing-run] test-gap-bead — no refs/etude/runs/test-gap-bead"
echo "audit: 1 hard gap(s) across 1 active bead(s)."
exit 1
FAILSENTINEL
chmod +x "$audit_fail"

HOOK_RC_B3=0
HOOK_OUT_B3="$(
  export DOGFOOD_HOOK_AUDIT_SCRIPT="$audit_fail"
  cd "$work_repo"
  printf '%s\n' "refs/heads/main abc123def456 refs/heads/main 0000000000000000000000000000000000000000" \
    | bash "$PRE_PUSH_HOOK" origin "$bare_origin" 2>&1
)" || HOOK_RC_B3=$?
assert_exit 1 "$HOOK_RC_B3" "code push with gap"

# ---------------------------------------------------------------------------
# B4: Mixed refs/etude/... + refs/heads/main → audit IS called
# ---------------------------------------------------------------------------
t_start "B4: mixed etude+heads push calls the audit"
rm -f "$audit_called_file"

mixed_refs="$(printf '%s\n' \
  "refs/etude/runs/test-bead abc123 refs/etude/runs/test-bead 0000000000000000000000000000000000000000" \
  "refs/heads/main abc456 refs/heads/main 0000000000000000000000000000000000000000")"

HOOK_RC_B4=0
HOOK_OUT_B4="$(
  export DOGFOOD_HOOK_AUDIT_SCRIPT="$audit_sentinel"
  cd "$work_repo"
  printf '%s\n' "$mixed_refs" | bash "$PRE_PUSH_HOOK" origin "$bare_origin" 2>&1
)" || HOOK_RC_B4=$?

if [ -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was NOT called for mixed push (heads present should trigger audit)"
fi

# ---------------------------------------------------------------------------
# B5: Deletion-only push (local-oid all-zeros) → exit 0, audit NOT called
# ---------------------------------------------------------------------------
t_start "B5: deletion-only push is exempt (exit 0, no audit)"
rm -f "$audit_called_file"

run_hook_split "refs/heads/feature 0000000000000000000000000000000000000000 refs/heads/feature deadbeef1234"
assert_exit 0 "$HOOK_RC" "deletion-only push"

t_start "B5: audit not called for deletion-only push"
if [ ! -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was called but should not have been (deletion-only push)"
fi

# ---------------------------------------------------------------------------
# B6: refs/tags/... → exit 0, audit NOT called
# ---------------------------------------------------------------------------
t_start "B6: tag push is exempt (exit 0, no audit)"
rm -f "$audit_called_file"

run_hook_split "refs/tags/v1.0.0 abc123def456 refs/tags/v1.0.0 0000000000000000000000000000000000000000"
assert_exit 0 "$HOOK_RC" "tag push"

t_start "B6: audit not called for tag push"
if [ ! -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was called but should not have been (tag push)"
fi

# ---------------------------------------------------------------------------
# B7: empty stdin → exit 0, audit NOT called
# ---------------------------------------------------------------------------
t_start "B7: empty stdin is exempt (exit 0, no audit)"
rm -f "$audit_called_file"

run_hook_split ""
assert_exit 0 "$HOOK_RC" "empty stdin"

t_start "B7: audit not called for empty stdin"
if [ ! -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was called but should not have been (empty stdin)"
fi

# ---------------------------------------------------------------------------
# B8: unknown-ref-only push (e.g. refs/notes/*) → exit 0, audit NOT called
# (non-exempt but no code ref → don't trigger the audit)
# ---------------------------------------------------------------------------
t_start "B8: unknown-ref-only push does not trigger audit (exit 0)"
rm -f "$audit_called_file"

run_hook_split "refs/notes/commits abc123def456 refs/notes/commits 0000000000000000000000000000000000000000"
assert_exit 0 "$HOOK_RC" "unknown-ref-only push"

t_start "B8: audit not called for unknown-ref-only push"
if [ ! -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was called but should not have been (unknown-ref-only push)"
fi

# ---------------------------------------------------------------------------
# B9: missing audit script → exit 0 (don't break unrelated repos), even for a
# code push, because the script-presence check short-circuits before parsing.
# ---------------------------------------------------------------------------
t_start "B9: missing audit script exits 0 (no break outside etude repo)"

HOOK_RC_B9=0
HOOK_OUT_B9="$(
  export DOGFOOD_HOOK_AUDIT_SCRIPT="$tmpdir/does-not-exist-audit.sh"
  cd "$work_repo"
  printf '%s\n' "refs/heads/main abc123def456 refs/heads/main 0000000000000000000000000000000000000000" \
    | bash "$PRE_PUSH_HOOK" origin "$bare_origin" 2>&1
)" || HOOK_RC_B9=$?
assert_exit 0 "$HOOK_RC_B9" "missing audit script"

# ---------------------------------------------------------------------------
# B10: audit exits 2 (env/usage error) on a code push → hook fails closed (1)
# ---------------------------------------------------------------------------
t_start "B10: audit exit 2 on code push fails closed (exit 1)"

audit_exit2="$tmpdir/audit_exit2.sh"
cat > "$audit_exit2" <<'EXIT2SENTINEL'
#!/usr/bin/env bash
echo "audit: usage error" >&2
exit 2
EXIT2SENTINEL
chmod +x "$audit_exit2"

HOOK_RC_B10=0
HOOK_OUT_B10="$(
  export DOGFOOD_HOOK_AUDIT_SCRIPT="$audit_exit2"
  cd "$work_repo"
  printf '%s\n' "refs/heads/main abc123def456 refs/heads/main 0000000000000000000000000000000000000000" \
    | bash "$PRE_PUSH_HOOK" origin "$bare_origin" 2>&1
)" || HOOK_RC_B10=$?
assert_exit 1 "$HOOK_RC_B10" "audit exit 2 fail-closed"

# ===========================================================================
# SECTION C: bd stdin coexistence test
# ===========================================================================
echo ""
echo "=== Section C: bd stdin coexistence ==="

# Prove that the bd block receives what it needs (uses $@ = remote name+URL,
# NOT stdin) AND the dogfood classifier sees the refs buffered from stdin.
# Empirical finding: bd hooks run pre-push does NOT read stdin; stdin passes
# through the bd block intact to the dogfood classifier.

t_start "C1: bd block and dogfood classifier coexist (stdin not consumed by bd)"
rm -f "$audit_called_file"

HOOK_RC_C1=0
HOOK_OUT_C1="$(
  export DOGFOOD_HOOK_AUDIT_SCRIPT="$audit_sentinel"
  cd "$work_repo"
  printf '%s\n' "refs/heads/main abc123def456 refs/heads/main 0000000000000000000000000000000000000000" \
    | bash "$PRE_PUSH_HOOK" origin "$bare_origin" 2>&1
)" || HOOK_RC_C1=$?

# Audit sentinel exits 0 → hook should exit 0
if [[ "$HOOK_RC_C1" -eq 0 ]]; then
  t_pass
else
  t_fail "hook exited $HOOK_RC_C1, expected 0 (bd+dogfood coexistence)"
fi

t_start "C1: dogfood classifier fired (audit was called after bd block)"
if [ -f "$audit_called_file" ]; then
  t_pass
else
  t_fail "audit was NOT called — dogfood classifier may not have seen the refs"
fi

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
