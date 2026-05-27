package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/retro"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/spf13/cobra"
)

const retrosPrefix = "refs/etude/retros/"

// validRetroScopes is the closed set of allowed scope values.
var validRetroScopes = map[string]bool{
	"run":      true,
	"phase":    true,
	"gate":     true,
	"cohort":   true,
	"bench":    true,
	"workflow": true,
}

type retroCaptureConfig struct {
	file           string
	metaFile       string
	occurredAt     string
	subjectRuns    []string
	beads          []string
	trigger        string
	decision       string
	supersedes     string
	gate           string
	bench          string
	eval           string
	refs           []string
	gitSHA         string
	skillID        string
	skillRepo      string
	skillVersion   string
	harness        string
	harnessVersion string
	model          string
	message        string
}

type retroGenerateConfig struct {
	occurredAt     string
	subjectRuns    []string
	beads          []string
	trigger        string
	decision       string
	supersedes     string
	gate           string
	bench          string
	eval           string
	refs           []string
	gitSHA         string
	skillID        string
	skillRepo      string
	skillVersion   string
	harness        string
	harnessVersion string
	model          string
	message        string
	generatorSpec  string
	stage          string
}

// retroWriteParams carries the parameters shared between capture and generate
// for the assembleAndWriteRetro call.
type retroWriteParams struct {
	occurredAt     string // RFC3339 string; empty = not set
	subjectRuns    []string
	beads          []string
	trigger        string
	decision       string
	supersedes     string
	gate           string
	bench          string
	eval           string
	extraRefs      map[string]string // already parsed, reserved keys already checked
	scope          string
	gitSHA         string
	skillID        string
	skillRepo      string
	skillVersion   string
	harness        string
	harnessVersion string
	model          string
	message        string
	// producedVia, if non-empty, is set as Refs["produced_via"]. Capture leaves
	// this empty; generate sets it to "generate".
	producedVia string
	// generatorSpec, if non-empty, is set as Refs["generator"].
	generatorSpec string
	// metaContent, if non-empty, is well-formed JSON to attach as a second
	// retro-meta stage in the manifest (role:retro-meta, application/json).
	// When nil/empty the manifest has exactly one stage (backward compatible).
	metaContent []byte
}

func newRetroCommand(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "retro",
		Short:         "Manage etude retro artifacts",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.AddCommand(newRetroCaptureCommand(out, errOut))
	cmd.AddCommand(newRetroGenerateCommand(out, errOut, nil))

	runner := &retroShowListRunner{
		store:  refstore.New(""),
		stdout: out,
	}
	cmd.AddCommand(newRetroListCommand(runner))
	cmd.AddCommand(newRetroShowCommand(runner))
	return cmd
}

type retroShowListRunner struct {
	store  refstore.Store
	stdout io.Writer
}

func newRetroListCommand(runner *retroShowListRunner) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "List all retros",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.list(cmd.Context())
		},
	}
}

func newRetroShowCommand(runner *retroShowListRunner) *cobra.Command {
	return &cobra.Command{
		Use:           "show <retro-id>",
		Short:         "Show details of a retro",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.show(cmd.Context(), args[0])
		},
	}
}

func (r *retroShowListRunner) list(ctx context.Context) error {
	refs, err := r.store.List(ctx, strings.TrimSuffix(retrosPrefix, "/"))
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		fmt.Fprintln(r.stdout, "no retros found")
		return nil
	}

	w := tabwriter.NewWriter(r.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RETRO ID\tSCOPE\tTRIGGER\tSUBJECTS\tMETA\tCREATED")
	for _, ref := range refs {
		id, manifest, err := loadManifestForRef(ctx, r.store, ref, retrosPrefix, "retro")
		if err != nil {
			return err
		}
		subjects := retroSubjectsList(manifest.Refs)
		metaCol := "N"
		if _, ok := findRetroMetaStage(manifest); ok {
			metaCol = "Y"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			id,
			manifest.Refs["scope"],
			manifest.Refs["trigger"],
			subjects,
			metaCol,
			manifest.Created.UTC().Format(time.RFC3339),
		)
	}
	return w.Flush()
}

func (r *retroShowListRunner) show(ctx context.Context, id string) error {
	if err := validateCLIIdentifier("retro id", id); err != nil {
		return err
	}
	if err := validateExtraID("retro", retro.IsValidRetroID(id), id); err != nil {
		return err
	}

	ref := retrosPrefix + id
	_, err := r.store.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, refstore.ErrNotFound) {
			return fmt.Errorf("retro %q not found", id)
		}
		return err
	}

	manifestBytes, err := r.store.ReadFile(ctx, ref, "manifest.json")
	if err != nil {
		return fmt.Errorf("retro %q: %w", id, err)
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return fmt.Errorf("retro %q: %w", id, err)
	}

	return printRetroDetail(r.store, ctx, ref, manifest, r.stdout)
}

// findRetroMetaStage scans m.Stages for a stage whose Output.Role is
// "retro-meta" and returns it. Detection is role-based, not positional, so it
// is robust to future stage additions.
func findRetroMetaStage(m runmanifest.Manifest) (runmanifest.Stage, bool) {
	for _, s := range m.Stages {
		if s.Output.Role == "retro-meta" {
			return s, true
		}
	}
	return runmanifest.Stage{}, false
}

// sortedRetroSubjects collects subject_run.N and bead.N values from refs,
// sorted by prefix (lexical: bead. before subject_run.) then by numeric index,
// and returns the ordered slice of values. Returns nil when no subject keys are
// present.
func sortedRetroSubjects(refs map[string]string) []string {
	type indexed struct {
		prefix string
		index  int
		value  string
	}
	var items []indexed
	for k, v := range refs {
		for _, pfx := range []string{"subject_run.", "bead."} {
			if strings.HasPrefix(k, pfx) {
				n, err := strconv.Atoi(strings.TrimPrefix(k, pfx))
				if err == nil {
					items = append(items, indexed{prefix: pfx, index: n, value: v})
				}
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	// Sort by prefix (lexical: bead before subject_run), then by index, for deterministic output.
	sort.Slice(items, func(i, j int) bool {
		if items[i].prefix != items[j].prefix {
			return items[i].prefix < items[j].prefix
		}
		return items[i].index < items[j].index
	})
	values := make([]string, len(items))
	for i, item := range items {
		values[i] = item.value
	}
	return values
}

// retroSubjectsList returns a comma-joined string of the sorted subject values
// for use in the retro list SUBJECTS column. Returns "" when no subjects exist.
func retroSubjectsList(refs map[string]string) string {
	return strings.Join(sortedRetroSubjects(refs), ",")
}

// printRetroDetail renders the full retro detail for retro show, including
// metadata, subjects, producer, and the inline body. It is deliberately
// self-contained and does not call printRunDetail.
func printRetroDetail(store refstore.Store, ctx context.Context, ref string, m runmanifest.Manifest, out io.Writer) error {
	fmt.Fprintf(out, "retro id:  %s\n", m.RunID)
	fmt.Fprintf(out, "scope:     %s\n", m.Refs["scope"])
	if v, ok := m.Refs["trigger"]; ok {
		fmt.Fprintf(out, "trigger:   %s\n", v)
	}
	if v, ok := m.Refs["decision"]; ok {
		fmt.Fprintf(out, "decision:  %s\n", v)
	}
	if v, ok := m.Refs["supersedes"]; ok {
		fmt.Fprintf(out, "supersedes: %s\n", v)
	}
	fmt.Fprintf(out, "created:   %s\n", m.Created.UTC().Format(time.RFC3339))
	if !m.OccurredAt.IsZero() {
		fmt.Fprintf(out, "occurred:  %s\n", m.OccurredAt.UTC().Format(time.RFC3339))
	}

	// Subjects: collect subject_run.N and bead.N sorted by prefix then index.
	for _, v := range sortedRetroSubjects(m.Refs) {
		fmt.Fprintf(out, "  subject: %s\n", v)
	}

	// Render all remaining Refs entries that are not already shown above.
	// "Already shown" means: the four flat keys (scope, trigger, decision,
	// supersedes) and any key with a subject_run.* or bead.* prefix.
	// Everything else — gate.N, bench.N, eval.N, and arbitrary custom --ref
	// keys — is rendered here, sorted lexically for deterministic output.
	{
		shown := map[string]bool{
			"scope": true, "trigger": true, "decision": true, "supersedes": true,
		}
		var extraKeys []string
		for k := range m.Refs {
			if shown[k] {
				continue
			}
			if strings.HasPrefix(k, "subject_run.") || strings.HasPrefix(k, "bead.") {
				continue
			}
			extraKeys = append(extraKeys, k)
		}
		if len(extraKeys) > 0 {
			sort.Strings(extraKeys)
			fmt.Fprintln(out, "metadata:")
			for _, k := range extraKeys {
				fmt.Fprintf(out, "  %s: %s\n", k, m.Refs[k])
			}
		}
	}

	// Producer from the single retro stage.
	if len(m.Stages) > 0 {
		stage := m.Stages[0]
		p := stage.Producer
		var parts []string
		if p.Harness.Name != "" {
			parts = append(parts, formatHarness(p.Harness))
		}
		if p.Model != "" {
			parts = append(parts, p.Model)
		}
		if p.Skill.ID != "" {
			parts = append(parts, p.Skill.ID+"@"+p.Skill.Version)
		}
		if len(parts) > 0 {
			fmt.Fprintf(out, "producer:  %s\n", strings.Join(parts, " "))
		}

		// Body inline.
		fmt.Fprintln(out, "--- retro body ---")
		bodyBytes, err := store.ReadFile(ctx, ref, stage.Output.Path)
		if err != nil {
			return fmt.Errorf("retro body: %w", err)
		}
		bodyIsContent := stage.Output.Storage == artifactstore.StorageContent
		if bodyIsContent {
			fmt.Fprint(out, string(bodyBytes))
		} else {
			fmt.Fprintf(out, "artifact: %s\n", stage.Output.Path)
		}

		// Render the retro-meta sidecar section when present.
		if metaStage, ok := findRetroMetaStage(m); ok {
			// Guarantee a newline before the meta divider. The content-storage
			// body is printed with Fprint (no trailing newline added), so if the
			// body bytes themselves don't end in '\n' the divider would glue onto
			// the last body line. The pointer-storage branch always ends in "\n".
			if bodyIsContent && len(bodyBytes) > 0 && bodyBytes[len(bodyBytes)-1] != '\n' {
				fmt.Fprintln(out)
			}
			fmt.Fprintln(out, "--- retro meta ---")
			metaBytes, err := store.ReadFile(ctx, ref, metaStage.Output.Path)
			if err != nil {
				return fmt.Errorf("retro meta: %w", err)
			}
			if metaStage.Output.Storage == artifactstore.StorageContent {
				var buf bytes.Buffer
				if json.Indent(&buf, metaBytes, "", "  ") == nil {
					out.Write(buf.Bytes())
					if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
						fmt.Fprintln(out)
					}
				} else {
					// Fall back to raw bytes if indent fails.
					fmt.Fprint(out, string(metaBytes))
				}
			} else {
				fmt.Fprintf(out, "artifact: %s\n", metaStage.Output.Path)
			}
		}
	}
	return nil
}

func newRetroCaptureCommand(out, errOut io.Writer) *cobra.Command {
	cfg := retroCaptureConfig{
		trigger:      "manual",
		skillRepo:    defaultSkillRepo,
		skillVersion: defaultSkillVersion,
	}
	cmd := &cobra.Command{
		Use:           "capture <scope>",
		Short:         "Capture an externally-authored retro into etude",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := retroCaptureRunner{
				now:    time.Now,
				store:  refstore.New(""),
				stdout: out,
			}
			return runner.run(cmd.Context(), args[0], cfg)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	flags := cmd.Flags()
	flags.StringVar(&cfg.file, "file", "", "path to retro markdown body, or - for stdin (required)")
	flags.StringVar(&cfg.metaFile, "meta-file", "", "path to retro-meta JSON sidecar, or - for stdin (optional; must be well-formed JSON)")
	flags.StringVar(&cfg.occurredAt, "occurred-at", "", "original event time the retro covers, as RFC3339 (optional; defaults to capture time in displays when absent)")
	flags.StringArrayVar(&cfg.subjectRuns, "subject-run", nil, "run id of a subject run (repeatable; >=1 required unless scope=workflow)")
	flags.StringArrayVar(&cfg.beads, "bead", nil, "bead id of a subject bead (repeatable)")
	flags.StringVar(&cfg.trigger, "trigger", "manual", "trigger that prompted this retro (e.g. manual, scheduled)")
	flags.StringVar(&cfg.decision, "decision", "", "retro decision: accepted, deferred, superseded, or informational")
	flags.StringVar(&cfg.supersedes, "supersedes", "", "retro id this retro supersedes")
	flags.StringVar(&cfg.gate, "gate", "", "gate reference as stage@attempt (for phase/gate scopes)")
	flags.StringVar(&cfg.bench, "bench", "", "bench id (for bench scope)")
	flags.StringVar(&cfg.eval, "eval", "", "eval id (for bench scope)")
	flags.StringArrayVar(&cfg.refs, "ref", nil, "additional ref as key=value (repeatable)")
	flags.StringVar(&cfg.gitSHA, "git-sha", "", "repo git sha at capture time")
	flags.StringVar(&cfg.skillID, "skill-id", "retro", "skill id")
	flags.StringVar(&cfg.skillRepo, "skill-repo", defaultSkillRepo, "skill repo")
	flags.StringVar(&cfg.skillVersion, "skill-version", defaultSkillVersion, "skill version")
	flags.StringVar(&cfg.harness, "harness", "", "agent runtime that produced this retro")
	flags.StringVar(&cfg.harnessVersion, "harness-version", "", "version of the agent runtime")
	flags.StringVar(&cfg.model, "model", "", "LLM model used (if any)")
	flags.StringVar(&cfg.message, "message", "", "retro ref commit message")

	return cmd
}

type retroCaptureRunner struct {
	now    func() time.Time
	store  refstore.Store
	stdout io.Writer
}

// validateSubjectsResolveSHA performs the validation shared by retro capture and
// retro generate, in their common order: require >=1 subject run unless scope is
// "workflow", validate each subject run id, then resolve the git SHA (trim,
// fall back to HEAD, else validate). It is a contiguous slice of both runners'
// preambles, so replacing each in place preserves the exact order of checks.
func validateSubjectsResolveSHA(ctx context.Context, scope string, subjectRuns []string, gitSHA string) (string, error) {
	if scope != "workflow" && len(subjectRuns) == 0 {
		return "", fmt.Errorf("--subject-run is required for scope %q (at least one)", scope)
	}
	for _, id := range subjectRuns {
		if !runmanifest.IsValidRunID(id) {
			return "", fmt.Errorf("invalid --subject-run %q: must be a valid run id", id)
		}
	}
	resolved := strings.TrimSpace(gitSHA)
	if resolved == "" {
		var err error
		resolved, err = currentHEAD(ctx)
		if err != nil {
			return "", err
		}
	} else if err := validateGitSHA(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// parseAndCheckRefs parses --ref key=value pairs and rejects reserved keys. It
// is called at each runner's existing point so the order of checks is preserved.
func parseAndCheckRefs(refs []string) (map[string]string, error) {
	extraRefs, err := parseRefs(refs)
	if err != nil {
		return nil, err
	}
	if err := checkReservedRefs(extraRefs); err != nil {
		return nil, err
	}
	return extraRefs, nil
}

func (r retroCaptureRunner) run(ctx context.Context, scope string, cfg retroCaptureConfig) error {
	// Validate scope.
	if !validRetroScopes[scope] {
		return fmt.Errorf("invalid scope %q: must be one of run, phase, gate, cohort, bench, workflow", scope)
	}

	// Validate --file is provided.
	if strings.TrimSpace(cfg.file) == "" {
		return fmt.Errorf("--file is required")
	}

	// Validate subjects and resolve the git SHA (shared with retro generate).
	gitSHA, err := validateSubjectsResolveSHA(ctx, scope, cfg.subjectRuns, cfg.gitSHA)
	if err != nil {
		return err
	}

	// Read the retro body.
	var bodyContent []byte
	if cfg.file == "-" {
		bodyContent, err = io.ReadAll(os.Stdin)
	} else {
		bodyContent, err = os.ReadFile(cfg.file)
	}
	if err != nil {
		return fmt.Errorf("read retro file: %w", err)
	}

	// Read the optional retro-meta JSON sidecar.
	var metaContent []byte
	if strings.TrimSpace(cfg.metaFile) != "" {
		if cfg.metaFile == "-" {
			metaContent, err = io.ReadAll(os.Stdin)
		} else {
			metaContent, err = os.ReadFile(cfg.metaFile)
		}
		if err != nil {
			return fmt.Errorf("read retro meta file: %w", err)
		}
		if !json.Valid(metaContent) {
			return fmt.Errorf("retro meta file %q is not valid JSON", cfg.metaFile)
		}
	}

	// Parse extra --ref values and check for reserved keys.
	extraRefs, err := parseAndCheckRefs(cfg.refs)
	if err != nil {
		return err
	}

	p := retroWriteParams{
		occurredAt:     cfg.occurredAt,
		subjectRuns:    cfg.subjectRuns,
		beads:          cfg.beads,
		trigger:        cfg.trigger,
		decision:       cfg.decision,
		supersedes:     cfg.supersedes,
		gate:           cfg.gate,
		bench:          cfg.bench,
		eval:           cfg.eval,
		extraRefs:      extraRefs,
		scope:          scope,
		gitSHA:         gitSHA,
		skillID:        cfg.skillID,
		skillRepo:      cfg.skillRepo,
		skillVersion:   cfg.skillVersion,
		harness:        cfg.harness,
		harnessVersion: cfg.harnessVersion,
		model:          cfg.model,
		message:        cfg.message,
		producedVia:    "",
		generatorSpec:  "",
		metaContent:    metaContent,
	}

	commit, retroID, err := assembleAndWriteRetro(ctx, r.store, r.now, bodyContent, p)
	if err != nil {
		return err
	}

	ref := "refs/etude/retros/" + retroID
	_, err = fmt.Fprintf(r.stdout, "captured %s\nref %s\n", commit, ref)
	return err
}

// checkReservedRefs validates that none of the extra --ref keys collide with
// reserved names. This is called by both capture and generate before passing
// extraRefs to assembleAndWriteRetro.
func checkReservedRefs(extraRefs map[string]string) error {
	reservedExactKeys := map[string]bool{
		"scope": true, "trigger": true, "decision": true, "supersedes": true,
		"produced_via": true, "generator": true,
	}
	reservedPrefixes := []string{"subject_run.", "bead.", "gate.", "bench.", "eval."}

	for k := range extraRefs {
		if reservedExactKeys[k] {
			return fmt.Errorf("--ref key %q is reserved; use the dedicated flag", k)
		}
		for _, pfx := range reservedPrefixes {
			if strings.HasPrefix(k, pfx) {
				return fmt.Errorf("--ref key %q is reserved; use the dedicated flag", k)
			}
		}
	}
	return nil
}

// assembleAndWriteRetro builds the retro artifact store, manifest, and writes
// the retro ref. It is shared between retro capture and retro generate.
// Returns the commit OID and the allocated retro ID.
//
// NOTE: extraRefs in params must already have reserved keys stripped (the
// caller's validation layer is responsible for that check).
func assembleAndWriteRetro(
	ctx context.Context,
	store refstore.Store,
	nowFn func() time.Time,
	bodyContent []byte,
	p retroWriteParams,
) (commit, retroID string, err error) {
	// Build artifact store with the body.
	artifactStoreInst := artifactstore.New()
	bodyArtifact, err := artifactStoreInst.AddContent("retro", "text/markdown; charset=utf-8", bodyContent)
	if err != nil {
		return "", "", fmt.Errorf("add retro body artifact: %w", err)
	}
	bodyRef := runmanifest.ArtifactFromManifestArtifact(bodyArtifact)

	// Optionally add the retro-meta JSON sidecar artifact.
	var metaRef runmanifest.ArtifactRef
	if len(p.metaContent) > 0 {
		metaArtifact, metaErr := artifactStoreInst.AddContent("retro-meta", "application/json", p.metaContent)
		if metaErr != nil {
			return "", "", fmt.Errorf("add retro-meta artifact: %w", metaErr)
		}
		metaRef = runmanifest.ArtifactFromManifestArtifact(metaArtifact)
	}

	files := artifactStoreInst.Files()

	// Build the Refs map.
	refsMap := make(map[string]string)

	for i, id := range p.subjectRuns {
		refsMap[fmt.Sprintf("subject_run.%d", i+1)] = id
	}
	for i, id := range p.beads {
		refsMap[fmt.Sprintf("bead.%d", i+1)] = id
	}
	if p.trigger != "" {
		refsMap["trigger"] = p.trigger
	}
	if p.decision != "" {
		refsMap["decision"] = p.decision
	}
	if p.supersedes != "" {
		refsMap["supersedes"] = p.supersedes
	}
	if p.gate != "" {
		refsMap["gate.1"] = p.gate
	}
	if p.bench != "" {
		refsMap["bench.1"] = p.bench
	}
	if p.eval != "" {
		refsMap["eval.1"] = p.eval
	}
	refsMap["scope"] = p.scope

	// Provenance keys set by generate (empty in capture).
	if p.producedVia != "" {
		refsMap["produced_via"] = p.producedVia
	}
	if p.generatorSpec != "" {
		refsMap["generator"] = p.generatorSpec
	}

	// Merge extra --ref values (caller already validated reserved keys).
	for k, v := range p.extraRefs {
		refsMap[k] = v
	}

	// Determine primary subject for the id base.
	primarySubject := "workflow"
	if len(p.subjectRuns) > 0 {
		primarySubject = p.subjectRuns[0]
	}

	now := nowFn().UTC()
	idBase := retro.RetroIDBase(p.scope, primarySubject, now)

	retroID, err = retro.AllocateRetroId(ctx, store, idBase)
	if err != nil {
		return "", "", err
	}

	skill := runmanifest.Skill{
		ID:      p.skillID,
		Repo:    p.skillRepo,
		Version: p.skillVersion,
	}

	producer := runmanifest.Producer{
		Harness: runmanifest.Harness{
			Name:    p.harness,
			Version: p.harnessVersion,
		},
		Model: p.model,
		Skill: skill,
	}

	stages := []runmanifest.Stage{
		{
			Name:       "retro",
			ProducedBy: "retro",
			GitSHA:     p.gitSHA,
			Skill:      skill,
			Producer:   producer,
			Inputs:     []runmanifest.ArtifactRef{},
			Output:     bodyRef,
			Timestamp:  now,
		},
	}

	// When a meta sidecar is present, append a second retro-meta stage.
	// The existing retro stage stays Stages[0]; retro-meta is Stages[1].
	if len(p.metaContent) > 0 {
		stages = append(stages, runmanifest.Stage{
			Name:       "retro-meta",
			ProducedBy: "retro-meta",
			GitSHA:     p.gitSHA,
			Skill:      skill,
			Producer:   producer,
			Inputs:     []runmanifest.ArtifactRef{},
			Output:     metaRef,
			Timestamp:  now,
		})
	}

	// Parse --occurred-at when provided. Validation happens here (before any ref
	// is written) so a malformed value fails fast with a clear error message.
	var occurredAtTime time.Time
	if strings.TrimSpace(p.occurredAt) != "" {
		parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(p.occurredAt))
		if parseErr != nil {
			return "", "", fmt.Errorf("--occurred-at %q is not a valid RFC3339 timestamp: %w", p.occurredAt, parseErr)
		}
		occurredAtTime = parsed.UTC()
	}

	manifest := runmanifest.Manifest{
		RunID:           retroID,
		Workflow:        "retro",
		WorkflowVersion: "retro-v1",
		Created:         now,
		OccurredAt:      occurredAtTime,
		Refs:            refsMap,
		Stages:          stages,
	}

	msg := p.message
	if strings.TrimSpace(msg) == "" {
		msg = fmt.Sprintf("retro: capture %s %s", p.scope, retroID)
	}

	commit, err = (retro.Writer{Store: store}).Write(ctx, manifest, files, retro.WriteOptions{Message: msg})
	if err != nil {
		return "", "", err
	}
	return commit, retroID, nil
}

// ---------------------------------------------------------------------------
// retro generate
// ---------------------------------------------------------------------------

type retroGenerateRunner struct {
	// generator is the retro.Generator to use. If nil, an ExecGenerator is built
	// from the resolved --generator spec at run time. Tests inject a StubGenerator.
	generator retro.Generator
	now       func() time.Time
	store     refstore.Store
	stdout    io.Writer
	// timeout overrides the default ExecGenerator timeout when non-zero.
	timeout time.Duration
}

// newRetroGenerateCommand constructs the 'retro generate' subcommand. The
// injectedGenerator parameter is non-nil only in tests; production callers
// pass nil and the command resolves the generator from --generator / git config.
func newRetroGenerateCommand(out, errOut io.Writer, injectedGenerator retro.Generator) *cobra.Command {
	return buildRetroGenerateCommand(out, errOut, &retroGenerateRunner{
		generator: injectedGenerator,
		now:       time.Now,
		store:     refstore.New(""),
		stdout:    out,
		timeout:   10 * time.Minute,
	})
}

// buildRetroGenerateCommand constructs the cobra.Command backed by r. Tests
// call this with an injected runner; production uses newRetroGenerateCommand.
func buildRetroGenerateCommand(out, errOut io.Writer, r *retroGenerateRunner) *cobra.Command {
	cfg := retroGenerateConfig{
		trigger:      "manual",
		skillRepo:    defaultSkillRepo,
		skillVersion: defaultSkillVersion,
	}
	var timeoutFlag time.Duration

	cmd := &cobra.Command{
		Use:           "generate <scope>",
		Short:         "Generate a retro by invoking an external generator script over covered run artifacts",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.timeout = timeoutFlag
			return r.run(cmd.Context(), args[0], cfg)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	flags := cmd.Flags()
	flags.StringArrayVar(&cfg.subjectRuns, "subject-run", nil, "run id of a subject run (repeatable; >=1 required unless scope=workflow)")
	flags.StringArrayVar(&cfg.beads, "bead", nil, "bead id of a subject bead (repeatable)")
	flags.StringVar(&cfg.occurredAt, "occurred-at", "", "original event time the retro covers, as RFC3339 (optional; defaults to capture time in displays when absent)")
	flags.StringVar(&cfg.trigger, "trigger", "manual", "trigger that prompted this retro (e.g. manual, post-bench)")
	flags.StringVar(&cfg.decision, "decision", "", "retro decision: accepted, deferred, superseded, or informational")
	flags.StringVar(&cfg.supersedes, "supersedes", "", "retro id this retro supersedes")
	flags.StringVar(&cfg.gate, "gate", "", "gate reference as stage@attempt (for phase/gate scopes)")
	flags.StringVar(&cfg.bench, "bench", "", "bench id (for bench scope)")
	flags.StringVar(&cfg.eval, "eval", "", "eval id (for bench scope)")
	flags.StringArrayVar(&cfg.refs, "ref", nil, "additional ref as key=value (repeatable)")
	flags.StringVar(&cfg.gitSHA, "git-sha", "", "repo git sha at capture time")
	flags.StringVar(&cfg.skillID, "skill-id", "retro", "skill id")
	flags.StringVar(&cfg.skillRepo, "skill-repo", defaultSkillRepo, "skill repo")
	flags.StringVar(&cfg.skillVersion, "skill-version", defaultSkillVersion, "skill version")
	flags.StringVar(&cfg.harness, "harness", "", "agent runtime that produced this retro")
	flags.StringVar(&cfg.harnessVersion, "harness-version", "", "version of the agent runtime")
	flags.StringVar(&cfg.model, "model", "", "LLM model used (if any)")
	flags.StringVar(&cfg.message, "message", "", "retro ref commit message")
	flags.StringVar(&cfg.generatorSpec, "generator", "", "generator command spec (e.g. ./gen.sh); falls back to git config etude.retroGenerator")
	flags.StringVar(&cfg.stage, "stage", "", "stage name to use for each subject run; required when a subject run has multiple stages")
	flags.DurationVar(&timeoutFlag, "timeout", 10*time.Minute, "per-invocation timeout for the generator (0 disables)")

	return cmd
}

func (r *retroGenerateRunner) run(ctx context.Context, scope string, cfg retroGenerateConfig) error {
	// Validate scope.
	if !validRetroScopes[scope] {
		return fmt.Errorf("invalid scope %q: must be one of run, phase, gate, cohort, bench, workflow", scope)
	}

	// Validate subjects and resolve the git SHA (shared with retro capture).
	gitSHA, err := validateSubjectsResolveSHA(ctx, scope, cfg.subjectRuns, cfg.gitSHA)
	if err != nil {
		return err
	}

	// Parse extra --ref values and check for reserved keys.
	extraRefs, err := parseAndCheckRefs(cfg.refs)
	if err != nil {
		return err
	}

	// Resolve the generator.
	gen, err := r.resolveGenerator(ctx, cfg.generatorSpec)
	if err != nil {
		return err
	}

	// Resolve and materialize each subject run's stage output + inputs.
	store := r.store
	subjects := make([]retro.SubjectArtifact, 0, len(cfg.subjectRuns))
	for _, runID := range cfg.subjectRuns {
		resolved, stageErr := resolveSubjectStage(ctx, store, runID, cfg.stage)
		if stageErr != nil {
			return fmt.Errorf("subject run %q: %w", runID, stageErr)
		}

		// Output-pointer guard (Opus note from gate): reject pointer-storage outputs.
		if resolved.Output.Storage == artifactstore.StoragePointer {
			return fmt.Errorf("subject run %q stage %q: output is a pointer artifact and has not been materialized; cannot generate retro", runID, resolved.Name)
		}

		// Read the stage output.
		outputBytes, readErr := store.ReadCommitFile(ctx, resolved.Commit, resolved.Output.Path)
		if readErr != nil {
			return fmt.Errorf("subject run %q: read output: %w", runID, readErr)
		}

		// Materialize inputs.
		var inputs []retro.SubjectInput
		for _, inp := range resolved.ResolvedInputs {
			content, inpErr := inp.ReadContent(ctx)
			if inpErr != nil {
				if errors.Is(inpErr, replay.ErrPointerNotMaterialized) {
					// Skip pointer inputs rather than aborting — the generator
					// does not require all inputs to be present.
					continue
				}
				return fmt.Errorf("subject run %q input %q: %w", runID, inp.Role, inpErr)
			}
			inputs = append(inputs, retro.SubjectInput{
				Role:    inp.Role,
				Content: content,
			})
		}

		subjects = append(subjects, retro.SubjectArtifact{
			RunID:         runID,
			OutputRole:    resolved.Output.Role,
			OutputContent: outputBytes,
			Inputs:        inputs,
		})
	}

	// Build the generator request.
	skill := runmanifest.Skill{
		ID:      cfg.skillID,
		Repo:    cfg.skillRepo,
		Version: cfg.skillVersion,
	}
	producer := runmanifest.Producer{
		Harness: runmanifest.Harness{
			Name:    cfg.harness,
			Version: cfg.harnessVersion,
		},
		Model: cfg.model,
		Skill: skill,
	}
	genReq := retro.GenerateRequest{
		Subjects: subjects,
		Scope:    scope,
		Trigger:  cfg.trigger,
		Producer: producer,
	}

	// Run the generator.
	genResult, err := gen.Generate(ctx, genReq)
	if err != nil {
		return fmt.Errorf("generator: %w", err)
	}

	// Assemble and write the retro.
	effectiveGeneratorSpec := cfg.generatorSpec
	if effectiveGeneratorSpec == "" {
		effectiveGeneratorSpec = gitConfigGet(ctx, "etude.retroGenerator")
	}

	p := retroWriteParams{
		occurredAt:     cfg.occurredAt,
		subjectRuns:    cfg.subjectRuns,
		beads:          cfg.beads,
		trigger:        cfg.trigger,
		decision:       cfg.decision,
		supersedes:     cfg.supersedes,
		gate:           cfg.gate,
		bench:          cfg.bench,
		eval:           cfg.eval,
		extraRefs:      extraRefs,
		scope:          scope,
		gitSHA:         gitSHA,
		skillID:        cfg.skillID,
		skillRepo:      cfg.skillRepo,
		skillVersion:   cfg.skillVersion,
		harness:        genResult.Producer.Harness.Name,
		harnessVersion: genResult.Producer.Harness.Version,
		model:          genResult.Producer.Model,
		message:        cfg.message,
		producedVia:    "generate",
		generatorSpec:  effectiveGeneratorSpec,
		metaContent:    genResult.Meta,
	}

	commit, retroID, err := assembleAndWriteRetro(ctx, r.store, r.now, genResult.Body, p)
	if err != nil {
		return err
	}

	ref := "refs/etude/retros/" + retroID
	_, err = fmt.Fprintf(r.stdout, "generated %s\nref %s\n", commit, ref)
	return err
}

// resolveGenerator returns r.generator if injected (test seam), otherwise
// builds an ExecGenerator from the provided spec (--generator flag), or falls
// back to git config etude.retroGenerator. Returns an error if no generator
// can be determined.
func (r *retroGenerateRunner) resolveGenerator(ctx context.Context, spec string) (retro.Generator, error) {
	if r.generator != nil {
		return r.generator, nil
	}

	if spec == "" {
		spec = gitConfigGet(ctx, "etude.retroGenerator")
	}

	if spec == "" {
		return nil, fmt.Errorf("no generator configured (set --generator or git config etude.retroGenerator)")
	}

	gen := retro.NewExecGenerator(spec)
	gen.Timeout = r.timeout
	return gen, nil
}

// resolveSubjectStage resolves a stage of a run for use as a generator subject.
//
// Selection logic:
//   - If stageName is non-empty: call replay.ResolveInputs with that name and
//     surface any error faithfully (ErrStageNotFound, ErrAmbiguousStage, etc.).
//   - If stageName is empty: read the run manifest; if it has exactly one stage,
//     use that stage. If it has multiple stages, return a clear error listing the
//     stage names and asking the caller to specify --stage.
func resolveSubjectStage(ctx context.Context, store refstore.Store, runID, stageName string) (replay.ResolvedStage, error) {
	if stageName != "" {
		// Explicit stage — resolve directly and surface all errors faithfully.
		resolved, err := replay.ResolveInputs(ctx, store, runID, stageName)
		if err != nil {
			if errors.Is(err, replay.ErrStageNotFound) {
				return replay.ResolvedStage{}, fmt.Errorf("stage %q not found in run %q", stageName, runID)
			}
			return replay.ResolvedStage{}, err
		}
		return resolved, nil
	}

	// No explicit stage — read the manifest to determine what stages exist.
	ref := "refs/etude/runs/" + runID
	commit, err := store.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, refstore.ErrNotFound) {
			return replay.ResolvedStage{}, fmt.Errorf("%w: %s", replay.ErrRunNotFound, runID)
		}
		return replay.ResolvedStage{}, err
	}
	raw, err := store.ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		return replay.ResolvedStage{}, err
	}
	manifest, err := runmanifest.ParseJSON(raw)
	if err != nil {
		return replay.ResolvedStage{}, err
	}
	if len(manifest.Stages) == 0 {
		return replay.ResolvedStage{}, fmt.Errorf("run %q has no stages", runID)
	}
	if len(manifest.Stages) > 1 {
		// Collect unique stage names for the error message.
		seen := make(map[string]struct{}, len(manifest.Stages))
		names := make([]string, 0, len(manifest.Stages))
		for _, s := range manifest.Stages {
			if _, ok := seen[s.Name]; !ok {
				seen[s.Name] = struct{}{}
				names = append(names, s.Name)
			}
		}
		return replay.ResolvedStage{}, fmt.Errorf(
			"run %q has multiple stages (%s); specify --stage <name>",
			runID, strings.Join(names, ", "),
		)
	}
	// Exactly one stage — use it.
	return replay.ResolveInputs(ctx, store, runID, manifest.Stages[0].Name)
}
