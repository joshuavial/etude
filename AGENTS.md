# Agent Instructions

## Issue Tracking

This project uses **bd (beads)** for issue tracking with embedded Dolt storage.
Run `bd prime` for current workflow context.

Quick reference:

- `bd ready` - find unblocked work.
- `bd create "Title" --type task --priority 2` - create an issue.
- `bd show <id>` - inspect an issue.
- `bd close <id>` - close completed work.
- `bd dolt push` - push beads data to the configured remote.

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
`docs/plans/product/BRIEF.md`.
