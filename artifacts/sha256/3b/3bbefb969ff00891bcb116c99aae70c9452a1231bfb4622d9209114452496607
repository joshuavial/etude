# Cadence retro 2 — isolation, exact proofs, and operating floors

Subjects: `etude-rep`, `etude-aqh`, `etude-bke`.
Trigger: scheduled after three closed beads.

## Retro summary

The cohort repaired a parallel-test false failure, strengthened a replay proof,
and stopped strict lanes while shared disk was below the operating floor. The
work went well overall because each fix addressed the measured failure without
weakening the check that exposed it.

## What went wrong

- Worktree-leak tests inspected the shared temporary directory, so unrelated
  parallel packages could make a correct package fail.
- Replay verification asserted only non-empty output, which did not prove that
  forward replay reproduced the original artifact byte for byte.
- Shared disk fell below the strict lane floor and temporarily prevented safe
  iteration.

## Root causes

- A test for process-local leakage relied on shared host state rather than an
  isolated fixture boundary.
- A behavioral acceptance criterion was represented by a weaker proxy
  assertion.
- Disk capacity is shared across lanes, while the safety check is evaluated by
  each lane independently.

## What worked well

- `etude-rep` isolated the leak tests and retained a negative control proving a
  real leak still fails.
- `etude-aqh` replaced the proxy assertion with exact artifact equality and
  removed shell parsing that depended on `ls` output.
- `etude-bke` preserved the hard floor and waited for capacity to recover
  instead of bypassing it.

## Recommended changes and dispositions

- Keep shared-state tests fixture-local and retain a negative control.
  **Applied:** `etude-rep` shipped both changes.
- Translate exact behavioral criteria into exact assertions rather than
  non-empty or substring proxies. **Applied:** `etude-aqh` pins byte equality
  and the one-round invariant.
- Preserve the existing disk hard floor. **Applied:** `etude-bke` restored
  capacity without weakening policy; no further artifact is warranted.

## Highest-leverage next step

Preserve the exact-proof pattern from `etude-aqh` in future verification plans:
name the acceptance value and assert that value directly.
