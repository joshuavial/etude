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

Product-path beads move the shipped CLI surface forward. Prefer these when
choosing "next" work. The Phase 0 capture-loop foundation has SHIPPED
(`etude-run-manifest`, `etude-workflow-schema`, `etude-init-command`,
`etude-capture-manual`, `etude-run-show-list`, `etude-sync-command` — all
closed), and since then so have replay, the eval library + `etude bench`,
`etude gc`, `etude reindex`, and gate-reviewer visibility (`etude capture-gate`
+ run-show gates). The current product frontier is the post-Phase-0 roadmap:
retros as a first-class artifact (`etude-14r`) and Phase 1 (xenota capture
adapter + GitHub import), plus the dogfood-workflow prep beads.

### Hardening

These improve already-built internals. Pick them when the next product bead
would rely heavily on the weak edge being hardened:

- `etude-ccj` - refstore edge cases (landed: SHA-1/SHA-256 OID acceptance,
  empty-old create, stdout/stderr split, control-char path rejection)

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

## Current Frontier

Phase 0 (the minimal git-native capture loop) is COMPLETE and several phases
beyond it have shipped: replay, the eval library + `etude bench`, `etude gc`,
`etude reindex`, generated CLI docs + the example workflow (Phase 3/4), and the
gate-reviewer-visibility epic (`etude-roadmap.2`: `etude capture-gate`, run-show
gates, the dogfood gate-capture script, backfill, and `docs/gates.md`).

The current frontier is the dogfood-workflow prep epic (`etude-phase-prep`:
retro ledger, docs refresh, degraded-gate policy, docs-reality guard) and the
post-Phase-0 roadmap: retros as a first-class artifact (`etude-14r`) and Phase 1
(xenota capture adapter + GitHub import), which is USER-BLOCKED pending external
context. Choose the highest-priority ready non-epic bead; net-new product scope
beyond the prep epic belongs to Phase 1.
