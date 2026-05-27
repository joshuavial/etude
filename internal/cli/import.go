package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/spf13/cobra"
)

const (
	importWorkflow        = "github-import"
	importWorkflowVersion = "github-import-v1"
	importProducedBy      = "import"
	importSkillID         = "github-import"
	importSkillRepo       = "github-import"
	importSkillVersion    = "github-import"
	importHarnessName     = "github-import"
)

// ghPR is the DTO for one pull request from gh pr list --json.
type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	MergedAt    string `json:"mergedAt"`
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	URL   string `json:"url"`
	State string `json:"state"`
}

// ghClient is the interface for gh CLI operations. Tests inject a stub.
type ghClient interface {
	AuthStatus(ctx context.Context) error
	ListPRs(ctx context.Context, repo, state string, limit int) ([]ghPR, error)
	Diff(ctx context.Context, repo string, number int) ([]byte, error)
	Version(ctx context.Context) string
}

// execGHClient is the production ghClient that shells out to the gh CLI.
type execGHClient struct{}

func (e *execGHClient) AuthStatus(ctx context.Context) error {
	// Check gh is on PATH first so missing-binary and unauthed produce the same
	// friendly error message that the runner surfaces to the user.
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh auth status failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (e *execGHClient) ListPRs(ctx context.Context, repo, state string, limit int) ([]ghPR, error) {
	args := []string{
		"pr", "list",
		"--repo", repo,
		"--state", state,
		"--limit", strconv.Itoa(limit),
		"--json", "number,title,body,mergedAt,mergeCommit,author,url,state",
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh pr list failed: %s", strings.TrimSpace(stderr.String()))
	}
	var prs []ghPR
	if err := json.Unmarshal([]byte(stdout.String()), &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	return prs, nil
}

func (e *execGHClient) Diff(ctx context.Context, repo string, number int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "diff", strconv.Itoa(number), "--repo", repo)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh pr diff %d failed: %s", number, strings.TrimSpace(stderr.String()))
	}
	return []byte(stdout.String()), nil
}

func (e *execGHClient) Version(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "gh", "--version")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	// "gh version 2.88.1 (2026-03-12)\n..."
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return line
}

// buildImportRunID converts an owner/repo pair and PR number into a valid run ID.
// Pipeline: replace invalid chars (incl. '/') with '-' → collapse '--' → collapse
// '..' → trim leading/trailing '-.' → strip '.lock' suffix.
// The caller also guards with runmanifest.IsValidRunID and skips any ID that
// does not pass (belt-and-suspenders for pathological repo names).
var invalidRunIDChar = regexp.MustCompile(`[^A-Za-z0-9_.\-]`)

func buildImportRunID(owner, repo string, number int) string {
	// Replace any invalid char (including '/') with '-'
	sanitizedOwner := invalidRunIDChar.ReplaceAllString(owner, "-")
	sanitizedRepo := invalidRunIDChar.ReplaceAllString(repo, "-")
	id := fmt.Sprintf("gh-%s-%s-pr%d", sanitizedOwner, sanitizedRepo, number)
	// Collapse multiple consecutive dashes to one
	for strings.Contains(id, "--") {
		id = strings.ReplaceAll(id, "--", "-")
	}
	// Collapse multiple consecutive dots to one (IsValidRunID rejects "..")
	for strings.Contains(id, "..") {
		id = strings.ReplaceAll(id, "..", ".")
	}
	// Strip leading/trailing dots or dashes
	id = strings.Trim(id, "-.")
	// Remove .lock suffix if somehow present
	id = strings.TrimSuffix(id, ".lock")
	return id
}

// parseOwnerRepo splits "owner/name" into (owner, repo) and validates both
// segments are non-empty.
func parseOwnerRepo(ownerRepo string) (string, string, error) {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("--repo must be in owner/name format (got %q)", ownerRepo)
	}
	// Ensure no additional slashes in either part
	if strings.Contains(parts[0], "/") || strings.Contains(parts[1], "/") {
		return "", "", fmt.Errorf("--repo must be in owner/name format (got %q)", ownerRepo)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// importConfig holds the flags for the import command.
type importConfig struct {
	fromGitHub bool
	repo       string
	last       int
	state      string
	dryRun     bool
	message    string
}

// newImportCommand is the public constructor (production: uses execGHClient).
func newImportCommand(out, errOut io.Writer) *cobra.Command {
	return buildImportCommand(out, errOut, &execGHClient{})
}

// buildImportCommand constructs the cobra.Command backed by the given ghClient.
// Tests inject a stubGHClient; production uses execGHClient via newImportCommand.
func buildImportCommand(out, errOut io.Writer, client ghClient) *cobra.Command {
	var cfg importConfig

	cmd := &cobra.Command{
		Use:           "import",
		Short:         "Import historical runs from an external source (e.g. GitHub PRs)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := &importRunner{
				now:    time.Now,
				store:  refstore.New(""),
				stdout: out,
				stderr: errOut,
				client: client,
			}
			return runner.run(cmd.Context(), cfg)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	flags := cmd.Flags()
	flags.BoolVar(&cfg.fromGitHub, "from-github", false, "import from GitHub PRs (required)")
	flags.StringVar(&cfg.repo, "repo", "", "GitHub repo as owner/name (required)")
	flags.IntVar(&cfg.last, "last", 50, "number of PRs to import")
	flags.StringVar(&cfg.state, "state", "merged", "PR state to import: merged, closed, or all")
	flags.BoolVar(&cfg.dryRun, "dry-run", false, "print what would be imported without writing anything")
	flags.StringVar(&cfg.message, "message", "", "commit message prefix for each imported run ref")

	return cmd
}

// importRunner holds the runtime dependencies for the import command.
type importRunner struct {
	now    func() time.Time
	store  refstore.Store
	stdout io.Writer
	stderr io.Writer
	client ghClient
}

func (r *importRunner) run(ctx context.Context, cfg importConfig) error {
	// Validate --from-github is set (required source selector).
	if !cfg.fromGitHub {
		return fmt.Errorf("exactly one source is required (--from-github)")
	}

	// Validate --repo.
	if strings.TrimSpace(cfg.repo) == "" {
		return fmt.Errorf("--repo is required (format: owner/name)")
	}
	owner, repoName, err := parseOwnerRepo(cfg.repo)
	if err != nil {
		return err
	}

	// Validate --last.
	if cfg.last < 1 {
		return fmt.Errorf("--last must be >= 1")
	}

	// Validate --state.
	switch cfg.state {
	case "merged", "closed", "all":
		// ok
	default:
		return fmt.Errorf("--state must be one of: merged, closed, all (got %q)", cfg.state)
	}

	// Preflight: check gh is installed and authenticated (routed through the
	// injected client so tests with a stub need no gh on PATH).
	if err := r.client.AuthStatus(ctx); err != nil {
		return fmt.Errorf("gh CLI is required and must be authenticated; install gh and run 'gh auth login' (etude import uses gh, not a GitHub token)")
	}

	ghVersion := r.client.Version(ctx)

	// Fetch PR list.
	prs, err := r.client.ListPRs(ctx, cfg.repo, cfg.state, cfg.last)
	if err != nil {
		return fmt.Errorf("fetch PRs from %s: %w", cfg.repo, err)
	}

	var (
		countImported int
		countSkipped  int // already present
		countNoMerge  int // no merge commit
		countDiffFail int // diff fetch failed
		countEmpty    int // zero valid stages
	)

	for _, pr := range prs {
		runID := buildImportRunID(owner, repoName, pr.Number)

		// Belt-and-suspenders: if a pathological repo name still produces an
		// invalid run ID after sanitization, skip rather than write a bad manifest.
		if !runmanifest.IsValidRunID(runID) {
			fmt.Fprintf(r.stderr, "warning: pr %d → invalid run id %q, skipping\n", pr.Number, runID)
			countSkipped++
			continue
		}

		// Skip PRs with no merge commit.
		if pr.MergeCommit.OID == "" {
			fmt.Fprintf(r.stderr, "warning: pr %d has no merge commit, skipping\n", pr.Number)
			countNoMerge++
			continue
		}

		// Validate git SHA.
		if err := validateGitSHA(pr.MergeCommit.OID); err != nil {
			fmt.Fprintf(r.stderr, "warning: pr %d merge commit OID %q is invalid (%v), skipping\n", pr.Number, pr.MergeCommit.OID, err)
			countNoMerge++
			continue
		}

		// Parse mergedAt.
		var occurredAt time.Time
		if pr.MergedAt != "" {
			occurredAt, err = time.Parse(time.RFC3339, pr.MergedAt)
			if err != nil {
				// Try RFC3339Nano.
				occurredAt, err = time.Parse(time.RFC3339Nano, pr.MergedAt)
				if err != nil {
					fmt.Fprintf(r.stderr, "warning: pr %d mergedAt %q could not be parsed, skipping\n", pr.Number, pr.MergedAt)
					countNoMerge++
					continue
				}
			}
			occurredAt = occurredAt.UTC()
		}

		if cfg.dryRun {
			fmt.Fprintf(r.stdout, "dry-run: would import pr %d as %s (sha=%s occurred_at=%s)\n",
				pr.Number, runID, pr.MergeCommit.OID, occurredAt.Format(time.RFC3339))
			continue
		}

		// Fetch the diff for this PR.
		diffBytes, diffErr := r.client.Diff(ctx, cfg.repo, pr.Number)
		if diffErr != nil {
			fmt.Fprintf(r.stderr, "warning: pr %d diff fetch failed (%v), skipping\n", pr.Number, diffErr)
			countDiffFail++
			continue
		}

		now := r.now().UTC()

		// Build the manifest and write it.
		imported, skip, err := r.importPR(ctx, pr, runID, owner, repoName, diffBytes, occurredAt, now, ghVersion, cfg.message)
		if err != nil {
			if errors.Is(err, refstore.ErrRefExists) {
				fmt.Fprintf(r.stdout, "pr %d already imported as %s, skipping\n", pr.Number, runID)
				countSkipped++
				continue
			}
			return fmt.Errorf("import pr %d: %w", pr.Number, err)
		}
		if skip {
			countEmpty++
			continue
		}
		_ = imported
		fmt.Fprintf(r.stdout, "imported pr %d as %s\n", pr.Number, runID)
		countImported++
	}

	if cfg.dryRun {
		fmt.Fprintf(r.stdout, "dry-run summary: would import %d PRs\n", len(prs))
		return nil
	}

	fmt.Fprintf(r.stdout, "summary: imported %d, skipped %d (already present), skipped %d (no merge commit / invalid), skipped %d (diff failure), skipped %d (zero stages)\n",
		countImported, countSkipped, countNoMerge, countDiffFail, countEmpty)
	return nil
}

// importPR builds a manifest for one PR and writes it via WriteManifestTree.
// Returns (commit, skipped, error). skipped=true means the PR had zero valid
// stages and was omitted with a warning (not a fatal error).
func (r *importRunner) importPR(
	ctx context.Context,
	pr ghPR,
	runID string,
	owner, repoName string,
	diffBytes []byte,
	occurredAt time.Time,
	now time.Time,
	ghVersion string,
	messagePrefix string,
) (string, bool, error) {
	artifactStore := artifactstore.New()
	skill := runmanifest.Skill{
		ID:      importSkillID,
		Repo:    importSkillRepo,
		Version: importSkillVersion,
	}
	harness := runmanifest.Harness{
		Name:    importHarnessName,
		Version: ghVersion,
	}
	producer := runmanifest.Producer{
		Harness: harness,
		Model:   "",
		Skill:   skill,
	}

	var stages []runmanifest.Stage

	hasDiff := len(strings.TrimSpace(string(diffBytes))) > 0
	hasBody := strings.TrimSpace(pr.Body) != ""

	if hasDiff && hasBody {
		// Main path: single "review" stage with diff as input, pr-body as output.
		diffArtifact, err := artifactStore.AddContent("diff", "text/x-diff; charset=utf-8", diffBytes)
		if err != nil {
			return "", false, fmt.Errorf("store diff artifact: %w", err)
		}
		bodyArtifact, err := artifactStore.AddContent("pr-body", "text/markdown; charset=utf-8", []byte(pr.Body))
		if err != nil {
			return "", false, fmt.Errorf("store pr-body artifact: %w", err)
		}

		stage := runmanifest.Stage{
			Name:       "review",
			ProducedBy: importProducedBy,
			GitSHA:     pr.MergeCommit.OID,
			Skill:      skill,
			Producer:   producer,
			Inputs:     []runmanifest.ArtifactRef{runmanifest.ArtifactFromManifestArtifact(diffArtifact)},
			Output:     runmanifest.ArtifactFromManifestArtifact(bodyArtifact),
			Timestamp:  now,
		}
		stages = append(stages, stage)
	} else if hasDiff {
		// Empty-body PR: write a "final-diff" stage whose output is the diff.
		diffArtifact, err := artifactStore.AddContent("diff", "text/x-diff; charset=utf-8", diffBytes)
		if err != nil {
			return "", false, fmt.Errorf("store diff artifact: %w", err)
		}
		stage := runmanifest.Stage{
			Name:       "final-diff",
			ProducedBy: importProducedBy,
			GitSHA:     pr.MergeCommit.OID,
			Skill:      skill,
			Producer:   producer,
			Inputs:     nil,
			Output:     runmanifest.ArtifactFromManifestArtifact(diffArtifact),
			Timestamp:  now,
		}
		stages = append(stages, stage)
	} else if hasBody {
		// No diff but has body: write a "review" stage with body as output only.
		bodyArtifact, err := artifactStore.AddContent("pr-body", "text/markdown; charset=utf-8", []byte(pr.Body))
		if err != nil {
			return "", false, fmt.Errorf("store pr-body artifact: %w", err)
		}
		stage := runmanifest.Stage{
			Name:       "review",
			ProducedBy: importProducedBy,
			GitSHA:     pr.MergeCommit.OID,
			Skill:      skill,
			Producer:   producer,
			Inputs:     nil,
			Output:     runmanifest.ArtifactFromManifestArtifact(bodyArtifact),
			Timestamp:  now,
		}
		stages = append(stages, stage)
	} else {
		// Zero stages: skip with warning.
		fmt.Fprintf(r.stderr, "warning: pr %d has neither diff nor body, skipping\n", pr.Number)
		return "", true, nil
	}

	refs := map[string]string{
		"pr":     strconv.Itoa(pr.Number),
		"repo":   owner + "/" + repoName,
		"source": "github",
		"url":    pr.URL,
		"author": pr.Author.Login,
	}

	manifest := runmanifest.Manifest{
		RunID:           runID,
		Workflow:        importWorkflow,
		WorkflowVersion: importWorkflowVersion,
		Created:         now,
		OccurredAt:      occurredAt,
		Refs:            refs,
		Stages:          stages,
	}

	files := artifactStore.Files()

	message := messagePrefix
	if message == "" {
		message = fmt.Sprintf("github-import: create run %s (pr #%d from %s/%s)", runID, pr.Number, owner, repoName)
	}

	// Create-only write: ExpectedOld="" means fail if ref already exists.
	written, err := runmanifest.WriteManifestTree(ctx, r.store, "refs/etude/runs/", manifest, files, refstore.WriteOptions{
		Message: message,
	})
	if err != nil {
		return "", false, err
	}
	return written, false, nil
}
