package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/joshuavial/etude/internal/sessionevidence"
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
	harness         string
	harnessVersion  string
	model           string
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
	flags.StringVar(&cfg.harness, "harness", "", "agent runtime that executed the stage (e.g. claude-code)")
	flags.StringVar(&cfg.harnessVersion, "harness-version", "", "version of the agent runtime")
	flags.StringVar(&cfg.model, "model", "", "LLM model used by this stage (e.g. claude-opus-4-7)")
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

	skill := runmanifest.Skill{
		ID:      skillID,
		Repo:    cfg.skillRepo,
		Version: cfg.skillVersion,
	}
	manifest.Stages = append(manifest.Stages, runmanifest.Stage{
		Name:       stageName,
		ProducedBy: cfg.producedBy,
		GitSHA:     gitSHA,
		Skill:      skill,
		Producer: runmanifest.Producer{
			Harness: runmanifest.Harness{
				Name:    cfg.harness,
				Version: cfg.harnessVersion,
			},
			Model: cfg.model,
			Skill: skill,
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
	if !runmanifest.IsValidIdentifier(value) {
		return fmt.Errorf("invalid %s %q", name, value)
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

// gateInputJSON is the input DTO for capture-gate. It mirrors the manifest
// GateAttempt shape but accepts raw_output as a file path (resolved into the
// content-addressed artifact store) rather than as an already-stored ArtifactRef.
type gateInputJSON struct {
	GateID         string                 `json:"gate_id"`
	Phase          string                 `json:"phase"`
	Round          int                    `json:"round"`
	Tier           int                    `json:"tier"`
	Status         string                 `json:"status"`
	ReviewedStages []reviewedRefInputJSON `json:"reviewed_stages"`
	Seats          []seatInputJSON        `json:"seats"`
	Decision       gateDecisionInputJSON  `json:"decision"`
	Timestamp      string                 `json:"timestamp"`
}

type reviewedRefInputJSON struct {
	Stage    string `json:"stage"`
	Artifact string `json:"artifact"`
	Role     string `json:"role"`
}

type seatInputJSON struct {
	Seat        string              `json:"seat"`
	Harness     harnessInputJSON    `json:"harness"`
	Provider    providerInputJSON   `json:"provider"`
	Skill       *skillInputJSON     `json:"skill"`
	Verdict     string              `json:"verdict"`
	Required    []string            `json:"required"`
	Optional    []string            `json:"optional"`
	RawOutput   *rawOutputInputJSON `json:"raw_output"`
	Session     *sessionInputJSON   `json:"session"`
	FailureNote string              `json:"failure_note"`
	Timestamp   string              `json:"timestamp"`
}

type harnessInputJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type providerInputJSON struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

type skillInputJSON struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Version string `json:"version"`
}

// rawOutputInputJSON specifies a seat's raw transcript as a local file path.
// role and media_type are optional; if media_type is empty it is inferred from the path.
type rawOutputInputJSON struct {
	Role      string `json:"role"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

type sessionInputJSON struct {
	SessionID      string `json:"session_id"`
	TranscriptURI  string `json:"transcript_uri"`
	TranscriptPath string `json:"transcript_path"`
}

type gateDecisionInputJSON struct {
	EscalationReason string   `json:"escalation_reason"`
	DegradedReason   string   `json:"degraded_reason"`
	DeferredBeads    []string `json:"deferred_beads"`
}

type captureGateConfig struct {
	runID    string
	gateFile string
}

func newCaptureGateCommand(out, errOut io.Writer) *cobra.Command {
	var cfg captureGateConfig
	cmd := &cobra.Command{
		Use:           "capture-gate",
		Short:         "Append a gate reviewer record to an existing etude run",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := captureRunner{
				now:    time.Now,
				store:  refstore.New(""),
				stdout: out,
			}
			return runner.runGate(cmd.Context(), cfg)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().StringVar(&cfg.runID, "run", "", "run id to append the gate record to (required)")
	cmd.Flags().StringVar(&cfg.gateFile, "gate-file", "", "path to gate JSON file, or - to read from stdin (required)")
	return cmd
}

func (r captureRunner) runGate(ctx context.Context, cfg captureGateConfig) error {
	if strings.TrimSpace(cfg.runID) == "" {
		return fmt.Errorf("--run is required")
	}
	if strings.TrimSpace(cfg.gateFile) == "" {
		return fmt.Errorf("--gate-file is required")
	}

	// Read gate JSON from file or stdin.
	var gateBytes []byte
	var err error
	if cfg.gateFile == "-" {
		gateBytes, err = io.ReadAll(os.Stdin)
	} else {
		gateBytes, err = os.ReadFile(cfg.gateFile)
	}
	if err != nil {
		return fmt.Errorf("read gate file: %w", err)
	}

	var input gateInputJSON
	dec := json.NewDecoder(bytes.NewReader(gateBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return fmt.Errorf("parse gate JSON: %w", err)
	}
	// Reject trailing data after the JSON object (a second token means extra content).
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("parse gate JSON: unexpected trailing data after gate object")
		}
		return fmt.Errorf("parse gate JSON: unexpected trailing data: %w", err)
	}

	// Resolve the existing run — a gate must attach to an existing run.
	ref := "refs/etude/runs/" + cfg.runID
	commit, err := r.store.Resolve(ctx, ref)
	if errors.Is(err, refstore.ErrNotFound) {
		return fmt.Errorf("run %q not found; a gate must attach to an existing run", cfg.runID)
	}
	if err != nil {
		return err
	}

	existing, priorFiles, err := r.readExistingRun(ctx, commit)
	if err != nil {
		return err
	}
	if existing.RunID != cfg.runID {
		return fmt.Errorf("existing manifest run_id %q does not match --run %q", existing.RunID, cfg.runID)
	}

	// Resolve raw_output file paths into the artifact store.
	artifactStore := artifactstore.New()
	seats := make([]runmanifest.SeatResult, 0, len(input.Seats))
	for _, s := range input.Seats {
		seat, err := buildSeatResult(artifactStore, s)
		if err != nil {
			return err
		}
		seats = append(seats, seat)
	}

	// Parse gate timestamp.
	gateTS, err := parseManifestTime("gate.timestamp", input.Timestamp)
	if err != nil {
		return err
	}

	// Build reviewed refs.
	reviewedStages := make([]runmanifest.ReviewedRef, 0, len(input.ReviewedStages))
	for _, r := range input.ReviewedStages {
		reviewedStages = append(reviewedStages, runmanifest.ReviewedRef{
			Stage:    r.Stage,
			Artifact: r.Artifact,
			Role:     r.Role,
		})
	}

	gate := runmanifest.GateAttempt{
		GateID:         input.GateID,
		Phase:          input.Phase,
		Round:          input.Round,
		Tier:           input.Tier,
		Status:         runmanifest.GateStatus(input.Status),
		ReviewedStages: reviewedStages,
		Seats:          seats,
		Decision: runmanifest.GateDecision{
			EscalationReason: input.Decision.EscalationReason,
			DegradedReason:   input.Decision.DegradedReason,
			DeferredBeads:    input.Decision.DeferredBeads,
		},
		Timestamp: gateTS,
	}

	// Append gate to the manifest.
	existing.Gates = append(existing.Gates, gate)

	// Merge prior files with new artifact files.
	files := artifactStore.Files()
	for path, content := range priorFiles {
		files[path] = content
	}

	opts := runmanifest.WriteOptions{
		ExpectedOld: commit,
		Message:     fmt.Sprintf("capture-gate: append run %s gate %s", cfg.runID, input.GateID),
	}
	written, err := (runmanifest.Writer{Store: r.store}).Write(ctx, existing, files, opts)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.stdout, "captured %s\nref %s\n", written, ref)
	return err
}

func buildSeatResult(store *artifactstore.Store, s seatInputJSON) (runmanifest.SeatResult, error) {
	ts, err := parseManifestTime("seat.timestamp", s.Timestamp)
	if err != nil {
		return runmanifest.SeatResult{}, err
	}

	var skill runmanifest.Skill
	if s.Skill != nil {
		skill = runmanifest.Skill{ID: s.Skill.ID, Repo: s.Skill.Repo, Version: s.Skill.Version}
	}

	var rawOutput *runmanifest.ArtifactRef
	if s.RawOutput != nil && s.RawOutput.Path != "" {
		role := s.RawOutput.Role
		if role == "" {
			role = s.Seat + "-transcript"
		}
		mediaType := s.RawOutput.MediaType
		if mediaType == "" {
			mediaType = inferMediaType(s.RawOutput.Path)
		}
		content, err := readRegularFile(s.RawOutput.Path)
		if err != nil {
			return runmanifest.SeatResult{}, fmt.Errorf("read raw_output %s: %w", s.RawOutput.Path, err)
		}
		artifact, err := store.AddContent(role, mediaType, content)
		if err != nil {
			return runmanifest.SeatResult{}, fmt.Errorf("add raw_output artifact: %w", err)
		}
		ref := runmanifest.ArtifactFromManifestArtifact(artifact)
		rawOutput = &ref
	}

	session, err := buildSessionEvidence(store, s.Seat, s.Session)
	if err != nil {
		return runmanifest.SeatResult{}, err
	}

	return runmanifest.SeatResult{
		Seat: s.Seat,
		Harness: runmanifest.Harness{
			Name:    s.Harness.Name,
			Version: s.Harness.Version,
		},
		Provider: runmanifest.Provider{
			Name:  s.Provider.Name,
			Model: s.Provider.Model,
		},
		Skill:       skill,
		Verdict:     runmanifest.SeatVerdict(s.Verdict),
		Required:    s.Required,
		Optional:    s.Optional,
		RawOutput:   rawOutput,
		Session:     session,
		FailureNote: s.FailureNote,
		Timestamp:   ts,
	}, nil
}

func buildSessionEvidence(store *artifactstore.Store, seat string, input *sessionInputJSON) (*runmanifest.SessionEvidence, error) {
	if input == nil {
		return nil, nil
	}
	evidence := &runmanifest.SessionEvidence{
		SessionID:       input.SessionID,
		TranscriptURI:   input.TranscriptURI,
		RetrievalStatus: runmanifest.SessionEvidenceNotApplicable,
		RedactionStatus: runmanifest.SessionEvidenceNotApplicable,
	}
	if strings.TrimSpace(input.TranscriptPath) == "" {
		return evidence, nil
	}
	content, err := sessionevidence.ReadRegularFile(input.TranscriptPath)
	if err != nil {
		evidence.RetrievalStatus = runmanifest.SessionEvidenceFailed
		return evidence, fmt.Errorf("read transcript %s: %w", input.TranscriptPath, err)
	}
	evidence.RetrievalStatus = runmanifest.SessionEvidenceRetrievalImported
	if err := sessionevidence.ScanForSecrets(content); err != nil {
		evidence.RedactionStatus = runmanifest.SessionEvidenceFailed
		return evidence, fmt.Errorf("redaction scan transcript %s: %w", input.TranscriptPath, err)
	}
	evidence.RedactionStatus = runmanifest.SessionEvidenceRedactionPassed
	artifact, err := store.AddContent(seat+"-transcript", inferMediaType(input.TranscriptPath), content)
	if err != nil {
		evidence.RetrievalStatus = runmanifest.SessionEvidenceFailed
		return evidence, fmt.Errorf("add transcript artifact: %w", err)
	}
	ref := runmanifest.ArtifactFromManifestArtifact(artifact)
	evidence.TranscriptArtifact = &ref
	return evidence, nil
}

// parseManifestTime parses an RFC3339Nano timestamp string used in gate input DTOs.
func parseManifestTime(field, value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, fmt.Errorf("%s is required", field)
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	return t.UTC(), nil
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

// readRegularFile opens path with O_NOFOLLOW so a final-component symlink fails
// atomically with ELOOP rather than being silently followed. It additionally
// rejects non-regular files (devices, FIFOs, directories). Use this wherever
// machine-fed or CI-emitted paths are read to prevent symlink-follow exfiltration.
func readRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|nofollowFlag, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return io.ReadAll(f)
}
