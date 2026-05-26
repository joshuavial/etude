# Retro Impact Ledger

Status: dogfood planning material (not shipped user docs). An INVENTORY — not a
new retro and not re-analysis — of every retrospective performed during the
`etude` dogfood effort and the concrete improvements each produced.

## How to read this

Retro lessons on this project take **three forms**, because most retros did NOT
produce a standalone `retro.md`:

- **Form A** — standalone retro docs (and retro-related planning docs).
- **Form B** — runbook-rule retros: a retro/real-spiral lesson that landed as
  rule additions to `review-gate-runbook.md` (+ sometimes scripts/skill files)
  and reset the autonomous-loop retro cadence.
- **Form C** — the docs-reality-drift retro, captured from the bead trail.

**Coverage method:** Form B is enumerated from **every rule-adding commit to
`docs/plans/dogfood/review-gate-runbook.md`** (`git log -- docs/plans/dogfood/review-gate-runbook.md`),
NOT a `grep -i retro` of subject lines (which misses lessons whose subject lacks
the word "retro"). Every such commit is either an inventoried record below or
named in the **Inclusion boundary**.

Each record: **Trigger/date/scope · Evidence · Findings · Recommended changes ·
Landed (`[IMPLEMENTED]`/`[PLANNED]` per change) · Follow-up beads · Remaining.**

---

## Form A — standalone retro docs / retro-related planning

### A1. Early dogfood workflow retro
- **Trigger/date/scope:** end of the agent-workflow-audit / consolidate-test-qa
  work (early dogfood setup). Scope: the dev workflow + review process.
- **Evidence:** `docs/plans/dogfood/dogfood-process-retro.md` (top section);
  commits 9965869, a22cc44, 0b71ca8.
- **Findings:** process drift (human gate → 3-reviewer), stale Verify-phase
  language, inline `bd update` backtick failures, Opus seat-role confusion.
- **Recommended changes / Landed:** gate execution checklist `[IMPLEMENTED]`;
  file/stdin `bd update` rule `[IMPLEMENTED]`; exact-artifact reviewer payloads
  `[IMPLEMENTED]`; optional-improvements-mandatory-before-advance `[IMPLEMENTED]`;
  reviewer run-status surfacing `[IMPLEMENTED]`; explicit reviewer-seat prompts
  `[IMPLEMENTED]` (all in review-gate-runbook.md / review-gate-process.md).
- **Follow-up beads / Remaining:** none open — the safe-bead-update rule landed
  in the runbook's "Safe Bead Updates" section and the dev-workflow skill
  delegates to that runbook. `[IMPLEMENTED]` (The separate scope-fence rule, B5,
  is a distinct later improvement.)

### A2. Gate-auth + four-seat expansion retro (2026-05-23)
- **Trigger/date/scope:** a nested `claude -p` reviewer hit a 401 auth failure,
  and a 3-seat panel proved too thin (codex caught a BLOCK the others missed).
- **Evidence:** dogfood-process-retro.md (lower section); commits 8f03314
  ("expand dogfood review gate to four reviewers") + 7d6b702 ("fix pi
  reviewer-seat invocation to use read-only tools"); review-gate-runbook.md,
  review-gate-process.md, verify-phase-design.md, capture-protocol.md.
- **Findings:** in-session `claude -p` can't auth; three seats miss real findings.
- **Recommended changes / Landed:** in-harness Claude Task-seat rule
  `[IMPLEMENTED]` (8f03314); pi/pilms 4th seat with a read-only tool allowlist
  `[IMPLEMENTED]` (7d6b702).
- **Remaining:** none (the four-seat gate has been the default since).

### A3. Retros as a first-class artifact (planning)
- **Scope:** `docs/plans/product/retrospectives.md` — design/PLANNING for capturing
  retros as etude artifacts. Not an executed retro; listed as retro-related.
- **Landed:** `[PLANNED]` → open bead **etude-14r** (see no-retro-stage section).

### A4. `etude retro` command (planning)
- **Scope:** `docs/plans/product/etude-retro-command.md` (commit a3334a1) — the
  concrete `etude retro` CLI + `refs/etude/retros/*` design / implementation plan
  for etude-14r. Not an executed retro.
- **Landed:** `[PLANNED]` → open bead **etude-14r**.

---

## Form B — runbook-rule retros

All Form-B changes are `[IMPLEMENTED]` (they ARE the committed runbook rules).

### B1. Loop retro (2026-05-24) — commit 7930450
- **Findings/changes:** plan must verify external-tool semantics empirically;
  docs must capture real built-binary output; gates are risk-tiered (Tier 1/2/3).
- **Artifacts:** dev-planner.md, dev-docs-writer.md, dev-workflow SKILL.md,
  review-gate-runbook.md ("Gate Weight").

### B2. Gate-spiral hardening cluster (2026-05-25)
- **Scope:** the real-spiral lessons that built the runbook's per-seat-sandbox,
  tooling-outage, and codex-caveat sections.
- **Evidence/changes:** 6045147 (reviewer seats NEVER revert the working tree
  mid-gate — a seat's mutation-test revert once wiped producer wiring out of
  `internal/cli/capture.go`); bf3d755 (per-seat sandbox constraints + the gemini
  GrepTool cross-file-bleed caveat); cc32afb (codex diff-only-from-first-attempt
  + the autonomous-loop tooling-outage fallback); a262870 (codex large-input
  ~1000+ line verdict truncation). Artifacts: review-gate-runbook.md,
  capture-protocol.md.
- **Follow-up beads:** **etude-ojz** — provision ripgrep to fix the gemini
  GrepTool cross-file bleed (originates from bf3d755). `[IMPLEMENTED]` → etude-ojz.

### B3. Phase 3 retro (2026-05-25) — commits 079aab3 + 25565d9
- **Findings/changes:** dogfood-capture script + gate-tooling fixes (gemini
  inline-from-first-attempt, pi 0-CPU-hang diagnosis, debug-flake-on-2nd-occurrence)
  (079aab3); ref-mutating manual tests must run in a throwaway repo + the codex
  doc-gate "reason only from the inlined note" rule (25565d9).
- **Evidence lesson:** the etude-bench-command cohort-recursion bug (bench
  re-benchmarking its own replays) surfaced ONLY from running the built binary
  twice. Artifacts: scripts/dogfood-capture.sh, capture-protocol.md, runbook.

### B4. Phase 4 retro (2026-05-25) — commit a74ded5
- **Findings/changes:** /tmp snapshots must preserve directory structure
  (`cp --parents`, after a basename collision); the conceptual-contradiction vs
  missing-detail plan-fix rule (author a contradiction's fix directly, don't
  round-trip it open-ended).
- **Evidence lesson:** etude-gc-command's two wasted planner round-trips on the
  "what does `--prune` delete?" contradiction.

### B5. Cadence retro, post-roadmap.2.2 (2026-05-25/26) — commits 59fe049 + 24d64f2
- **One retro, two rule-commits.** 59fe049: seat-disagreement-resolution (prefer
  the consensus-safe option; "it works" ≠ "the right surface") + the
  no-conditional-plan rule. 24d64f2: implement-scope-discipline / **scope-fence**
  (1 bead = 1 commit; the orchestrator scope-checks the working tree against the
  plan's Files list; sub-agents never `git commit`).
- **Note:** the scope-fence was first mis-placed in the global
  `~/.claude/skills/dev-workflow` skill, REVERTED per the "skills live in the
  repo" correction, then relocated to the repo runbook as 24d64f2.

### B6. Cadence retro (2026-05-26) — commit d07e65c
- **Findings/changes:** recurring avoidable plan-gate blocks — cover ALL of a
  struct's fields (incl. optional), read acceptance criteria literally, and
  verify before any irreversible/push step.

### B7. Cadence retro (2026-05-26) — commit a568056
- **Findings/changes:** plan-deviation flagging — when implementation reveals the
  gated plan is factually wrong, deviate to the correct approach AND flag the
  deviation explicitly to the implement gate (don't silently follow a wrong plan,
  don't silently deviate).

### B8. Cadence retro (2026-05-26) — post etude-14r (q87/8t4/n0t)
- **Trigger/scope:** 3-ticket cadence after shipping the whole retro feature
  (etude-q87 capture/storage, etude-8t4 list/show, etude-n0t generate+seam), all
  Tier-1/Tier-2 gated.
- **Findings/changes:** added a "Recurring Defect Classes (implement gate)" section
  to `review-gate-runbook.md` capturing two patterns the implement gates caught
  repeatedly: (1) **reserve every command-generated Refs/manifest key against
  `--ref` override** — the `--ref` spoof/override class recurred (q87
  subject_run/scope; n0t reintroduced it via an unreserved `generator` provenance
  key); (2) **the in-harness repo-aware reviewer seat must do adversarial +
  spec-completeness review, not just "it works"** — it GO'd 4 times on defects the
  spec-focused inlined seats (codex/gemini) correctly BLOCKED (the two `--ref`
  holes, `retro show` dropping gate/bench/eval/custom metadata, `resolveSubjectStage`
  silently picking one stage of a multi-stage run). Concrete evidence the multi-seat
  gate is load-bearing.
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` ("Recurring Defect
  Classes"). Counter reset.
- **Follow-up beads:** etude-712 (make gen-docs `TestDriftGuard` expectedCommands
  exhaustive — a related guard-completeness gap surfaced during etude-8t4).

### B9. Cadence retro (2026-05-26) — post misc-backlog sweep (0rt/712/4o0)
- **Trigger/scope:** 3-ticket cadence after the first three misc-backlog beads
  (etude-0rt shared WriteManifestTree extraction [Tier 1], etude-712 gen-docs
  drift-guard exhaustive [Tier 2], etude-4o0 epic-close reconcile gate [Tier 3]).
- **Findings/changes:** added a THIRD item to "Recurring Defect Classes": **a
  negative/failure-mode test must exercise the claimed failure path for the RIGHT
  reason.** Caught at etude-712's PLAN gate (codex) — a test that derived its
  expected set from the generated dir claimed "delete a generated file proves
  missing-committed" but actually only proved ORPHAN, and omitted byte-stale
  entirely; two of three fault paths were unproven while the test looked thorough.
  Fix: inject one fault at a time on the correct side, assert the error names that
  victim, via a dir-args helper. Also reinforces B8's "gate is load-bearing even on
  TEST/dev-tooling beads, not just product code."
- **What worked (preserve):** tier right-sizing held — 0rt (write-path refactor) got
  full Tier-1 3-seat, 712 got Tier-2, 4o0 (docs/process) got Tier-3 single-Opus; the
  lighter gates were faster and still caught the real defect (712's at Tier-2 plan).
  And the planner's HONEST thin-delta call on 4o0 (compose existing checks + promote
  prose to mandatory, rather than build parallel per-epic machinery) avoided
  over-building.
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` ("Recurring Defect Classes"
  item 3). Counter reset.
- **Follow-up beads:** etude (remove dead q87 VerifyArtifactFile/ReferencedArtifactPaths
  wrappers) + etude (gitignore the gen-docs build artifact) — both filed during the sweep.

### Inclusion boundary

These commits touched `review-gate-runbook.md` but are deliberately NOT retro
records: **50feb02** (the runbook baseline), **57deaf9** (structured
gate-capture TOOLING, an etude-roadmap.2.4 deliverable, not a lesson),
**1392817** (the degraded-policy CONSOLIDATION = etude-phase-prep.3 itself,
structural not a fresh lesson), **a66f87e** / **3c614e9** (docs-hygiene /
Phase-0-completion notes).

---

## Form C — docs-reality-drift retro

### C1. Docs-reality-drift retro (2026-05-25)
- **Trigger/date/scope:** a post-Phase-3/4 docs-vs-CLI audit. (The write-up was
  authored mid-session but reverted as out-of-scope per the scope-fence rule; it
  survives at `/tmp/docs-reality-drift-retro.md` and is captured here from the
  bead trail.)
- **Findings:** README omitted shipped `bench`/`gc`/`reindex`; BRIEF called
  shipped `bench` "future work"; a stale local `./bin/etude` could misreport the
  command surface; there was no mechanical source-built-CLI inventory check.
- **Recommended changes / Landed:** a mechanical docs-reality guard
  `[IMPLEMENTED]` → **etude-phase-prep.5** (commit 7851222:
  `scripts/docs-reality-check.sh` + `make docs-reality` + a docs-checklist step);
  the README/BRIEF/index refresh `[IMPLEMENTED]` → **etude-phase-prep.2** (commit
  e16b9b6, which made the guard pass — a closed loop).
- **Follow-up beads / Remaining:** etude-phase-prep.5 (closed), etude-phase-prep.2
  (closed). An epic-close holistic docs/reality reconciliation: `[IMPLEMENTED]` →
  **etude-4o0** (`make reconcile` target composing `make docs-reality` +
  `make docs-check`; mandatory checklist gate in docs-checklist.md; Epic-Close Gate
  section in review-gate-runbook.md; commit SHA: etude-4o0).

---

## Finding: no retro is captured as an etude run stage

No `refs/etude/runs/*` manifest contains a `retro` stage. Verified empirically:
across all current run manifests the only stage names are
`plan`/`implement`/`verify`/`review`/`docs`. Every retro above lives as a doc,
a runbook-rule commit, and/or bead notes — **none is an `etude` run artifact.**
This is exactly the gap that **etude-14r** ("Capture retros and session activity
as a first-class etude artifact", open P0) and its design in
[retrospectives.md](../product/retrospectives.md) /
[etude-retro-command.md](../product/etude-retro-command.md) propose to close: a
`retro` artifact/run keyed to the cohort of beads it covers, recording findings
+ the durable changes it produced. Until then, this ledger is the manual
stand-in.

## Retro cadence

The standing autonomous-`/loop` rule is a retro every **3 closed tickets**
(small fixes inline, large ones become beads). Retro outputs go ONLY to skills,
formulas, or repo docs (runbooks/checklists/this ledger) — **never to memory.**
This ledger should be appended when a new retro lands a process change.
