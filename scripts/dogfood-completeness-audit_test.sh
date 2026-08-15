#!/usr/bin/env bash
#
# dogfood-completeness-audit_test.sh — fixture tests for the completeness audit.
#
# Each case builds a throwaway repo with a bare origin and a `bd` PATH shim, so
# nothing here touches real repo data. Replaced a 1579-line predecessor in bead
# etude-9uf.4 alongside the script it tests.
#
# What matters most here is the EXIT CODE CONTRACT: .beads/hooks/pre-push
# branches on 0/1/2 and fails closed on anything unexpected, so a change that
# turns a gap into a warning (or a usage error into a gap) silently changes what
# the push gate enforces for every lane.
#
# Run: bash scripts/dogfood-completeness-audit_test.sh   (or: make dogfood-audit-test)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AUDIT="$SCRIPT_DIR/dogfood-completeness-audit.sh"

pass_count=0
fail_count=0
current_test=""
t_start() { current_test="$1"; echo "--- TEST: $current_test"; }
t_pass()  { (( pass_count++ )) || true; echo "    PASS: $current_test"; }
t_fail()  { (( fail_count++ )) || true; echo "    FAIL: $current_test — $1" >&2; }

assert_exit() {
  local expected="$1" actual="$2" ctx="${3:-}"
  [[ "$actual" -eq "$expected" ]] && t_pass \
    || t_fail "expected exit $expected, got $actual${ctx:+ ($ctx)}"
}
assert_contains() {
  grep -qE "$1" <<< "$2" && t_pass || t_fail "expected pattern '$1' in output"
}
assert_not_contains() {
  grep -qE "$1" <<< "$2" && t_fail "pattern '$1' present but should not be" || t_pass
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

# new_repo <bead-json> — fresh repo + bare origin + bd shim emitting <bead-json>.
# Echoes the repo path. Every case gets its own, so none can affect another.
new_repo() {
  local beads="$1" n="repo$RANDOM$RANDOM" repo bare
  repo="$tmpdir/$n"; bare="$tmpdir/$n.git"
  git init -q --bare "$bare"
  git init -q "$repo"
  (
    cd "$repo"
    git config user.email t@t; git config user.name T
    touch f; git add f; git commit -qm init
    git remote add origin "$bare"; git push -q origin HEAD:refs/heads/main
    mkdir -p scripts
    cp "$AUDIT" scripts/dogfood-completeness-audit.sh
    printf '#!/usr/bin/env bash\ncat <<'\''J'\''\n%s\nJ\n' "$beads" > bd
    chmod +x bd
  )
  echo "$repo"
}

# add_run <repo> <bead> <n-gates> — mint refs/etude/runs/<bead> whose manifest
# carries <n-gates> gate attempts.
add_run() {
  local repo="$1" bead="$2" n="$3" gates="[]"
  [[ "$n" -gt 0 ]] && gates="[$(for ((i=0;i<n;i++)); do printf '{"gate_id":"g%d"},' "$i"; done | sed 's/,$//')]"
  (
    cd "$repo"
    local blob tree commit
    blob=$(printf '{"manifest_version":3,"run_id":"%s","stages":[],"gates":%s}' "$bead" "$gates" | git hash-object -w --stdin)
    tree=$(printf '100644 blob %s\tmanifest.json\n' "$blob" | git mktree)
    commit=$(git commit-tree "$tree" -m "run $bead")
    git update-ref "refs/etude/runs/$bead" "$commit"
  )
}
push_etude() { ( cd "$1" && git push -q origin 'refs/etude/*:refs/etude/*' ); }

# add_run_without_manifest <repo> <bead> — a run ref whose tree carries no
# manifest.json at all. Reachable from a hand-made ref or a partial clone whose
# promisor object cannot be fetched.
add_run_without_manifest() {
  local repo="$1" bead="$2"
  (
    cd "$repo"
    local blob tree commit
    blob=$(echo placeholder | git hash-object -w --stdin)
    tree=$(printf '100644 blob %s\tnot-a-manifest.txt\n' "$blob" | git mktree)
    commit=$(git commit-tree "$tree" -m "run $bead")
    git update-ref "refs/etude/runs/$bead" "$commit"
  )
}

# add_run_bad_json <repo> <bead> — manifest.json present but not valid JSON.
add_run_bad_json() {
  local repo="$1" bead="$2"
  (
    cd "$repo"
    local blob tree commit
    blob=$(printf 'this is not json' | git hash-object -w --stdin)
    tree=$(printf '100644 blob %s\tmanifest.json\n' "$blob" | git mktree)
    commit=$(git commit-tree "$tree" -m "run $bead")
    git update-ref "refs/etude/runs/$bead" "$commit"
  )
}

run_audit() { # run_audit <repo> [args...] -> OUT, RC
  local repo="$1"; shift
  RC=0
  OUT="$( cd "$repo" && PATH="$repo:$PATH" bash scripts/dogfood-completeness-audit.sh "$@" 2>&1 )" || RC=$?
}

ONE='[{"id":"b1","closed_at":"2026-08-15T10:00:00Z"}]'
THREE='[{"id":"b1","closed_at":"2026-08-15T10:00:00Z"},{"id":"b2","closed_at":"2026-08-15T09:00:00Z"},{"id":"b3","closed_at":"2026-08-15T08:00:00Z"}]'

# ---------------------------------------------------------------------------
# Exit-code contract — the part the pre-push hook depends on
# ---------------------------------------------------------------------------
echo "=== exit-code contract ==="

t_start "clean run exits 0"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
run_audit "$r" --last 1
assert_exit 0 "$RC" "$OUT"

t_start "clean run says so"
assert_contains "dogfood-audit: clean" "$OUT"

t_start "unknown flag is a usage error (exit 2, NOT a gap)"
run_audit "$r" --bogus
assert_exit 2 "$RC" "$OUT"

t_start "--last 0 is rejected (exit 2), not treated as an empty window"
run_audit "$r" --last 0
assert_exit 2 "$RC" "$OUT"

t_start "--last with a non-numeric value exits 2"
run_audit "$r" --last abc
assert_exit 2 "$RC" "$OUT"

t_start "the no-args default audits a window rather than erroring"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
run_audit "$r"
assert_exit 0 "$RC" "$OUT"

t_start "--help exits 0 and prints the exit-code contract"
run_audit "$r" --help
assert_contains "1  one or more hard gaps" "$OUT"

# The shipped allowlist is comment-heavy and blank-line separated. The BLANK-line
# guard is the load-bearing half: without it an empty line yields an empty array
# key, which aborts the script under `set -e` before any check runs. (The comment
# guard is cosmetic by comparison — a "#" id simply matches no bead — so this case
# pins the half that can actually break, rather than asserting something that
# cannot fire.)
t_start "an allowlist with comments and blank lines still runs to a verdict"
r=$(new_repo "$ONE")
printf '# a comment\n\n  \nb1  # test exemption\n' > "$r/scripts/dogfood-completeness-allow.txt"
run_audit "$r" --last 1
assert_exit 0 "$RC" "$OUT"

t_start "and the real allowlist entry still applies"
assert_contains "bypass: b1" "$OUT"

t_start "no closed beads exits 2, not 0"
r_empty=$(new_repo '[]')
run_audit "$r_empty" --last 5
assert_exit 2 "$RC" "$OUT"

# ---------------------------------------------------------------------------
# (a) run ref present — the only detector of a bead closed with no run at all
# ---------------------------------------------------------------------------
echo ""
echo "=== (a) run ref present ==="

t_start "missing run ref is a hard gap (exit 1)"
r=$(new_repo "$ONE")
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

t_start "missing run ref names the bead"
assert_contains "GAP  \[missing-run\] b1" "$OUT"

# ---------------------------------------------------------------------------
# (b) run has gates — the only detector of captured-but-never-gated
# ---------------------------------------------------------------------------
echo ""
echo "=== (b) run has gates ==="

t_start "run ref with zero gates is a hard gap"
r=$(new_repo "$ONE"); add_run "$r" b1 0; push_etude "$r"
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

t_start "zero-gate gap is reported as gateless-run, not missing-run"
assert_contains "GAP  \[gateless-run\] b1" "$OUT"

# REGRESSION (implement gate, both seats): the first version of this check read
# the manifest through a pipeline with an `|| echo -1` fallback. Under
# `set -o pipefail` a failing `git cat-file` made the substitution non-zero even
# though python had already printed the sentinel, so the fallback appended a
# SECOND line and the value matched no case arm — a run ref with no manifest
# passed CLEAN, exit 0, on a hard check that gates every lane's push.
t_start "a run ref with NO manifest.json is a hard gap, not a clean pass"
r=$(new_repo "$ONE"); add_run_without_manifest "$r" b1; push_etude "$r"
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

t_start "missing manifest is reported as bad-manifest"
assert_contains "GAP  \[bad-manifest\] b1" "$OUT"

t_start "a manifest that is not valid JSON is also a hard gap"
r=$(new_repo "$ONE"); add_run_bad_json "$r" b1; push_etude "$r"
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

# REGRESSION (implement gate r2): a non-list `gates` satisfies len() and would
# pass a naive count clean.
t_start "a manifest whose 'gates' is not a list is a hard gap"
r=$(new_repo "$ONE")
( cd "$r"
  blob=$(printf '{"manifest_version":3,"run_id":"b1","stages":[],"gates":"nope"}' | git hash-object -w --stdin)
  tree=$(printf '100644 blob %s\tmanifest.json\n' "$blob" | git mktree)
  git update-ref refs/etude/runs/b1 "$(git commit-tree "$tree" -m r)" )
push_etude "$r"
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

# REGRESSION (implement gate r2): a truncated bd window must not read as clean.
t_start "malformed bd output is an environment error (exit 2), not a short clean window"
r=$(new_repo '[{"id":"b1","closed_at":"2026-08-15T10:00:00Z"},{"closed_at":"2026-08-15T09:00:00Z"}]')
run_audit "$r" --last 2
assert_exit 2 "$RC" "$OUT"

t_start "a gated run does not trip (b)"
r=$(new_repo "$ONE"); add_run "$r" b1 2; push_etude "$r"
run_audit "$r" --last 1
assert_not_contains "gateless-run" "$OUT"

# An assert_not_contains alone would also pass on the empty output of a crashed
# script, so pin the exit code beside it.
t_start "and that run is clean overall"
assert_exit 0 "$RC" "$OUT"

# REGRESSION (verify gate r2 mutation sweep): the subprocess-error branches are
# the last unpinned members of the fail-open family. `git ls-remote` failure is
# already pinned by the unreachable-origin case; its siblings were not, so a
# `|| true` there would let a hard check pass vacuously with the suite green.
# A `git` shim on PATH reaches them the same way the `bd` shim does.
t_start "a for-each-ref failure is an environment error, not a vacuous pass"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
REAL_GIT="$(command -v git)"
cat > "$r/git" <<SHIM
#!/usr/bin/env bash
# Fail ONLY the local-ref enumeration; everything else is the real git, reached
# by absolute path so this shim cannot recurse into itself.
if [[ "\$1" == "for-each-ref" && "\$*" == *"refs/etude/runs"* ]]; then exit 3; fi
exec "$REAL_GIT" "\$@"
SHIM
chmod +x "$r/git"
run_audit "$r" --last 1
assert_exit 2 "$RC" "$OUT"

t_start "the for-each-ref failure does not report refs as clean"
assert_not_contains "dogfood-audit: clean" "$OUT"
rm -f "$r/git"

# The sibling branch: if python3 itself fails, the gate count is unknown. It must
# fall to the fail-CLOSED sentinel, not to a value that reads as "one gate".
t_start "a python3 failure makes the gate count a gap, not an assumed pass"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
REAL_PY2="$(command -v python3)"
cat > "$r/python3" <<SHIM
#!/usr/bin/env bash
# Fail ONLY the gate-count query; everything else is the real interpreter.
if [[ "\$*" == *"isinstance"* ]]; then exit 4; fi
exec "$REAL_PY2" "\$@"
SHIM
chmod +x "$r/python3"
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

t_start "the python3 failure is reported as bad-manifest"
assert_contains "bad-manifest" "$OUT"
rm -f "$r/python3"

# ---------------------------------------------------------------------------
# (d) refs pushed
# ---------------------------------------------------------------------------
echo ""
echo "=== (d) refs pushed ==="

t_start "a local ref absent from origin is a hard gap"
r=$(new_repo "$ONE"); add_run "$r" b1 1   # deliberately not pushed
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

t_start "unpushed gap names the ref"
assert_contains "GAP  \[unpushed-ref\] refs/etude/runs/b1" "$OUT"

t_start "a ref that diverged from origin is a hard gap"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
add_run "$r" b1 3   # rewrite the ref locally, do not re-push
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

t_start "divergence is reported with both shas and the right remedy"
assert_contains "local .* is AHEAD of origin .* — push it" "$OUT"

# REGRESSION (implement gate, codex): `git ls-remote` failure was swallowed by
# `|| true`, so an unreachable origin turned every local ref into an
# "absent from origin" gap and exited 1. To the operator the hook is addressing,
# exit 1 says "push your refs" and exit 2 says "your environment is broken" —
# reporting the second as the first sends them to do work that cannot help.
t_start "an unreachable origin is an environment error (exit 2), not a pile of gaps"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
( cd "$r" && git remote set-url origin "$tmpdir/definitely-not-a-repo.git" )
run_audit "$r" --last 1
assert_exit 2 "$RC" "$OUT"

t_start "the origin failure is not reported as unpushed refs"
assert_not_contains "unpushed-ref" "$OUT"

# REGRESSION (implement gate r2): a ref BEHIND origin needs a fetch, not a push,
# and telling the operator to push sends them to do work that cannot help.
t_start "a ref behind origin is reported as stale-ref, not unpushed-ref"
r=$(new_repo "$ONE"); add_run "$r" b1 2; push_etude "$r"
( cd "$r"
  # rewind the local ref to an earlier single-gate version of the same run
  blob=$(printf '{"manifest_version":3,"run_id":"b1","stages":[],"gates":[{"gate_id":"g0"}]}' | git hash-object -w --stdin)
  tree=$(printf '100644 blob %s\tmanifest.json\n' "$blob" | git mktree)
  base=$(git commit-tree "$tree" -m base)
  git update-ref refs/etude/runs/b1 "$base"
  git push -q -f origin "refs/etude/runs/b1:refs/etude/runs/b1"
  # now advance origin past local
  tip=$(git commit-tree "$tree" -p "$base" -m tip)
  git push -q origin "$tip:refs/etude/runs/b1" )
run_audit "$r" --last 1
assert_contains "stale-ref" "$OUT"

t_start "a behind-origin ref is still a hard gap"
assert_exit 1 "$RC" "$OUT"

# add_retro_ref <repo> <id> — mint a refs/etude/retros/<id>. Check (d) sweeps
# runs AND retros; without a case here, half of it can be deleted silently.
add_retro_ref() {
  local repo="$1" id="$2"
  (
    cd "$repo"
    local blob tree commit
    blob=$(printf '{"manifest_version":3,"refs":{"trigger":"cadence-retro"}}' | git hash-object -w --stdin)
    tree=$(printf '100644 blob %s\tmanifest.json\n' "$blob" | git mktree)
    commit=$(git commit-tree "$tree" -m "retro $id")
    git update-ref "refs/etude/retros/$id" "$commit"
  )
}

# REGRESSION (verify gate): every case minted only RUN refs, so deleting
# 'refs/etude/retros' from check (d)'s for-each-ref left the suite fully green —
# half of a hard check, silently removable. Unpushed retro refs are precisely the
# "invisible until the worktree is gone" case (d) exists for.
t_start "an unpushed RETRO ref is a hard gap, not just an unpushed run ref"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
add_retro_ref "$r" cohort-test-1     # deliberately not pushed
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

t_start "the unpushed retro ref is named"
assert_contains "GAP  \[unpushed-ref\] refs/etude/retros/cohort-test-1" "$OUT"

t_start "a pushed retro ref does not gap"
push_etude "$r"
run_audit "$r" --last 1
assert_exit 0 "$RC" "$OUT"

# REGRESSION (verify gate): pin the CLASS, not just the instance. The case
# statement's fail-closed catch-all is what stops an unexpected gate count from
# passing; deleting that arm previously left the suite green. A python3 shim
# emitting a two-line value reproduces the round-1 shape directly.
t_start "an unexpected gate-count value is a hard gap, not a silent pass"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
REAL_PY="$(command -v python3)"
cat > "$r/python3" <<SHIM
#!/usr/bin/env bash
# Emit the two-line value the round-1 pipefail bug produced, but only for the
# gate-count query; every other call goes to the real interpreter by ABSOLUTE
# path — resolving it through PATH would find this shim again and recurse.
if [[ "\$*" == *"isinstance"* ]]; then printf -- '-1\\n-1\\n'; exit 0; fi
exec "$REAL_PY" "\$@"
SHIM
chmod +x "$r/python3"
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"
rm -f "$r/python3"

# REGRESSION (verify gate): the script must query the repo it lives in, not the
# ambient cwd, or it audits another checkout's refs against this one's allowlist.
# Running from a SUBDIRECTORY is not a real probe: git walks up and finds the
# same repo either way. The mutation that matters is running from inside a
# DIFFERENT git repo, where an ambient-cwd query audits the wrong refs entirely.
t_start "running from another git repo still audits the script's own repo"
r=$(new_repo "$ONE")
add_run "$r" b1 1; push_etude "$r"          # this repo is CLEAN
elsewhere="$tmpdir/elsewhere$RANDOM"
git init -q "$elsewhere"
( cd "$elsewhere" && git config user.email t@t && git config user.name T \
    && touch g && git add g && git commit -qm other )
# Mint an unpushed etude ref in the OTHER repo. If the audit queried the ambient
# cwd it would see this ref and gap; querying its own repo, it must stay clean.
( cd "$elsewhere"
  blob=$(printf '{}' | git hash-object -w --stdin)
  tree=$(printf '100644 blob %s\tmanifest.json\n' "$blob" | git mktree)
  git update-ref refs/etude/runs/not-ours "$(git commit-tree "$tree" -m x)" )
RC=0
OUT="$( cd "$elsewhere" && PATH="$r:$PATH" bash "$r/scripts/dogfood-completeness-audit.sh" --last 1 2>&1 )" || RC=$?
assert_exit 0 "$RC" "$OUT"

t_start "the other repo's refs are not audited"
assert_not_contains "not-ours" "$OUT"

# REGRESSION (implement gate r3, codex): when origin's tip object is NOT present
# locally — the normal state of a repo that is behind — the audit cannot decide
# ahead from behind, and must say so rather than assert "push it".
t_start "an undecidable divergence is reported as diverged-ref, not a push instruction"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
# advance origin from a SEPARATE clone, so the new tip never enters r's object store
clone="$tmpdir/clone$RANDOM"
git clone -q "$(cd "$r" && git remote get-url origin)" "$clone"
( cd "$clone"
  git config user.email t@t; git config user.name T
  blob=$(printf '{"manifest_version":3,"run_id":"b1","stages":[],"gates":[{"gate_id":"g0"},{"gate_id":"g1"}]}' | git hash-object -w --stdin)
  tree=$(printf '100644 blob %s\tmanifest.json\n' "$blob" | git mktree)
  tip=$(git commit-tree "$tree" -m advanced)
  git push -q -f origin "$tip:refs/etude/runs/b1" )
run_audit "$r" --last 1
assert_contains "diverged-ref" "$OUT"

t_start "an undecidable divergence does not tell the operator to push"
assert_not_contains "diverged-ref.*push it" "$OUT"

# ---------------------------------------------------------------------------
# (c) retro cadence — WARNS, never fails. This is the case most likely to be
# broken by accident, because making a warning hard looks like an improvement.
# ---------------------------------------------------------------------------
echo ""
echo "=== (c) retro cadence is WARN only ==="

t_start "3 uncovered beads with clean hard checks still exits 0"
r=$(new_repo "$THREE")
for b in b1 b2 b3; do add_run "$r" "$b" 1; done
push_etude "$r"
run_audit "$r" --last 3
assert_exit 0 "$RC" "$OUT"

t_start "cadence overdue is reported as WARN"
assert_contains "WARN \[cadence-overdue\] 3 active" "$OUT"

t_start "cadence overdue is NOT counted as a hard gap"
assert_not_contains "GAP  \[cadence" "$OUT"

t_start "under the threshold, cadence reports ok"
r=$(new_repo "$ONE"); add_run "$r" b1 1; push_etude "$r"
run_audit "$r" --last 1
assert_contains "ok   cadence: 1 uncovered" "$OUT"

# ---------------------------------------------------------------------------
# Allowlist — exempts (a)/(b) AND is (c)'s denominator, and stays visible
# ---------------------------------------------------------------------------
echo ""
echo "=== allowlist ==="

t_start "an allowlisted bead with no run ref does not gap"
r=$(new_repo "$ONE")
printf 'b1  # test exemption\n' > "$r/scripts/dogfood-completeness-allow.txt"
run_audit "$r" --last 1
assert_exit 0 "$RC" "$OUT"

t_start "the exemption is REPORTED, not silent"
assert_contains "bypass: b1 — test exemption" "$OUT"

t_start "allowlisted beads leave the active count lower"
assert_contains "1 closed bead\(s\) in window, 0 active" "$OUT"

t_start "allowlisting does NOT exempt a ref from the pushed check"
# b1 is allowlisted, but its ref exists locally and is unpushed: (d) is repo-wide
r=$(new_repo "$ONE")
printf 'b1  # test exemption\n' > "$r/scripts/dogfood-completeness-allow.txt"
add_run "$r" b1 1
run_audit "$r" --last 1
assert_exit 1 "$RC" "$OUT"

# ---------------------------------------------------------------------------
echo ""
echo "==========================================="
echo "Test results: $pass_count passed, $fail_count failed"
echo "==========================================="
[[ "$fail_count" -eq 0 ]] || exit 1
exit 0
