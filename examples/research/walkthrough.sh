#!/usr/bin/env bash
# walkthrough.sh — end-to-end research workflow example using only git + sh + the etude binary.
#
# Proves the engine has no dev-specific assumptions: a genuinely non-dev
# 5-stage workflow (research → fact-check → draft → review → tone-police)
# runs live via `etude run`, captures by construction, executes a gate that
# resolves from a shared registry, and forward-replays deterministically.
# NO real LLM or network access is needed: all runners and the gate seat are
# deterministic shell stubs in this directory.
#
# Usage:
#   bash examples/research/walkthrough.sh
#   ETUDE_BIN=/path/to/etude bash examples/research/walkthrough.sh
#
# When ETUDE_BIN is unset the script builds etude from source into a temp dir.
# The smoke test (internal/example/research_example_test.go) always sets
# ETUDE_BIN so it can supply a freshly compiled binary.
set -euo pipefail

# ---------------------------------------------------------------------------
# Resolve paths: SCRIPT_DIR is this file's directory; REPO_ROOT is the
# etude source root (two levels up); EXAMPLES_DIR is this directory and
# holds the runner/seat/check scripts with ABSOLUTE paths.
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
EXAMPLES_DIR="$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Create the temp dirs and install the cleanup trap BEFORE any fallible step
# so nothing leaks if an early command fails.
# WORK is the throwaway demo repo.
# BINDIR holds a locally-built binary (empty when ETUDE_BIN is supplied).
# ---------------------------------------------------------------------------
WORK="$(mktemp -d)"
BINDIR=""
trap '[ -n "$BINDIR" ] && rm -rf "$BINDIR"; rm -rf "$WORK"' EXIT

# ---------------------------------------------------------------------------
# Build or locate the etude binary.
# ---------------------------------------------------------------------------
if [ -n "${ETUDE_BIN:-}" ]; then
    ETUDE="$ETUDE_BIN"
    echo "==> Using etude binary: $ETUDE"
else
    echo "==> Building etude from source..."
    BINDIR="$(mktemp -d)"
    (cd "$REPO_ROOT" && go build -o "$BINDIR/etude" ./cmd/etude)
    ETUDE="$BINDIR/etude"
    echo "    built: $ETUDE"
fi

# ---------------------------------------------------------------------------
# Initialise the throwaway git repository for the demo.
# ---------------------------------------------------------------------------
echo ""
echo "==> Initialising throwaway git repo: $WORK"
git init "$WORK" --quiet
git -C "$WORK" config user.name  "Etude Example"
git -C "$WORK" config user.email "example@etude.local"

# ---------------------------------------------------------------------------
# Commit the sample task document so HEAD resolves (required by etude run).
# ---------------------------------------------------------------------------
mkdir -p "$WORK/inputs"
cp "$EXAMPLES_DIR/inputs/topic.txt" "$WORK/inputs/"
git -C "$WORK" add inputs/
git -C "$WORK" commit --quiet -m "add research task"

cd "$WORK"

# ---------------------------------------------------------------------------
# Write .etude/workflow.yaml with ABSOLUTE script paths interpolated so the
# gate check command resolves regardless of working directory.
# ---------------------------------------------------------------------------
echo ""
echo "==> Writing .etude/workflow.yaml and .etude/registry.yaml"
mkdir -p .etude

cat > .etude/workflow.yaml << EOF
name: research
stages:
  - name: research
    produces: findings
    inputs:
      - task
    skill: research-skill
    runner:
      name: stage-runner
  - name: fact-check
    produces: checked
    inputs:
      - task
      - findings
    skill: fact-check-skill
    runner:
      name: stage-runner
  - name: draft
    produces: draft
    inputs:
      - checked
    skill: draft-skill
    runner:
      name: stage-runner
  - name: review
    produces: reviewed
    inputs:
      - draft
    skill: review-skill
    runner:
      name: stage-runner
    gate:
      tier: L1
      max_rounds: 1
      checks:
        - command: $EXAMPLES_DIR/gate-check.sh
  - name: tone-police
    produces: toned
    inputs:
      - reviewed
    skill: tone-police-skill
    runner:
      name: stage-runner
EOF

# Write .etude/registry.yaml with absolute paths to the deterministic stubs.
cat > .etude/registry.yaml << EOF
quorum: unanimous
seats:
  stage-runner:
    provider: deterministic/stage-runner
    harness: shell
    invoke: $EXAMPLES_DIR/stage-runner.sh
  approver:
    provider: deterministic/approver
    harness: shell
    invoke: $EXAMPLES_DIR/approve-seat.sh
tiers:
  L1:
    name: Research review tier
    seats:
      - approver
EOF
echo "    .etude/ written"

# ---------------------------------------------------------------------------
# Commit the etude config so the HEAD commit includes it (good practice;
# not strictly required since etude reads from filesystem, not git tree).
# ---------------------------------------------------------------------------
git -C "$WORK" add .etude/
git -C "$WORK" commit --quiet -m "add etude research workflow config"

# ---------------------------------------------------------------------------
# Step 1: run the research workflow live.
# etude run research prints "captured <oid>" per stage, "captured gate <id>
# status=pass" when the gate passes, then "ref refs/etude/runs/<id>".
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 1: etude run research (5 stages + review gate)"
RUN_OUT="$("$ETUDE" run research --task inputs/topic.txt)"
echo "$RUN_OUT"
RUN_ID="$(printf '%s\n' "$RUN_OUT" | grep '^ref refs/etude/runs/' | sed 's|^ref refs/etude/runs/||')"
echo ""
echo "==> captured run: $RUN_ID"

# ---------------------------------------------------------------------------
# Step 2: inspect the run.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 2: etude run show $RUN_ID"
SHOW_OUT="$("$ETUDE" run show "$RUN_ID")"
echo "$SHOW_OUT"

# Read stage output paths from the run's machine-readable manifest rather than
# coupling the walkthrough to the human `run show` layout. Forward replay prints
# each stage output sequentially, so their concatenation is the exact expected
# replay stream.
MANIFEST="$WORK/run-manifest.json"
OUTPUT_ARTIFACTS="$WORK/output-artifacts.tsv"
git -C "$WORK" show "refs/etude/runs/$RUN_ID:manifest.json" > "$MANIFEST"
# Etude writes canonical pretty JSON with one key per line; fail-closed count
# checks below make any future layout change explicit rather than silently skip.
awk '
    /^[[:space:]]*"stage":[[:space:]]*/ {
        stage = $0
        sub(/^[^:]*:[[:space:]]*"/, "", stage)
        sub(/".*$/, "", stage)
    }
    /^[[:space:]]*"output":[[:space:]]*\{/ { want_output_path = 1; next }
    want_output_path && /^[[:space:]]*"path":[[:space:]]*/ {
        path = $0
        sub(/^[^:]*:[[:space:]]*"/, "", path)
        sub(/".*$/, "", path)
        print stage "\t" path
        want_output_path = 0
    }
' "$MANIFEST" > "$OUTPUT_ARTIFACTS"

EXPECTED_REPLAY="$WORK/expected-replay.out"
: > "$EXPECTED_REPLAY"
TONED_PATH=""
OUTPUT_COUNT=0
while IFS=$'\t' read -r stage artifact_path; do
    [ -n "$artifact_path" ] || continue
    git -C "$WORK" show "refs/etude/runs/$RUN_ID:$artifact_path" >> "$EXPECTED_REPLAY"
    OUTPUT_COUNT=$((OUTPUT_COUNT + 1))
    if [ "$stage" = "tone-police" ] && [ -z "$TONED_PATH" ]; then
        TONED_PATH="$artifact_path"
    fi
done < "$OUTPUT_ARTIFACTS"
[ "$OUTPUT_COUNT" -eq 5 ] || { echo "expected 5 stage outputs in manifest, found $OUTPUT_COUNT" >&2; exit 1; }
[ -n "$TONED_PATH" ] || { echo "tone-police output path missing from manifest" >&2; exit 1; }
git -C "$WORK" show "refs/etude/runs/$RUN_ID:$TONED_PATH" > "$WORK/original-toned.out"

compare_bytes() {
    local label="$1"
    local expected="$2"
    local actual="$3"
    if cmp "$expected" "$actual"; then
        return 0
    fi

    local expected_bytes="$(wc -c < "$expected" | tr -d ' ')"
    local actual_bytes="$(wc -c < "$actual" | tr -d ' ')"
    echo "byte mismatch ($label): expected_bytes=$expected_bytes actual_bytes=$actual_bytes" >&2
    return 1
}

# ---------------------------------------------------------------------------
# Step 3: forward replay — re-execute all 5 stages with the same recorded
# inputs.  Output is byte-stable: stage-runner.sh is deterministic.
# The engine resolves runners from .etude/registry.yaml (stage-runner seat).
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 3: etude replay $RUN_ID (forward — all 5 stages)"
echo "    replay output:"
"$ETUDE" replay "$RUN_ID" > "$WORK/replay.out"
cat "$WORK/replay.out"
compare_bytes "full replay" "$EXPECTED_REPLAY" "$WORK/replay.out"
TONED_BYTES="$(wc -c < "$WORK/original-toned.out" | tr -d ' ')"
tail -c "$TONED_BYTES" "$WORK/replay.out" > "$WORK/replayed-toned.out"
compare_bytes "toned output" "$WORK/original-toned.out" "$WORK/replayed-toned.out"
echo "    replay bytes match the original stage artifacts (including toned)"

# ---------------------------------------------------------------------------
# Done.
# ---------------------------------------------------------------------------
echo ""
echo "==> Walkthrough complete."
echo "    git repo, binary, and temp dirs will be cleaned up on exit."
