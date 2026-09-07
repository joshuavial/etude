# Agent Instructions

## Skills

Agent skills must be symlinked so `.claude/skills` and `.agent/skills` resolve to
the same directory, in user space (`~`) and in this project. `.claude/skills`
holds the real skills; `.agent/skills` is a symlink to it. Ensure this link
exists when adding/removing skills or setting up the repo.

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

## Development session boot

After `bd prime`, read `docs/development.md`, the current contract for
`dev-claude` and `dev-codex`. Read relevant technical docs before planning;
ship accurate technical documentation alongside code. Historical dogfood plans
and generic loop skills do not override this repo's current proportional review
and independent QA policy. In worktrees, verify access to the canonical beads
store before claiming work.
