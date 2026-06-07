---
name: etude-review
description: Run an ephemeral etude review gate — a tier-defined panel of model-identity seats (NOT role personas) that all review ONE shared prompt and vote GO/BLOCK, WITHOUT capturing into a run. Use to review any artifact (a design doc, a diff, a draft) against an L1/L2/L3/L4 panel. Invoke with /etude-review or when asked to "run X past an Ln etude gate/review".
---

# Etude Review

An **ephemeral** review using the project's gate panels. It runs the same
multi-seat panel a real gate uses, but **never captures** — no run required, no
`etude capture-gate`, output is stdout only.

```
pick tier (L1/L2/L3/L4) -> build ONE shared prompt -> run that tier's model seats in
parallel -> aggregate GO/BLOCK (unanimous) -> report verdict + required/optional
```

## Review vs gate (read this first)

- **Review (this skill):** ephemeral. Reviews ANY artifact. Does NOT write to
  `refs/etude/*`. Use it for design docs, drafts, or a diff you just want a
  panel's read on. This is the thing to invoke when someone says "run this past
  an L2 etude gate" and there is **no run/phase** behind it.
- **Gate (dev-workflow):** the same panel run on a phase artifact, whose verdicts
  ARE captured into the run with `etude capture-gate`. If the user wants the
  result recorded as a phase gate, that is the dev-workflow path, not this skill.

When unsure which they mean: if there is an active run + phase and they want it
recorded → gate. Otherwise → review (this skill).

## Core rules (do not violate)

1. **Seats are MODELS, not personas.** Every seat gets the **identical** prompt.
   Do NOT assign roles like "Systems Critic" / "Security Auditor" / "Pragmatist"
   — that is the `design-council` skill and it manufactures review axes the user
   did not ask for. Divergence must come from model diversity alone.
2. **Tiers come from `.etude/gates.yaml`** — read it; never hardcode the panel.
   L1 = heaviest (Gemini + Opus + Codex + Grok), L2 = three (Opus + Codex +
   Grok), L3 = two (Opus + Codex), L4 = one (Opus). Higher number = lighter.
   Match the tier to risk; when unsure, go heavier.
3. **Unanimous.** GO only if every seat returns GO. Any BLOCK → BLOCK. A seat
   failure (auth/quota/empty/truncation) is NOT a GO — reroll it (smaller input)
   or, only for the `optional: true` local seat, disregard per degraded-gate
   policy and say so.
4. **Seats must not mutate the working tree** and must reason only from what you
   inline — never hidden context.

## Steps

### 1. Resolve the panel
Read `.etude/gates.yaml`. Take the requested tier (default L2 if unspecified and
the target is code; L4 for docs-only/no shipping-code changes, or the matching
`phase_gates` tier when a phase is named). Expand to its `seats` and each seat's
`invoke` + `mode`.

### 2. Gather inputs and write a working dir
Put scratch in `.etude/reviews/<topic>/` (kebab topic from the subject). Write
the artifact under review to `target.md` (or capture the diff). Derive any
"ground-truth" facts the prompt asserts from the SOURCE (spec/code/runbook), not
memory — an incomplete paraphrase makes a seat correctly BLOCK on a non-defect.

For a **code** target: snapshot the changed files read-only with `cp --parents`
(preserve directory structure — flattening collides same-basename files) BEFORE
dispatching, and re-verify the snapshot is unchanged after the seats run.

### 3. Build ONE shared prompt
Same text for every seat. **Lead the prompt with the phase's `abstraction` block
from `.etude/gates.yaml` `phase_gates`** — it sets the review altitude and is the
first thing each seat reads. For a PLAN gate this is the validated wording ("BLOCK
only if the plan (a) misses a requirement, (b) regresses/loosens an observable
behavior/contract, or (c) specifies an unimplementable mechanism; implementation
minutiae → OPTIONAL"). This is not optional polish: a bench (Variant B vs A on the
Dolt-recovered etude-2bm.1 plan rounds) showed it cuts the plan-gate review-spiral
— stopping false blocks on minutiae while still catching real regressions. Then
include: what the artifact is, the artifact/diff inlined, the cited ground-truth
facts, and this return contract:
```
- VERDICT: GO | BLOCK
- ONE-LINE RATIONALE
- REQUIRED CHANGES (numbered; empty only on an unconditional GO)
- OPTIONAL IMPROVEMENTS (numbered)
```
For a **design/doc** review, add: "Reason ONLY from the inlined artifact and the
cited facts; do NOT read repo files."

### 4. Dispatch seats in parallel (per-`mode` constraints)
- **opus** (`inline`): `claude -p --model opus` with the prompt on stdin. May
  mutation-test via a `/tmp` copy, never the repo file.
- **codex** (`diff-only`): prompt on stdin to the `invoke` command. Inline the
  changed production code + the diff only; **summarize tests in prose, don't
  paste them**; tell it "do NOT run go build/go test/go vet; the green results
  are provided and trustworthy." Keep the prompt **under ~700 lines** — on large
  inputs codex truncates with no VERDICT line. No VERDICT = truncation glitch
  (reroll smaller), never a silent GO. For a doc gate, inline the whole note and
  say "reason ONLY from the inlined note; do NOT read repo files."
- **gemini** (`inline-no-tools`): run from `/tmp`, pass the prompt via `-p
  "$(cat <prompt>)"`. Inline all code; tell it to reason without calling tools
  (its GrepTool bleeds matches across files and it will try a non-existent
  shell tool). Ground-truth-check any gemini BLOCK that cites a specific string
  in a file before acting.

Run independent seats concurrently (background bash); wait for all before
synthesizing — do not treat a slow seat as missing.

### 5. Aggregate and report (no capture)
Print:
```
etude review — tier <Ln> (<seat list>) — VERDICT: GO | BLOCK
  seat <name> (<provider>): GO|BLOCK — one-line rationale
  ...
required (union of blocking seats): ...
optional (union): ...
```
Then a short synthesis: consensus (≥2 seats), disagreements, and — if any seat
hit an L1 surface or found shipped-behavior/schema/data risk on an L2/L3 run —
**escalate: rerun at L1** before trusting the GO.

**Do not** run `etude capture-gate` or write to `refs/etude/*`. This is ephemeral.

## Scratch
Working files live in `.etude/reviews/<topic>/` (git-ignored). They are throwaway
evidence, not run records.
