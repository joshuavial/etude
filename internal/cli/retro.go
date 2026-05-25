package cli

import (
	"context"
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
	fmt.Fprintln(w, "RETRO ID\tSCOPE\tTRIGGER\tSUBJECTS\tCREATED")
	for _, ref := range refs {
		id := strings.TrimPrefix(ref, retrosPrefix)
		manifestBytes, err := r.store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return fmt.Errorf("retro %q: %w", id, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return fmt.Errorf("retro %q: %w", id, err)
		}
		subjects := retroSubjectsList(manifest.Refs)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			id,
			manifest.Refs["scope"],
			manifest.Refs["trigger"],
			subjects,
			manifest.Created.UTC().Format(time.RFC3339),
		)
	}
	return w.Flush()
}

func (r *retroShowListRunner) show(ctx context.Context, id string) error {
	if err := validateCLIIdentifier("retro id", id); err != nil {
		return err
	}
	if err := validateRetroIDExtra(id); err != nil {
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

// validateRetroIDExtra enforces the retro id rules from runmanifest that are
// not covered by validateCLIIdentifier. It delegates to retro.IsValidRetroID
// so the shared spec has a single source of truth. This runs before any git
// call so it works outside a repo.
func validateRetroIDExtra(id string) error {
	if !retro.IsValidRetroID(id) {
		return fmt.Errorf("invalid retro id %q", id)
	}
	return nil
}

// retroSubjectsList collects subject_run.N and bead.N values from refs,
// sorted by their numeric index, and returns a comma-joined string. Returns
// an empty string when no subject keys are present.
func retroSubjectsList(refs map[string]string) string {
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
		return ""
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
	return strings.Join(values, ",")
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

	// Subjects: collect subject_run.N and bead.N sorted by prefix then index.
	type indexedSubject struct {
		prefix string
		index  int
		value  string
	}
	var subjects []indexedSubject
	for k, v := range m.Refs {
		for _, pfx := range []string{"subject_run.", "bead."} {
			if strings.HasPrefix(k, pfx) {
				n, err := strconv.Atoi(strings.TrimPrefix(k, pfx))
				if err == nil {
					subjects = append(subjects, indexedSubject{prefix: pfx, index: n, value: v})
				}
			}
		}
	}
	if len(subjects) > 0 {
		sort.Slice(subjects, func(i, j int) bool {
			if subjects[i].prefix != subjects[j].prefix {
				return subjects[i].prefix < subjects[j].prefix
			}
			return subjects[i].index < subjects[j].index
		})
		for _, s := range subjects {
			fmt.Fprintf(out, "  subject: %s\n", s.value)
		}
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
			if p.Harness.Version != "" {
				parts = append(parts, p.Harness.Name+" "+p.Harness.Version)
			} else {
				parts = append(parts, p.Harness.Name)
			}
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
		if stage.Output.Storage == artifactstore.StorageContent {
			fmt.Fprint(out, string(bodyBytes))
		} else {
			fmt.Fprintf(out, "artifact: %s\n", stage.Output.Path)
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

func (r retroCaptureRunner) run(ctx context.Context, scope string, cfg retroCaptureConfig) error {
	// Validate scope.
	if !validRetroScopes[scope] {
		return fmt.Errorf("invalid scope %q: must be one of run, phase, gate, cohort, bench, workflow", scope)
	}

	// Validate --file is provided.
	if strings.TrimSpace(cfg.file) == "" {
		return fmt.Errorf("--file is required")
	}

	// Validate subject runs are present unless scope is workflow.
	if scope != "workflow" && len(cfg.subjectRuns) == 0 {
		return fmt.Errorf("--subject-run is required for scope %q (at least one)", scope)
	}

	// Validate each subject run id.
	for _, id := range cfg.subjectRuns {
		if !runmanifest.IsValidRunID(id) {
			return fmt.Errorf("invalid --subject-run %q: must be a valid run id", id)
		}
	}

	// Resolve git SHA.
	var err error
	gitSHA := strings.TrimSpace(cfg.gitSHA)
	if gitSHA == "" {
		gitSHA, err = currentHEAD(ctx)
		if err != nil {
			return err
		}
	} else if err := validateGitSHA(gitSHA); err != nil {
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

	// Build artifact store with the body.
	artifactStoreInst := artifactstore.New()
	bodyArtifact, err := artifactStoreInst.AddContent("retro", "text/markdown; charset=utf-8", bodyContent)
	if err != nil {
		return fmt.Errorf("add retro body artifact: %w", err)
	}
	bodyRef := runmanifest.ArtifactFromManifestArtifact(bodyArtifact)
	files := artifactStoreInst.Files()

	// Parse extra --ref values.
	extraRefs, err := parseRefs(cfg.refs)
	if err != nil {
		return err
	}

	// Build the Refs map: indexed subject_run.N, bead.N, plus named flat keys.
	refsMap := make(map[string]string)

	// Indexed subject runs.
	for i, id := range cfg.subjectRuns {
		refsMap[fmt.Sprintf("subject_run.%d", i+1)] = id
	}

	// Indexed beads.
	for i, id := range cfg.beads {
		refsMap[fmt.Sprintf("bead.%d", i+1)] = id
	}

	// Flat optional keys.
	if cfg.trigger != "" {
		refsMap["trigger"] = cfg.trigger
	}
	if cfg.decision != "" {
		refsMap["decision"] = cfg.decision
	}
	if cfg.supersedes != "" {
		refsMap["supersedes"] = cfg.supersedes
	}
	if cfg.gate != "" {
		refsMap["gate.1"] = cfg.gate
	}
	if cfg.bench != "" {
		refsMap["bench.1"] = cfg.bench
	}
	if cfg.eval != "" {
		refsMap["eval.1"] = cfg.eval
	}
	refsMap["scope"] = scope

	// Reserved exact keys and prefixes that --ref must not collide with.
	reservedExactKeys := map[string]bool{
		"scope": true, "trigger": true, "decision": true, "supersedes": true,
	}
	reservedPrefixes := []string{"subject_run.", "bead.", "gate.", "bench.", "eval."}

	// Merge extra --ref values; reject keys that collide with reserved names.
	for k, v := range extraRefs {
		if reservedExactKeys[k] {
			return fmt.Errorf("--ref key %q is reserved; use the dedicated flag", k)
		}
		for _, pfx := range reservedPrefixes {
			if strings.HasPrefix(k, pfx) {
				return fmt.Errorf("--ref key %q is reserved; use the dedicated flag", k)
			}
		}
		refsMap[k] = v
	}

	// Determine primary subject for the id base.
	primarySubject := "workflow"
	if len(cfg.subjectRuns) > 0 {
		primarySubject = cfg.subjectRuns[0]
	}

	now := r.now().UTC()
	idBase := retro.RetroIDBase(scope, primarySubject, now)

	retroID, err := retro.AllocateRetroId(ctx, r.store, idBase)
	if err != nil {
		return err
	}

	skill := runmanifest.Skill{
		ID:      cfg.skillID,
		Repo:    cfg.skillRepo,
		Version: cfg.skillVersion,
	}

	manifest := runmanifest.Manifest{
		RunID:           retroID,
		Workflow:        "retro",
		WorkflowVersion: "retro-v1",
		Created:         now,
		Refs:            refsMap,
		Stages: []runmanifest.Stage{
			{
				Name:       "retro",
				ProducedBy: "retro",
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
				Inputs:    []runmanifest.ArtifactRef{},
				Output:    bodyRef,
				Timestamp: now,
			},
		},
	}

	msg := cfg.message
	if strings.TrimSpace(msg) == "" {
		msg = fmt.Sprintf("retro: capture %s %s", scope, retroID)
	}

	ref := "refs/etude/retros/" + retroID
	commit, err := (retro.Writer{Store: r.store}).Write(ctx, manifest, files, retro.WriteOptions{Message: msg})
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(r.stdout, "captured %s\nref %s\n", commit, ref)
	return err
}
