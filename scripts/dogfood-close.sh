#!/usr/bin/env bash
#
# dogfood-close.sh — orchestrate the full close sequence for one bead:
#   preflight → capture run → capture gate records → terminal audit --bead
#
# This is the one command to run at bead close. It calls dogfood-capture.sh
# and dogfood-gate-capture.sh (orchestrate, don't replace), then runs the
# completeness audit as the terminal, blocking gate. The bead is not complete
# until this exits 0.
#
# Usage:
#   scripts/dogfood-close.sh <bead-id> <commit-sha> <verify-file> <review-file> [gate-dir]
#
#   <bead-id>      bead to close (same as dogfood-capture's first arg)
#   <commit-sha>   implement-stage diff source (passed to dogfood-capture)
#   <verify-file>  path to the Verify-stage artifact
#   <review-file>  path to the Final-Review-stage artifact
#   [gate-dir]     OPTIONAL directory of *.json GateAttempt files (one per gate
#                  attempt). Each is captured in lexical order via
#                  dogfood-gate-capture.sh. If omitted the terminal audit will
#                  fail check (b) for a non-allowlisted bead — which is the
#                  correct loud signal that gate records are missing.
#
# NOTE: capture-run is create-only. If refs/etude/runs/<bead-id> already exists
# this script errors at preflight (no accidental double-capture). To re-close:
#   git update-ref -d refs/etude/runs/<bead-id>
#   git push origin --delete refs/etude/runs/<bead-id>
#
# Requires: bd, git, go, python3, and the capture scripts in the same directory.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Allow test injection: override the paths to the capture/audit scripts.
# These are used by tests to stub out the heavy scripts without rebuilding etude.
DOGFOOD_CAPTURE="${DOGFOOD_CLOSE_CAPTURE_SCRIPT:-$SCRIPT_DIR/dogfood-capture.sh}"
DOGFOOD_GATE_CAPTURE="${DOGFOOD_CLOSE_GATE_CAPTURE_SCRIPT:-$SCRIPT_DIR/dogfood-gate-capture.sh}"
DOGFOOD_AUDIT="${DOGFOOD_CLOSE_AUDIT_SCRIPT:-$SCRIPT_DIR/dogfood-completeness-audit.sh}"

# ---------------------------------------------------------------------------
# Argument validation (exit 2 = usage error)
# ---------------------------------------------------------------------------
if [ "$#" -lt 4 ] || [ "$#" -gt 5 ]; then
  echo "usage: $0 <bead-id> <commit-sha> <verify-file> <review-file> [gate-dir]" >&2
  exit 2
fi

bead="$1"; commit="$2"; verify_file="$3"; review_file="$4"
gate_dir="${5:-}"

# ---------------------------------------------------------------------------
# Preflight: validate inputs and state (no mutation)
# ---------------------------------------------------------------------------
repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Check artifact files exist
for f in "$verify_file" "$review_file"; do
  [ -f "$f" ] || { echo "error: artifact file not found: $f" >&2; exit 1; }
done

# Check gate-dir if provided
if [ -n "$gate_dir" ]; then
  [ -d "$gate_dir" ] || { echo "error: gate-dir not found: $gate_dir" >&2; exit 1; }
fi

# Verify run ref does NOT already exist (capture-run is create-only).
# Mirror dogfood-capture's create-only guard so we fail before any side effects.
ref="refs/etude/runs/$bead"
if git rev-parse --verify --quiet "$ref" >/dev/null 2>&1; then
  echo "error: run ref $ref already exists — cannot double-capture." >&2
  echo "To re-close, first delete the existing ref:" >&2
  echo "  git update-ref -d $ref" >&2
  echo "  git push origin --delete $ref" >&2
  exit 1
fi

# Check allowlist — notice if bypassed (but still run capture/gates if gate-dir given)
allow="$repo_root/scripts/dogfood-completeness-allow.txt"
is_allowlisted=false
if [ -f "$allow" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    [ -z "${line##\#*}" ] && continue   # skip comment lines
    line_bead="${line%%#*}"
    # trim whitespace
    line_bead="$(echo "$line_bead" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
    if [ "$line_bead" = "$bead" ]; then
      is_allowlisted=true
      break
    fi
  done < "$allow"
fi

if $is_allowlisted; then
  echo "note: $bead is in the allowlist — the terminal audit will report bypass: $bead"
fi

# ---------------------------------------------------------------------------
# Step 2: Capture the run
# ---------------------------------------------------------------------------
echo ""
echo "=== Step 2: capture run ==="
bash "$DOGFOOD_CAPTURE" "$bead" "$commit" "$verify_file" "$review_file"

# ---------------------------------------------------------------------------
# Step 3: Capture gate records (lexical order)
# ---------------------------------------------------------------------------
if [ -n "$gate_dir" ]; then
  echo ""
  echo "=== Step 3: capture gate records from $gate_dir ==="
  # Sort gate files lexically; use find + sort for POSIX portability
  while IFS= read -r gate_file; do
    [ -f "$gate_file" ] || continue
    echo "  capturing gate: $gate_file"
    bash "$DOGFOOD_GATE_CAPTURE" "$bead" "$gate_file"
  done < <(find "$gate_dir" -maxdepth 1 -name '*.json' | sort)
else
  echo ""
  echo "=== Step 3: no gate-dir supplied — skipping gate capture ==="
  echo "  note: the terminal audit will fail check (b) if $bead is not allowlisted"
fi

# ---------------------------------------------------------------------------
# Step 4: Terminal gate — run the audit and propagate exit code
# ---------------------------------------------------------------------------
echo ""
echo "=== Step 4: terminal gate — dogfood-completeness-audit --bead $bead ==="
echo ""
set +e
bash "$DOGFOOD_AUDIT" --bead "$bead"
audit_exit=$?
set -e

if [ "$audit_exit" -eq 0 ]; then
  echo ""
  echo "dogfood-close: OK — bead $bead is closed and complete."
elif [ "$audit_exit" -eq 1 ]; then
  echo ""
  echo "dogfood-close: FAILED — bead $bead has hard gaps (see GAP lines above)." >&2
  echo "Resolve the gaps and re-run, or add $bead to the allowlist with a reason." >&2
else
  echo ""
  echo "dogfood-close: ERROR — audit exited $audit_exit (environment/usage error)." >&2
fi

exit "$audit_exit"
