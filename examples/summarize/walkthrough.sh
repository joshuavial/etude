#!/usr/bin/env bash
# walkthrough.sh — end-to-end etude example using only git + sh + the etude binary.
#
# This script demonstrates that etude is a reusable git-native primitive that
# works WITHOUT beads, xenota, any issue tracker, LLM, or network access.
# Compare scripts/dogfood-capture.sh which is tightly coupled to the bd tracker.
#
# Usage:
#   bash examples/summarize/walkthrough.sh
#   ETUDE_BIN=/path/to/etude bash examples/summarize/walkthrough.sh
#
# When ETUDE_BIN is unset the script builds etude from source into a temp dir.
# The smoke test (internal/example/example_test.go) always sets ETUDE_BIN so
# it can supply a freshly compiled binary.
set -euo pipefail

# ---------------------------------------------------------------------------
# Resolve repository root (so we can find runner/judge/docs with absolute paths
# regardless of where this script is invoked from).
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
EXAMPLES_DIR="$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Create the temp dirs and install the cleanup trap BEFORE any fallible step
# (notably the go build below) so nothing leaks if an early command fails.
# WORK is the throwaway demo repo; BINDIR holds a locally-built binary and
# stays empty when ETUDE_BIN is supplied. The single trap always removes WORK
# and removes BINDIR only when we built the binary ourselves (BINDIR non-empty).
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
# Commit the sample documents so HEAD resolves (required by etude capture).
# ---------------------------------------------------------------------------
mkdir -p "$WORK/docs"
cp "$EXAMPLES_DIR/docs/alpha.txt" "$WORK/docs/"
cp "$EXAMPLES_DIR/docs/beta.txt"  "$WORK/docs/"
cp "$EXAMPLES_DIR/docs/gamma.txt" "$WORK/docs/"
git -C "$WORK" add docs/
git -C "$WORK" commit --quiet -m "add sample docs"

cd "$WORK"

# ---------------------------------------------------------------------------
# Step 1: etude init — scaffold .etude/ config.
# origin is absent so the refspec step is skipped; that is expected and fine.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 1: etude init"
"$ETUDE" init
echo "    .etude/ scaffolded"

# ---------------------------------------------------------------------------
# Step 2: capture three original summarize runs.
# The "v1 skill" is deliberately terse: just the first line of each document.
# Run ids are plain strings — NOT issue-tracker IDs.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 2: generating v1 (original) summaries — first line only"
mkdir -p "$WORK/summaries"
for doc in alpha beta gamma; do
  head -n 1 "docs/$doc.txt" > "summaries/$doc-v1.txt"
done
echo "    v1 summaries written to summaries/"

echo ""
echo "==> Step 2: capture original summarize runs (v1 skill)"

"$ETUDE" capture summarize \
  --run doc-alpha \
  --input  doc=docs/alpha.txt \
  --output summary=summaries/alpha-v1.txt \
  --workflow summarize \
  --produced-by original

"$ETUDE" capture summarize \
  --run doc-beta \
  --input  doc=docs/beta.txt \
  --output summary=summaries/beta-v1.txt \
  --workflow summarize \
  --produced-by original

"$ETUDE" capture summarize \
  --run doc-gamma \
  --input  doc=docs/gamma.txt \
  --output summary=summaries/gamma-v1.txt \
  --workflow summarize \
  --produced-by original

echo "    captured: doc-alpha, doc-beta, doc-gamma (v1 summaries)"

# ---------------------------------------------------------------------------
# Step 3: inspect runs.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 3: etude run list"
"$ETUDE" run list

echo ""
echo "==> Step 3: etude run show doc-alpha"
"$ETUDE" run show doc-alpha

# ---------------------------------------------------------------------------
# Step 4: replay one run.
# Absolute paths for --runner because etude sets cwd to a scratch worktree.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 4: etude replay doc-alpha summarize"
REPLAY_OUT="$WORK/replayed-alpha.txt"
"$ETUDE" replay doc-alpha summarize \
  --runner "$EXAMPLES_DIR/summarize-runner.sh" \
  --output "$REPLAY_OUT"
echo "    replayed output:"
cat "$REPLAY_OUT"

# ---------------------------------------------------------------------------
# Step 5: bench across the cohort of 3 runs.
# The judge picks the longer summary (pure content rule, no LLM needed).
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 5: etude bench summarize --last 3"
"$ETUDE" bench summarize \
  --last 3 \
  --runner "$EXAMPLES_DIR/summarize-runner.sh" \
  --judge  "$EXAMPLES_DIR/pick-longer-judge.sh"

# ---------------------------------------------------------------------------
# Step 6: maintenance — reindex then gc.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 6: etude reindex"
"$ETUDE" reindex

echo ""
echo "==> Step 6: etude gc"
"$ETUDE" gc

echo ""
echo "==> Walkthrough complete."
echo "    git repo, binary, and temp dirs will be cleaned up on exit."
