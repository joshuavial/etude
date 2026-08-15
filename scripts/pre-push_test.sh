#!/usr/bin/env bash
#
# pre-push_test.sh — fixture-based tests for the .beads/hooks/pre-push
# dogfood enforcement block.
#
# These cases resolve $PRE_PUSH_HOOK through this checkout's own REPO_ROOT, so
# they exercise the tracked hook as it stands here rather than a copy of its
# logic. Note that git config core.hooksPath points at the MAIN checkout's
# .beads/hooks, so from a worktree the hook under test is not yet the one
# executing on pushes; it becomes so once the change lands and that copy is
# updated. Bead etude-6d9 covers making such drift detectable.
#
# Extracted in etude-9uf.3 from the retired scripts/dogfood-close_test.sh.
# That file tested two things: the close wrapper (deleted with it) and this
# hook (which survives). Only the hook half is kept here.
#
# Harness pattern:
#   - throwaway git repo + bare-repo origin
#   - bd PATH shim emitting canned closed-bead JSON, so the hook's bd block runs
#     against a known stub rather than the machine's real bd
#   - DOGFOOD_HOOK_AUDIT_SCRIPT injection so no real audit ever runs
#
# Run directly:
#   bash scripts/pre-push_test.sh
#
# Or via make:
#   make pre-push-test
#
# Requires: bash 4+, git. Does NOT mutate any real repo data.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
PRE_PUSH_HOOK="$REPO_ROOT/.beads/hooks/pre-push"

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
trap 'rm -rf "$tmpdir"' EXIT

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

# ---------------------------------------------------------------------------
# bd shim: returns canned closed-bead JSON
# ---------------------------------------------------------------------------
BD_SHIM="$tmpdir/bd"
cat > "$BD_SHIM" <<'BDSHIM'
#!/usr/bin/env bash
if [[ "$*" == *"show"* ]]; then
  echo "bead-id   Test bead [● P1 · CLOSED]"
else
  echo "[]"
fi
BDSHIM
chmod +x "$BD_SHIM"
export PATH="$tmpdir:$PATH"

# ===========================================================================
# SECTION B: pre-push hook classification tests
# ===========================================================================
echo ""
echo "=== Section B: pre-push hook tests ==="

# We test the hook by running it directly with synthetic stdin. The hook reads
# its audit script from DOGFOOD_HOOK_AUDIT_SCRIPT when that is set, so each case
# exports it to point at a sentinel below and no real audit ever runs.

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

# The BLOCKED message is the operator's only pointer to what to do next. It named
# the retired close script until etude-9uf.3 deleted it; pin the replacement so a
# future edit cannot quietly leave a dangling instruction behind.
t_start "B3: blocked message points at the current capture/gate/sync flow"
assert_output_contains "etude capture.*etude gate.*etude sync" "$HOOK_OUT_B3"

# Presence of the new pointer is not absence of the old one: a message naming
# both would satisfy the assertion above while still sending an operator to a
# script deleted in etude-9uf.3.
t_start "B3: blocked message no longer names the retired close script"
assert_output_not_contains "dogfood-close" "$HOOK_OUT_B3"

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
