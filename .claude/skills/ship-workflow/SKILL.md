---
name: ship-workflow
description: Wrap up a proposal - verify tests, check docs, create PR, handle reviews, merge. Use when all beads are complete. Invoke with /ship.
---

# Ship Workflow

Finalize a proposal: verify everything, create PR, get it merged.

## When to Use

- All beads under a proposal are complete
- User says `/ship <name>`
- Ready to create PR and merge

## Core Concept

**Ship = Verify + PR + Merge**

Before shipping:
- All beads must be closed
- Integration tests must pass and cover specs
- Docs must be updated
- Code must be ready for review

## Command Interface

```bash
/ship <proposal-id>        # Full ship flow
/ship <proposal-id> verify # Just run verification
/ship <proposal-id> pr     # Create PR (after verify passes)
/ship status <proposal-id> # Check ship readiness
```

## Workflow

### Step 1: Verify All Beads Complete

```bash
bd show <proposal-id>
```

Check all child beads are closed. If any open:
- Show which beads remain
- Ask user to complete them first or close manually

**Gate**: Cannot proceed with open beads.

### Step 2: Run Full Test Suite

```bash
npm test
# or pytest, make test, etc.
```

All tests must pass. No exceptions.

**Gate**: Cannot proceed with failing tests.

### Step 3: Run Integration Tests

```bash
npm run test:integration
# or pytest tests/integration/
```

Integration tests specifically must pass.

**Gate**: Cannot proceed with failing integration tests.

### Step 4: Verify Spec Coverage

Compare integration tests against spec scenarios:

1. Read `openspec/changes/<name>/specs/*/spec.md`
2. Extract all `#### Scenario:` blocks
3. Verify each scenario has a corresponding integration test

Output:
```markdown
## Spec Coverage

| Scenario | Test | Status |
|----------|------|--------|
| Valid connection | test_valid_connection | ✅ |
| Connection timeout | test_timeout | ✅ |
| File upload | ❌ MISSING | ⚠️ |
```

**Gate**: All scenarios must have tests. Refuse to ship if missing.

### Step 5: Check Documentation

Verify:
- [ ] README updated (if user-facing changes)
- [ ] API docs updated (if API changes)
- [ ] CHANGELOG entry added (if CHANGELOG.md exists)

If missing, either:
- Fix automatically (simple cases)
- Flag for user to address

### Step 6: Generate Manual Test Script (Optional)

If requested or complex UI changes:

```markdown
## Manual Test Script

### Test: <feature>

1. Navigate to /settings
2. Click "Notifications"
3. Toggle email notifications ON
4. Verify confirmation toast appears
5. Check email arrives within 2 minutes

Expected: Email received with correct content
```

### Step 7: Create PR

```bash
gh pr create --title "<proposal title>" --body "$(cat <<'EOF'
## Summary

<Brief description from proposal.md>

## Changes

- <Key change 1>
- <Key change 2>
- <Key change 3>

## Testing

- All unit tests pass
- Integration tests pass and cover spec scenarios
- Manual testing completed (if applicable)

## Specs

See `openspec/changes/<name>/` for detailed requirements.

Closes #<issue> (if linked)
EOF
)"
```

Link PR to proposal bead:
```bash
bd update <proposal-id> --pr="<pr-url>"
```

### Step 8: Handle Review Feedback

If reviewers request changes:

1. Create fix beads under the proposal:
   ```bash
   bd create --type=task --title="Address review: <feedback>" --parent=<proposal-id>
   ```

2. Run `/dev <fix-bead>` for each fix

3. Push fixes, re-request review

4. Repeat until approved

### Step 9: Merge

Once approved:

```bash
gh pr merge <pr-number> --squash  # or --merge based on project convention
```

### Step 10: Archive and Close

```bash
# Archive the openspec change
openspec archive <name>

# Close the proposal bead
bd close <proposal-id> --reason="Merged in PR #<number>"

# Clean up branch (optional)
git branch -d feat/<name>
```

## Checklist

Before creating PR:

- [ ] All child beads closed
- [ ] All tests pass
- [ ] Integration tests pass
- [ ] Every spec scenario has integration test coverage
- [ ] Documentation updated
- [ ] No debug code or console.logs
- [ ] Commits are clean and well-scoped

Before merging:

- [ ] PR approved by reviewers
- [ ] CI passes
- [ ] No merge conflicts
- [ ] Manual testing complete (if required)

## Gates Summary

| Gate | Requirement | Can Override? |
|------|-------------|---------------|
| All beads closed | Every child bead status=closed | No |
| Tests pass | `npm test` exits 0 | No |
| Integration tests pass | Integration suite exits 0 | No |
| Spec coverage | Every scenario has test | No |
| Docs updated | README/CHANGELOG current | User can defer |
| PR approved | At least 1 approval | No |

## Output

### Verification Report

```markdown
## Ship Verification: <proposal>

### Beads
✅ 4/4 beads closed

### Tests
✅ 47 tests passing
✅ 12 integration tests passing

### Spec Coverage
✅ 8/8 scenarios covered

### Documentation
✅ README updated
✅ CHANGELOG entry added
⚠️ API docs - no changes needed

### Ready to Ship
All gates pass. Create PR?
```

### After Merge

```markdown
## Shipped: <proposal>

**PR:** #42 (merged)
**Branch:** feat/<name> (deleted)
**Specs:** Archived to openspec/specs/

### Summary
- 4 beads completed
- 847 lines added, 23 removed
- 12 integration tests added

Proposal closed.
```

## Integration with Other Commands

| Before | After |
|--------|-------|
| `/dev` (all beads) | `/ship` |
| `/ship` | Done (or next `/scope` for next proposal) |
