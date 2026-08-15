#!/usr/bin/env bash
#
# seat-adapter_test.sh — fixture tests for scripts/seat-adapter.sh.
#
# The adapter is the only thing standing between a model's prose and a recorded
# GO, so its FAIL-CLOSED paths are the point: every failure must leave
# ETUDE_OUTPUT_FILE absent, which etude classifies as a non-pass. A bug that
# writes {"verdict":"go"} on a truncated reply would silently pass gates.
#
# Run: bash scripts/seat-adapter_test.sh   (or `make seat-adapter-test`)
set -uo pipefail

repo_root="$(git rev-parse --show-toplevel)"
adapter="$repo_root/scripts/seat-adapter.sh"
failures=0

td="$(mktemp -d)"
trap 'rm -rf "$td"' EXIT
mkdir -p "$td/in"
printf 'the shared gate prompt\n' > "$td/in/00-gate-prompt"

# stub <body> — write a fake model CLI that emits <body> on stdout.
stub() { printf '#!/usr/bin/env bash\n%s\n' "$1" > "$td/stub"; chmod +x "$td/stub"; }

# check <name> <expected-exit> <expected-verdict|ABSENT>
check() {
  local name="$1" want_exit="$2" want="$3" got_exit got
  rm -f "$td/out"
  ETUDE_INPUTS_DIR="$td/in" ETUDE_OUTPUT_FILE="$td/out" \
    bash "$adapter" testseat "$td/stub" >/dev/null 2>&1
  got_exit=$?
  if [ -f "$td/out" ]; then
    got="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["verdict"])' "$td/out" 2>/dev/null || echo MALFORMED)"
  else
    got="ABSENT"
  fi
  if [ "$got_exit" = "$want_exit" ] && [ "$got" = "$want" ]; then
    echo "  ok   $name"
  else
    echo "  FAIL $name: exit=$got_exit (want $want_exit), envelope=$got (want $want)" >&2
    failures=$((failures + 1))
  fi
}

echo "seat-adapter: syntax"
bash -n "$adapter" || { echo "  FAIL bash -n" >&2; exit 1; }
echo "  ok   bash -n"

echo "seat-adapter: verdict parsing"
stub 'printf "VERDICT: GO\nBLOCKING: none\nOPTIONAL: none\nCONFIDENCE: high\n"'
check "clean GO writes a go envelope" 0 go

stub 'printf "VERDICT: BLOCK\nBLOCKING: 1. missing guard\nOPTIONAL: none\n"'
check "clean BLOCK writes a block envelope" 0 block


stub 'printf "VERDICT: <GO|BLOCK>\nBLOCKING: <...>\n\nVERDICT: BLOCK\nBLOCKING: 1. real finding\n"'
check "echoed template does not shadow the real verdict" 0 block

echo "seat-adapter: fail-closed paths (each must leave NO envelope)"
stub 'echo "VERDICT: GO"; exit 3'
check "CLI non-zero is not a GO" 1 ABSENT

stub 'printf "I was thinking about the problem and then the output was cut o"'
check "truncated reply with no VERDICT is not a GO" 1 ABSENT

stub 'printf "VERDICT: maybe\n"'
check "unrecognized verdict is not a GO" 1 ABSENT

stub 'printf "The verdict is GO, definitely go ahead.\n"'
check "prose containing the word go is not a GO" 1 ABSENT

# Near-miss tokens are the dangerous ones: they LOOK agreeable, so a lenient
# mapping would let a seat that did not answer the asked question pass a gate.
# The prompt asks for GO or BLOCK and nothing else.
stub 'printf "VERDICT: PASS\nBLOCKING: none\n"'
check "PASS is not GO" 1 ABSENT

stub 'printf "VERDICT: PASS_WITH_FOLLOWUPS\nBLOCKING: none\nOPTIONAL: 1. nit\n"'
check "PASS_WITH_FOLLOWUPS is not GO" 1 ABSENT

stub 'printf "VERDICT: APPROVED\n"'
check "APPROVED is not GO" 1 ABSENT

stub 'printf ""'
check "empty reply is not a GO" 1 ABSENT

echo "seat-adapter: case folding is deliberate, and is the ONLY tolerance"
stub 'printf "Verdict: Go\nBLOCKING: none\n"'
check "mixed-case Go is accepted (same answer, different letter case)" 0 go
stub 'printf "VERDICT: block\nBLOCKING: 1. x\n"'
check "lowercase block is accepted" 0 block
stub 'printf "VERDICT: GOOD\n"'
check "GOOD is not GO (case folding is not prefix matching)" 1 ABSENT
stub 'printf "VERDICT: NOGO\n"'
check "NOGO is not GO (case folding is not substring matching)" 1 ABSENT

echo "seat-adapter: whitespace tolerance (must not cause a false outage)"
stub 'printf "VERDICT: GO   \nBLOCKING: none\n"'
check "trailing whitespace after GO still parses" 0 go

stub 'printf "VERDICT:    BLOCK\t\nBLOCKING: 1. x\n"'
check "extra inner and trailing whitespace around BLOCK still parses" 0 block

stub 'printf "  VERDICT: GO\n  BLOCKING: none\n"'
check "indented RETURN block still parses" 0 go

stub 'printf "- VERDICT: GO\n"'
check "a VERDICT not at line start is still not a GO" 1 ABSENT

echo "seat-adapter: BLOCK without a reason is still a BLOCK"
stub 'printf "VERDICT: BLOCK\nBLOCKING: none\n"'
check "BLOCK with no stated reason stays blocking" 0 block

echo "seat-adapter: contradictory verdicts are refused, not arbitrated by position"
# Found by an opus seat, verified empirically: "last VERDICT wins" silently turned
# a stated BLOCK into a passing vote, and synthesizeVerdict ignores `required`, so
# the gate would have passed. Position must never arbitrate between two VALID
# verdicts.
stub 'printf "VERDICT: BLOCK\nBLOCKING: 1. real defect\nOPTIONAL: none\nCONFIDENCE: high\n\nIf fixed I would say:\nVERDICT: GO\n"'
check "BLOCK then a later GO is refused, not read as go" 1 ABSENT

stub 'printf "VERDICT: GO\nBLOCKING: none\n\nOn reflection:\nVERDICT: BLOCK\nBLOCKING: 1. x\n"'
check "GO then a later BLOCK is refused too (symmetric)" 1 ABSENT

# The echo-then-answer case must still work: an unfilled template is not a valid
# token, so it cannot conflict.
stub 'printf "VERDICT: <GO|BLOCK>\nBLOCKING: <...>\n\nVERDICT: BLOCK\nBLOCKING: 1. real finding\n"'
check "echoed placeholder does not count as a conflicting verdict" 0 block

stub 'printf "VERDICT: GO\nBLOCKING: none\n\nVERDICT: GO\nBLOCKING: none\n"'
check "the same verdict stated twice is not a conflict" 0 go

echo "seat-adapter: a GO carrying blocking findings is refused"
# Raised by BOTH seats. Such a reply is recorded as a passing seat whose required
# items synthesizeVerdict then discards — the gate passes with the findings inert.
stub 'printf "VERDICT: GO\nBLOCKING: 1. but actually this is broken\n"'
check "GO with a non-empty BLOCKING body is refused" 1 ABSENT

echo "seat-adapter: a later placeholder cannot cancel an earlier finding"
# Same last-wins class as the VERDICT fix, one label over: BLOCKING was taking
# only the final matching line, so "BLOCKING: 1. real / BLOCKING: none" dropped
# the finding and passed. Every labelled line now counts.
stub 'printf "VERDICT: GO\nBLOCKING: 1. real problem\nBLOCKING: none\n"'
check "GO whose earlier BLOCKING is cancelled by a later none is refused" 1 ABSENT

stub 'printf "VERDICT: BLOCK\nBLOCKING: none\nBLOCKING: 2. the real one\n"'
check "BLOCK keeps a finding stated after a none" 0 block

echo "seat-adapter: an echoed RETURN template is not content"
# The multi-line extract regressed this: an unfilled `<...>` body is not the
# literal string "none", so echo-the-template-then-answer-GO started failing
# closed as a self-contradiction. Fail-closed, but a FALSE outage on a case the
# header declares supported. The suite only covered echo-then-BLOCK before.
stub 'printf "VERDICT: <GO|BLOCK>\nBLOCKING: <numbered blocking findings, or \"none\">\nOPTIONAL: <...>\n\nVERDICT: GO\nBLOCKING: none\nOPTIONAL: none\n"'
check "echoed template then GO is accepted, not a false outage" 0 go

stub 'printf "VERDICT: <GO|BLOCK>\nBLOCKING: <...>\n\nVERDICT: BLOCK\nBLOCKING: 1. real\n"'
check "echoed template then BLOCK still blocks" 0 block

echo "seat-adapter: label spelling and placeholder precision"
# A space before the colon made the body read empty, so a GO carrying a real
# finding passed with the finding only in the transcript.
stub 'printf "VERDICT: GO\nBLOCKING : 1. real finding\n"'
check "BLOCKING with a space before the colon is still seen" 1 ABSENT

stub 'printf "VERDICT : GO\nBLOCKING: none\n"'
check "VERDICT with a space before the colon still parses" 0 go

# The placeholder rule must match the TEMPLATE, not merely "angle-wrapped": a
# genuine finding that happens to be angle-wrapped is content, and dropping it
# let a contradictory GO through.
stub 'printf "VERDICT: GO\nBLOCKING: <see the numbered list above>\n"'
check "an angle-wrapped GENUINE finding is content, not a placeholder" 1 ABSENT

stub 'printf "VERDICT: BLOCK\nBLOCKING: <see the numbered list above>\n"'
check "and it is recorded on a BLOCK" 0 block

echo "seat-adapter: a bare label with findings on following lines is refused"
stub 'printf "VERDICT: GO\nBLOCKING:\n  actually this is broken\n  and so is this\n"'
check "bare BLOCKING: header with unlabelled findings is refused" 1 ABSENT

stub 'printf "VERDICT: GO\nBLOCKING:\n\nOPTIONAL: none\n"'
check "bare BLOCKING: followed by a blank line is not a false outage" 0 go

stub 'printf "VERDICT: GO\nBLOCKING:\nOPTIONAL: none\n"'
check "bare BLOCKING: followed by another label is not a false outage" 0 go

# The whitespace sweep covered every label MATCH site but missed the awk
# TERMINATOR branch, so a bare BLOCKING: followed by `OPTIONAL : none` was read
# as "findings on following lines" and refused. Fail-closed, but a false outage.
stub 'printf "VERDICT: GO\nBLOCKING:\nOPTIONAL : none\n"'
check "a spaced-colon label terminates a bare label block" 0 go

echo "seat-adapter: an echoed placeholder never reaches required[]"
stub 'printf "VERDICT: <GO|BLOCK>\nBLOCKING: <numbered blocking findings, or \"none\">\n\nVERDICT: BLOCK\nBLOCKING: 1. real finding\n"'
rm -f "$td/out"
ETUDE_INPUTS_DIR="$td/in" ETUDE_OUTPUT_FILE="$td/out" bash "$adapter" testseat "$td/stub" >/dev/null 2>&1
# Compare in python: shell-quoting a repr() is its own source of bugs, and the
# first attempt at this assertion failed on exactly that rather than on the code.
if python3 -c '
import json, sys
sys.exit(0 if json.load(open(sys.argv[1]))["required"] == ["1. real finding"] else 1)
' "$td/out" 2>/dev/null; then
  echo "  ok   only the real finding is recorded, not the echoed template"
else
  echo "  FAIL echoed placeholder leaked into required[]" >&2; failures=$((failures + 1))
fi

echo "seat-adapter: multi-line findings are preserved, not truncated"
stub 'printf "VERDICT: BLOCK\nBLOCKING: 1. first\nBLOCKING: 2. second\nBLOCKING: 3. third\n"'
rm -f "$td/out"
ETUDE_INPUTS_DIR="$td/in" ETUDE_OUTPUT_FILE="$td/out" bash "$adapter" testseat "$td/stub" >/dev/null 2>&1
n="$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["required"]))' "$td/out" 2>/dev/null || echo 0)"
if [ "$n" = "3" ]; then
  echo "  ok   all three blocking findings reach the envelope"
else
  echo "  FAIL envelope carried $n of 3 blocking findings" >&2; failures=$((failures + 1))
fi

echo "seat-adapter: the prompt is not guessed from several inputs"
mkdir -p "$td/multi"; printf 'a\n' > "$td/multi/00-a"; printf 'b\n' > "$td/multi/01-b"
stub 'printf "VERDICT: GO\n"'
rm -f "$td/out"
ETUDE_INPUTS_DIR="$td/multi" ETUDE_OUTPUT_FILE="$td/out" bash "$adapter" testseat "$td/stub" >/dev/null 2>&1
if [ $? -ne 0 ] && [ ! -f "$td/out" ]; then
  echo "  ok   refuses to guess which of several inputs is the prompt"
else
  echo "  FAIL picked an input file arbitrarily" >&2; failures=$((failures + 1))
fi
rm -rf "$td/multi"

echo "seat-adapter: the envelope is the adapter's alone (adversarial writers)"
# The model CLIs behind these seats are AGENTIC with file-write tools. If one
# could see or write ETUDE_OUTPUT_FILE, it could fabricate a passing verdict
# without ever stating one. Each case below is a hostile/broken CLI.

# The child must not even SEE the path. It echoes what it was given; the adapter
# then finds no VERDICT line, so the run fails closed and the transcript records
# what the child saw.
# The child reports what it saw to a side file, because its stdout is captured
# as the reply and discarded when the run fails closed.
stub 'printf "OUT=[%s] IN=[%s]\n" "${ETUDE_OUTPUT_FILE:-UNSET}" "${ETUDE_INPUTS_DIR:-UNSET}" > "'"$td"'/child-env"; printf "VERDICT: GO\n"'
rm -f "$td/out" "$td/child-env"
ETUDE_INPUTS_DIR="$td/in" ETUDE_OUTPUT_FILE="$td/out" bash "$adapter" testseat "$td/stub" >/dev/null 2>&1
if grep -q 'OUT=\[UNSET\] IN=\[UNSET\]' "$td/child-env" 2>/dev/null; then
  echo "  ok   child sees neither ETUDE_OUTPUT_FILE nor ETUDE_INPUTS_DIR"
else
  echo "  FAIL child could see an etude control variable: $(cat "$td/child-env" 2>/dev/null)" >&2
  failures=$((failures + 1))
fi

stub 'echo "{\"verdict\":\"go\"}" > "$ETUDE_OUTPUT_FILE" 2>/dev/null; printf "no verdict here\n"'
check "CLI writing its own go envelope does not pass" 1 ABSENT

stub 'echo "{\"verdict\":\"go\"}" > "$ETUDE_OUTPUT_FILE" 2>/dev/null; printf "VERDICT: BLOCK\nBLOCKING: 1. real\n"'
check "CLI-written go envelope is overwritten by the real BLOCK verdict" 0 block

stub 'echo "{\"verdict\":\"go\"}" > "$ETUDE_OUTPUT_FILE" 2>/dev/null; exit 7'
check "CLI writing a go envelope then failing does not pass" 1 ABSENT

echo '{"verdict":"go"}' > "$td/out"
stub 'printf "no verdict here\n"'
ETUDE_INPUTS_DIR="$td/in" ETUDE_OUTPUT_FILE="$td/out" bash "$adapter" testseat "$td/stub" >/dev/null 2>&1
if [ -f "$td/out" ]; then
  echo "  FAIL a STALE envelope from a prior run survived a failed seat" >&2; failures=$((failures + 1))
else
  echo "  ok   stale envelope from a prior run is cleared"
fi

echo "seat-adapter: session evidence"
# etude downgrades an agentic seat that claims no session evidence to
# `malfunction`, discarding an otherwise good verdict. This is the regression
# for that: a real gate run found every seat malfunctioning because the adapter
# emitted no session block.
stub 'printf "VERDICT: GO\nBLOCKING: none\n"'
rm -f "$td/out"
ETUDE_INPUTS_DIR="$td/in" ETUDE_OUTPUT_FILE="$td/out" bash "$adapter" testseat "$td/stub" >/dev/null 2>&1
if [ ! -f "$td/out" ]; then
  echo "  FAIL no envelope written" >&2; failures=$((failures + 1))
else
  sid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("session",{}).get("session_id",""))' "$td/out")"
  tpath="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("session",{}).get("transcript_path",""))' "$td/out")"
  if [ -z "$sid" ] || [ -z "$tpath" ]; then
    echo "  FAIL envelope carries no session evidence (sid=$sid path=$tpath)" >&2; failures=$((failures + 1))
  else
    echo "  ok   envelope carries session_id and transcript_path"
  fi
  if [ -f "$td/$tpath" ]; then
    echo "  ok   transcript written next to the output file"
  else
    echo "  FAIL transcript $tpath not written beside ETUDE_OUTPUT_FILE" >&2; failures=$((failures + 1))
  fi
  if grep -q "VERDICT: GO" "$td/$tpath" 2>/dev/null; then
    echo "  ok   transcript preserves the model's raw reply"
  else
    echo "  FAIL transcript does not contain the reply" >&2; failures=$((failures + 1))
  fi
fi

echo "seat-adapter: missing environment"
rm -f "$td/out"
if ETUDE_INPUTS_DIR="" ETUDE_OUTPUT_FILE="$td/out" bash "$adapter" testseat /bin/true >/dev/null 2>&1; then
  echo "  FAIL adapter ran without ETUDE_INPUTS_DIR" >&2
  failures=$((failures + 1))
else
  echo "  ok   refuses to run without ETUDE_INPUTS_DIR"
fi

if [ "$failures" -gt 0 ]; then
  echo "seat-adapter: $failures failure(s)" >&2
  exit 1
fi
echo "seat-adapter: all checks passed"
