# Verify Phase Design

Status: planning note. This defines the intended Verify phase boundary for the
dogfood workflow before any external skill files are changed.

## Decision

Use one externally visible **Verify** phase.

Keep `dev-test-writer`, `dev-qa`, and manual testing as separate internal
specialists for now. Do not expose them as separate top-level gates. The user
sees one Verify artifact with a single status recommendation for the bead.
Advancement is controlled by the three-reviewer gate defined in
[Review Gate Process](review-gate-process.md).

This keeps the workflow simple enough to dogfood while preserving useful
specialization internally. Revisit merging `dev-test-writer` and `dev-qa` only
after the unified Verify artifact has been used on real beads.

## Public Contract

The Verify phase answers one question:

> Is the implementation ready for documentation and final review?

Its output is a single approval artifact. Every Verify outcome is reviewed by
the three-reviewer panel defined in [Review Gate Process](review-gate-process.md).
The workflow moves phase only after Gemini Pro, Claude Opus, and a fresh
GPT-5.5 xhigh agent all return `GO`.

The artifact has a machine-readable status:

- `pass`: implementation is ready for Docs
- `fail`: implementation must return to Implement with concrete blockers
- `blocked`: Verify cannot complete because required context, tools, or human
  input is missing

The recommendation is derived from status and must agree with it:

- `pass` -> proceed to Docs
- `fail` -> return to Implement
- `blocked` -> needs missing input, dependency, tool access, or decision

The artifact also includes:

- automated test changes made during Verify
- test change state: committed, staged, dirty, or none
- automated test commands run and their results
- manual test plan and results, when manual testing is relevant
- documentation style-guide findings, when docs changed
- QA findings about correctness, coverage, regressions, and code quality
- unresolved risks or explicit statement that no material risks remain
- provenance envelope for the Verify phase

## Artifact Ownership

The parent workflow agent owns Verify orchestration and the final artifact
envelope. It runs the internal lanes, collects their outputs, and presents the
single Verify artifact to the review panel.

QA owns synthesis and the status recommendation. It receives the test-writer
and manual-test outputs, evaluates adequacy and risk, and returns a structured
finding to the parent workflow agent.

The normal internal lane order is: test-writer output, QA scoping decision for
manual testing, manual-test lane when required, then QA synthesis and status
recommendation.

The parent workflow agent assembles the final approval artifact from:

- implementation reference
- test-writer output
- manual-test output, when applicable
- QA synthesis
- provenance, reviewer-result, and approval-surface references

## Internal Responsibilities

### Test Writer Lane

`dev-test-writer` owns test design and test implementation inside Verify.

Responsibilities:

- identify the behavioral surface changed by Implement
- add or update focused automated tests when useful
- avoid broad unrelated test refactors
- never modify non-test implementation code
- run the new or relevant tests
- report test files changed, commands run, pass/fail output, and coverage gaps

The test writer may return "no test change recommended" only with a reason,
such as a planning-only bead, docs-only change, or behavior already covered by
existing tests.

If implementation code must change to make tests pass, Verify fails and the
bead returns to Implement. The fix must be captured as a new Implement
artifact, not hidden inside Verify.

### Manual Test Lane

Manual testing is optional and belongs inside Verify.

QA decides whether manual testing is required before the manual-test lane runs.
The parent workflow agent records that decision, then runs the manual-test lane
when QA requires it.

Use manual testing when the bead affects:

- browser-visible behavior
- workflows that are hard to validate with unit tests alone
- visual layout, interaction, or generated artifacts
- anything with an existing manual test plan

Manual testing should produce the same kind of evidence as automated testing:
what was tested, how it was tested, observed result, failures, screenshots or
artifact references when useful, and bug beads for defects that should not be
fixed in the current bead.

AI-driven browser testing, such as Playwright execution, is still a Verify
lane. If the test genuinely requires human execution, Verify returns `blocked`
with a request for the human manual-test input. After that input is supplied,
the Verify artifact is updated and the three-reviewer gate reruns.

### QA Lane

`dev-qa` owns synthesis and quality judgment inside Verify.

Responsibilities:

- review the implementation against the approved plan
- inspect test adequacy rather than only test pass/fail status
- check changed docs against the applicable writing style guide
- check for regressions, edge cases, and integration risks
- include manual test results when present
- decide whether unresolved risks block progress
- produce the single Verify recommendation

If test coverage is inadequate but the implementation appears sound, QA routes
back to the test-writer lane internally before issuing the final Verify
artifact.

QA should not silently redo implementation. Any required implementation change,
however small, is a `fail`; the bead returns to Implement so the fix is
captured as an Implement artifact.

If Verify reveals that the approved plan was wrong or incomplete, use `blocked`
with a recommendation for human input and re-planning rather than routing the
problem to Implement. After the missing decision is supplied, rerun the
three-reviewer gate.

## Artifact Shape

Store the Verify artifact as an append-only bead note/comment. Render it to the
configured approval surface for review.

Verify artifact schema:

```text
## Verify

Status: pass | fail | blocked
Recommendation: derived from status

Inputs:
- bead: <id>
- implementation artifact: <commit or diff reference>
- approved plan: <bead design or file reference>

Automated tests:
- changes: <files, or none with reason such as planning-only bead>
- state: committed | staged | dirty | none
- commands: <commands run>
- result: <pass/fail output summary>

Manual tests:
- required: yes | no
- reason: <why>
- result: <summary or not applicable>

Docs:
- changed: yes | no
- style guide: <path or not applicable>
- result: <summary or not applicable>

QA findings:
- correctness:
- coverage:
- regressions:
- code quality:
- risks:

Provenance:
- follows the standard capture envelope defined by the dogfood capture protocol

Review gate:
- reviewers: Gemini Pro, Claude Opus, fresh GPT-5.5 xhigh
- results: GO | BLOCK for each reviewer
- required changes: <summary or none>
- optional improvements handled: <summary or deferred bead>
- rerun count:
```

Review-gate results are appended after the panel review completes. They are not
edited into the original Verify artifact body.

## Failure Handling

Every Verify result goes through the three-reviewer gate:

- if all three reviewers give `GO` on `pass`, move to `phase:docs`
- if all three reviewers give `GO` on `fail`, move back to `phase:implement`
- if all three reviewers give `GO` on `blocked`, remain in `phase:verify` with
  the missing input or decision recorded
- if any reviewer gives `BLOCK`, incorporate required feedback and rerun the
  full three-reviewer gate
- if any reviewer cannot complete because of auth, quota, model access,
  allowance, timeout, or tooling failure, stop and escalate to the user
- if the same gate receives `BLOCK` results through attempt 4 (the initial run
  plus three reruns), escalate to the user with the reviewer feedback and a
  proposed resolution

When Verify fails:

- keep the failed Verify result reviewable
- move the bead back to `phase:implement`
- record the blockers in bead notes
- only create follow-up bug beads for defects intentionally deferred outside
  the current bead

When Verify is blocked:

- state the missing dependency or human decision
- do not proceed to Docs
- leave the bead in `phase:verify` until unblocked
- after the missing input is supplied, update the Verify artifact and rerun the
  three-reviewer gate

## Workflow Implications

The future `dev-workflow` update should expose Verify as one phase:

```text
Plan -> Implement -> Verify -> Docs -> Final Review
```

Internally, Verify may call test-writing, automated test execution, manual
testing, and QA workers. Those details should be invisible to the top-level
review gate except through the single Verify artifact.

Final Review trusts the Verify artifact and does not rerun the test suite by
default. It may request more verification only when the Verify artifact is
missing, inconsistent, or contradicted by later changes.

When documentation changes, Verify checks the changed docs against the
applicable writing style guide before Docs or Final Review. For current dogfood
planning docs, use [Writing Style Guide](writing-style-guide.md).

## Open Implementation Notes

- The capture protocol should define the standard provenance envelope referenced
  by this note.
- The capture protocol should define whether failed Verify attempts become
  separate captured artifacts or revisions within one phase attempt.
- The capture protocol should define how to count and reference internal
  test-writer loops, blocked-then-resupplied attempts, and review-gate reruns.
- For planning-only beads, Verify can pass without tests when it confirms the
  planning artifact is in the right location, links resolve, and no shipped docs
  claim planned behavior exists, and changed docs follow the applicable writing
  style guide.
