#!/usr/bin/env sh
# Echo runner for liverun tests: reads all input files from ETUDE_INPUTS_DIR
# in sorted (NN-role) order and writes them pipe-separated to ETUDE_OUTPUT_FILE.
set -e
out=""
for f in $(ls "$ETUDE_INPUTS_DIR" 2>/dev/null | sort); do
    content=$(cat "$ETUDE_INPUTS_DIR/$f")
    if [ -n "$out" ]; then
        out="${out}|${content}"
    else
        out="$content"
    fi
done
printf '%s' "$out" > "$ETUDE_OUTPUT_FILE"
