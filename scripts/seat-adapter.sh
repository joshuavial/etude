#!/usr/bin/env bash
#
# seat-adapter.sh — turn a model CLI into a conformant etude gate seat.
#
# Usage (from a registry seat's `invoke`):
#   scripts/seat-adapter.sh <seat-name> <command> [args...]
#
# etude runs a seat as a subprocess with a deliberately small environment and
# reads its verdict from a file:
#   ETUDE_INPUTS_DIR   — the shared gate prompt is at $ETUDE_INPUTS_DIR/00-gate-prompt
#   ETUDE_OUTPUT_FILE  — where this script must write the JSON verdict envelope
#
# A bare model CLI writes prose to stdout and nothing to ETUDE_OUTPUT_FILE, so
# etude would classify it `empty` and every gate would escalate. This adapter is
# the bridge: it feeds the prompt to the CLI on stdin, parses the four-line
# RETURN block off stdout, and writes the envelope.
#
# FAIL CLOSED. The adapter never synthesizes a verdict and never defaults to
# "go". Every failure path leaves ETUDE_OUTPUT_FILE ABSENT and exits non-zero,
# which etude already classifies as a non-pass — so the fail-closed behavior is
# inherited from the engine rather than reimplemented here.
#
# THE ENVELOPE IS THIS SCRIPT'S ALONE. The model CLIs behind these seats are
# AGENTIC and have file-write tools, and they would otherwise inherit
# ETUDE_OUTPUT_FILE in their environment — a reviewer could then write its own
# `{"verdict":"go"}` and the gate would read it as a pass without any verdict
# having been parsed. So the child is invoked with ETUDE_OUTPUT_FILE and
# ETUDE_INPUTS_DIR REMOVED from its environment (it gets the prompt on stdin and
# needs neither), the file is cleared before the run, and an EXIT trap deletes it
# on every path except a write this script performed itself. Failure modes
# covered:
#
#   1. CLI exits non-zero              -> no envelope, exit 1   (etude: failed)
#   2. no VERDICT: line in the reply   -> no envelope, exit 1   (etude: empty)
#      (this is the truncation case; it must never read as a silent GO)
#   3. VERDICT: is not exactly GO/BLOCK-> no envelope, exit 1
#   4. VERDICT: BLOCK with no reason   -> block, with a placeholder reason
#   4b. two VERDICT lines disagreeing  -> no envelope, exit 1
#   4c. VERDICT: GO with blocking text -> no envelope, exit 1
#       (every BLOCKING line counts, so a later "none" cannot cancel a finding)
#   5. child writes the envelope itself -> removed by the trap (it cannot even
#                                          see the path)
#   6. stale envelope from a prior run  -> cleared before the CLI is invoked
#   7. partial/failed envelope write    -> removed; absent beats malformed
#
# The ONLY path that writes {"verdict":"go"} is a reply whose last VERDICT: line
# holds the token GO, compared case-insensitively after trimming surrounding
# whitespace. Case folding is deliberate: a model that answers `Verdict: Go` has
# unambiguously said GO, and rejecting it would escalate a gate over letter case.
# It is a fold of exactly two accepted tokens, NOT a fuzzy match — no substring
# search, no synonyms, and nothing else is accepted.
set -uo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <seat-name> <command> [args...]" >&2
  exit 2
fi

seat="$1"; shift

: "${ETUDE_INPUTS_DIR:?seat adapter requires ETUDE_INPUTS_DIR (set by etude)}"
: "${ETUDE_OUTPUT_FILE:?seat adapter requires ETUDE_OUTPUT_FILE (set by etude)}"

# The role prefix is etude's business and may change, so a single input file with
# a different name is accepted. But silently picking one of SEVERAL is not: that
# would review something other than the gate prompt and report a confident
# verdict about it.
prompt="$ETUDE_INPUTS_DIR/00-gate-prompt"
if [ ! -f "$prompt" ]; then
  input_count="$(find "$ETUDE_INPUTS_DIR" -maxdepth 1 -type f | wc -l | tr -d ' ')"
  if [ "$input_count" != "1" ]; then
    echo "seat $seat: no 00-gate-prompt in $ETUDE_INPUTS_DIR and $input_count other inputs; refusing to guess which is the prompt" >&2
    exit 1
  fi
  prompt="$(find "$ETUDE_INPUTS_DIR" -maxdepth 1 -type f)"
fi
if [ -z "${prompt:-}" ] || [ ! -f "$prompt" ]; then
  echo "seat $seat: no gate prompt found in $ETUDE_INPUTS_DIR" >&2
  exit 1
fi

# Capture stdout and stderr separately: only stdout carries the verdict, and a
# CLI that chatters on stderr must not corrupt the parse.
reply_file="$(mktemp)"
err_file="$(mktemp)"

# envelope_written flips to 1 only after THIS script writes a valid envelope.
# Until then the trap removes the file on any exit, so no other writer — the
# model CLI, a previous run, a partial write — can leave one behind.
envelope_written=0
cleanup() {
  rm -f "$reply_file" "$err_file"
  if [ "$envelope_written" -ne 1 ]; then
    rm -f "$ETUDE_OUTPUT_FILE"
  fi
}
trap cleanup EXIT INT TERM

# Clear any pre-existing envelope before the CLI runs, and CONFIRM it is gone —
# an unwritable scratch dir is the one place a stale envelope could survive and
# be mistaken for this run's verdict.
rm -f "$ETUDE_OUTPUT_FILE"
if [ -e "$ETUDE_OUTPUT_FILE" ]; then
  echo "seat $seat: could not clear a pre-existing envelope at $ETUDE_OUTPUT_FILE" >&2
  exit 1
fi

# Strip the etude control variables from the child's environment. An agentic
# reviewer must not be able to see, let alone write, the file its own verdict is
# read from.
if ! env -u ETUDE_OUTPUT_FILE -u ETUDE_INPUTS_DIR "$@" < "$prompt" > "$reply_file" 2> "$err_file"; then
  echo "seat $seat: model CLI exited non-zero" >&2
  tail -5 "$err_file" >&2
  exit 1
fi

# Last VERDICT: line wins — models often echo the prompt's RETURN template
# BEFORE emitting their own answer, and the real verdict is the final one. That
# ordering is the supported shape: echo-then-answer. The reverse — answer, then a
# trailing echoed `VERDICT: <GO|BLOCK>` — is REFUSED, because the final VERDICT
# line must itself be a valid token. That is deliberate: resolving instead via
# "the single distinct valid token" would let `VERDICT: GO ... VERDICT: BLOCK`
# write a go.
#
# But "last wins" is only safe while the earlier lines are NOT themselves valid
# verdicts. A reply that states "VERDICT: BLOCK ... If fixed I would say: VERDICT:
# GO" has two valid, CONTRADICTORY tokens, and taking the last one silently turns
# a stated BLOCK into a passing vote. Nothing downstream compensates:
# synthesizeVerdict counts the token and ignores `required` entirely. So a
# conflict is refused outright — the same standard applied to every other
# ambiguity here, since refusing costs a re-roll and guessing costs a gate.
# Allow leading whitespace: a model that indents its RETURN block (inside a list
# item, or a fenced block) is still answering. Rejecting that is fail-closed but
# escalates a gate over formatting, which is a false outage nobody can act on.
verdict_lines="$(grep -ai '^[[:space:]]*VERDICT[[:space:]]*:' "$reply_file")"
if [ -z "$verdict_lines" ]; then
  echo "seat $seat: no VERDICT: line in reply (truncated or off-format); refusing to guess" >&2
  exit 1
fi

# Normalize every VERDICT line to its token, keep only the VALID ones, and refuse
# if they disagree. An echoed "<GO|BLOCK>" template is not a valid token, so the
# common echo-then-answer case is unaffected.
normalize_verdict() { # reads a VERDICT line on stdin
  sed 's/^[[:space:]]*[Vv][Ee][Rr][Dd][Ii][Cc][Tt][[:space:]]*:[[:space:]]*//; s/[[:space:]]*$//' \
    | tr -d '\r' | tr '[:lower:]' '[:upper:]'
}
distinct_valid="$(printf '%s\n' "$verdict_lines" | normalize_verdict \
  | grep -xE 'GO|BLOCK' | sort -u | tr '\n' ' ' | sed 's/ *$//')"
case "$distinct_valid" in
  *" "*)
    echo "seat $seat: reply contains contradictory verdicts ($distinct_valid); refusing to guess" >&2
    exit 1
    ;;
esac

verdict_line="$(printf '%s\n' "$verdict_lines" | tail -1)"

# Strip leading AND trailing whitespace: a model that pads its output with a
# trailing space would otherwise fall to the wildcard below. That is fail-CLOSED
# (no envelope, seat recorded empty) rather than fail-open, but it escalates a
# gate for a formatting artifact, which is a false outage nobody can act on.
verdict_raw="$(printf '%s' "$verdict_line" | normalize_verdict)"
# GO and BLOCK only, case-insensitively (the token was upper-cased above). The
# gate prompt asks for exactly those two tokens, so any other word is a seat that
# did not answer the question asked — including near-misses like PASS or
# PASS_WITH_FOLLOWUPS, which look agreeable and are precisely where a lenient
# mapping would let an unclear answer through as a pass. Refusing them costs a
# re-roll; accepting them costs a gate.
#
# Case folding is the ONLY tolerance here, and it is deliberate: `Go` and `go`
# are the same answer, whereas `PASS` is a different word. Tolerating case
# prevents a false outage; tolerating synonyms would prevent a real block.
case "$verdict_raw" in
  GO)    verdict="go" ;;
  BLOCK) verdict="block" ;;
  *)
    echo "seat $seat: verdict '$verdict_raw' is not GO or BLOCK; refusing to guess" >&2
    exit 1
    ;;
esac

# Collect EVERY line carrying this label, not just the last one, emitting one
# item per line. "Last wins" is wrong here for the same reason it was wrong for
# VERDICT: a seat that writes
#     BLOCKING: 1. real problem
#     BLOCKING: none
# would have its finding silently dropped and the gate would pass. Taking every
# line also stops a multi-line list being truncated to its final entry.
# Placeholder bodies (empty, "none", "n/a") are dropped individually, so a real
# finding cannot be cancelled by a later "none".
extract() { # <LABEL>  -> zero or more items, one per line
  grep -ai "^[[:space:]]*$1[[:space:]]*:" "$reply_file" \
    | sed "s/^[[:space:]]*[^:]*:[[:space:]]*//" \
    | tr -d '\r' \
    | while IFS= read -r body; do
        normalized="$(printf '%s' "$body" | tr '[:upper:]' '[:lower:]' | sed 's/[[:space:]]*$//')"
        case "$normalized" in
          ""|none|n/a) continue ;;
          # An UNFILLED template body. Models routinely echo the RETURN block
          # before answering, which the header declares supported; counting the
          # echo as content turned "echo the template, then answer GO" into a
          # false outage.
          #
          # This matches the ACTUAL template vocabulary, not merely "wrapped in
          # angle brackets": a body-shaped rule silently dropped a genuine
          # finding that happened to be angle-wrapped, e.g. `<see the numbered
          # list above>`. The template's bodies are alternations (`<GO|BLOCK>`,
          # `<high|medium|low>`), the findings prompts ending in `or "none"`, or
          # a bare `<...>`.
          #
          # KNOWN RESIDUE: the alternation arm is the one remaining body-shaped
          # heuristic, so a genuine finding whose WHOLE body is angle-wrapped AND
          # contains a pipe (`<a|b is wrong>`) is still dropped. The arm cannot
          # simply go — `<GO|BLOCK>` and `<high|medium|low>` are real template
          # bodies. Narrower than before (it now needs wrapping AND a pipe,
          # rather than wrapping alone) and tracked rather than hidden.
          "<"*"|"*">"|"<...>"|"<"*'or "none"'*">") continue ;;
        esac
        printf '%s\n' "$body"
      done
}

blocking="$(extract BLOCKING)"
optional="$(extract OPTIONAL)"

# A BLOCK with no stated reason is still a BLOCK — a blocking verdict with an
# empty reason is a bad review, not a pass.
if [ "$verdict" = "block" ] && [ -z "$blocking" ]; then
  blocking="seat returned BLOCK without a stated reason"
fi

# A bare "BLOCKING:" header with the findings on following unlabelled lines is a
# formatting the label parser cannot read: the body is empty, so the findings are
# invisible to the GO-with-findings check below and a stated problem would pass.
# Refuse rather than guess where such a list ends.
bare_label_with_body() { # <LABEL>
  awk -v label="$1" '
    BEGIN { IGNORECASE = 1; pending = 0 }
    {
      line = $0
      sub(/\r$/, "", line)
      if (pending) {
        if (line ~ /^[[:space:]]*$/) { pending = 0 }
        else if (line ~ /^[[:space:]]*[A-Za-z_]+[[:space:]]*:/) { pending = 0 }
        else { found = 1; exit }
      }
      if (tolower(line) ~ "^[[:space:]]*" tolower(label) "[[:space:]]*:[[:space:]]*$") { pending = 1 }
    }
    END { exit(found ? 0 : 1) }
  ' "$reply_file"
}
if bare_label_with_body BLOCKING; then
  echo "seat $seat: a bare BLOCKING: header with findings on following lines cannot be parsed; refusing to guess" >&2
  exit 1
fi

# A GO carrying blocking findings is self-contradictory. It would be recorded as
# a passing seat whose `required` items synthesizeVerdict then discards, so the
# gate passes with the findings inert on the record. Refuse it.
if [ "$verdict" = "go" ] && [ -n "$blocking" ]; then
  echo "seat $seat: GO with a non-empty BLOCKING body is self-contradictory; refusing to guess" >&2
  exit 1
fi

# Session evidence. etude requires it for any agentic (non-deterministic, non-
# shell) seat and downgrades a seat that claims none to `malfunction` — so a seat
# with a perfectly good verdict is discarded unless the raw reply is preserved
# alongside it. Write the reply into the seat's own scratch dir (the directory
# etude gave us for ETUDE_OUTPUT_FILE) and reference it by basename; etude
# resolves a relative transcript path against that scratch dir first.
seat_scratch="$(dirname "$ETUDE_OUTPUT_FILE")"
transcript_name="${seat}-transcript.txt"
if ! cp "$reply_file" "$seat_scratch/$transcript_name" 2>/dev/null; then
  echo "seat $seat: could not write transcript to $seat_scratch" >&2
  exit 1
fi

# A content hash makes the session id deterministic for a given reply, so the
# same review is identifiable without inventing a random token.
session_id="$seat-$(shasum -a 256 "$reply_file" | cut -c1-16)"

VERDICT="$verdict" BLOCKING="$blocking" OPTIONAL="$optional" \
SESSION_ID="$session_id" TRANSCRIPT="$transcript_name" \
  python3 -c '
import json, os, sys
required = [l for l in os.environ["BLOCKING"].split("\n") if l.strip()]
optional = [l for l in os.environ["OPTIONAL"].split("\n") if l.strip()]
json.dump({
    "verdict": os.environ["VERDICT"],
    "required": required,
    "optional": optional,
    "session": {
        "session_id": os.environ["SESSION_ID"],
        "transcript_path": os.environ["TRANSCRIPT"],
    },
}, open(sys.argv[1], "w"))
' "$ETUDE_OUTPUT_FILE" || {
  # Never leave a partial envelope behind: a truncated JSON file is classified
  # `malfunction`, but an absent one is the honest signal. The trap removes it.
  echo "seat $seat: failed to write verdict envelope" >&2
  exit 1
}

# Only now is the envelope ours and complete; stop the trap from removing it.
envelope_written=1
