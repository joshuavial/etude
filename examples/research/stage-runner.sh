#!/bin/sh
# stage-runner.sh — deterministic stage runner for the research example.
#
# Concatenates all inputs from ETUDE_INPUTS_DIR (sorted by filename) into
# ETUDE_OUTPUT_FILE.  Input filenames are <NN>-<role> so sorting by name
# gives a stable, dependency-order concatenation.
#
# This script is intentionally simple: no LLM, no network, no randomness.
# Output is byte-stable across repeated runs with the same inputs, which
# makes forward replay deterministic.
#
# Environment (set by etude):
#   ETUDE_INPUTS_DIR   — directory containing one file per stage input
#   ETUDE_OUTPUT_FILE  — file to write stage output to

: > "$ETUDE_OUTPUT_FILE"
for f in $(ls "$ETUDE_INPUTS_DIR" | sort); do
    cat "$ETUDE_INPUTS_DIR/$f" >> "$ETUDE_OUTPUT_FILE"
done
