# Codex Fallback Approval Prompts

Prompts for repos that do not define their own phase gate. In the `etude`
dogfood repo, use the three-reviewer gate instead.

Codex evaluates a phase artifact and returns structured JSON.

## Plan Approval

```text
Review this implementation plan.

BEAD: {{bead_id}}
TASK: {{bead_title}}
TYPE: {{bead_type}}

DESCRIPTION:
{{bead_description}}

PROPOSED PLAN:
{{design_field}}

Evaluate:
1. Does the plan address the task requirements completely?
2. Is the approach reasonable and not over-engineered?
3. Are tests and docs considered appropriately?
4. Are obvious risks or missing inputs called out?
5. Is the scope appropriate for one bead/commit?

Respond with JSON matching this schema:
{
  "approved": boolean,
  "confidence": number (0-1),
  "reason": "brief explanation",
  "concerns": ["specific concern 1", ...]
}

Set confidence < 0.7 if:
- Requirements are ambiguous.
- The approach seems risky or unusual.
- Tests or docs are missing without rationale.
- Security implications are unclear.
- A major architecture decision is involved.
```

## Implement Approval

```text
Review this implementation artifact.

BEAD: {{bead_id}}
TASK: {{bead_title}}

APPROVED PLAN:
{{design_field}}

IMPLEMENTATION NOTES:
{{notes_field}}

FILES CHANGED:
{{changed_files}}

GIT DIFF SUMMARY:
{{diff_stats}}

Evaluate:
1. Does implementation match the approved plan?
2. Are deviations explained and reasonable?
3. Is the scope focused for one bead?
4. Are focused checks recorded when useful?
5. Are there obvious code quality, safety, or maintainability issues?

Respond with JSON matching this schema:
{
  "approved": boolean,
  "confidence": number (0-1),
  "reason": "brief explanation",
  "concerns": ["specific concern 1", ...]
}

Set confidence < 0.7 if:
- Implementation deviates significantly from plan.
- Scope seems too large.
- Risky changes lack explanation.
- Obvious quality issues are visible from the artifact.
```

## Verify Approval

```text
Review this Verify artifact.

BEAD: {{bead_id}}
TASK: {{bead_title}}

APPROVED PLAN:
{{design_field}}

IMPLEMENTATION:
{{notes_field}}

VERIFY ARTIFACT:
{{verify_section}}

Evaluate:
1. Is the status one of pass, fail, or blocked?
2. Does the recommendation agree with the status?
3. Are automated tests adequate for the change, or is no-test rationale sound?
4. Was manual testing considered and run when relevant?
5. Are correctness, coverage, regression, code quality, and risk findings clear?
6. If implementation changes are needed, does Verify return fail instead of hiding the fix?

Respond with JSON matching this schema:
{
  "approved": boolean,
  "confidence": number (0-1),
  "reason": "brief explanation",
  "concerns": ["specific concern 1", ...]
}

Set confidence < 0.7 if:
- Tests are failing without a `fail` recommendation.
- Coverage seems inadequate.
- Manual testing was skipped despite browser-visible or workflow behavior.
- Results are ambiguous or incomplete.
- Implementation fixes were made inside Verify without returning to Implement.
```

## Docs Approval

```text
Review this Docs artifact.

BEAD: {{bead_id}}
TASK: {{bead_title}}

VERIFY ARTIFACT:
{{verify_section}}

DOCS ARTIFACT:
{{docs_section}}

DOCS DIFF SUMMARY:
{{docs_diff_stats}}

Evaluate:
1. Are docs updated when user-facing behavior changed?
2. If no docs changed, is the rationale sound?
3. Do shipped docs describe only implemented behavior?
4. Are planning notes kept under the appropriate planning area?
5. Are generated references kept separate from hand-written guides?

Respond with JSON matching this schema:
{
  "approved": boolean,
  "confidence": number (0-1),
  "reason": "brief explanation",
  "concerns": ["specific concern 1", ...]
}

Set confidence < 0.7 if:
- Required docs are missing.
- Planned behavior is described as shipped behavior.
- Docs contradict the verified implementation.
- The docs artifact is too vague to review.
```

## Final Review Approval

```text
Review this completed bead.

BEAD: {{bead_id}}
TASK: {{bead_title}}

PLAN:
{{design_field}}

IMPLEMENTATION:
{{notes_field}}

VERIFY ARTIFACT:
{{verify_section}}

DOCS ARTIFACT:
{{docs_section}}

FINAL REVIEW ARTIFACT:
{{review_section}}

FILES CHANGED:
{{changed_files}}

GIT DIFF SUMMARY:
{{diff_stats}}

Evaluate:
1. Does the completed work satisfy the bead?
2. Are Plan, Implement, Verify, and Docs artifacts present and consistent?
3. Are docs accurate and policy-compliant?
4. Are there unresolved bugs, regressions, accidental files, or scope issues?
5. Is the close recommendation justified?

Respond with JSON matching this schema:
{
  "approved": boolean,
  "confidence": number (0-1),
  "reason": "brief explanation",
  "concerns": ["specific concern 1", ...]
}

Set confidence < 0.7 if:
- Any prior phase artifact is missing or inconsistent.
- Docs are missing or inaccurate.
- Verify is inadequate or contradicted by later changes.
- Final review identifies unresolved blockers.
```

## Variable Substitution

When invoking Codex, substitute these variables from bead state:

| Variable | Source |
|---|---|
| `{{bead_id}}` | Bead ID |
| `{{bead_title}}` | Bead title field |
| `{{bead_type}}` | Bead type |
| `{{bead_description}}` | Bead description field |
| `{{design_field}}` | Bead `--design` field content |
| `{{notes_field}}` | Bead `--notes` field content |
| `{{verify_section}}` | Verify artifact from bead notes/comments |
| `{{docs_section}}` | Docs artifact from bead notes/comments |
| `{{review_section}}` | Final Review artifact from bead notes/comments |
| `{{changed_files}}` | List from `git diff --name-only` |
| `{{diff_stats}}` | Output of `git diff --stat` |
| `{{docs_diff_stats}}` | Docs-only diff summary |

## Invocation Pattern

```bash
codex exec \
  -c model_reasoning_effort=high \
  --sandbox read-only \
  --output-schema ~/.claude/skills/dev-workflow/reference/approval-schema.json \
  "<prompt with variables substituted>"
```

## Response Handling

```text
IF approved == true AND confidence >= threshold:
  -> Proceed to next phase

IF approved == false OR confidence < threshold:
  -> Escalate with bead ID, phase, reason, concerns, and confidence
```
