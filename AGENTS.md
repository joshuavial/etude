# Agent Instructions

## Documentation

Treat `docs/` as user-facing documentation for behavior that has actually been
implemented.

- Do not document planned features as if they exist.
- Put future-work notes, design sketches, architecture plans, and open
  decisions under `docs/plans/`.
- If a feature moves from planned to implemented, move or rewrite the relevant
  notes into the main docs as accurate user-facing documentation.
- Keep `docs/plans/` clearly labeled as non-shipped work.
- When adding Go CLI commands, update implemented docs only after the command
  works, and keep generated command reference docs separate from hand-written
  guides.

The current product brief is planning material, so it lives at
`docs/plans/BRIEF.md`.
