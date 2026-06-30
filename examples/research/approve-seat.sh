#!/bin/sh
# approve-seat.sh — deterministic stub gate seat for the research example.
#
# Always votes "go".  Writes the JSON verdict envelope to ETUDE_OUTPUT_FILE
# exactly as a real model seat would.
#
# In a production workflow this script would be replaced by a real model
# invocation (e.g. claude -p --model opus).  The registry wires the
# invocation command; the engine does not care whether the runner is a stub
# or a real LLM — only the JSON envelope matters.
#
# Environment (set by etude):
#   ETUDE_OUTPUT_FILE  — file to write the JSON verdict envelope to

printf '{"verdict":"go"}' > "$ETUDE_OUTPUT_FILE"
