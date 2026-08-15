# Retro 2 — epic etude-9uf

Subjects: `etude-9uf.1` (etude gate), `etude-5f6` (fallback ladder),
`etude-1od` (docs-stage gating). Cadence retro; retro 1 covered `etude-rep`,
`etude-9uf.2` and the start of `.1`.

## The gate is not ceremony, and this is the evidence

Across these three beads the panel produced **eleven BLOCK rounds**, every one a
real defect in my work, none argued down. Three were fail-OPEN — a state where
something other than a stated GO could produce a passing verdict:

| Where | Defect |
|---|---|
| seat adapter | the agentic child CLI inherited `$ETUDE_OUTPUT_FILE` and could write its own `go` |
| seat adapter | "last VERDICT wins" turned a stated BLOCK into a pass |
| seat adapter | `BLOCKING: real / BLOCKING: none` cancelled a finding and passed |

The most important fact about those three: **codex had already passed the first
one twice** before an opus seat reproduced it. A single-seat green is not the
same thing as a gate.

## The pattern that cost the most rounds

Three of the eleven were one family — *a parsing rule safe in one direction is
unsafe in the other* — found at three different sites across rounds 5, 6 and 7 of
`.1`. Each time I fixed the site the seat named, and the next round found the
same bug one label over. Round 6's fix then INTRODUCED a fail-CLOSED regression
(an echoing seat could no longer cast a GO at all), which is not the safe failure
it looks like: a false outage escalates every gate rather than one seat.

**Durable change, now in the supervisor runbook:** when a seat names a NEW SITE
for a finding you already fixed, stop patching and enumerate every site the
invariant covers. Applied at round 8; the exhaustive pass took one audit and
closed a filed follow-up as a side effect.

## Two things only a literal reading caught

- **The acceptance said `--stage verify`.** I had proven the command on `plan`
  and `implement`. Running the literal invocation found that `execCheckRunner`
  ignored `env_allowlist` entirely, so the verify stage's own `make test` /
  `make lint` could never pass — on the one stage the acceptance names. Eight
  implement rounds missed it because none ran that stage.
- **A runbook section warning supervisors not to pattern-match stage names**
  contained a factual error I made by pattern-matching the stage list: I wrote
  "four of five roles track their name" when `implement` produces `diff` as well
  as `docs` producing `docs-diff`. Three of five, two traps. A seat caught it.

## A BLOCK that was my packet, not my work

codex BLOCKed `etude-1od` because a folded requirement — a tracker note — was
absent from the diff. The note existed; this repo tracks in `bd`/dolt, which
cannot appear in a git diff. Correct BLOCK, packet bug on my side.

**Durable change, now in the runbook:** every requirement a packet asks a seat to
adjudicate must appear IN the packet, in whatever medium it lives — tracker
state, a ref, command output, a file outside the diff.

## Where I was simply wrong

I declared the opus seat unreachable after six attempts and filed a P0 blocker
that stopped the lane. The seat was fine: I had been NAMING the spawned agent,
which makes it a persistent teammate that idles instead of returning a report.
Six identical failures across two different prompt shapes should have indicted
the mechanism, not the content — a flaky seat varies.

Cost: an unwarranted stop, a burned substitute quota, and an escalation the user
had to overrule. I have since written the root cause onto another lane's P0
(`etude-4jr`) which hit the same wall, so that lesson is paid for once.

## Claims I had to retract

Two, both mine, both caught by seats rather than by me:

- bead `.2` claimed the `docs-reality` check would force the runbook to be
  amended once `gate` shipped. It would not have — the check needs the stale
  phrase and the command name on the same line, and my wording never paired
  them. Corrected in the runbook itself rather than quietly dropped.
- bead `.1`'s verify artifact said the in-harness seat was "machine-honored" via
  `invocation_fallbacks`. A seat verified live that `etude gate` never reached
  the fallback. It was expressed in config, not consumed — which `etude-5f6`
  then actually fixed.

Both were the epic's own target defect in miniature: something written down,
believed, and not executed.

## Process notes worth keeping

- **Freeze the tree during a gate round.** Rounds 6 and 7 of `.1` were each
  reviewed against code that shifted mid-review, and both opus seats flagged it.
  Round 8 recorded the artifact's sha256 in the packet and verified it unchanged
  across both verdicts.
- **A negative control per fold.** Every fix in these beads has one — remove the
  guard, watch the test fail. It caught that a count-only "untouched" assertion
  would have passed a manifest rewrite.
- **`etude-nad` was solved by another lane**, not me: `etude init` registers
  `refs/etude/*` as a fetch refspec, so any `git fetch --prune` deletes unpushed
  run refs. My sentinel survived every test I ran because I never ran `--prune`.
  The mitigation I adopted (push after every append) was right by instinct, not
  by diagnosis.
