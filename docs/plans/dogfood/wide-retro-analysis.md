# Wide Retro Analysis: Dogfood Completeness

Status: scratchpad / planning analysis. This is not shipped user-facing
documentation.

Date: 2026-05-27

Related phase: `etude-8hq` - Phase: enforce dogfood completeness.

## Scope

This analysis looks across the dogfood retro corpus after the migration into
`refs/etude/retros/*`, not at a single bead or phase gate.

Inputs reviewed:

- `etude retro list` and `etude retro show` for the migrated retros.
- Workflow retros A1-A2, B1-B7, C1.
- Cohort retros B8-B16.
- `docs/plans/dogfood/retro-ledger.md`.
- `docs/plans/dogfood/review-gate-runbook.md`.
- External Gemini critique. Claude CLI was attempted repeatedly but produced no
  output and had to be killed, so there is no Claude critique to include.

At the time of review, the retro store contained workflow retros plus cohort
retros through B16. All visible retros had `META=N`.

## Executive Summary

Etude has made real progress: runs, gate reviewer records, and retros are now
first-class artifacts. The migration proved the system can preserve and inspect
dogfood evidence after the fact.

The deeper failure pattern is that capture remains out-of-band. The workflow
builds observability features, then depends on agents remembering to invoke
them. That leaves the strongest invariants in prose runbooks rather than in the
critical path. The consequence showed up twice in the same shape:

- retros existed in the ledger but not in `refs/etude/retros/*`;
- gate review work existed in review artifacts but not in captured run gate
  records.

The correction should therefore not be another reminder. The next phase should
move dogfood completeness into a mechanical close/push gate: if the required
run, gate, retro, pushed-ref, and docs-reconciliation artifacts are missing,
work cannot be called complete.

## Patterns Individual Retros Understated

### 1. Capture Is Out-Of-Band

The individual retros repeatedly improved capture surfaces, review prompts, and
docs checks, but the same omission pattern persisted: new artifacts were not
captured until a user spot-check or backfill.

The root cause is not only weak discipline. It is that capture is an extra action
outside the natural completion path. If `etude capture`, `capture-gate`, and
`retro capture` are optional follow-up steps, the expected long-run failure mode
is omission.

Durable implication: make the close/push path call a dogfood completeness gate.
A read-only audit is useful, but only as the mechanism used by the gate.

### 2. The System Is Better At Recovery Than Prevention

B16 shows that rich review artifacts made faithful backfill possible. That is a
strength worth preserving. However, backfill is still a recovery path. The
product goal should be to prevent missing evidence before the bead closes.

This is the same shape as docs drift: a later reconciliation can find the gap,
but the real improvement was adding `make docs-reality` and `make reconcile`.
Dogfood capture needs the same kind of mechanical reconciliation.

### 3. Runbook Knowledge Is Becoming Operational Load

The runbook contains many correct rules, but it has become a dense memory palace:
per-seat caveats, prompt shaping rules, capture obligations, docs rules, disputed
claim handling, risk tiers, and close protocol steps all coexist as prose.

This does not mean the runbook is bad. It means it should become rationale and
reference, while high-frequency checks move into short checklists or commands.
Agents are already failing the "remember every current rule" test.

### 4. Reviewer Model Identity Is A Weak Abstraction

The retros often say that one model caught a defect while another missed it.
The more durable distinction is review role:

- spec adversary;
- runtime verifier;
- docs/reality checker;
- security and data-integrity checker.

Different models may be good seats, but the gate should encode the review mode
explicitly. Otherwise the process overfits to current model behavior and misses
why the multi-seat panel works.

### 5. Retros Are First-Class But Not Yet Machine-Useful

The retro bodies are now visible through Etude, but all migrated retros are
`META=N`. That means cross-retro analysis still depends on reading markdown.
The `retro-meta` feature exists, but dogfood is not yet using it.

The right sequencing is:

1. define one stable cadence retro metadata convention;
2. require future cadence retros to include it;
3. backfill migrated retros with sidecars;
4. revisit cross-retro indexing once real sidecar data exists.

### 6. Backfill Time Distorts Event Time

Backfilled retros appear in `etude log` at migration time, not at the time the
retro happened. The body text carries the original date, but the artifact
timeline does not.

Etude needs a way to distinguish capture time from event time for backfilled or
externally authored retros. Otherwise the log is accurate as storage history but
misleading as process history.

### 7. Subject Claims Need Consistency Checks

`retro-cohort-etude-6j8-20260526T215942Z` has a body title that names
`6j8/kig/nm6`, but its manifest subjects are only `etude-6j8` and `etude-kig`.
That may be legitimate if `nm6` has no run ref, but the artifact does not encode
that explanation.

This is a small data issue with large diagnostic value: Etude should make it hard
for body/header claims to diverge from manifest refs silently.

## Recommendations Ranked By Leverage

### P0: Enforced Dogfood Completeness Gate

Build a mechanical check for dogfood completeness and put it in the close/push
path. The check should verify, at minimum:

- closed dogfood beads have corresponding run refs;
- gated runs have gate reviewer records;
- cadence thresholds have retro refs;
- retro refs are pushed;
- docs-reality/reconcile checks are represented where docs changed;
- known bypasses are explicit and auditable.

This is the highest-leverage change because it moves capture from memory into
the critical path.

Tracked by:

- `etude-8hq.4` - Implement dogfood completeness audit.
- `etude-8hq.1` - Enforce dogfood completeness in close/push path.

### P1: Retro Metadata Convention And Requirement

Define the minimal cadence retro sidecar schema and require it going forward.
The sidecar should include failure modes, root causes, follow-up beads, decisions,
original event date, and the durable process/docs changes landed.

Tracked by:

- `etude-8hq.3` - Define and require cadence retro-meta sidecars.
- `etude-8hq.5` - Backfill structured metadata for migrated retros.

### P1: Original Event Time For Retros

Add an explicit event-time field or manifest ref so `etude log` can distinguish
"this retro was captured during a backfill" from "this retro occurred now".

Tracked by:

- `etude-8hq.2` - Add original event time to retro artifacts.

### P1: Subject Consistency Guard

Repair or annotate the B16 subject mismatch, then add a guard so future backfills
cannot silently capture a body/title that names subjects absent from manifest
refs.

Tracked by:

- `etude-8hq.8` - Repair and guard retro subject consistency.

### P1: Role-Based Review Gate

Refactor the gate prompt/runbook around review roles rather than model identity.
Keep model seats, but make the expected review mode explicit and short.

Tracked by:

- `etude-8hq.6` - Refactor review gate into explicit reviewer roles.

### P1: Executable Checklists

Compress the high-frequency dogfood runbook rules into short checklists and
commands. Leave the long runbook as rationale and reference.

Tracked by:

- `etude-8hq.7` - Compress dogfood runbook into executable checklists.

## Lower-Leverage Work

These are still worth doing, but they should not displace the completeness gate:

- fixing one historical mismatch without adding a guard;
- adding richer retro sidecars without requiring them in the workflow;
- adding event-time metadata without making capture mandatory;
- adding another prose rule to the runbook.

Each improves record quality, but none prevents the next omission by itself.

## Phase Definition

The new phase is `etude-8hq`: **Phase: enforce dogfood completeness**.

Child beads:

- `etude-8hq.4` - Implement dogfood completeness audit.
- `etude-8hq.1` - Enforce dogfood completeness in close/push path.
- `etude-8hq.3` - Define and require cadence retro-meta sidecars.
- `etude-8hq.2` - Add original event time to retro artifacts.
- `etude-8hq.8` - Repair and guard retro subject consistency.
- `etude-8hq.6` - Refactor review gate into explicit reviewer roles.
- `etude-8hq.7` - Compress dogfood runbook into executable checklists.
- `etude-8hq.5` - Backfill structured metadata for migrated retros.

Important dependencies:

- `etude-8hq.1` depends on `etude-8hq.4`.
- `etude-8hq.5` depends on `etude-8hq.3`.
- deferred `etude-9ey` depends on `etude-8hq.5` before cross-retro indexing
  should be revisited.

## Highest-Leverage Next Step

Start with `etude-8hq.4`: implement the dogfood completeness audit.

The audit should be designed as the mechanism that `etude-8hq.1` will later put
into the close/push path. If it is only a manual report, the phase will have
missed the core lesson.
