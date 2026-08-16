# etude doctor

`etude doctor` is a strictly read-only health check for a repository's Etude
setup. It is safe to run during active work and exits non-zero if any check
fails, so it can gate CI or lane startup.

```bash
etude doctor
```

Doctor reports every finding before it exits:

- `OK` means the checked condition is healthy.
- `WARN` means the setup deserves attention but is not known to be unsafe.
- `FAIL` makes the command exit non-zero.
- `PROXY` marks evidence that cannot prove the complete condition. For example,
  finding a reviewer executable does not prove authentication, quota, or tier
  eligibility.

## What it checks

Doctor parses `.etude/workflow.yaml` and `.etude/registry.yaml` strictly and
verifies that referenced tiers, seats, rubrics, and runner commands
cross-resolve. Rubric paths must be readable regular files contained under
`.etude`; relative runner and adapter scripts must exist and be executable.

Seat reachability is resolved through supported `env` prefixes and
`scripts/seat-adapter.sh` to the actual reviewer executable. Doctor does not
mistake a reachable adapter or `env` binary for a reachable reviewer. A
host-provided `in-harness:` invocation is reported as a proxy because there is
no local executable to inspect. Arbitrary shell and command-interpreter wrappers
are also reported as `NOT CHECKED`: doctor does not execute or guess at the
program hidden inside them. Implementation-specific `env` options receive the
same treatment when their behavior cannot be reproduced without executing the
installed utility.

For every configured Git remote, doctor checks:

- fetch refspecs cannot prune authoritative local `refs/etude/<kind>/*` refs;
- ordinary fetches mirror each kind into
  `refs/etude-mirror/<remote>/<kind>/*`, not the old authoritative namespace;
- name-preserving push refspecs cover every Etude ref kind;
- each local run, retro, and eval ref is present in the last locally fetched
  mirror, without recommending a push when the mirrored ref is newer or its
  direction cannot be established.

Doctor performs no network or credential operation. Remote reachability is
reported as `NOT CHECKED`, never inferred from a URL or local executable. Run-ref
comparison uses `refs/etude-mirror/<remote>/<kind>/*`, the disposable local
snapshot updated by the last fetch. Git does not record a reliable per-mirror
fetch timestamp, so doctor reports the update time as `NOT RECORDED`.

That staleness matters: an absent mirrored ref may already exist remotely, a
matching ref may since have changed, and a differing ref may now have another
relationship. Doctor states those limits in each comparison. Its `etude sync`
remedy is safe because sync fetches first and uses non-forced transfers; an
unknown or newer mirrored ref receives human-only guidance instead. Multiple
remote URLs or any configured `pushurl` also suppress sync advice because the
single fetched mirror cannot establish the state of every push destination.

## Remediation

When a deterministic command fixes the exact observed state, doctor prints it
after `remedy:`. Commands that edit Git config are scoped to the configuration
file and exact value doctor observed, and command arguments are quoted for the
platform running doctor.

Some failures have no derivable command. A missing rubric, for example, needs
project-specific evaluation criteria that doctor cannot invent. These findings
say exactly what is missing and what it must contain, prefixed with:

```text
HUMAN AUTHORSHIP REQUIRED:
```

That marker is intentional. Doctor never emits a plausible-looking command
that cannot actually repair the diagnosed condition.

## Snapshot and exit behavior

Doctor performs ordered local observations rather than locking the repository.
It reads mirror refs before authoritative local refs, then rechecks remote
configuration. This is not a transaction; doctor always reports that limitation
and warns when it observes concurrent configuration changes. Rerun it after
concurrent work settles.

The command writes no files, refs, config, index entries, credentials, or
network state, and does not contact a remote. It prints to standard output and
returns success only when there are no `FAIL` findings. Like other Git tooling,
doctor trusts the host `git` executable selected by the process environment; a
malicious replacement executable is outside the repository health-check trust
boundary.
