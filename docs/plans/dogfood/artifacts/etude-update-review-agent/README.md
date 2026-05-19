# etude-update-review-agent Artifact

Status: planning capture artifact for dogfood bead
`etude-update-review-agent`.

This directory snapshots the updated `dev-pr-reviewer` shared agent instruction
file. The live source file is outside this repository at
`/Users/jv/.claude-accounts/jv/agents/dev-pr-reviewer.md`, and that directory
is not a git repository. This committed snapshot is the durable reconstruction
source for this bead.

Because the live source is unversioned, it can drift silently after this bead.
Use the snapshot here as the reconstruction source for this reviewed change.
The `cmp -s` parity check recorded during this bead is the snapshot's
provenance anchor; the live source has no independent version identifier.

Source git SHA for the etude repo when the bead started:
`f5aa32dfb51d904d467908e9f6f030662fd9851e`.

## Source File

- `/Users/jv/.claude-accounts/jv/agents/dev-pr-reviewer.md`

## Snapshot File

- `agents/dev-pr-reviewer.md`

## Refresh Rule

If the live shared agent changes again, update this snapshot only as part of a
new reviewed bead or explicitly refresh it in a bead that records the changed
source, compare command, and commit hash.
Update this README in the refreshing bead if the source SHA, provenance note,
or refresh procedure changes.

Concrete refresh commands:

```bash
# Run from the etude repo root.
cp /Users/jv/.claude-accounts/jv/agents/dev-pr-reviewer.md \
  docs/plans/dogfood/artifacts/etude-update-review-agent/agents/dev-pr-reviewer.md
cmp -s /Users/jv/.claude-accounts/jv/agents/dev-pr-reviewer.md \
  docs/plans/dogfood/artifacts/etude-update-review-agent/agents/dev-pr-reviewer.md \
  && echo "live and snapshot match" || echo "live and snapshot drift"
git add docs/plans/dogfood/artifacts/etude-update-review-agent/README.md \
  docs/plans/dogfood/artifacts/etude-update-review-agent/agents/dev-pr-reviewer.md
```
