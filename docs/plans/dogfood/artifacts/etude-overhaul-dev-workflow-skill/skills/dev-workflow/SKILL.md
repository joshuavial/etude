---
name: dev-workflow
description: Work on a bead through Plan, Implement, Verify, Docs, and Final Review phases. Each bead = one commit. Invoke with /dev.
---

# Dev Workflow

Structured development for commit-sized work:

`PLAN -> IMPLEMENT -> VERIFY -> DOCS -> FINAL REVIEW -> commit/close`

This workflow is currently dogfooding `etude`: every phase produces a
reviewable artifact, records capture/provenance details, and advances only
after the configured gate passes.

## When to Use

- Working on a bead (feature, bug, or task)
- User says `/dev` or `/dev <bead-id>`
- After `/scope` has set up the proposal and beads

## Core Concept

**1 Bead = 1 Commit**

A bead is the atomic unit of work:
- Has a clear, single purpose
- Results in one clean commit
- Includes code, tests, docs when relevant, and refactoring
- Goes through five gated phases before completion

## Command Interface

```bash
/dev                         # Show ready beads, pick one
/dev <bead-id>               # Work on specific bead
/dev <bead-id> plan          # Force Plan phase
/dev <bead-id> implement     # Force Implement phase
/dev <bead-id> verify        # Force Verify phase
/dev <bead-id> docs          # Force Docs phase
/dev <bead-id> review        # Force Final Review phase
/dev <bead-id> status        # Show current state
/dev auto                    # Auto-mode on all ready beads
/dev <bead-id> auto          # Auto-mode on specific bead
```

Legacy aliases may be accepted for compatibility:
- `exec` -> `implement`
- `qa` -> `verify`

## Dogfood Gate

While building `etude`, phase advancement is controlled by the current repo's
dogfood review gate, not by human approval alone. Follow the repo-local
dogfood runbook when present:

1. Write the phase artifact.
2. Record the capture envelope and artifact references.
3. Run the configured review gate.
4. Incorporate required changes and rerun if blocked.
5. Handle optional improvements or defer them to named beads.
6. Move to the next phase only after the gate passes.

For `/Users/jv/projects/etude`, the gate is the three-reviewer process in
`docs/plans/dogfood/review-gate-runbook.md`.

## Auto Mode

Auto mode replaces manual approval prompts with the configured review gate. In
repos without a project-specific gate, Codex may be used as the evaluator. In
the `etude` dogfood repo, use the three-reviewer gate for every phase.

See `reference/auto-mode.md` for details.

## Five Phases

Every bead goes through all five phases. No skipping. A phase may record "not
applicable" for tests, manual testing, or docs, but it still produces an
artifact and gate result.

### Phase 1: Plan

**Agent**: `dev-planner` (opus)

1. Understand requirements from bead description and dependencies.
2. Explore the codebase and relevant docs.
3. Design the implementation approach.
4. Identify files to modify/create.
5. Plan tests and docs work.
6. Assess risks.
7. Write the plan to the bead `--design` field.
8. Record capture/provenance details.
9. Run the phase gate before implementation.

**Output**:

```markdown
## Plan for <bead-id>: <title>

**Approach**: <how you'll solve it>

**Files**:
- path/to/file.ts - <what changes>

**Tests**:
- <what will be tested, or why no test change is needed>

**Docs**:
- <docs to update, or why docs are not needed>

**Risks**: <concerns, or "None">

**Capture**:
- inputs: <bead fields, dependencies, relevant files>
- output: bead design field
```

### Phase 2: Implement

**Agent**: `dev-executor` (sonnet) coordinates:
- `dev-coder` (sonnet) - writes and refactors implementation code

Implementation does not own final test adequacy or QA. It may run focused
tests for fast feedback, but the externally visible test/QA decision happens
in Verify.

1. Implement the approved plan.
2. Refactor touched code for maintainability.
3. Run focused checks when useful.
4. Update bead `--notes` with implementation summary and changed files.
5. Record capture/provenance details.
6. Run the phase gate before Verify.

Do not commit yet unless the local workflow requires an implementation
checkpoint. The normal bead commit happens after Final Review so docs and
review fixes are included in one atomic commit.

**Output**:

```markdown
## Implement

**Implemented**:
- <what was built>
- <key decisions>

**Checks run**:
- <commands and results, or "not run">

**Changed files**:
- <paths>

**Capture**:
- inputs: approved plan and prior capture envelope
- output: working tree diff and bead notes
```

### Phase 3: Verify

**Agent**: `dev-qa` (opus) owns synthesis and may coordinate:
- `dev-test-writer` (sonnet) - writes/updates focused automated tests
- `manual-test` skill - runs manual/browser checks when relevant

Verify answers one question: **is the implementation ready for Docs and Final
Review?**

1. Review implementation against the approved plan.
2. Add or update focused automated tests when useful.
3. Run relevant automated tests.
4. Decide whether manual testing is required; run it when needed.
5. Assess coverage, correctness, regressions, and code quality.
6. Produce one Verify artifact with status `pass`, `fail`, or `blocked`.
7. Record capture/provenance details.
8. Run the phase gate.

If implementation code must change, Verify returns `fail` and the bead moves
back to Implement. Do not hide implementation fixes inside Verify.

**Output**:

```markdown
## Verify

Status: pass | fail | blocked
Recommendation: proceed to Docs | return to Implement | needs input

Automated tests:
- changes: <files, or none with reason>
- commands: <commands run>
- result: <pass/fail summary>

Manual tests:
- required: yes | no
- reason: <why>
- result: <summary or not applicable>

QA findings:
- correctness:
- coverage:
- regressions:
- code quality:
- risks:

Capture:
- inputs: implementation artifact, approved plan
- output: append-only bead note/comment
```

### Phase 4: Docs

**Agent**: `dev-docs-writer` (sonnet)

Docs happen after Verify and before Final Review, so the reviewer can evaluate
the completed bead including documentation.

1. Determine whether user-facing docs changed.
2. Update implemented docs only for behavior that actually works.
3. Keep future work, design sketches, and open decisions under planning docs.
4. Update generated command references separately from hand-written guides.
5. Record docs summary, changed docs paths, and capture/provenance details.
6. Run the phase gate.

If no docs are needed, record that decision and rationale as the Docs artifact.

**Output**:

```markdown
## Docs

**Changed**: yes | no
**Files**:
- <docs paths, or "none">

**Policy check**:
- shipped docs describe implemented behavior only
- plans remain under docs/plans

**Capture**:
- inputs: verified implementation and docs policy
- output: docs diff or no-docs rationale
```

### Phase 5: Final Review

**Agent**: `dev-pr-reviewer` (opus)

Final Review is bead-level readiness review. It is not a substitute for the
proposal/PR review that may happen later across multiple beads.

1. Review Plan, Implement, Verify, and Docs artifacts.
2. Check the working tree diff against the approved plan.
3. Verify docs correctness and docs policy compliance.
4. Check for regressions, accidental files, debug code, and commit quality.
5. Produce a close recommendation.
6. Record capture/provenance details.
7. Run the phase gate.
8. If the gate passes with a close recommendation, the parent workflow commits,
   removes `phase:review`, adds `phase:complete`, closes the bead, pushes git,
   and pushes bead storage.

Final Review trusts the Verify artifact and does not rerun the test suite by
default. It may request more verification if Verify is missing, inconsistent,
or contradicted by later changes.

**Output**:

```markdown
## Final Review

**Recommendation**: close | return to Plan | return to Implement | return to Verify | return to Docs

**Reviewed**:
- plan artifact
- implementation diff
- verify artifact
- docs artifact

**Findings**:
- <bugs, risks, missing docs/tests, or "None">

**Commit**:
- <hash and message after commit>
```

## State Storage

| Artifact | Storage | Updated By |
|---|---|---|
| Plan | bead `--design` field | dev-planner |
| Implement summary | bead `--notes` field plus working tree diff | dev-executor |
| Verify artifact | append-only bead note/comment | dev-qa |
| Docs artifact | docs diff plus append-only bead note/comment | dev-docs-writer |
| Final Review | append-only bead note/comment | dev-pr-reviewer |
| Capture/provenance | append-only bead note/comment | phase owner |
| Phase | labels (`phase:*`) | phase owner |

## Labels

- `phase:plan` - Currently planning
- `phase:implement` - Currently implementing
- `phase:verify` - Currently verifying tests/QA/manual checks
- `phase:docs` - Currently updating or validating docs
- `phase:review` - Currently in bead-level final review
- `phase:complete` - Bead finished

## Sub-Agents

| Agent | Model | Purpose |
|---|---|---|
| dev-planner | opus | Design implementation approach |
| dev-executor | sonnet | Coordinate implementation |
| dev-coder | sonnet | Write and refactor implementation code |
| dev-test-writer | sonnet | Write and run focused tests inside Verify |
| dev-qa | opus | Synthesize Verify result |
| dev-docs-writer | sonnet | Update docs before Final Review |
| dev-pr-reviewer | opus | Review completed bead including docs |

**Spawning**: Always use Task tool directly, never CLI.

```python
Task(subagent_type="dev-planner", model="opus", prompt="...")
Task(subagent_type="dev-coder", prompt="...")
Task(subagent_type="dev-qa", model="opus", prompt="...")
```

## Bead Types

| Type | Use Case |
|---|---|
| `feature` | New functionality |
| `bug` | Fix broken behavior |
| `task` | Chores, refactoring, docs, tests |

## Error Handling

- **Plan blocked**: revise plan, rerun Plan gate.
- **Implement blocked**: address required changes, rerun Implement gate.
- **Verify fails**: record Verify `fail`, move back to Implement.
- **Verify blocked**: request missing input/tooling, rerun Verify after it is supplied.
- **Docs blocked**: fix docs or record no-docs rationale, rerun Docs gate.
- **Final Review blocked**: return to the named phase and capture a new attempt.
- **Agent fails**: retry up to 2 times, then report the failure as a process blocker.

## Phase Detection

From bead labels:

```text
No phase:* label     -> Check fields
phase:plan          -> In Plan
phase:implement     -> In Implement
phase:verify        -> In Verify
phase:docs          -> In Docs
phase:review        -> In Final Review
phase:complete      -> Done
```

From fields if no label exists:

```text
Empty --design           -> Start Plan
Plan pending             -> Resume Plan gate
Plan gated, no Implement -> Start Implement
Implement pending        -> Resume Implement gate
Implement gated, no Verify -> Start Verify
Latest Verify pending    -> Resume Verify gate
Latest Verify pass gated -> Start Docs
Latest Verify fail gated -> Start Implement unless a newer Implement gated attempt exists
Latest Verify blocked gated -> Wait for missing input, then resume Verify
Latest Docs pending      -> Resume Docs gate
Docs gated, no Final Review -> Start Final Review
Latest Final Review pending -> Resume Final Review gate
Latest Final Review returns to a phase and no newer target-phase attempt exists -> Start that phase
Latest Plan gated and newer than Final Review return -> Start Implement
Latest Implement gated and newer than Final Review return -> Start Verify
Latest Verify pass gated and newer than Final Review return -> Start Docs
Latest Verify fail gated and newer than Final Review return -> Start Implement
Latest Verify blocked gated and newer than Final Review return -> Wait for input, then resume Verify
Latest Docs gated and newer than Final Review return -> Start Final Review
Final Review recommends close -> Commit and close
```

When returning from Verify `fail` to Implement, keep the failed Verify artifact
append-only and write a new Implement attempt. Phase detection must use the
latest attempt per phase, not merely the existence of any Verify section:

```text
Latest gated Verify is fail and no newer Implement attempt exists -> Start Implement
Latest gated Implement attempt is newer than latest failed Verify -> Start Verify
```

The same superseding rule applies to Final Review return recommendations. A
newer attempt for the returned-to phase supersedes the old Final Review return
artifact for resume detection, and routing proceeds from that newer attempt.
Final Review return recommendations are acted on only after the Final Review
gate passes.

## Integration with Other Commands

| Before | After |
|---|---|
| `/scope` | `/dev` (creates beads to work on) |
| `/dev` (all beads done) | `/ship` (proposal integration and PR work) |

Integration remains an epic/proposal-level phase when multiple beads need to
be checked together before shipping.

## Quality Standards

See `reference/quality-standards.md` for:
- Test coverage expectations
- Refactoring requirements
- Docs verification expectations
- Code quality checks
- Commit standards
