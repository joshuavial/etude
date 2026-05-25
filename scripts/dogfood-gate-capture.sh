#!/usr/bin/env bash
#
# dogfood-gate-capture.sh — append one structured review-gate attempt to a bead's
# etude run and push it, with a local verify-before-push safety check.
#
# Usage:
#   scripts/dogfood-gate-capture.sh <bead-id> <gate-json-file>
#
#   <bead-id>         the run id / bead ref to append the gate attempt to
#   <gate-json-file>  a JSON GateAttempt document (shape: see
#                     docs/plans/dogfood/review-gate-runbook.md "Structured
#                     capture") consumed by `etude capture-gate --gate-file`
#
# Unlike a stage capture (dogfood-capture.sh, run once post-close), a gate attempt
# is recorded per attempt at gate time. This wrapper ALWAYS builds etude fresh
# (never an ambient binary), fetches the run ref so capture-gate's optimistic-
# concurrency CAS guards a stale tip, appends the gate LOCALLY, VERIFIES the local
# manifest (manifest_version 3 + the appended gate_id present) BEFORE pushing, and
# only then pushes refs/etude/runs/<bead> to origin.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <bead-id> <gate-json-file>" >&2
  exit 2
fi

bead="$1"; gate_file="$2"
[ -f "$gate_file" ] || { echo "error: gate file not found: $gate_file" >&2; exit 1; }
# Absolutize the gate-file path BEFORE cd-ing to the repo root, so a relative
# path passed from another directory still resolves.
gate_file="$(cd "$(dirname "$gate_file")" && pwd)/$(basename "$gate_file")"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
ref="refs/etude/runs/$bead"

# Build etude fresh — never reuse an ambient binary.
bindir="$(mktemp -d)"
trap 'rm -rf "$bindir"' EXIT
bin="$bindir/etude"
go build -o "$bin" ./cmd/etude

# Fetch the run ref so the local tip matches origin; capture-gate's ExpectedOld
# CAS then fails loudly on a stale tip rather than clobbering a concurrent update.
# Best-effort: a run created locally this session may not be on origin yet.
git fetch origin "$ref:$ref" 2>/dev/null || true

# A gate must attach to an existing run.
if ! git rev-parse --verify --quiet "$ref" >/dev/null; then
  echo "error: run ref $ref not found; a gate must attach to an existing run" >&2
  exit 1
fi

# Append the gate attempt LOCALLY (capture-gate validates referential integrity
# and fails without mutating the ref on bad input).
"$bin" capture-gate --run "$bead" --gate-file "$gate_file"

# VERIFY locally BEFORE pushing: manifest_version must be 3 and the appended
# gate_id must be present. `etude run show` has no --json, so read the manifest
# straight from the ref tree.
gate_id="$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['gate_id'])" "$gate_file")"
git cat-file -p "$ref:manifest.json" | python3 -c "
import json, sys
m = json.load(sys.stdin)
gid = sys.argv[1]
v = m.get('manifest_version')
ids = [g.get('gate_id') for g in m.get('gates', [])]
assert v == 3, f'expected manifest_version 3 after gate append, got {v}'
assert gid in ids, f'gate_id {gid!r} not found in gates {ids}'
print(f'verified: manifest_version={v}, gates={len(ids)} {ids}; appended {gid!r}')
" "$gate_id"

echo "local tip: $(git rev-parse "$ref")"

# Only push after local verification passed.
git push origin "$ref"
echo "pushed $ref"
