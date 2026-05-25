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
recorded artifacts (its output or one of its inputs). Invalid input is rejected without changing the run. A run that carries
any gates is written as `manifest_version` 3; gate-less runs are unchanged.

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

## See also

- [Manual Capture](capture.md) and [Runs](run.md) — capturing stages and
  inspecting runs.
- [CLI reference](cli/etude_capture-gate.md) and
  [`run show`](cli/etude_run_show.md) — generated per-command flag reference.
- [Gate reviewer record schema](plans/product/gate-reviewer-record-schema.md) —
  the full data model and validation rules.
