# Dogfood Capture Protocol

Status: planning note. This defines the temporary manual capture protocol for
dogfooding `etude` while the product capture commands are not implemented.

## Decision

Use one bead as one provisional `etude` run.

Until `etude capture` exists, the bead, git history, and dogfood planning docs
are the capture store. Each workflow phase records a first-draft artifact, a
provenance envelope, and review-gate results in stable locations that can later
be imported into `etude` as run and stage records.

Captured phase artifacts are append-only after review starts. If a phase fails
review or must be redone, create a new attempt entry instead of editing the
reviewed artifact in place.

## Run Mapping

Each bead maps to one future `etude` run:

- run id: bead id
- run title: bead title
- run status: bead status
- run parent: parent bead or epic
- run dependencies: dependency edges recorded by beads
- run start: timestamp when the bead is claimed
- run end: timestamp when the bead is closed or deferred
- source repo: current git remote and branch

The bead remains the task tracker. `etude` later imports captured artifacts; it
does not replace bead status, priorities, dependencies, or ownership.

## Stage Names

Use these stage names for dogfood capture:

1. `plan`
2. `implement`
3. `verify`
4. `docs`
5. `final-review`
6. `retro`, when a triggered or manual retro is produced

The first five stages are the normal linear bead workflow described in
[Dev Workflow Audit](dev-workflow-audit.md). `retro` is an optional triggered
artifact described in the product planning note for
[Retrospectives](../product/retrospectives.md).

See [Retro Capture](#retro-capture) for the manual dogfood rules that make a
`retro` stage reconstructable without making it part of the normal blocking
phase sequence.

## Storage Rules

Use the most structured existing store for each artifact:

| Artifact | Temporary storage | Mutability |
|---|---|---|
| Plan artifact | bead `design` field | mutable until plan review starts, append-only afterward |
| Capture envelope | bead note/comment | append-only after review starts |
| Implementation artifact | git diff or commit plus bead note summary | append-only through commits and notes |
| Verify artifact | bead note/comment | append-only |
| Docs artifact | git diff or commit plus bead note summary | append-only through commits and notes |
| Final review artifact | bead note/comment | append-only |
| Retro artifact | bead note/comment or planning file linked from a note | append-only after capture |
| Reviewer results | bead note/comment | append-only |
| Large outputs | file path, screenshot path, log path, or external reference recorded in bead notes | preserve by reference |

For retro artifact details, see [Retro Capture](#retro-capture).

Planning-only beads still have an implementation artifact. The artifact is the
planning note created or changed under `docs/plans/`, not shipped
documentation.

Do not move planning material into implemented docs just to make it easier to
capture. The documentation policy in this repo still applies: shipped docs
describe implemented behavior, and plans live under `docs/plans/`.

## Capture Envelope

Every stage artifact records this envelope before review:

```text
## Capture: <stage> attempt <n>

Stage: plan | implement | verify | docs | final-review | retro
Attempt: <integer starting at 1 for this stage>
Bead: <id and title>
Runner: <agent, tool, skill, or human>
Runner version: <model, skill revision, CLI version, or unknown>
Started at: <ISO-8601 timestamp or best available timestamp>
Started git SHA: <git rev-parse HEAD>
Started dirty state:
<git status --short output, or "clean">

Inputs:
- <stable references to bead fields, prior artifacts, files, commands, prompts>

Output artifact:
- <bead field, note, commit, diff, file path, or external reference>

Ended at: <ISO-8601 timestamp or best available timestamp>
Ended git SHA: <git rev-parse HEAD>
Ended dirty state:
<git status --short output, or "clean">

Approval surface:
- <chat, tmux pane, PR comment, local file, or other surface>

Review gate:
- required: yes | no
- attempt: <gate attempt number, when applicable>
- result: pending | pass | rerun required | escalated | not applicable
```

The envelope records references rather than copying large artifacts. For exact
review, the gate prompt must include the full current artifact text or exact
changed excerpts, following [Review Gate Runbook](review-gate-runbook.md).

Example:

```text
## Capture: implement attempt 1

Stage: implement
Attempt: 1
Bead: etude-dogfood-capture-protocol - Define manual etude capture protocol
Runner: codex
Runner version: GPT-5.5
Started at: 2026-05-19T10:14:00+12:00
Started git SHA: abc1234
Started dirty state:
clean

Inputs:
- bead description from etude-dogfood-capture-protocol
- docs/plans/dogfood/dev-workflow-audit.md
- docs/plans/dogfood/verify-phase-design.md

Output artifact:
- docs/plans/dogfood/capture-protocol.md
- docs/plans/README.md
- docs/plans/dogfood/README.md

Ended at: 2026-05-19T10:42:00+12:00
Ended git SHA: abc1234
Ended dirty state:
 M docs/plans/README.md
 M docs/plans/dogfood/README.md
?? docs/plans/dogfood/capture-protocol.md

Approval surface:
- Codex chat

Review gate:
- required: yes
- attempt: 1
- result: pending
```

## Attempt Rules

Attempts are counted per stage.

- The first artifact for a stage is attempt `1`.
- If review blocks and required changes alter the artifact, the next reviewed
  artifact is attempt `2`.
- If a tool fails before producing a reviewable artifact, record a short
  blocked note but do not increment the stage attempt unless the partial output
  is used as an artifact.
- Gate reruns are counted separately inside the stage attempt.

This keeps the future import model simple: stage attempts are artifact
versions; gate attempts are reviews of one artifact version.

## Verify Failure And Blocked Results

Each completed Verify artifact is captured, regardless of status.

- `pass` becomes a Verify stage attempt whose recommendation is to proceed to
  Docs.
- `fail` becomes a Verify stage attempt whose recommendation is to return to
  Implement.
- `blocked` becomes a Verify stage attempt whose recommendation is to collect
  missing input and rerun Verify.

When Verify returns `fail`, the subsequent implementation fix is a new
Implement stage attempt. Do not hide implementation changes inside Verify.

When Verify returns `blocked`, record the missing input in the Verify artifact.
After the input is supplied, create a new Verify attempt that references the
prior blocked attempt and the newly supplied input.

## Internal Loops

Internal specialist loops are captured as references inside the parent stage,
not as top-level stages.

For Verify, record:

- test-writer lane output, including test files changed and commands run
- QA lane output and status recommendation
- manual-test lane output, when required
- internal loop count, when QA sends work back to test-writer before the final
  Verify artifact

Only the final parent Verify artifact goes through the four-reviewer gate. If
an internal lane reveals required implementation work, Verify returns `fail`
and the bead moves back to Implement.

**A built-binary manual test for a command that WRITES to `refs/etude/*` (or
any persistent repo state) MUST run in a throwaway repo, never the working
repo.** Build the binary, then exercise it against a fresh `mktemp -d` + `git
init` repo seeded with just the runs/artifacts the test needs. `etude bench`,
`etude replay --record`, and `etude capture` all record new runs/evals into
`refs/etude/runs|evals/*`; running them against the working repo during a manual
test pollutes real dogfood data with throwaway runs (observed: a first
`etude bench` manual test recorded 12 junk replay runs + eval results into the
working repo that had to be hand-deleted). The manual test is still required —
it catches stateful/integration bugs that static code review cannot (the
`etude bench` cohort-recursion bug — bench re-benchmarking its own recorded
replays — was invisible to all four review seats and only surfaced from running
the built binary twice). Isolate it; do not skip it.

For planning-only beads, Verify may record that test-writer and manual-test
lanes were not applicable. QA still checks that the planning artifact is in the
right location, links resolve, changed docs follow the writing style guide, and
planned behavior is not described as shipped behavior.

## Retro Capture

Retros are optional, triggered artifacts. They explain what happened in a run,
phase, gate sequence, or workflow, but they do not replace the gate result,
test result, or bead status that established what passed or failed.

Manual dogfood capture supports these retro triggers now:

- **End-of-run retro**: after a bead closes, summarize what changed, what gates
  found, and which process improvements should be considered.
- **Repeated gate-block retro**: after the same phase gate receives repeated
  `BLOCK` results, analyze why the artifact kept failing review.
- **Blocked-state retro**: when a run is blocked by missing context, auth,
  quota, tool access, or human input, record the blocker and prevention path.
- **Failed Verify retro**: when Verify returns `fail`, capture whether the
  failure came from implementation quality, test inadequacy, plan defects, or
  missing workflow rules.
- **Manual retro**: when the user or workflow operator explicitly requests one
  for a bead, phase, gate sequence, or workflow issue.

For manual dogfood capture, "repeated" is operator judgment unless a later
workflow config defines a threshold. The trigger names below intentionally use
manual event names: `end-of-run` maps to the product note's `close` trigger,
and `repeated-gate-block` maps to the product note's `repeated-block` trigger.
The remaining manual trigger names match the product planning note.

Post-bench retros and configurable automatic retro policies are product design
work for later `etude` commands. While dogfooding manually, mention those ideas
only as planned behavior and do not capture them as if `etude bench` or
automated retro policies already exist.

### Retro Artifact Shape

Store manual retros as append-only bead notes or as planning files under
`docs/plans/dogfood/` linked from a bead note. Use this schema:

```text
## Retro: <scope> attempt <n>

Scope: run | phase | gate | workflow
Trigger: end-of-run | repeated-gate-block | blocked-state | failed-verify | manual
Attempt: <integer starting at 1 for this retro scope and trigger>
Bead: <id and title>
Related stage: <stage name, or "run">
Related gate attempts: <reviewer result note refs, or "not applicable">
Related commits or diffs: <commit hashes, diff refs, or "not applicable">

Inputs:
- <phase artifacts, gate results, command logs, git state, linked issues>

Summary:
<concise narrative of what happened>

Timeline or key events:
- <event>

Failure modes:
- <category and evidence, or "none">

Root causes:
- <process, skill, tool, context, or planning cause>

Worked well:
- <practice worth preserving>

Recommendations:
- <proposed change and target artifact path>

Follow-up refs:
- <beads, PRs, docs, skills, workflow config, or "none">

Decision/status:
accepted | deferred | superseded | informational

Capture:
- follows the standard capture envelope for `retro`
```

When the retro attempt count is unclear, preserve the artifact anyway and use
bead note append order or timestamps as the practical ordering source.

The field names intentionally mirror the product planning note for
[Retrospectives](../product/retrospectives.md), but this protocol is only the
manual capture contract. It does not imply an implemented `etude retro`
command.

### Retro Links

Every retro must link back to stable run evidence:

- the bead id and title for the future run id
- the triggering phase or gate attempt, when relevant
- reviewer result notes for repeated gate-block retros
- the failed Verify artifact for failed-Verify retros
- the blocker note or user-input request for blocked-state retros
- commits, diffs, logs, screenshots, or artifact paths used as evidence

Retros may propose follow-up beads, but they should not silently create broad
work. If a recommendation is accepted into active work, link the new bead or
commit from the retro's `follow-up refs`.

### Retro Gates

Retros do not gate the normal `plan -> implement -> verify -> docs ->
final-review` sequence. They are explanatory artifacts that can be produced
after a close, after a repeated blocker, or on request.

If a later bead makes a retro itself the artifact under review, that bead uses
the normal four-reviewer gate for its own phase. Otherwise, retro capture does
not block product work from advancing.

### Retro Import

Future import should treat retro notes and linked retro files as `retro` stage
attempts attached to the same run manifest as the bead. Import should preserve:

- scope and trigger
- links to related phase attempts and gate attempts
- source bead note or file path
- commits, diffs, logs, and linked issues referenced as inputs
- decision/status and follow-up refs

If a retro references planned behavior, import should keep that text as a
planning artifact. It must not promote the retro into shipped user-facing docs.

## Review Gate Capture

After every phase gate, append reviewer results to the bead notes:

```text
<Stage> gate attempt <n> for stage attempt <m>:
- Gemini Pro: GO | BLOCK | failed (<reason>)
- Claude Opus: GO | BLOCK | failed (<reason>)
- fresh GPT-5.5 xhigh: GO | BLOCK | failed (<reason>)
- pi/pilms: GO | BLOCK | failed (<reason>)
- required changes incorporated: <summary or none>
- optional improvements handled: <summary or deferred bead>
- result: pass | rerun required | escalated
```

Optional improvements from `GO` reviewers are handled before advancing or
explicitly deferred to a named follow-up bead.

Reviewer auth, quota, model access, allowance, timeout, or tooling failures are
captured as reviewer failures. They do not count as `GO`.

The normal `plan`, `implement`, `verify`, `docs`, and `final-review` stages go
through the four-reviewer gate. Retro artifacts do not gate the main workflow
unless a later bead explicitly makes a retro the artifact being advanced.
For retro-specific interpretation of gate results, see
[Retro Capture](#retro-capture).

## Import Path

When `etude import` or equivalent local tooling exists, import dogfood beads as
runs by reading:

1. bead metadata for run identity, status, ownership, dependencies, and
   timestamps
2. bead `design` for the Plan artifact
3. append-only bead notes for capture envelopes, Verify, Final Review, reviewer
   results, and retros
4. git commits and diffs referenced by implementation and docs notes
5. referenced files for large outputs, screenshots, logs, or manual-test
   evidence

Import should preserve original timestamps and stage attempt numbers. It should
store imported artifacts under `refs/etude/*` as immutable blobs and create a
manifest that links each stage attempt to the exact source reference used
during manual capture.

If a bead lacks enough data to reconstruct a stage, import should mark that
stage as incomplete instead of inventing missing artifacts.

## Checklist

At the start of a bead:

- claim the bead
- record the starting git SHA and dirty state
- confirm the parent, dependencies, and phase labels

For each phase:

- write the first-draft artifact to its temporary storage location
- append or update the capture envelope before review starts
- run the review gate defined in [Review Gate Process](review-gate-process.md)
- append reviewer results
- handle optional improvements or defer them to named beads
- advance to the next phase only after the gate passes

At bead close:

- ensure every completed phase has a capture envelope
- ensure reviewer results are appended for every gated phase
- ensure implementation and docs commits are referenced
- run `git status`
- commit and push repository changes
- run `bd dolt push` for bead storage

## Open Questions

- The first `etude import` implementation should decide whether to preserve
  bead note text verbatim as one artifact per note or parse capture envelopes
  into separate structured artifacts. Owner: Phase 0 import work.
- The workflow skill update should decide the exact label transitions for
  `phase:plan`, `phase:implement`, `phase:verify`, `phase:docs`,
  `phase:review`, and `phase:complete`. Owner:
  `etude-overhaul-dev-workflow-skill`.
