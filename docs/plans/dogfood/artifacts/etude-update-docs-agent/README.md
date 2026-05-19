# etude-update-docs-agent Artifact

Status: planning capture artifact for dogfood bead
`etude-update-docs-agent`.

This directory snapshots the updated `dev-docs-writer` shared agent instruction
file. The live source file is outside this repository at
`/Users/jv/.claude-accounts/jv/agents/dev-docs-writer.md`, and that directory
is not a git repository. This committed snapshot is the durable reconstruction
source for this bead.

Because the live source is unversioned, it can drift silently after this bead.
Use the snapshot here as the reconstruction source for this reviewed change.
The `cmp -s` parity check recorded during this bead is the snapshot's
provenance anchor; the live source has no independent version identifier.

Source git SHA for the etude repo when the bead started:
`a22cc44143479e20c3e579682b1ee77694f06fdd`.

## Source File

- `/Users/jv/.claude-accounts/jv/agents/dev-docs-writer.md`

## Snapshot File

- `agents/dev-docs-writer.md`

## Refresh Rule

If the live shared agent changes again, update this snapshot only as part of a
new reviewed bead or explicitly refresh it in a bead that records the changed
source, compare command, and commit hash.
