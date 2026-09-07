# Developing Etude

This is the current development contract for this repository. It supersedes the
five mandatory phase gates in the historical dogfood runbooks and the generic
`etude-loop` instructions wherever they conflict. Both profiles use the same
acceptance criteria, documentation obligations and quality bar.

## Choose a profile and a bounded task

Use `dev-claude` for a Claude-led lane or `dev-codex` for a Codex-led lane. The
executable profiles are `.etude/workflows/dev-claude.yaml` and
`.etude/workflows/dev-codex.yaml`; `.etude/workflow.yaml` defaults to dev-codex.
Review model identities and invocations live in `.etude/registry.yaml`.

| Responsibility | dev-claude | dev-codex |
| --- | --- | --- |
| Scope, risk classification, difficult decisions | Fable 5.1 | Astra |
| Bounded implementation, tests and docs | Sonnet | Terra |
| More demanding implementation | Opus | Sol |
| Architectural uncertainty or persistent failure | Fable 5.1 | Astra |
| Routine independent final review | Sol | Sonnet |
| Consequential final review and high-risk plan review | Fresh Astra + Fable | Fresh Astra + Fable |

The host selects and launches actual workers; these assignments are policy, not
engine-managed model routing. Use inexpensive context gathering only when it
provides a useful independent task. Keep one worker through code, tests and docs.
Difficulty selects the worker; the consequence of an error selects review depth.
Record the model that actually ran, including any explicit substitution.

Track work in beads. In an isolated Git worktree, use the main checkout's beads
store instead of initializing another database. The launcher may write the
canonical `.beads` path to ignored `.beads/redirect`; verify `bd show <bead>`
works before assigning work. `bd prime` alone does not verify database health.
Keep one bead's changes in one commit, with separate worktrees for parallel lanes.

## Read documentation before planning

Read relevant guides in `docs/`, the affected component's technical explanation,
contracts, tests and source. Check important documentation claims against the
implementation. Distinguish stale docs from a code defect before choosing which
to change. Plans belong in `docs/plans/` or captured run artifacts, not shipped
usage documentation.

The plan identifies acceptance criteria, invariants, affected components, failure
cases, documentation updates and a verification method for each requirement.
High-risk designs (storage integrity, authorization, lifecycle, destructive or
broad compatibility changes) get an independent Astra/Fable plan gate. Routine
plans need no model gate. A consequential but understood fix can proceed directly
to implementation with a strong final gate.

## Implement code, tests and technical docs together

Maintain technical documentation in the same delivery as code: changed contracts,
component responsibilities, data flow, failure/recovery behavior, configuration
and significant design decisions. Include usage examples for observable changes.
Generated command references supplement maintained explanations.

Every change needs a documentation assessment. If it restores already-documented
behavior, record the relevant paths and why no edit is necessary. Never defer
necessary docs merely to ship code. Update or mark affected planning notes as
superseded when behavior ships. Do not turn a focused fix into documenting the
whole repository.

## Independent QA and verification

The implementer adds meaningful regression tests and runs checks. Demonstrate the
reported failure before the fix where practical. A fresh QA worker inspects the
requirements, actual code/docs and test coverage, rather than accepting the
implementer's summary. QA may add focused tests; production fixes return to the
implementation worker and invalidate affected evidence. A routine final reviewer
may also perform QA if its report distinguishes the independent assessment.

| Check | When required |
| --- | --- |
| `make test` and `make lint` | Code changes |
| `make shell-test` | Shell scripts, hooks or process changes; this suite is separate from Go tests |
| `make docs-check` | CLI/reference changes and final integration |
| `make docs-reality` | Changed documentation claims and final integration |
| `go test -race ./...` or affected packages | Concurrency/lifecycle changes |
| Fresh binary in temporary realistic repositories | Git, CLI, artifact or subprocess behavior changes |

Build with `make build`, and exercise `bin/etude` by its absolute path. Do not
replace the installed binary unless installation itself is under test. Check
exit status, output, filesystem/ref effects and failure/recovery behavior. For
security boundaries, actually attempt both allowed and denied operations.

Capture one verification record for the exact evaluated revision/artifact:
acceptance criterion to evidence; commands, environment and results; realistic
product exercises; docs updated or unchanged rationale; independent QA findings;
and status `pass`, `fail` or `blocked`. Missing evidence is not a pass. Later
changes require rerunning affected checks; integration checks cover interactions
between independently verified lanes. A configured check is not evidence it ran.

## Capture and review

These profiles support **externally supervised capture/gate workflows**. They
intentionally have no stage runner bindings: `etude run` is not an autonomous
implementation driver for them. The supervisor launches workers and checks,
captures their outputs, and invokes only the selected gates. Conditional risk
routing and delivery completeness are supervisor responsibilities, not native
engine enforcement.

Capture plan, implementation (`diff`), verification (`verify`) and documentation
(`docs-diff`) artifacts for every task. A docs artifact can carry a justified
unchanged-docs assessment. Then capture a final **review packet**, not a prior
reviewer's verdict. It contains requirements, plan, exact revision and diff,
relevant full source/contracts, documentation and independent QA evidence. Both
reviewers receive the same complete packet in fresh contexts. Claude reviewers
have tools disabled, so a path without its contents is not sufficient evidence.
Codex reviewers also have a read-only sandbox; config `mode` alone is not an
access-control mechanism.

Example commands, using an existing plan file and a new named run:

```sh
bin/etude capture plan --run etude-3343-dev-claude \
  --workflow dev-claude --output plan=.etude/tmp/etude-3343/plan.md
bin/etude gate --run etude-3343-dev-claude --workflow dev-claude \
  --stage plan --artifact .etude/tmp/etude-3343/plan.md --timeout 20m
```

Run the plan gate only for high-risk designs. For the final gate, choose exactly
one of `review` (consequential, L2) or `routine-review` (routine, L3 for dev-claude,
L4 for dev-codex), capturing the matching output role before gating it. Supply
`--harness`, `--model` and `--git-sha` when capturing actual agent work. Always
recapture changed bytes before a review, preserving earlier attempts.

Every configured gate explicitly requires `pass_threshold: 1`. L2 means both
Astra and Fable must pass. Auth failures, timeout, missing tools or truncated
output leave review incomplete. Require concrete blockers with a trigger,
consequence and evidence; optional suggestions can be deferred with a reason.
Investigate disagreements with source or a reproduction. After two unsuccessful
substantive revision rounds, surface the unresolved issue rather than polling for
approval. `max_rounds: 3` bounds engine-managed attempts; the supervisor also
bounds separately invoked gate attempts.

Reviewer-only environment names are declared on registry seats. At execution,
Etude combines those names with the workflow allowlist **only for review seats**,
including invocation fallbacks. The same named seat used as a stage/check does
not receive reviewer-only variables. Names must be valid, unique and non-reserved;
credential values are resolved at runtime and are never configuration values.
The workflow permits HOME for normal tooling; it does not share Claude OAuth or
Codex profile variables with project commands. This narrows environment exposure;
it is not an operating-system credential vault or isolation from all home files.

## Finish and monitor

Ship only when acceptance criteria, relevant checks, technical documentation and
the selected independent review are complete. The supervisor inspects actual
logs, exit status, captured identities and run records, repairs process failures
within scope, and never synthesizes a passing verdict.

`.etude/run-map.tsv` maps a bead to its active run when a new profile needs a new
run identity. An explicit mapping overrides the legacy run named after the bead.
The completeness audit checks that mapped run exists and has gate attempts, and
that metadata refs are pushed. It does not prove phase completeness or passing
verdicts; the supervisor must check those before closing work. Existing unmapped
beads retain the legacy lookup. Record the run mapping in bead notes as well.

Sync metadata with `etude sync`, push the branch explicitly, and sync beads with
`bd dolt push`. The historical cadence-retro warning remains useful feedback,
not an extra model gate. Review recurring process faults and update the relevant
repo instructions or shared skill rather than repeating a workaround silently.
