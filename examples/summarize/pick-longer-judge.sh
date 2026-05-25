#!/usr/bin/env bash
# pick-longer-judge.sh — content-aware pairwise judge for the etude example.
#
# Judge I/O contract (bench pairwise):
#   ETUDE_INPUTS_DIR  — directory containing exactly two target files:
#                         00-target-left   (presented first)
#                         01-target-right  (presented second)
#                       Presentation order is randomised per pair by etude to
#                       reduce position bias; the judge must decide by content.
#   ETUDE_OUTPUT_FILE — path the judge must write its JSON verdict to:
#                         {"winner":"A"}  if the LEFT target is better
#                         {"winner":"B"}  if the RIGHT target is better
#                         {"winner":"tie"} if equal
#                       etude maps position-relative (left=A, right=B) back to
#                       canonical A/B after applying the presentation swap.
#
# Decision rule: longer content (more bytes) wins.
# Equal lengths => tie.
# This is deterministic and swap-safe: the longer content always maps to the
# same canonical winner regardless of which position it occupies.
set -euo pipefail

left_file=$(ls "$ETUDE_INPUTS_DIR"/00-target-* 2>/dev/null | head -1)
right_file=$(ls "$ETUDE_INPUTS_DIR"/01-target-* 2>/dev/null | head -1)

if [ -z "$left_file" ] || [ -z "$right_file" ]; then
  echo "pick-longer-judge: expected 00-target-* and 01-target-* in $ETUDE_INPUTS_DIR" >&2
  exit 1
fi

left_size=$(wc -c < "$left_file" | tr -d ' ')
right_size=$(wc -c < "$right_file" | tr -d ' ')

if [ "$left_size" -gt "$right_size" ]; then
  winner="A"
elif [ "$right_size" -gt "$left_size" ]; then
  winner="B"
else
  winner="tie"
fi

printf '{"winner":"%s"}\n' "$winner" > "$ETUDE_OUTPUT_FILE"
