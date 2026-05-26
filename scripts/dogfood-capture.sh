#!/usr/bin/env bash
#
# dogfood-capture.sh — capture one closed bead's dev-workflow phases as an etude
# run under refs/etude/runs/<bead-id>, then push the ref to origin.
#
# This automates the previously-manual 5-artifact + spec + capture-run + push
# sequence used after each gated bead closes. It builds a YAML spec describing
# all four dev-workflow stages (plan/implement/verify/review), stages all five
# artifacts into a dedicated spec directory, and invokes `etude capture-run`
# once. It ALWAYS builds etude fresh from source first, so it never inspects
# or writes with a stale ambient binary.
#
# Usage:
#   scripts/dogfood-capture.sh <bead-id> <commit-sha> <verify-file> <review-file>
#
#   <bead-id>      bead whose run to capture (also the run id and bead ref)
#   <commit-sha>   commit whose `git show` diff is the implement-stage artifact
#   <verify-file>  path to the Verify-stage artifact (any extension)
#   <review-file>  path to the Final-Review-stage artifact (any extension)
#
# The plan-stage artifact is the bead's `design` field; the task-stage input is
# `bd show <bead-id>`. Stage skills/models mirror the dev-workflow seats:
#   plan=dev-planner/opus  implement=dev-coder/sonnet  verify=dev-qa/opus
#   review=dev-pr-reviewer/opus
#
# NOTE: capture-run is create-only. If refs/etude/runs/<bead-id> already exists
# this script errors immediately (no accidental double-capture). To re-capture a
# bead, first delete the existing ref:
#   git update-ref -d refs/etude/runs/<bead-id>
#   git push origin --delete refs/etude/runs/<bead-id>
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
specdir="$(mktemp -d)"
trap 'rm -rf "$bindir" "$specdir"' EXIT
bin="$bindir/etude"

# Build etude fresh — never reuse an ambient binary.
go build -o "$bin" ./cmd/etude

# Generate the three script-produced artifacts directly into the spec dir.
bd show "$bead" > "$specdir/task.md"
bd show "$bead" --json | python3 -c "
import sys, json
d = json.load(sys.stdin)
d = d[0] if isinstance(d, list) else d
sys.stdout.write(d.get('design') or '')
" > "$specdir/plan.md"
git show "$commit" > "$specdir/diff.patch"

# Copy the two arg files preserving their source extension so inferMediaType
# produces the same media_type as the old `capture` calls (which inferred from
# the original path). The extension is taken from the BASENAME only — matching
# Go's filepath.Ext, which ignores dots in parent directories — so a path like
# /tmp/foo.bar/verify (dotted dir, no file extension) stages without an
# extension rather than misparsing "bar/verify" as the suffix.
stage_ext_name() { # <dest-base> <src-path> -> echoes dest name with src extension
  local base; base="${2##*/}"
  case "$base" in
    *.*) printf '%s.%s' "$1" "${base##*.}" ;;
    *)   printf '%s' "$1" ;;
  esac
}
vname="$(stage_ext_name verify "$verify_file")"
cp -- "$verify_file" "$specdir/$vname"

rname="$(stage_ext_name review "$review_file")"
cp -- "$review_file" "$specdir/$rname"

harness_version="$(claude --version 2>/dev/null || echo unknown)"

# Write the capture-run YAML spec. All paths are relative basenames so
# capture-run's path-confinement check passes. diff.patch and plan.md each
# appear in two stages; capture-run de-dupes to one content-addressed blob.
cat > "$specdir/run.yaml" <<SPEC
run_id: ${bead}
workflow: default
workflow_version: v1
git_sha: ${commit}
harness: claude-code
harness_version: ${harness_version}
refs:
  bead: ${bead}
stages:
  - stage: plan
    produced_by: original
    model: claude-opus-4-7
    skill: {id: dev-planner}
    inputs:
      - {role: task, path: task.md}
    output: {role: plan, path: plan.md}
  - stage: implement
    produced_by: original
    model: claude-sonnet-4-6
    skill: {id: dev-coder}
    inputs:
      - {role: plan, path: plan.md}
    output: {role: diff, path: diff.patch}
  - stage: verify
    produced_by: original
    model: claude-opus-4-7
    skill: {id: dev-qa}
    inputs:
      - {role: diff, path: diff.patch}
    output: {role: verify, path: ${vname}}
  - stage: review
    produced_by: original
    model: claude-opus-4-7
    skill: {id: dev-pr-reviewer}
    inputs:
      - {role: diff, path: diff.patch}
      - {role: verify, path: ${vname}}
    output: {role: review, path: ${rname}}
SPEC

"$bin" capture-run "$specdir/run.yaml"

git push origin "refs/etude/runs/$bead"
echo "captured + pushed run: $bead"
"$bin" run show "$bead" | grep -E "stage:|model:|skill:"
