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

The Phase 0 product path is COMPLETE. `etude-run-manifest`,
`etude-capture-manual`, `etude-workflow-schema`, `etude-init-command`,
`etude-run-show-list`, and `etude-sync-command` are all closed, alongside the
refstore and artifact-store foundations — the minimal git-native capture loop
(declare workflow, capture, inspect runs, sync `refs/etude/*`) now exists end to
end.

There is no remaining ready product-path leaf. The default next work is now
hardening and polish taken opportunistically — `etude-ccj` (refstore edge
cases), `etude-cr2` (typed artifact discriminator), `etude-88o` — plus the
deferred follow-ups `etude-dpz` (shared run-id validation) and `etude-zcq`
(sync fetchBangAbort coverage). Net-new product scope belongs to Phase 1; pick a
hardening/polish bead by priority unless the user opens Phase 1 planning.
