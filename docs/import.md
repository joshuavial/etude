# etude import

Import historical GitHub pull requests as etude run records.

## Synopsis

```
etude import --from-github --repo <owner/name> [--last N] [--state merged|closed|all] [--dry-run] [--message <msg>]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--from-github` | (required) | Source selector. Must be supplied; future sources (`--from-gitlab`, etc.) are additive. |
| `--repo <owner/name>` | (required) | GitHub repository in `owner/name` format. Both segments must be non-empty. |
| `--last N` | 50 | Number of PRs to import. Must be >= 1. |
| `--state merged\|closed\|all` | merged | PR state to import. Only merged PRs carry a merge commit; closed/open PRs without a merge commit are skipped with a warning. |
| `--dry-run` | false | Fetch and map PRs, then print what would be imported without writing any refs. |
| `--message <msg>` | (auto) | Optional commit message prefix for each imported run ref. Default: `github-import: create run <id> (pr #N from owner/repo)`. |

## gh CLI requirement

`etude import` uses the `gh` CLI to fetch PR data. It does NOT use a GitHub token. Before any fetch, it runs `gh auth status`. If `gh` is not on `PATH` or not authenticated, the command exits with:

```
gh CLI is required and must be authenticated; install gh and run 'gh auth login' (etude import uses gh, not a GitHub token)
```

## PR to run mapping

Each imported PR becomes one etude run. The mapping is:

| Field | Value |
|-------|-------|
| `run_id` | `gh-<owner>-<repo>-pr<number>` (sanitized to satisfy `IsValidRunID`) |
| `git_sha` | `mergeCommit.oid` — the PR merge commit |
| `occurred_at` | `mergedAt` — so `etude log` places runs at their real merge time, not import time |
| `workflow` | `github-import` |
| `workflow_version` | `github-import-v1` |
| `refs.pr` | PR number as string |
| `refs.repo` | `owner/name` |
| `refs.source` | `github` |
| `refs.url` | PR URL |
| `refs.author` | PR author login |

## Stage shape (review-only — honest limitation)

Imported runs carry **only review/docs-usable artifacts** (final diff + PR body). Plan and implement first-draft artifacts were never saved and are deliberately absent.

**Normal merged PR (with body):** one `review` stage:
- Input: the final diff artifact (role `diff`)
- Output: the PR body artifact (role `pr-body`)
- `produced_by: import`

**Empty-body PR:** one `final-diff` stage:
- Output: the diff artifact (role `diff`)
- No review output (no body to store)

**No merge commit (e.g. closed-unmerged):** PR is skipped with a warning.

**Diff fetch failure:** that specific PR is skipped with a warning; the rest of the batch continues.

Because plan and implement stages are absent, imported runs are suitable for benchmarking `review` and `docs` skills but NOT for `plan` or `implement` skill benchmarking.

## gh CLI calls

```
# Preflight
gh auth status

# List PRs (one call)
gh pr list --repo <owner/name> --state <state> --limit <N> \
  --json number,title,body,mergedAt,mergeCommit,author,url,state

# Per-PR diff (one call each)
gh pr diff <number> --repo <owner/name>
```

## Idempotency

Runs are written create-only (`refs/etude/runs/<id>`). Re-running `etude import` on the same repo never clobbers existing runs — each already-present PR is skipped with an informational message.

## Read-only

`etude import` only reads from GitHub (no `gh pr edit`, no API writes). It writes only `refs/etude/runs/*` locally. To push refs to a remote, run `etude sync` separately.

## Example

```bash
# Import last 10 merged PRs from cli/cli
etude import --from-github --repo cli/cli --last 10

# Preview without writing
etude import --from-github --repo cli/cli --last 10 --dry-run

# Import closed PRs too
etude import --from-github --repo cli/cli --last 20 --state all

# Inspect an imported run
etude run show gh-cli-cli-pr1234
etude log
```
