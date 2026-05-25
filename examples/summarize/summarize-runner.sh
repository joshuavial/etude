#!/usr/bin/env bash
# summarize-runner.sh — deterministic document summarizer for the etude example.
#
# Runner I/O contract:
#   ETUDE_INPUTS_DIR  — directory containing <NN>-<role> input files.
#                       For the summarize stage the input role is "doc", so the
#                       file is named 00-doc.
#   ETUDE_OUTPUT_FILE — path the runner must write its output to.
#
# Output is a pure function of the input content: first line of the document
# followed by a word count.  No timestamps, randomness, or network calls.
set -euo pipefail

doc_file=$(ls "$ETUDE_INPUTS_DIR"/*-doc 2>/dev/null | head -1)
if [ -z "$doc_file" ]; then
  echo "summarize-runner: no *-doc file found in $ETUDE_INPUTS_DIR" >&2
  exit 1
fi

first_line=$(head -1 "$doc_file")
word_count=$(wc -w < "$doc_file" | tr -d ' ')

printf '%s\n[%s words]\n' "$first_line" "$word_count" > "$ETUDE_OUTPUT_FILE"
