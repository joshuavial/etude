# OpenSpec Format Reference

This document defines the exact format required for OpenSpec specs to pass validation.

## Directory Structure

```
openspec/
├── project.md                    # Project context (optional)
├── specs/                        # Source of truth (archived specs live here)
│   └── <capability>/
│       └── spec.md
└── changes/                      # Proposed changes
    └── <change-name>/
        ├── proposal.md           # Rationale and overview (MUST have ## Why)
        ├── tasks.md              # Implementation checklist (optional)
        └── specs/                # REQUIRED: Capability folders with deltas
            └── <capability>/
                └── spec.md       # Delta file with requirements
```

**CRITICAL**: The `specs/` directory inside a change MUST contain capability folders (e.g., `specs/dev-vps/spec.md`), NOT flat files (e.g., `specs/01-dev-vps.md`).

## Proposal File Format (`proposal.md`)

The proposal.md file requires specific sections:

```markdown
# <Change Name>

## Why

<Problem statement - why this change is needed>

## What

<High-level description of the change>

## How

<Technical approach and key decisions>
```

**Required**: The `## Why` section is mandatory. Validation fails without it.

**Optional sections**: `## Status`, `## Tasks`, `## Open Questions`, `## Non-Goals`

## Delta File Format (`spec.md`)

Each spec.md file must use this exact structure:

```markdown
# <Capability Name>

## ADDED Requirements

### Requirement: <Short descriptive name>

<Description using SHALL/MUST/SHOULD language>

#### Scenario: <scenario-name>

- **Given**: <precondition>
- **When**: <action>
- **Then**: <expected outcome>

#### Scenario: <another-scenario>

- **Given**: <precondition>
- **When**: <action>
- **Then**: <expected outcome>

## MODIFIED Requirements

### Requirement: <Existing requirement name>

<Complete updated requirement text - NOT a diff>

#### Scenario: <scenario-name>

- **Given**: <precondition>
- **When**: <action>
- **Then**: <expected outcome>

## REMOVED Requirements

### Requirement: <Deprecated requirement name>

**Rationale**: <Why this requirement is being removed>

## RENAMED Requirements

### Requirement: <Old Name> → <New Name>

**Rationale**: <Why the name changed>
```

## Validation Rules

The `openspec validate` command checks:

1. **Delta headers present**: At least one of `## ADDED`, `## MODIFIED`, `## REMOVED`, or `## RENAMED Requirements`
2. **Requirements have scenarios**: Every `### Requirement:` block must have at least one `#### Scenario:` block
3. **Scenario format**: Must use level-4 heading (`####`), NOT bullets or bold text
4. **Capability folders**: Specs must be in `specs/<capability>/spec.md` structure

## Common Validation Errors

### "Change must have a Why section"

**Problem**: The proposal.md file is missing the required `## Why` section.

**Fix**: Add a `## Why` section to your proposal.md:
```markdown
## Why

The cortex needs to control remote containers but currently has no SSH/SCP capabilities...
```

### "No deltas found"

**Problem**: The spec files don't have delta headers or aren't in capability folders.

**Fix**: Ensure your specs are in:
```
openspec/changes/<change>/specs/<capability>/spec.md
```

And each spec.md has at least one:
```markdown
## ADDED Requirements
```

### "Requirement missing scenario"

**Problem**: A requirement doesn't have a scenario block.

**Fix**: Add at least one scenario:
```markdown
### Requirement: User authentication

The system SHALL authenticate users via SSH keys.

#### Scenario: Valid key authentication

- **Given**: A user with a valid SSH key
- **When**: They attempt to connect
- **Then**: The connection is established
```

### "Invalid scenario format"

**Problem**: Using bullets or bold instead of heading for scenario.

**Wrong**:
```markdown
**Scenario: Test case**
- Scenario: Test case
```

**Correct**:
```markdown
#### Scenario: Test case
```

## Example: Complete Spec File

```markdown
# VPS Control Library

## ADDED Requirements

### Requirement: SSH Connection Management

The Target class SHALL manage SSH connections to remote hosts using the ssh2 library.

The system SHALL support:
- Host and port configuration
- SSH key-based authentication
- Connection timeout configuration
- Automatic reconnection on failure

#### Scenario: Successful connection

- **Given**: A valid SSH host, port, and private key
- **When**: Target.connect() is called
- **Then**: An SSH connection is established and Target.isConnected returns true

#### Scenario: Connection timeout

- **Given**: An unreachable host
- **When**: Target.connect() is called with a 5 second timeout
- **Then**: A TimeoutError is thrown after 5 seconds

#### Scenario: Invalid credentials

- **Given**: An invalid private key
- **When**: Target.connect() is called
- **Then**: A ConnectionError is thrown with authentication failure details

### Requirement: Command Execution

The Target class SHALL execute commands on remote hosts via SSH.

#### Scenario: Successful command execution

- **Given**: An established SSH connection
- **When**: target.exec("echo hello") is called
- **Then**: The result contains code: 0, stdout: "hello\n"

#### Scenario: Command failure

- **Given**: An established SSH connection
- **When**: target.exec("exit 1") is called
- **Then**: The result contains code: 1

### Requirement: File Transfer

The Target class SHALL support file transfer via SCP.

#### Scenario: Upload file

- **Given**: An established SSH connection
- **When**: target.scpTo("./local.txt", "/remote/path.txt") is called
- **Then**: The file exists at /remote/path.txt on the target

#### Scenario: Download file

- **Given**: An established SSH connection and a file at /remote/file.txt
- **When**: target.scpFrom("/remote/file.txt", "./local.txt") is called
- **Then**: The file exists at ./local.txt locally
```

## Converting Prose Specs to OpenSpec Format

If you have existing prose documentation, convert it by:

1. **Identify capabilities**: Group related functionality into capability folders
2. **Extract requirements**: Each major feature becomes a `### Requirement:`
3. **Add scenarios**: For each requirement, write test-like scenarios
4. **Use delta headers**: Wrap in `## ADDED Requirements` for new specs

## CLI Commands

```bash
# Validate a specific change
openspec validate <change-name>

# Show parsed deltas (debug)
openspec change show <change-name> --json --deltas-only

# List all changes
openspec list

# Show change details
openspec show <change-name>

# Archive completed change (merges to specs/)
openspec archive <change-name>
```

## Sources

- [OpenSpec GitHub](https://github.com/Fission-AI/OpenSpec)
- [OpenSpec Documentation](https://openspec.dev/)
