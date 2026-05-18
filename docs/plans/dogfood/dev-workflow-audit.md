# Dev Workflow Audit

Status: planning note. This audits the current Claude dev agents before etude
implementation starts.

## Desired Shape

Use five gated phases for each bead:

1. **Plan** - produce the implementation plan.
2. **Implement** - produce the code or planning artifact.
3. **Verify** - test, manual test, and QA the produced artifact.
4. **Docs** - update documentation after verification, before review.
5. **Final review** - review the complete bead including docs.

This is the workflow etude should dogfood while it is being built. Each phase
needs a reviewable artifact and a review gate. The current dogfood gate is the
three-reviewer process defined in [Review Gate Process](review-gate-process.md).

## Current Agent Inventory

| Agent or skill | Current purpose | Current artifact |
|---|---|---|
| `dev-planner` | Explores a bead and writes an implementation plan. | Bead `--design` field. |
| `dev-executor` | Coordinates code and tests, then commits. | Bead `--notes` field plus git commit. |
| `dev-coder` | Writes and refactors implementation code. | Working tree diff. |
| `dev-test-writer` | Writes and runs automated tests. | Test files and test output returned to executor. |
| `manual-test` skill | Runs manual test files through Playwright. | Manual test result output; optional bug beads. |
| `dev-qa` | Runs tests, checks coverage/functionality/code quality, records results. | Bead comment. |
| `dev-docs-writer` | Updates docs when needed, usually during ship/integration. | Docs diff and summary. |
| `dev-integrator` | Runs integration checks, verifies specs, updates docs/changelog. | Proposal bead comment. |
| `dev-pr-reviewer` | Reviews branch readiness before PR. | Proposal bead comment. |
| `dev-workflow` skill | Orchestrates `PLAN -> EXECUTE -> QA`. | Bead fields, labels, comments, commit. |

## Gaps

### 1. Docs Happen Too Late

`dev-docs-writer` is currently invoked during integration/ship, while
`dev-pr-reviewer` performs final review. That means review can catch missing
docs, but it cannot reliably review docs as part of the completed bead unless
docs were already written.

Target behavior: docs become a bead phase before final review. The final
reviewer should verify docs accuracy, not merely detect that docs are missing.

### 2. Test Writing And QA Overlap

`dev-test-writer` writes and runs tests during execution. `dev-qa` then runs
tests again, assesses coverage, checks functionality, and reviews code quality.
This creates a blurry boundary:

- test-writer owns tests and initial test output
- QA owns test adequacy and quality review
- both may run the same suite

This should be consolidated or made explicit. The likely target is a single
**Verify** phase that owns automated tests, manual tests when relevant, coverage
judgment, functionality checks, and quality review. It may still use multiple
agents internally, but it should produce one clear verification artifact.

### 3. Manual Testing Is Not Integrated

The `manual-test` skill is useful but external to the bead workflow. The
workflow needs a rule for when manual tests are required, where manual test
plans live, and how results get attached to a bead.

Target behavior: manual testing is optional but belongs under Verify. When used,
it should produce an append-only bead comment and bug beads for failures.

### 4. Final Review Is Proposal-Oriented

`dev-pr-reviewer` is currently written for proposal/branch review. That remains
useful at ship time, but dogfooding etude needs a bead-level final review as
well.

Target behavior: each bead gets a final review artifact before close. Proposal
review can still happen later across all completed beads.

### 5. Artifact Capture Is Implicit

The current workflow stores useful artifacts, but not under a single explicit
capture contract. Etude needs the first draft outputs to survive.

Target behavior: every phase names its artifact, storage location, and whether
the artifact is mutable or append-only.

## Recommended Dogfood Artifact Contract

Every phase should record both the reviewable artifact and a provenance
envelope. Reproducibility needs the exact inputs, the git state, and the
agent/session that produced the output.

Minimum provenance per phase:

- `phase`: one of `plan`, `implement`, `verify`, `docs`, `review`.
- `started_git_sha`: `git rev-parse HEAD` at phase start.
- `ended_git_sha`: `git rev-parse HEAD` at phase end, or same as start if no
  commit changed HEAD.
- `dirty_state`: concise `git status --short` snapshot at phase start and end.
- `inputs`: stable references to all phase inputs, including bead ID, bead
  title/description, dependency IDs, approved prior artifacts, file paths,
  command outputs, and any prompt text given to a subagent.
- `runner`: the tool or agent used, such as `codex`, `claude`,
  `dev-planner`, `dev-coder`, `dev-docs-writer`, `gemini`, or manual human.
- `runner_version`: best available version string, model name, or skill
  revision.
- `session_refs`: pointers to actual logs: Claude transcript/session ID, Codex
  conversation/log path, Gemini log path, tmux pane capture, or command log.
- `outputs`: artifact references produced by the phase.
- `approval_surface`: where the artifact and reviewer results were presented,
  such as a tmux pane, Codex chat message, PR comment, local file, or another
  configured review surface.
- `review_results`: Gemini Pro, Claude Opus, and fresh GPT-5.5 xhigh reviewer
  results for the gate.

The workflow should require a passing review gate, not a specific UI surface.
In this repo's current dogfood session, tmux pane `.2` can be the chosen
approval surface, but the protocol should allow other setups to use a different
target.

| Phase | Input | Output artifact | Temporary storage |
|---|---|---|---|
| Plan | Bead title, description, dependencies, codebase context, starting git state | First implementation plan plus provenance envelope | Bead `--design`; provenance in append-only bead comment; review through the configured gate. |
| Implement | Approved plan and recorded plan provenance | Diff, implementation summary, commit if code changed, implementation provenance | Git diff/commit, bead `--notes`, append-only provenance comment. |
| Verify | Implementation artifact, approved plan, implementation provenance | Test output, QA findings, manual test results if relevant, verification provenance | Append-only bead comment. |
| Docs | Verified implementation, verification output, docs policy | Docs diff, docs summary, docs provenance | Git diff plus append-only bead comment. |
| Final review | Plan, implementation, verify output, docs diff, all phase provenance | Review findings, close recommendation, review provenance | Append-only bead comment; review through the configured gate. |

For planning-only beads, the implementation artifact can be a planning note
under `docs/plans/dogfood/` or `docs/plans/product/`, depending on whether it
describes the dogfood process or product design. That is still a real artifact,
but it must not be promoted to user-facing docs.

## Recommended Workflow Changes

1. Update `dev-workflow` from `PLAN -> EXECUTE -> QA` to
   `PLAN -> IMPLEMENT -> VERIFY -> DOCS -> FINAL REVIEW`.
2. Add a review gate after every phase. For the current dogfood process, the
   gate requires clear `GO` from Gemini Pro, Claude Opus, and a fresh GPT-5.5
   xhigh agent. The review artifact and results should be presented on a
   configured approval surface; for the current session that surface may be
   tmux pane `.2`, while other setups can choose chat, PR comments, files, or
   another local UI.
3. Require every phase to write a provenance envelope with reproducible inputs,
   starting git hash, ending git hash, runner identity, and session/log
   references.
4. Rename labels to match the five phases:
   `phase:plan`, `phase:implement`, `phase:verify`, `phase:docs`,
   `phase:review`, `phase:complete`.
5. Change `dev-docs-writer` so it runs before final review and follows this
   repo's docs policy: `docs/` is implemented behavior only;
   `docs/plans/` is planned work.
6. Change `dev-pr-reviewer` so docs correctness is part of final review.
7. Keep `dev-test-writer` and `dev-qa` as separate specialist workers for now,
   but make them internal to one externally visible Verify phase. The Verify
   phase should produce one approval artifact that includes test changes, test
   output, QA findings, manual test results when used, a `pass | fail |
   blocked` status, and a recommendation derived from that status. Revisit
   merging the agents only after the unified Verify artifact has been
   dogfooded.
8. Add manual test integration as a Verify subpath, not a separate top-level
   phase by default.

## Follow-Up Beads

- `etude-consolidate-test-qa` should decide the internal Verify phase design.
- `etude-dogfood-capture-protocol` should turn the artifact contract above
  into a repeatable capture checklist.
- `etude-overhaul-dev-workflow-skill` should update the workflow skill after
  those decisions are approved.
- `etude-update-docs-agent` should update docs guidance.
- `etude-update-review-agent` should update final review guidance.
