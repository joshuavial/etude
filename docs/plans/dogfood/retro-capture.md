# Retro Capture

Status: planning note. Extracted from the retired dogfood capture protocol in
`etude-9uf.2` — this material describes how retros are captured with the
existing `etude retro` command and is unaffected by the move to supervised
worker lanes. For how a bead is worked and gated, see the
[Supervisor runbook](supervisor-runbook.md).


Retros are optional, triggered artifacts. They explain what happened in a run,
phase, gate sequence, or workflow, but they do not replace the gate result,
test result, or bead status that established what passed or failed.

**Cadence retros should carry a `--meta-file` sidecar** (etude-8hq.3): from
2026-05-27 onward every `--trigger cadence-retro` capture is expected to include
`--meta-file` with the 7-key convention. This was enforced by the completeness
audit until bead `etude-9uf.4` removed that check; it is now a convention that
nothing verifies, while `scripts/retro-meta-index.sh` still consumes the
sidecars (bead `etude-k47`). See
`docs/plans/dogfood/retro-ledger.md` §"Cadence retro-meta sidecar (required,
etude-8hq.3)" and `scripts/retro-meta-cadence.example.json`.

#### Cadence subjects without run refs

When a cadence cohort covers a bead that has **no `refs/etude/runs/<id>`** —
for example, a data-backfill bead, an allowlisted bead, or any bead that was
never run through the dev-workflow — record that bead as `--bead <id>` (which
writes a `bead.N` ref in the manifest), NOT as `--subject-run <id>`.

Using `--subject-run <id>` requires a valid run ref to exist
(`internal/cli/retro.go:471-474`); it will fail if the run has not been
captured yet. The `--bead` flag records the bead identity without requiring a
run ref.

The retro-subject consistency guard treated both `subject_run.*` and `bead.*`
refs equally as "subject present in refs", so `--bead` was the way to record
these cases. That guard was removed in `etude-9uf.4`, so nothing checks the correspondence
today — though the audit's retro-cadence warning survives, and its refs-pushed
check still covers `refs/etude/retros/*`; recording
it remains good practice. The canonical example is `etude-nm6`
(gate-record backfill, allowlisted), which has no run ref and must be recorded
via `--bead etude-nm6` when it appears as a cohort subject.

The supervised worker-lane model supports these retro triggers:

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

For a manually supervised lane, "repeated" is operator judgment unless a later
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
the normal phase gate for its own phase, at the tier its stage declares. Otherwise, retro capture does
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

