# Dev Workflow Phases

## Per-Bead Phases

Each bead goes through these five phases:

1. Plan
2. Implement
3. Verify
4. Docs
5. Final Review

Each phase produces a reviewable artifact and records capture/provenance
details before advancement.

## Phase 1: Plan

### Purpose

Understand the problem, explore the codebase, and design the approach before
writing code.

### Process

1. Understand the bead description, dependencies, and acceptance criteria.
2. Explore relevant files, tests, and docs.
3. Decide the implementation strategy.
4. Identify files to create or modify.
5. Plan automated tests, manual tests if likely relevant, and docs work.
6. Assess risks and unknowns.
7. Write the plan to the bead `--design` field.
8. Record capture/provenance details.
9. Run the configured phase gate.

### Plan Must Include

- Approach
- Files
- Tests
- Docs
- Risks
- Capture references

### Labels

Remove stale phase labels and add `phase:plan` when entering this phase.

---

## Phase 2: Implement

### Purpose

Implement the approved plan with focused, maintainable changes.

### Process

1. Write the implementation following the approved plan.
2. Refactor touched code where it improves maintainability.
3. Run focused checks when useful.
4. Record implementation notes and changed files.
5. Record capture/provenance details.
6. Run the configured phase gate.

### Implement Must Include

- Implementation matching the approved plan
- Explanation for any plan deviations
- Changed-file list
- Focused check results, when run

### Boundaries

Implementation does not own final test adequacy, manual testing, or QA
synthesis. Those happen in Verify. If Verify finds implementation defects,
capture a Verify `fail` and return to Implement.

### Labels

Remove stale phase labels and add `phase:implement` when entering this phase.

---

## Phase 3: Verify

### Purpose

Decide whether the implementation is ready for Docs and Final Review.

### Process

1. Review implementation against the approved plan.
2. Add or update focused automated tests when useful.
3. Run relevant tests.
4. Decide whether manual testing is required and run it when needed.
5. Assess correctness, coverage, regressions, and code quality.
6. Produce one Verify artifact with status `pass`, `fail`, or `blocked`.
7. Record capture/provenance details.
8. Run the configured phase gate.

### Verify Status

- `pass`: proceed to Docs after the gate passes.
- `fail`: return to Implement after the gate passes on the failure artifact.
- `blocked`: remain in Verify while missing input/tooling is gathered.

After a `fail`, the failed Verify artifact remains append-only. The next
Implement attempt supersedes that failure for phase detection; once the new
Implement gate passes, resume with a fresh Verify attempt.

### Verify Must Include

- Automated test changes, commands, and results
- Manual-test decision and results when relevant
- QA findings
- Recommendation derived from status
- Capture references

### Labels

Remove stale phase labels and add `phase:verify` when entering this phase.

---

## Phase 4: Docs

### Purpose

Update or explicitly validate documentation before Final Review.

### Process

1. Determine whether docs are required.
2. Update shipped docs only for implemented behavior.
3. Keep planning material under planning docs.
4. Keep generated command references separate from hand-written guides.
5. Record a docs artifact, even if the artifact is a no-docs-needed rationale.
6. Record capture/provenance details.
7. Run the configured phase gate.

### Docs Must Include

- Docs changed: yes/no
- Paths changed or no-docs rationale
- Policy check for shipped-vs-planned behavior
- Capture references

### Labels

Remove stale phase labels and add `phase:docs` when entering this phase.

---

## Phase 5: Final Review

### Purpose

Review the completed bead, including docs, before close.

### Process

1. Review Plan, Implement, Verify, and Docs artifacts.
2. Inspect the final diff.
3. Confirm docs are accurate and in the correct location.
4. Check for regressions, accidental files, debug code, and scope drift.
5. Produce a close or return recommendation.
6. Record capture/provenance details.
7. Run the configured phase gate.
8. Commit, close the bead, push git, and push bead storage.

### Final Review Must Verify

- Work matches the approved plan or explains deviations.
- Verify result is adequate and not contradicted by later changes.
- Docs are complete and policy-compliant.
- The commit will be atomic and well-scoped.
- No accidental files or debug code are present.

### Labels

Remove stale phase labels and add `phase:review` when entering this phase. The
parent workflow removes `phase:review` and adds `phase:complete` after the
Final Review gate passes, the commit is created, and the bead is closed.

---

## Epic Or Proposal Integration

After all task beads complete, a proposal or epic may still need integration
and PR review. That work belongs to `/ship` or the relevant proposal workflow,
not to a single bead's Final Review.

Integration should verify:

- Completed beads work together.
- Integration/e2e checks pass when relevant.
- Requirements and acceptance criteria are satisfied.
- PR-level docs, changelog, and generated references are correct.
- The branch is ready for external review.

---

## Phase Transitions

```text
Plan
  -> Implement
  -> Verify
       pass    -> Docs
       fail    -> Implement
       blocked -> Verify after missing input is supplied
  -> Docs
  -> Final Review
       close recommendation -> commit and close bead
       return recommendation -> named earlier phase
```

## Phase Detection

Use the latest attempt for each phase, not just the existence of any section:

```text
No dev notes section          -> Start Plan
Plan pending                  -> Resume Plan gate
Plan gated, no Implement      -> Start Implement
Implement pending             -> Resume Implement gate
Implement gated, no Verify    -> Start Verify
Implement gated and newer than failed Verify -> Start Verify
Verify pending                -> Resume Verify gate
Verify fail and gated         -> Start Implement
Verify blocked and gated      -> Wait for missing input, then resume Verify
Verify pass and gated, no Docs -> Start Docs
Docs pending                  -> Resume Docs gate
Docs gated, no Final Review   -> Start Final Review
Final Review pending          -> Resume Final Review gate
Final Review returns to Plan and no newer Plan -> Start Plan
Final Review returns to Implement and no newer Implement -> Start Implement
Final Review returns to Verify and no newer Verify -> Start Verify
Final Review returns to Docs and no newer Docs -> Start Docs
Plan gated and newer than Final Review return -> Start Implement
Implement gated and newer than Final Review return -> Start Verify
Verify pass gated and newer than Final Review return -> Start Docs
Verify fail gated and newer than Final Review return -> Start Implement
Verify blocked gated and newer than Final Review return -> Wait for input, then resume Verify
Docs gated and newer than Final Review return -> Start Final Review
Final Review recommends close -> Commit and close bead
```

Earlier artifacts remain append-only. Resume detection compares latest attempt
order or timestamps so a newer returned-to phase attempt supersedes an older
Final Review return recommendation.
Prefer explicit attempt numbers. When attempt numbers are missing, use bead
note append order or note timestamps as the practical ordering source.
