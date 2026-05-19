# etude-overhaul-dev-workflow-skill Artifact

Status: capture artifact for dogfood bead `etude-overhaul-dev-workflow-skill`.

This directory snapshots the final contents of the external dev-workflow skill
and related agent instruction files changed for the bead. The live files are
under `/Users/jv/.claude-accounts/jv`, which is not a git repository, so this
artifact is the committed reconstruction source for the dogfood run.

## Source Files

- `/Users/jv/.claude-accounts/jv/skills/dev-workflow/SKILL.md`
- `/Users/jv/.claude-accounts/jv/skills/dev-workflow/reference/phases.md`
- `/Users/jv/.claude-accounts/jv/skills/dev-workflow/templates/dev-notes.md`
- `/Users/jv/.claude-accounts/jv/skills/dev-workflow/reference/auto-mode.md`
- `/Users/jv/.claude-accounts/jv/skills/dev-workflow/reference/approval-prompts.md`
- `/Users/jv/.claude-accounts/jv/skills/dev-workflow/reference/quality-standards.md`
- `/Users/jv/.claude-accounts/jv/agents/dev-planner.md`
- `/Users/jv/.claude-accounts/jv/agents/dev-executor.md`
- `/Users/jv/.claude-accounts/jv/agents/dev-test-writer.md`
- `/Users/jv/.claude-accounts/jv/agents/dev-qa.md`
- `/Users/jv/.claude-accounts/jv/agents/dev-docs-writer.md`
- `/Users/jv/.claude-accounts/jv/agents/dev-pr-reviewer.md`

## Snapshot Layout

- `skills/dev-workflow/` mirrors the changed dev-workflow skill files.
- `agents/` mirrors the changed agent instruction files.
