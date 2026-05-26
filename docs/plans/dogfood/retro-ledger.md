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

### B10. Cadence retro (2026-05-26) — post misc-backlog sweep cont. (ojz/jb7/7no)
- **Trigger/scope:** 3-ticket cadence after the back half of the misc sweep
  (etude-ojz provision-gemini-ripgrep [Tier 2], etude-jb7 capture-run batch capture
  [Tier 1], etude-7no etude prime [Tier 1]).
- **Findings/changes:** added a FOURTH "Recurring Defect Classes" item: **an
  "X appears in rendered output" assertion must match X at its exact rendered slot
  (whole token + indent/position), never substring/`Contains`** — names that prefix
  (`capture`/`capture-gate`) or nest (`run`/`run list`) silently satisfy a loose
  check. From etude-7no's `etude prime` drift guard, which took FOUR implement
  rounds as codex surfaced one collision class per round; only indent-anchored
  whole-token matching closed them. Generalizes B9-item-3 to output-membership
  guards (same theme as etude-712's gen-docs guard).
- **Reinforced (not new):** item-2 (the in-harness Opus seat must do adversarial +
  spec-completeness review) — across ojz/jb7/7no, codex repeatedly caught real
  defects Opus + gemini GO'd (ojz symlink-clobber; jb7 path-containment +
  trailing-doc parse; 7no hallucinated flags + 3 drift-guard precision gaps). The
  multi-seat gate keeps being load-bearing; the lone repo-aware seat is not enough.
- **Process note:** 7no's 4-round implement spiral was costly for a P3 — the
  output-membership matcher should have been written robustly in round one (item 4
  is the fix so future such guards are).
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` ("Recurring Defect Classes"
  item 4). Counter reset.
- **Follow-up beads:** etude-jb7 noted a symlink-path-traversal residual (operator
  authors the spec) + the dogfood-capture.sh rewire onto capture-run — both deferred.

### B11. Cadence retro (2026-05-26) — post sweep-tail + Phase-C-extra-1 (quk/f6h/egg)
- **Trigger/scope:** 3-ticket cadence after the 2 sweep loose-ends (etude-quk dead
  exported-wrapper removal, etude-f6h gitignore gen-docs — both trivial single-gate)
  + the first deferred Phase-C extra (etude-egg, the `retros:` workflow.yaml block).
- **Findings/changes:** added a FIFTH "Recurring Defect Classes" item: **an optional
  config/struct block must preserve absent / present-null / present-empty** — a plain
  `*T` field conflates absent with present-null, and synthesizing-on-absent destroys
  the presence bit + breaks round-trip. From etude-egg, which BLOCKED TWICE on exactly
  this (plan: synthesize-on-absent lost the presence bit + broke legacy round-trip;
  implement: present-null `retros:` decoded `*T`→nil == absent, skipping validation).
  Fix pattern: nil-for-absent + accessor-computed defaults + `yaml.Node` to distinguish
  the three states + re-impose KnownFields on node decode + validate-only-when-present.
- **Reinforced (3rd time):** item-2 — both egg BLOCKs were STATE-MODEL bugs the spec-
  focused seats (codex/gemini) caught while the test-running repo-aware seat (opus) GO'd.
  Pattern is now unmistakable: reasoning about the state model / spec catches a class the
  "run it, it passes" seat structurally cannot.
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` ("Recurring Defect Classes"
  item 5). Counter reset.
- **Remaining:** 2 more Phase-C extras in flight (etude-2ku retro-meta sidecar, etude-qih
  etude log).

### B12. Cadence retro (2026-05-26) — Phase-C extras 2 & 3 + follow-up (2ku/qih/aqt)
- **Trigger/scope:** 3-ticket cadence after the last two deferred Phase-C extras
  (etude-2ku optional retro-meta JSON sidecar as a 2nd manifest stage; etude-qih new
  read-only `etude log` runs+retros timeline command) and the first of the follow-ups
  they spawned (etude-aqt render the retro-meta sidecar in `retro show`/`list`).
- **Findings/changes:** added a SIXTH "Recurring Defect Classes" item: **a change that
  touches GENERATED artifacts has a blast radius beyond its own file — the plan's
  file-scope must enumerate EVERY regenerated output.** From etude-qih's PLAN gate:
  adding a top-level command regenerates not just `docs/cli/etude_log.md` but the root
  `docs/cli/etude.md` `SEE ALSO` list; the plan scoped only the new page, leaving the
  root stale (docs-check / TestDriftGuard red). Fix: run `make docs` + `git status` (or
  reason the blast radius) before finalizing scope.
- **Reinforced (4th time, now decisive) — item 2:** the spec-vs-running-code blind spot
  recurred in BOTH remaining beads, and notably codex (not the repo-aware Opus seat)
  caught them even at the IMPLEMENT gate with the binary in hand: qih `--subject` matched
  a retro by its OWN id (spec: retros match only by their subjects) — Opus AND gemini
  GO'd, gemini explicitly rationalizing it correct; aqt's `--- retro meta ---` divider
  would glue onto a body lacking a trailing newline (`Fprint` adds none) — Opus "no
  divergence", gemini "defensively sound", codex BLOCKED at the PLAN gate. Updated item 2
  with a new failure sub-class (d): a match-set/output invariant stated in PROSE that no
  test enforces. Strengthened the rule: do NOT let two GO seats outweigh one source-cited
  BLOCK — every two-seats-missed BLOCK this run verified TRUE against source.
- **Worked well (preserve):** the disputed-claim discipline (verify each BLOCK against
  source before accepting) — zero false BLOCKs this cadence; all three codex BLOCKs were
  real. The narrow-correction-then-advance pattern (amend the bead `--design` with the
  fix + re-gate only the corrected point) kept velocity without re-running clean phases.
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` (new item 6 + item-2
  strengthening + intro bead list). Counter reset.
- **Remaining:** 3 Phase-C follow-up P3s open (etude-9ey cross-retro failure-mode index,
  etude-21z rewire dogfood-capture onto capture-run, etude-094 capture-run symlink-
  traversal hardening). Phase 1 (xenota/github-import) remains USER-BLOCKED.

### B13. Cadence retro (2026-05-26) — Phase-C follow-up backlog (094/21z/sb4 + 9ey defer)
- **Trigger/scope:** 3-ticket cadence closing out the Phase-C follow-up backlog —
  etude-094 (capture-run symlink-traversal hardening), etude-21z (rewire
  dogfood-capture.sh from 4 `capture` calls onto one `capture-run` spec), etude-sb4
  (document the recommended retro-meta sidecar convention) — plus the etude-9ey DEFER.
- **Findings/changes:** added a new **"Plan-Phase Discipline"** section to
  `review-gate-runbook.md` with two items:
  - **P1 Verify the verification.** etude-21z's PLAN was BLOCKED by ALL THREE seats —
    not on the rewrite but on its VERIFICATION method, broken three ways: it diffed a
    non-existent `etude run show --json` flag; it left `refs.bead` un-normalized (the
    diff would falsely fail on the throwaway ids); and `run show` TEXT omits artifact
    hashes + media_type. A proof that can't run or is field-blind proves nothing. Rule:
    the gate vets the proof method — confirm flags exist, normalize every volatile field
    (incl. id-derived refs), and compare the load-bearing surface (diff the raw
    `manifest.json` blob, not human text). Counter-example that worked: etude-094's
    EMPIRICAL adversarial escape-probe + planted-secret leak-audit.
  - **P2 Premise-check before designing.** etude-9ey was correctly DEFERRED, not built:
    zero retros carry a sidecar (empty source), the sidecar is schema-free by design
    (etude-2ku), and the de-facto convention contradicted itself (`root_cause` vs
    `root_causes`). Rule: the planner's first output for a feature bead is a
    BUILD-vs-DEFER call; if the premise fails, DEFER + name the prerequisite + `bd defer`
    rather than building speculative infra. The deferral spun off etude-sb4 (pin the
    convention), which then shipped docs-only (resolving the contradiction on the plural
    arrays, NO enforcement — preserving 2ku's verbatim storage).
- **Reinforced — item 2 (5th time) + the disputed-claim discipline:** every BLOCK this
  cadence was verified TRUE against source and accepted; codex caught the 094 relative-
  specDir EvalSymlinks bug + the 21z dir-dot extension bug that Opus/gemini rated
  non-blocking; gemini caught the 094 missing-file diagnostic masking. Zero false BLOCKs.
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` (new "Plan-Phase Discipline"
  section, items P1+P2). Counter reset.
- **Remaining:** NO non-blocked ready work — `bd ready` shows only the USER-BLOCKED
  Phase 1 beads (xenota-capture-adapter, github-import). etude-9ey stays deferred
  (needs real sidecar data). Natural pause point.

### B14. Cadence retro (2026-05-26) — first review-finding beads (shd/4n7/hp7)
- **Trigger/scope:** 3-ticket cadence after the FULL-REVIEW phase (user-directed audit
  of docs + code → 9 beads filed). First three cleared: etude-shd (timeout + output-cap
  on the exec runner/generator/judge), etude-4n7 (capture-gate/replay symlink-follow
  confinement via atomic O_NOFOLLOW), etude-hp7 (README retro generate/list/show docs).
- **Findings/changes:** added a SEVENTH "Recurring Defect Classes" item: **platform-
  specific API usage must be build-tagged AND verified with a CROSS-COMPILE — the native
  dev build + `go test ./...` do NOT catch a symbol undefined on another GOOS.** From
  etude-4n7: `syscall.O_NOFOLLOW` used directly in cross-platform files built + passed the
  whole suite on darwin (the plan even mis-asserted "Windows = 0/no-op"), but
  `GOOS=windows go build ./...` failed `undefined: syscall.O_NOFOLLOW` — a regression
  (HEAD compiled for windows). codex+gemini caught it by cross-compiling at the implement
  gate; the darwin-only empirical Opus seat (full suite + exploit probes) MISSED it. Fix:
  build-tagged `nofollowFlag` (unix→syscall.O_NOFOLLOW, !unix→0).
- **Reinforced — items 2 + P1 + the disputed-claim discipline:** both P2 hardening beads
  had the multi-seat gate catch real concurrency/security defects the test-passing/host
  seat missed (shd: cmd.WaitDelay pipe-drain hang + LimitReader TOCTOU, codex plan catch;
  4n7: read TOCTOU, UNANIMOUS plan BLOCK + the windows regression). Every BLOCK this cadence
  was verified TRUE against source before accepting. **What worked well (preserve):** seeding
  the gate BRIEF with explicit scrutiny points (I flagged the 4n7 capture-gate read TOCTOU in
  the plan-gate payload, and all three seats then caught it) — a cheap force-multiplier;
  keep pre-loading the gate brief with the specific risks I suspect.
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` (new defect-class item 7 + intro
  bead list). Counter reset.
- **Remaining:** 5 review-finding P3 beads open (etude-x0r docs-plans refresh, etude-5ft
  manifest-load helper, etude-kig retro preamble, etude-8b7 dead pointer chain, etude-6j8
  inferMediaType move + cleanups, etude-0ew gate-input validation). Phase 1 still USER-BLOCKED;
  etude-9ey still deferred.

### B15. Cadence retro (2026-05-27) — review-finding beads cont. (x0r/5ft/0ew + 8b7 defer)
- **Trigger/scope:** 3-ticket cadence: etude-x0r (plans-README refresh: shipped-vs-planned),
  etude-5ft (extract the manifest-load helper, dup'd 4×), etude-0ew (strict gate-JSON parse
  via DisallowUnknownFields), plus the etude-8b7 DEFER.
- **Findings/changes:** STRENGTHENED Plan-Phase Discipline **P2** with a corollary: **an
  audit/review-derived bead is a HYPOTHESIS, not a validated fact — re-verify its premise
  against current code before building.** The premise-check repeatedly caught over-stated
  findings from my own review audit:
  - etude-8b7 ("remove dead pointer chain") — `deadcode` "zero callers" was true but the
    pointer chain is the WRITE half of a documented, schema-validated (`StoragePointer` at
    manifest.go:592/628), well-tested, planned storage variant → RESERVED scaffolding, DEFERRED
    not removed. "zero callers ≠ removable" when it's a documented/reserved variant.
  - etude-0ew ("reject negative round/tier + unknown fields") — the round/tier half was ALREADY
    enforced by Validate/validateGate (manifest.go:430-433) on the write path → scoped down to
    just the real gap (DisallowUnknownFields). "missing validation" may already be enforced.
- **What worked well (preserve):** the plan-phase premise-check is now a proven safety net — it
  deferred 9ey + 8b7 and scoped-down 0ew BEFORE any wasted implementation. Audit findings should
  be filed as beads (capture the hypothesis) but always re-verified at plan time. etude-5ft was
  a clean pure-refactor win (Tier-2 unanimous, behavior-identical, full suite green).
- **Landed:** `[IMPLEMENTED]` in `review-gate-runbook.md` (P2 corollary). Counter reset.
- **Remaining:** 2 review-finding P3 beads (etude-6j8 inferMediaType move + cleanups, etude-kig
  retro capture/generate preamble). etude-8b7 + etude-9ey deferred; Phase 1 USER-BLOCKED.

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
