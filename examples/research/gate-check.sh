#!/bin/sh
# gate-check.sh — deterministic gate check for the research example.
#
# Exit 0 (pass) when the reviewed artifact input is non-empty.
# Exit 1 (block) when all inputs are empty.
#
# The review gate is designed to always pass: stage-runner.sh always
# produces non-empty output when given non-empty inputs.
#
# Environment (set by etude):
#   ETUDE_INPUTS_DIR   — directory containing the gate's input artifact(s)

for f in "$ETUDE_INPUTS_DIR"/*; do
    [ -f "$f" ] || continue
    [ -s "$f" ] && exit 0
done
exit 1
