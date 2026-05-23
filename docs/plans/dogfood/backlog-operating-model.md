# Backlog Operating Model

Status: dogfood process note. This describes how to read and choose work from
the current beads backlog while building `etude`.

## Working Principles

- One bead should produce one coherent commit.
- Epics organize the graph; they are not normally the next work item.
- Product-path beads outrank polish unless the polish blocks confidence in the
  next product bead.
- Follow-up polish beads must have a priority and a clear trigger for when to
  pick them up.
- Use `bd ready` to find unblocked work, then filter out epics unless the
  user explicitly asks to revise the roadmap.

## Work Categories

### Product Path

These beads move Phase 0 toward a usable capture loop. Prefer these when
choosing "next" work:

- `etude-run-manifest`
- `etude-workflow-schema`
- `etude-init-command`
- `etude-capture-manual`
- `etude-run-show-list`
- `etude-sync-command`

### Hardening

These improve already-built internals. Pick them when the next product bead
would rely heavily on the weak edge being hardened:

- `etude-ccj` - refstore edge cases

### Polish

These should not interrupt Phase 0 unless they are very cheap or directly
reduce risk in the next bead:

- `etude-cr2` - typed artifact storage discriminator

### Epics

Epics are structure and progress markers. Do not pick an epic as the next bead
unless the task is explicitly to rescope, split, close, or revise the epic.

## Next-Bead Selection

When the user says "next", use this order:

1. Prefer the highest-priority ready product-path bead.
2. Prefer beads that unblock multiple downstream beads.
3. Prefer schema/storage/capture foundations before UI or polish.
4. Consider hardening only when it protects the next product-path bead.
5. Ignore ready epics unless the user asks for planning or roadmap work.

## Current Phase 0 Bias

The schema and capture foundations now exist: `etude-run-manifest`,
`etude-capture-manual`, and `etude-workflow-schema` are closed, alongside the
refstore and artifact-store foundations. The remaining Phase 0 product path, in
default next-bead order, is:

1. `etude-init-command` - scaffolds workflow config (via `workflow.Default().YAML()`)
   and repo etude settings.
2. `etude-run-show-list` - makes captured runs inspectable.
3. `etude-sync-command` - moves the `refs/etude/*` namespace between clones.

`etude-init-command` is the current default next bead: it is the highest ready
product-path leaf and consumes the now-landed `internal/workflow` schema.
