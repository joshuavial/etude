# Gate Reviewer Records

A **gate reviewer record** captures a review-gate attempt on a run: which
reviewer seats looked at which artifacts, what each one decided, across one or
more rounds. It is stored on the run alongside its stages and is inspectable
with `etude run show`.

## Reviewers are not producers

A run's **stages** record *producers* — who (which harness/model/skill) produced
an artifact (a plan, a diff, a verify report). A **gate** records *reviewers* —
the seats that evaluated those artifacts and returned a verdict. These are
different axes: a gate does not produce the artifact it reviews, it evaluates it,
and one gate attempt can review several stages at once. Gate records therefore
live in their own top-level block on the run, separate from stage producers.

## Capturing a gate attempt

`etude capture-gate` appends one gate attempt to an existing run (it does not
create runs — capture the stages first):

```bash
etude capture-gate --run <run-id> --gate-file <path|->
```

The gate file is a single JSON object. Field names mirror the stored record:

```jsonc
{
  "gate_id": "plan.r2",        // unique per run; convention <phase>.r<round>
  "phase": "plan",             // the phase this gate guards
  "round": 2,                  // 1-based; each rerun is a new attempt, round+1
  "tier": 2,                   // 0 (unknown) | 1 | 2 | 3
  "status": "pass",            // pass | rerun | escalated
  "reviewed_stages": [         // >=1; each stage must exist on the run
    {"stage": "plan", "role": "plan", "artifact": ""}  // artifact optional (sha); "" = name-only
  ],
  "seats": [                   // one entry per reviewer seat
    {
      "seat": "opus",
      "harness":  {"name": "claude-code", "version": "opus-4-7"},
      "provider": {"name": "anthropic", "model": "claude-opus-4-7"},
      "skill":    {"id": "", "repo": "", "version": ""}, // optional reviewer skill identity
      "verdict":  "go",        // go | block | failed | empty | malfunction | disregarded
      "required": [],          // required changes (meaningful on block)
      "optional": [],          // optional improvements (meaningful on go)
      "failure_note": "",      // required for failed/empty/malfunction/disregarded; forbidden on go/block
      "raw_output": null,      // optional {role,path,media_type}; path is a local transcript file hashed in
      "timestamp": "2026-05-26T02:01:00Z"
    }
  ],
  "decision": {                // aggregate decision detail
    "escalation_reason": "",   // required when status == escalated
    "degraded_reason": "",     // why a disregarded/malfunction seat was allowed (degraded-gate fallback)
    "deferred_beads": []       // follow-up refs for deferred optional improvements
  },
  "timestamp": "2026-05-26T02:05:00Z"
}
```

Appending is additive and validated: existing stages and prior gate attempts are
preserved, `gate_id` and `(phase, round)` must be unique on the run, and a
`reviewed_stages` artifact (when set) must match one of the named stage's
recorded artifacts (its output or one of its inputs). The gate JSON is parsed
strictly: an unknown or misspelled field (at any nesting level) is rejected
rather than silently dropped, as is any trailing content after the gate object.
Invalid input is rejected without changing the run. A run that carries
any gates is written as `manifest_version` 3; gate-less runs are unchanged.

A seat's `raw_output.path` must point at a **regular file**: it is opened
without following symlinks (on Unix, atomically via `O_NOFOLLOW`), so a symlink
or other non-regular file at that path is rejected rather than read through.
This prevents a machine-generated gate file from causing `etude` to capture a
file outside the intended transcript (e.g. via a symlink to a sensitive path).
Absolute and working-directory-relative paths to regular files are unaffected.

### Reviewer seat fields

Each seat records its identity on three axes plus its verdict:

- **provider** — the model provider and model, e.g. `lmstudio` /
  `qwen/qwen3.6-35b-a3b`, `openai` / `gpt-5.5`, `google` /
  `gemini-3.1-pro-preview`, `anthropic` / `claude-opus-4-7`.
- **harness** — the runtime that invoked the model, e.g. `pi`, `codex`,
  `gemini-cli`, `claude-code`.
- **skill** — an optional reviewer skill/tool identity (omitted when the seat is
  a raw model invocation).

`provider` (name + model) and `harness.name` are always recorded; `skill` and
the feedback/raw-output fields are recorded only when present.

Verdicts: `go` (passed) and `block` (changes required) are the normal outcomes;
`failed` (invocation error — auth/quota/tool/timeout), `empty` (ran but produced
no verdict), `malfunction` (a root-caused tooling outage), and `disregarded` (an
outage seat deliberately skipped under a degraded-gate fallback) all carry a
`failure_note` and never count as a pass.

## Inspecting gates

`etude run show <run-id>` prints each gate attempt after the stages, in the order
captured:

```
gate: plan.r2
  phase:    plan
  round:    2
  tier:     2
  status:   pass
  reviewed: plan (role=plan)
  degraded: pilms outage all session; 3 substantive GO per degraded-gate policy
  seat: gemini
    provider: google / gemini-3.1-pro-preview
    harness:  gemini-cli 3.1
    verdict:  go
  seat: opus
    provider: anthropic / claude-opus-4-7
    harness:  claude-code opus-4-7
    verdict:  go
  seat: codex
    provider: openai / gpt-5.5
    harness:  codex gpt-5.5-xhigh
    verdict:  go
  seat: pilms
    provider: lmstudio / qwen/qwen3.6-35b-a3b
    harness:  pi x
    verdict:  disregarded
    note:     0-CPU client hang reproduced; known artifact
```

Runs with no gates print no gate section.

## Backfilled gate records

Runs whose gate data exists only as PROSE (in bead notes) predate structured
capture. `scripts/backfill-gate-records.sh <bead-id> ...` does a best-effort,
STRICT backfill: it parses only the canonical "Recording Results" block (the
`<Phase> gate attempt <n>:` header with the four exact seat lines and a
`result:` line) and appends one `GateAttempt` per block via `capture-gate`.

Backfilled records are clearly marked, never silently presented as observed data:

- **`tier: 0`** — the schema's "unknown/backfilled" tier; the machine-queryable
  signal that a record was imported, not captured live.
- **`decision.degraded_reason`** carries a leading `[backfilled from <bead>
  notes; … provider/model convention-derived; … timestamps approximate]` marker.
- **provider/model are convention-derived** from the runbook seat table (filled
  only for recognized seat tokens), not observed; timestamps are approximate;
  `reviewed_stages` cite the run's real stages.

The importer **refuses rather than invents**: any block it cannot parse
unambiguously (unknown seat token, unrecognized verdict, a phase that names no
stage on the run, a missing `result:` line) is skipped with a message and a
non-zero exit — it never guesses a verdict, round, or provider.

This makes the importer effectively **forward-looking**. Historical bead notes
in this repo use a looser narrative format (e.g. `## Plan gate — … PASS` with
`- codex (…): BLOCK→GO`) that collapses multiple rounds into one line; that is
genuinely ambiguous for the per-round `GateAttempt` model, so it is refused, not
guessed. The strict parser will backfill runs whose notes use the canonical
format going forward, and honestly declines the rest. Capturing gates live with
`etude capture-gate` (per the dogfood conventions) is the reliable path.

## Live gate execution

During a live `etude run`, the engine executes gate blocks automatically —
no manual `etude capture-gate` call is needed. See [Runs](run.md#gate-execution)
for the full live execution semantics (check/seat invocation, synthesis,
rerun, escalate). This section documents the **seat output envelope** contract
that seat runners must satisfy when invoked by the live engine.

### Seat output envelope

A seat runner writes a JSON object to `ETUDE_OUTPUT_FILE`:

```json
{"verdict":"go","required":[],"optional":[]}
```

Fields:

| Field | Type | Description |
|---|---|---|
| `verdict` | `"go"` or `"block"` | The seat's vote |
| `required` | array of strings | Required changes; meaningful on block |
| `optional` | array of strings | Suggestions; meaningful on go |

The envelope is parsed strictly. A runner that exits 0 but writes no file, an
empty file, or non-JSON content is recorded as `empty` or `malfunction` and
excluded from the vote. A runner that exits nonzero is recorded as `failed`
regardless of output. All three carry a `failure_note` in the stored
`SeatResult` (see the capture-gate record schema above).

**Check runners** do not write an output envelope. Their verdict is the
process exit code: 0 = pass, nonzero = hard BLOCK. A check that cannot
launch, times out, or exits nonzero is always a hard veto, independent of seat
votes.

### Synthesis and the stored record

After synthesis the engine writes the gate attempt directly to the run
manifest (same CAS path as stage captures). The stored `GateAttempt` follows
the same schema used by `etude capture-gate` (see above), with these
live-specific characteristics:

- `gate_id` is `"<phase>.r<round>"` where round is 1-based and monotonically
  increases across reruns and tier changes.
- Check seats appear as `SeatResult` entries with `harness.name="exec"` and
  `provider.name="deterministic"`.
- `decision.degraded_reason` is set whenever any seat is non-usable (failed,
  empty, or malfunction) — even when the gate still passes — so the audit
  record reflects a degraded panel.
- `decision.escalation_reason` is required when `status="escalated"`.
- A run that carries any gate attempts is stored as `manifest_version` 3.

For the dogfood operational checklist (manual reviewer seats, runbook format,
degraded-gate policy), see
[Review Gate Runbook](plans/dogfood/review-gate-runbook.md).

## See also

- [Manual Capture](capture.md) and [Runs](run.md) — capturing stages and
  inspecting runs.
- [CLI reference](cli/etude_capture-gate.md) and
  [`run show`](cli/etude_run_show.md) — generated per-command flag reference.
- [Gate reviewer record schema](plans/product/gate-reviewer-record-schema.md) —
  the full data model and validation rules.
