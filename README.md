# etude

> Empirically test AI coding-agent skills by replaying them against your real past work.

## What it is

Improving the skills that drive a coding agent — planning, implementation, test
design, code review, docs — is mostly guesswork. You edit a skill, it *feels*
better, and you ship it. There's no signal that an upgraded planning skill
actually produces better plans.

`etude` makes it empirical. It captures the artifacts produced at each stage of
a development workflow as immutable, git-native records, then lets you **replay**
any stage with a different skill version and **evaluate** whether the result
improved — so a skill change comes with a number, not a vibe.

## How it works

- **Capture** — record each workflow stage's input and output as an immutable,
  content-addressed artifact under a `refs/etude/*` git namespace.
- **Replay** — re-run one stage of past work with a new skill version, against
  its original recorded inputs.
- **Bench** — replay and evaluate a stage across your last N real PRs, and
  report a win rate for the skill change.

## Status

Design brief — nothing is built yet. The full design is in
[`docs/plans/BRIEF.md`](docs/plans/BRIEF.md).

Planning notes for components that do not exist yet live under
[`docs/plans/`](docs/plans/).
