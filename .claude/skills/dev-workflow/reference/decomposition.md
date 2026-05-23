# Work Decomposition

## When to Decompose

Break work into multiple tasks when:
- Implementation has distinct, separable parts
- Work would result in multiple unrelated changes
- Different parts have different dependencies
- A single commit would be too large to review easily

Keep as single task when:
- Work is small and focused
- Changes are tightly coupled
- Splitting would create artificial boundaries

## Task Sizing

### Right-Sized Task
- One logical change
- Reviewable in isolation
- Can be described in one sentence
- Results in one clean commit
- Typically under 300 lines of changes

### Too Small
- Just renaming a variable
- Adding a single import
- Trivial one-line fix
(These can be combined into a larger logical task)

### Too Large
- Multiple unrelated features
- Changes spanning many domains
- Would require "and" to describe
- Can't be reviewed without losing context

## Decomposition Patterns

### By Layer
```
Epic: Add user preferences
├── Task: Create preferences database schema
├── Task: Add preferences API endpoints
└── Task: Build preferences UI component
```

### By Feature
```
Epic: Implement notification system
├── Task: Add email notifications
├── Task: Add in-app notifications
└── Task: Add notification preferences
```

### By Dependency
```
Epic: Add payment processing
├── Task: Integrate Stripe SDK (blocks all)
├── Task: Create payment form (depends on SDK)
├── Task: Add payment confirmation (depends on form)
└── Task: Handle payment webhooks (depends on SDK)
```

### By Risk
```
Epic: Migrate to new database
├── Task: Add new database adapter (safe, reversible)
├── Task: Create data migration scripts (testable)
├── Task: Switch read operations (can rollback)
└── Task: Switch write operations (final step)
```

## Setting Dependencies

Use `bd dep add <task> <depends-on>` to establish order:

```bash
# Task 102 depends on 101 (101 must complete first)
bd dep add zk-102 zk-101

# Task 103 depends on both 101 and 102
bd dep add zk-103 zk-101
bd dep add zk-103 zk-102
```

### Dependency Guidelines
- Set dependencies when order matters for correctness
- Don't over-constrain (allow parallel work where possible)
- Consider: "Would this task break if the other isn't done?"

## Presenting Decomposition

When presenting task breakdown to user:

```markdown
I've broken this into 3 tasks:

1. **zk-101: Add email service integration**
   Foundation for sending emails. No dependencies.

2. **zk-102: Create notification trigger system**
   Determines when to send notifications.
   Depends on: zk-101 (needs email service)

3. **zk-103: Build notification preferences UI**
   Lets users configure their notification settings.
   Depends on: zk-101, zk-102

Does this breakdown look right?
```

## Adjusting Decomposition

If user requests changes:
- Merge tasks that are too granular
- Split tasks that are too large
- Adjust dependencies as needed
- Re-present for approval

The decomposition is a proposal, not a mandate. User has final say on task boundaries.
