package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/spf13/cobra"
)

const (
	defaultWorkflow        = "manual"
	defaultWorkflowVersion = "manual-v1"
	defaultProducedBy      = "original"
	defaultSkillRepo       = "manual"
	defaultSkillVersion    = "manual"
)

type captureConfig struct {
	runID           string
	output          []string
	inputs          []string
	refs            []string
	workflow        string
	workflowVersion string
	producedBy      string
	gitSHA          string
	skillID         string
	skillRepo       string
	skillVersion    string
	message         string
}

func newCaptureCommand(out, errOut io.Writer) *cobra.Command {
	cfg := captureConfig{
		workflow:        defaultWorkflow,
		workflowVersion: defaultWorkflowVersion,
		producedBy:      defaultProducedBy,
		skillRepo:       defaultSkillRepo,
		skillVersion:    defaultSkillVersion,
	}
	cmd := &cobra.Command{
		Use:           "capture <stage>",
		Short:         "Capture a stage artifact into an etude run",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := captureRunner{
				now:    time.Now,
				store:  refstore.New(""),
				stdout: out,
			}
			return runner.run(cmd.Context(), args[0], cfg, captureFlagState{
				workflowChanged:        cmd.Flags().Changed("workflow"),
				workflowVersionChanged: cmd.Flags().Changed("workflow-version"),
			})
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	flags := cmd.Flags()
	flags.StringVar(&cfg.runID, "run", "", "run id to capture into")
	flags.StringArrayVar(&cfg.output, "output", nil, "output artifact as role=path")
	flags.StringArrayVar(&cfg.inputs, "input", nil, "input artifact as role=path")
	flags.StringArrayVar(&cfg.refs, "ref", nil, "external ref as key=value")
	flags.StringVar(&cfg.workflow, "workflow", defaultWorkflow, "workflow name")
	flags.StringVar(&cfg.workflowVersion, "workflow-version", defaultWorkflowVersion, "workflow version")
	flags.StringVar(&cfg.producedBy, "produced-by", defaultProducedBy, "producer identity")
	flags.StringVar(&cfg.gitSHA, "git-sha", "", "repo git sha for this stage")
	flags.StringVar(&cfg.skillID, "skill-id", "", "skill id")
	flags.StringVar(&cfg.skillRepo, "skill-repo", defaultSkillRepo, "skill repo")
	flags.StringVar(&cfg.skillVersion, "skill-version", defaultSkillVersion, "skill version")
	flags.StringVar(&cfg.message, "message", "", "run ref commit message")
	return cmd
}

type captureFlagState struct {
	workflowChanged        bool
	workflowVersionChanged bool
}

type captureRunner struct {
	now    func() time.Time
	store  refstore.Store
	stdout io.Writer
}

func (r captureRunner) run(ctx context.Context, stageName string, cfg captureConfig, flags captureFlagState) error {
	if err := validateCLIIdentifier("stage", stageName); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.runID) == "" {
		return fmt.Errorf("--run is required")
	}
	if len(cfg.output) != 1 {
		return fmt.Errorf("exactly one --output is required")
	}
	refs, err := parseRefs(cfg.refs)
	if err != nil {
		return err
	}
	gitSHA := strings.TrimSpace(cfg.gitSHA)
	if gitSHA == "" {
		gitSHA, err = currentHEAD(ctx)
		if err != nil {
			return err
		}
	} else if err := validateGitSHA(gitSHA); err != nil {
		return err
	}
	skillID := cfg.skillID
	if strings.TrimSpace(skillID) == "" {
		skillID = stageName
	}

	artifactStore := artifactstore.New()
	inputs := make([]runmanifest.ArtifactRef, 0, len(cfg.inputs))
	for _, spec := range cfg.inputs {
		artifact, err := addFileArtifact(artifactStore, spec)
		if err != nil {
			return err
		}
		inputs = append(inputs, artifact)
	}
	output, err := addFileArtifact(artifactStore, cfg.output[0])
	if err != nil {
		return err
	}

	ref := "refs/etude/runs/" + cfg.runID
	commit, resolveErr := r.store.Resolve(ctx, ref)
	now := r.now().UTC()
	manifest := runmanifest.Manifest{
		RunID:           cfg.runID,
		Workflow:        cfg.workflow,
		WorkflowVersion: cfg.workflowVersion,
		Created:         now,
		Refs:            refs,
	}
	files := artifactStore.Files()
	opts := runmanifest.WriteOptions{Message: cfg.message}
	appending := false
	if resolveErr == nil {
		appending = true
		existing, priorFiles, err := r.readExistingRun(ctx, commit)
		if err != nil {
			return err
		}
		if existing.RunID != cfg.runID {
			return fmt.Errorf("existing manifest run_id %q does not match --run %q", existing.RunID, cfg.runID)
		}
		if flags.workflowChanged && cfg.workflow != existing.Workflow {
			return fmt.Errorf("--workflow %q conflicts with existing workflow %q", cfg.workflow, existing.Workflow)
		}
		if flags.workflowVersionChanged && cfg.workflowVersion != existing.WorkflowVersion {
			return fmt.Errorf("--workflow-version %q conflicts with existing workflow_version %q", cfg.workflowVersion, existing.WorkflowVersion)
		}
		manifest = existing
		if manifest.Refs == nil {
			manifest.Refs = make(map[string]string)
		}
		for key, value := range refs {
			manifest.Refs[key] = value
		}
		for path, content := range priorFiles {
			files[path] = content
		}
		opts.ExpectedOld = commit
	} else if !errors.Is(resolveErr, refstore.ErrNotFound) {
		return resolveErr
	}

	manifest.Stages = append(manifest.Stages, runmanifest.Stage{
		Name:       stageName,
		ProducedBy: cfg.producedBy,
		GitSHA:     gitSHA,
		Skill: runmanifest.Skill{
			ID:      skillID,
			Repo:    cfg.skillRepo,
			Version: cfg.skillVersion,
		},
		Inputs:    inputs,
		Output:    output,
		Timestamp: now,
	})
	if opts.Message == "" {
		action := "create"
		if appending {
			action = "append"
		}
		opts.Message = fmt.Sprintf("capture: %s run %s stage %s", action, cfg.runID, stageName)
	}
	written, err := (runmanifest.Writer{Store: r.store}).Write(ctx, manifest, files, opts)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.stdout, "captured %s\nref %s\n", written, ref)
	return err
}

func (r captureRunner) readExistingRun(ctx context.Context, commit string) (runmanifest.Manifest, map[string][]byte, error) {
	manifestBytes, err := r.store.ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		return runmanifest.Manifest{}, nil, err
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return runmanifest.Manifest{}, nil, err
	}
	files := make(map[string][]byte)
	for _, path := range runmanifest.ArtifactPaths(manifest) {
		content, err := r.store.ReadCommitFile(ctx, commit, path)
		if err != nil {
			return runmanifest.Manifest{}, nil, err
		}
		files[path] = content
	}
	return manifest, files, nil
}

func addFileArtifact(store *artifactstore.Store, spec string) (runmanifest.ArtifactRef, error) {
	role, filePath, err := splitKeyValue(spec, "artifact")
	if err != nil {
		return runmanifest.ArtifactRef{}, err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return runmanifest.ArtifactRef{}, fmt.Errorf("read %s: %w", filePath, err)
	}
	artifact, err := store.AddContent(role, inferMediaType(filePath), content)
	if err != nil {
		return runmanifest.ArtifactRef{}, err
	}
	return runmanifest.ArtifactFromManifestArtifact(artifact), nil
}

func parseRefs(values []string) (map[string]string, error) {
	refs := make(map[string]string, len(values))
	for _, value := range values {
		key, refValue, err := splitKeyValue(value, "ref")
		if err != nil {
			return nil, err
		}
		if err := validateCLIIdentifier("ref key", key); err != nil {
			return nil, err
		}
		if strings.TrimSpace(refValue) == "" {
			return nil, fmt.Errorf("ref %q value is required", key)
		}
		refs[key] = refValue
	}
	return refs, nil
}

func splitKeyValue(value, name string) (string, string, error) {
	key, rest, ok := strings.Cut(value, "=")
	if !ok || key == "" || rest == "" {
		return "", "", fmt.Errorf("invalid %s %q: want key=value", name, value)
	}
	return key, rest, nil
}

func validateCLIIdentifier(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return fmt.Errorf("invalid %s %q", name, value)
		}
	}
	return nil
}

// validateGitSHA requires a user-supplied --git-sha to be a full lowercase hex
// object id (SHA-1 = 40, SHA-256 = 64) so a recorded stage git sha is a real
// commit id rather than arbitrary text. An empty --git-sha is resolved from HEAD
// instead and never reaches here.
func validateGitSHA(sha string) error {
	if len(sha) != 40 && len(sha) != 64 {
		return fmt.Errorf("invalid --git-sha %q: want a 40- or 64-character hex commit id", sha)
	}
	for _, r := range sha {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("invalid --git-sha %q: must be lowercase hex", sha)
		}
	}
	return nil
}

func currentHEAD(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("could not resolve HEAD; run capture inside a repo with at least one commit or pass --git-sha")
	}
	return strings.TrimSpace(string(out)), nil
}

func inferMediaType(filePath string) string {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".diff", ".patch":
		return "text/x-diff; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
