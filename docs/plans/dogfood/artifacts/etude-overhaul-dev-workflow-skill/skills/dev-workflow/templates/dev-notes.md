# Dev Notes Template

This is the structured comment format used on beads to track development
progress and temporary `etude` capture data.

## Format

```markdown
## Dev Notes

### Plan
**Approach:** [How you'll solve the problem]
**Files:** [Files to create/modify]
**Tests:** [What test coverage is needed]
**Docs:** [Docs to update, or why none]
**Risks:** [Any concerns, or "None"]
**Capture:** [Inputs and output reference]
**Status:** pending | gated

### Implement
**Implemented:**
- [What was built/changed]
- [Key decisions made]
**Checks run:** [Commands and results, or "not run"]
**Changed files:** [Paths]
**Plan deviations:** [None, or explanation]
**Capture:** [Inputs and output reference]
**Status:** pending | gated

### Verify
**Verify status:** pass | fail | blocked
**Recommendation:** proceed to Docs | return to Implement | needs input
**Automated tests:**
- changes: [Test files, or none with reason]
- commands: [Commands run]
- result: [Summary]
**Manual tests:**
- required: yes | no
- reason: [why]
- result: [Summary or not applicable]
**QA findings:**
- correctness:
- coverage:
- regressions:
- code quality:
- risks:
**Capture:** [Inputs and output reference]
**Status:** pending | gated

### Docs
**Changed:** yes | no
**Files:** [Docs paths, or "none"]
**Rationale:** [What changed, or why docs were not needed]
**Policy check:** [Implemented docs vs planning docs]
**Capture:** [Inputs and output reference]
**Status:** pending | gated

### Final Review
**Recommendation:** close | return to Plan | return to Implement | return to Verify | return to Docs
**Reviewed:**
- plan artifact
- implementation diff
- verify artifact
- docs artifact
**Findings:** [None, or required changes]
**Commit:** [commit hash after close]
**Capture:** [Inputs and output reference]
**Status:** pending | gated
```

## Auto Mode Format

When running in auto mode, add this section:

```markdown
### Auto Mode
**Mode:** auto
**Gate:** project-configured | codex
**Escalations:** 0

| Phase | Gate result | Notes |
|---|---|---|
| Plan | GO | Clear requirements |
| Implement | GO | Diff matches plan |
| Verify | pending | Not run yet |
| Docs | pending | Not run yet |
| Final Review | pending | Not run yet |
```

If escalation occurs:

```markdown
### Auto Mode
**Mode:** auto -> escalated
**Gate:** project-configured | codex
**Escalations:** 1

| Phase | Gate result | Notes |
|---|---|---|
| Verify | BLOCK | Coverage missing for timeout handling |

**Escalation reason:** [reason]
**Concerns:**
- [concern 1]
- [concern 2]
```

## Example: Feature Task

```markdown
## Dev Notes

### Plan
**Approach:** Add theme toggle using CSS variables and React context. Store preference in localStorage.
**Files:** src/contexts/ThemeContext.tsx, src/components/ThemeToggle.tsx, src/styles/globals.css
**Tests:** Unit test context state changes and localStorage persistence.
**Docs:** Update settings guide with theme preference behavior.
**Risks:** None
**Capture:** inputs: bead description and existing theme styles; output: bead design
**Status:** gated

### Implement
**Implemented:**
- ThemeProvider context with light/dark themes
- ThemeToggle component with accessibility support
- CSS variables for all color tokens
**Checks run:** npm test -- theme.test.tsx, pass
**Changed files:** src/contexts/ThemeContext.tsx, src/components/ThemeToggle.tsx, src/styles/globals.css
**Plan deviations:** None
**Capture:** inputs: approved plan; output: working tree diff and bead notes
**Status:** gated

### Verify
**Verify status:** pass
**Recommendation:** proceed to Docs
**Automated tests:**
- changes: theme.test.tsx
- commands: npm test -- theme.test.tsx
- result: pass
**Manual tests:**
- required: yes
- reason: changed behavior requires browser confirmation
- result: browser toggle and refresh behavior passed
**QA findings:**
- correctness: theme switches and persists
- coverage: adequate for changed behavior
- regressions: none found
- code quality: follows existing React patterns
- risks: none material
**Capture:** inputs: implementation diff; output: append-only Verify note
**Status:** gated

### Docs
**Changed:** yes
**Files:** docs/settings.md
**Rationale:** Added implemented theme preference behavior.
**Policy check:** Shipped docs describe implemented behavior only.
**Capture:** inputs: verified implementation; output: docs diff
**Status:** gated

### Final Review
**Recommendation:** close
**Reviewed:** plan artifact, implementation diff, verify artifact, docs artifact
**Findings:** None
**Commit:** a1b2c3d add theme preference toggle
**Capture:** inputs: all phase artifacts; output: final review note and commit
**Status:** gated
```

## Updating Dev Notes

Each phase updates its section:

1. Plan writes the Plan section and capture references.
2. Plan gate marks Plan as gated.
3. Implement writes the Implement section and capture references.
4. Implement gate marks Implement as gated.
5. Verify writes one status artifact and capture references.
6. Verify gate determines whether to proceed, return, or remain blocked.
7. Docs writes changed-docs or no-docs rationale and capture references.
8. Docs gate marks Docs as gated.
9. Final Review writes close/return recommendation and capture references.
10. Final Review gate marks Final Review as gated; close recommendation commits and closes the bead.

## Reading Dev Notes for Phase Detection

```text
No "## Dev Notes" section       -> Start Plan
Plan pending                    -> Resume Plan gate
Plan gated, no Implement        -> Start Implement
Implement pending               -> Resume Implement gate
Implement gated, no Verify      -> Start Verify
Implement gated and newer than failed Verify -> Start Verify
Verify pending                  -> Resume Verify gate
Verify fail and gated           -> Start Implement
Verify blocked and gated        -> Wait for missing input, then resume Verify
Verify pass and gated, no Docs  -> Start Docs
Docs pending                    -> Resume Docs gate
Docs gated, no Final Review     -> Start Final Review
Final Review pending            -> Resume Final Review gate
Final Review returns to Plan and no newer Plan -> Start Plan
Final Review returns to Implement and no newer Implement -> Start Implement
Final Review returns to Verify and no newer Verify -> Start Verify
Final Review returns to Docs and no newer Docs -> Start Docs
Plan gated and newer than Final Review return -> Start Implement
Implement gated and newer than Final Review return -> Start Verify
Verify pass gated and newer than Final Review return -> Start Docs
Verify fail gated and newer than Final Review return -> Start Implement
Verify blocked gated and newer than Final Review return -> Wait for input, then resume Verify
Docs gated and newer than Final Review return -> Start Final Review
Final Review recommends close   -> Commit and close bead
```

When a later phase attempt supersedes an earlier artifact, do not edit the old
artifact. Compare attempt order or timestamps. This applies to failed Verify
artifacts and Final Review return recommendations.
Prefer explicit attempt numbers. When attempt numbers are missing, use bead
note append order or note timestamps as the practical ordering source.
