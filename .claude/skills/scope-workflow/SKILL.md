---
name: scope-workflow
description: Scope out a proposal - create branch, specs, and beads before coding begins. Use when starting work on a PR-sized feature. Invoke with /scope.
---

# Scope Workflow

Set up everything needed before coding begins: branch, specs, and beads.

## When to Use

- Starting a new proposal (PR-sized work)
- User says `/scope <name>`
- After `/pm` identifies proposals to create
- When work needs branch + specs + breakdown before `/dev`

## Core Concept

**1 Proposal = 1 Branch = 1 PR**

A proposal is the unit of shippable work. It has:
- A git branch
- OpenSpec requirements (maps to integration tests)
- Child beads (each = 1 commit)

## Command Interface

```bash
/scope <name>              # Full setup flow
/scope <name> --from-prd <file>  # Reference parent PRD
/scope status <name>       # Check proposal status
```

## Workflow

### Step 1: Create Branch

```bash
git checkout -b feat/<name>
# or fix/<name> for bug-focused proposals
```

**Always create branch first.** This is non-negotiable.

### Step 2: Create OpenSpec Structure

```bash
mkdir -p openspec/changes/<name>/specs
```

Create:
- `proposal.md` - Why and what changes
- `specs/<capability>/spec.md` - Requirements with scenarios

### Step 3: Draft Proposal

`openspec/changes/<name>/proposal.md`:

```markdown
# <Proposal Name>

> One-line summary

## Why

<Problem being solved - keep it crisp>

## What Changes

<High-level description of changes>
- Component/file 1
- Component/file 2

## Scope

### In Scope
- Feature A
- Feature B

### Out of Scope
- Explicitly excluded items
```

### Step 4: Draft Specs

Each capability gets `specs/<capability>/spec.md`:

```markdown
# <Capability Name>

## ADDED Requirements

### Requirement: <Name>

<Description using SHALL/MUST/SHOULD>

#### Scenario: <name>

- **Given**: <precondition>
- **When**: <action>
- **Then**: <expected outcome>
```

**Critical**: Every requirement needs scenarios. These become integration tests.

### Step 5: Validate Specs

```bash
openspec validate <name>
```

Must pass before proceeding. Check:
- Capability folder structure
- Delta headers present
- All requirements have scenarios

### Step 6: Create Proposal Bead

```bash
bd create --type=proposal --title="<Name>" \
  --description="<summary>" \
  --branch="feat/<name>" \
  --spec="openspec/changes/<name>"
```

The proposal bead tracks:
- Branch name
- Spec path
- Child beads
- Overall status

### Step 7: Break Down into Beads

Analyze the proposal and create child beads:

```bash
bd create --type=feature --title="<bead title>" --parent=<proposal-id>
bd create --type=task --title="<bead title>" --parent=<proposal-id>
bd dep add <child> <depends-on>
```

**Bead sizing**: Each bead = 1 commit = 1 logical change

Types:
- `feature` - New functionality
- `bug` - Fix broken behavior
- `task` - Chores, refactoring, docs, tests

### Step 8: Present for Approval

Show user:
- Branch created
- Proposal summary
- Spec overview
- Bead breakdown with dependencies

Get approval before any `/dev` work begins.

## Checklist

Before completing `/scope`:

- [ ] Branch created and checked out
- [ ] `openspec/changes/<name>/` structure exists
- [ ] `proposal.md` has Why and What Changes sections
- [ ] All specs have requirements with scenarios
- [ ] `openspec validate <name>` passes
- [ ] Proposal bead created (type=proposal)
- [ ] Child beads created with dependencies
- [ ] User approved the plan

## Output

When complete, show:

```markdown
## Proposal Scoped: <name>

**Branch:** `feat/<name>`
**Proposal bead:** <id>

### Beads Created

1. <id>: <title> (feature)
2. <id>: <title> (task) - depends on 1
3. <id>: <title> (task) - depends on 1, 2

### Ready for Development

Run `/dev` to start working on ready beads.
Run `bd ready` to see what's unblocked.
```

## Integration with Other Commands

| Before | After |
|--------|-------|
| `/pm` | `/scope` |
| `/scope` | `/dev` (repeat per bead) |
| `/dev` (all beads done) | `/ship` |

## Reference

See `reference/openspec-format.md` for spec formatting details.
