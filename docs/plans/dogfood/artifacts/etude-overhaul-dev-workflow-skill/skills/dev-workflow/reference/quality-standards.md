# Quality Standards

## Test Coverage

### Expectations
- **New code**: Should have tests covering primary functionality
- **Bug fixes**: Must have test that would have caught the bug
- **Refactoring**: Existing tests should continue to pass

### What "Adequate Coverage" Means
- Happy path is tested
- Key edge cases are tested
- Error handling is tested
- Integration points are tested

### What's NOT Required
- 100% line coverage
- Screenshot evidence of tests
- Strict TDD ceremony (write test first)
- Coverage reports in documentation

### Judgment Calls
Use common sense:
- Trivial changes may not need new tests
- Complex logic needs thorough testing
- UI changes may need different test approaches
- Config changes may just need validation tests

---

## Refactoring

### Always Do
- Remove dead code
- Extract duplicated logic
- Use meaningful names
- Keep functions focused
- Follow existing patterns in codebase

### Consider Doing
- Improve unclear code you touch
- Add types where helpful
- Simplify complex conditionals
- Extract constants from magic values

### Don't Over-Engineer
- Don't refactor unrelated code
- Don't add abstractions for single use cases
- Don't optimize prematurely
- Don't change patterns project-wide for one feature

---

## Code Quality

### Must Have
- Code compiles/runs without errors
- No obvious bugs
- Follows existing code style
- Handles errors appropriately
- No security vulnerabilities

### Should Have
- Clear variable/function names
- Appropriate comments for complex logic
- Consistent formatting
- Reasonable file organization

### Avoid
- Commented-out code
- Console.log debugging left in
- TODO comments for the current task
- Overly clever solutions

---

## Commit Quality

### Commit Message
- Single line, under 72 characters
- Starts with lowercase verb (add, fix, update, remove)
- Describes what changed, not how
- Never mentions Claude, AI, or automation

### Good Examples
```
add email service with SendGrid integration
fix user session timeout on mobile
update dashboard to show real-time stats
remove deprecated payment API
```

### Bad Examples
```
WIP
Fixed stuff
Updated files
🤖 Generated with Claude Code
Add feature (addresses PR feedback)
```

### Commit Scope
- One logical change per commit
- Task = one commit (usually)
- If task needs multiple commits, consider if it should be multiple tasks

---

## QA Verification

### Verify Checks
- [ ] All tests pass
- [ ] New tests cover the changes
- [ ] Tests actually test what they claim to
- [ ] Code follows quality standards above
- [ ] Changes match the approved plan
- [ ] No obvious regressions
- [ ] Manual testing was considered and run when relevant
- [ ] Verify status is `pass`, `fail`, or `blocked`
- [ ] Recommendation matches the Verify status

### Docs Verification
- [ ] User-facing docs were updated when implemented behavior changed
- [ ] No planned or future behavior is described as shipped behavior
- [ ] Planning notes live under the repo's planning-docs area
- [ ] Generated command references are separate from hand-written guides
- [ ] Links added or changed by the docs update resolve

### Final Review Checks
- [ ] Plan, Implement, Verify, and Docs artifacts are present
- [ ] Final diff matches the approved plan or explains deviations
- [ ] Verify is adequate and not contradicted by later changes
- [ ] Docs are accurate and policy-compliant
- [ ] No accidental files, debug output, or unrelated refactors
- [ ] Commit scope and message are appropriate

### Epic/PR Review Checks
- [ ] All tasks completed
- [ ] Commits are coherent together
- [ ] Overall coverage is adequate
- [ ] No conflicts or integration issues
- [ ] Matches original issue requirements
- [ ] Ready for external review

---

## When to Push Back

Verify should flag issues when:
- Tests are missing for significant new code
- Code is obviously hard to maintain
- Changes don't match the plan without explanation
- Security concerns are present
- Performance issues are obvious
- Manual testing is skipped for browser-visible or workflow behavior
- The implementation needs changes, which should return the bead to Implement

Verify should NOT be pedantic about:
- Minor style preferences
- Test coverage percentages
- Theoretical edge cases unlikely to occur

Docs or Final Review should flag issues when:
- Required docs are missing
- Shipped docs describe planned behavior as implemented
- Docs contradict the verified behavior
- The docs/no-docs rationale is too vague to review
