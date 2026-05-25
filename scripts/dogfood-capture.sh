#!/usr/bin/env bash
#
# dogfood-capture.sh — capture one closed bead's dev-workflow phases as an etude
# run under refs/etude/runs/<bead-id>, then push the ref to origin.
#
# This automates the previously-manual 5-artifact + 4-capture-call + push
# sequence used after each gated bead closes. It ALWAYS builds etude fresh from
# source first, so it never inspects/writes with a stale ambient binary (a stale
# /tmp/etude once produced a phantom "run list is broken" investigation).
#
# Usage:
#   scripts/dogfood-capture.sh <bead-id> <commit-sha> <verify-file> <review-file>
#
#   <bead-id>      bead whose run to capture (also the run id and bead ref)
#   <commit-sha>   commit whose `git show` diff is the implement-stage artifact
#   <verify-file>  path to the Verify-stage markdown artifact
#   <review-file>  path to the Final-Review-stage markdown artifact
#
# The plan-stage artifact is the bead's `design` field; the task-stage input is
# `bd show <bead-id>`. Stage skills/models mirror the dev-workflow seats:
#   plan=dev-planner/opus  implement=dev-coder/sonnet  verify=dev-qa/opus
#   review=dev-pr-reviewer/opus
#
# Requires: bd, git, go, python3, and `claude --version` for the harness version.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <bead-id> <commit-sha> <verify-file> <review-file>" >&2
  exit 2
fi

bead="$1"; commit="$2"; verify_file="$3"; review_file="$4"
repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Resolve to a full 40-char hex id; etude --git-sha rejects short SHAs.
commit="$(git rev-parse --verify "${commit}^{commit}")"

for f in "$verify_file" "$review_file"; do
  [ -f "$f" ] || { echo "error: artifact file not found: $f" >&2; exit 1; }
done

# Scratch dirs (cleaned on exit). Set before the trap so set -u is safe.
bindir="$(mktemp -d)"
tmp="$(mktemp -d)"
trap 'rm -rf "$bindir" "$tmp"' EXIT
bin="$bindir/etude"

# Build etude fresh — never reuse an ambient binary.
go build -o "$bin" ./cmd/etude

bd show "$bead" > "$tmp/task.md"
bd show "$bead" --json | python3 -c "
import sys, json
d = json.load(sys.stdin)
d = d[0] if isinstance(d, list) else d
sys.stdout.write(d.get('design') or '')
" > "$tmp/plan.md"
git show "$commit" > "$tmp/diff.patch"

harness_version="$(claude --version 2>/dev/null || echo unknown)"
common=(--run "$bead" --harness claude-code --harness-version "$harness_version"
        --git-sha "$commit" --produced-by original
        --workflow default --workflow-version v1 --ref "bead=$bead")

"$bin" capture plan      "${common[@]}" --skill-id dev-planner     --model claude-opus-4-7   --input task=$tmp/task.md   --output plan=$tmp/plan.md
"$bin" capture implement "${common[@]}" --skill-id dev-coder       --model claude-sonnet-4-6 --input plan=$tmp/plan.md   --output diff=$tmp/diff.patch
"$bin" capture verify    "${common[@]}" --skill-id dev-qa          --model claude-opus-4-7   --input diff=$tmp/diff.patch --output verify="$verify_file"
"$bin" capture review    "${common[@]}" --skill-id dev-pr-reviewer --model claude-opus-4-7   --input diff=$tmp/diff.patch --input verify="$verify_file" --output review="$review_file"

git push origin "refs/etude/runs/$bead"
echo "captured + pushed run: $bead"
"$bin" run show "$bead" | grep -E "stage:|model:|skill:"
