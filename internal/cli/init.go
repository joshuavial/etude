package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/joshuavial/etude/internal/workflow"
	"github.com/spf13/cobra"
)

// actionStatus classifies the outcome of an initAction for tallying.
type actionStatus string

const (
	statusCreated           actionStatus = "created"
	statusSkipped           actionStatus = "skipped"
	statusConfigured        actionStatus = "configured"
	statusAlreadyConfigured actionStatus = "already configured"
	// statusNote is uncounted — used for informational messages (e.g. remote not found).
	statusNote actionStatus = "note"
)

// Shared format strings — kept here so apply output and dry-run reporting
// cannot drift from each other.
const (
	fmtCreated           = "created %s"
	fmtSkipped           = "skipped %s"
	fmtConfigured        = "configured %s = %s"
	fmtAlreadyConfigured = "already configured %s = %s"
	fmtRemoteNotFound    = "remote %s not found, skipping refspec configuration"
)

type actionLine struct {
	status actionStatus
	text   string
}

type initAction struct {
	run func(force, dryRun bool) ([]actionLine, error)
}

func newInitCommand(out, errOut io.Writer) *cobra.Command {
	var force bool
	var remote string
	var dryRun bool

	cmd := &cobra.Command{
		Use:           "init",
		Short:         "Scaffold .etude/ config and register refs/etude/* refspecs",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := initRunner{
				stdout: out,
				stderr: errOut,
				stdin:  cmd.InOrStdin(),
			}
			return runner.run(cmd.Context(), force, dryRun, remote, cmd.Flags().Changed("remote"))
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing scaffolded files with fresh generated content")
	cmd.Flags().StringVar(&remote, "remote", "origin", "git remote to configure refspecs on (default: origin)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the planned actions without writing files or modifying git config")
	return cmd
}

type initRunner struct {
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
}

func (r initRunner) run(ctx context.Context, force, dryRun bool, remote string, remoteChanged bool) error {
	if err := validateRemoteName(remote); err != nil {
		return err
	}

	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	actions, err := plan(ctx, root, force, remote, remoteChanged)
	if err != nil {
		return err
	}

	return apply(r.stdout, actions, force, dryRun)
}

// plan performs read-only derivation and builds the ordered action list.
// It never calls remoteExists and never returns remoteNotFoundErr.
func plan(ctx context.Context, root string, force bool, remote string, remoteChanged bool) ([]initAction, error) {
	etudDir := filepath.Join(root, ".etude")

	// Guard: .etude exists as a regular file.
	if info, err := os.Stat(etudDir); err == nil && !info.IsDir() {
		return nil, fmt.Errorf(".etude exists as a regular file, not a directory: %s", etudDir)
	}

	// Generate workflow.yaml bytes and self-check.
	wf := workflow.Default()
	yamlBytes, err := wf.YAML()
	if err != nil {
		return nil, fmt.Errorf("generate workflow.yaml: %w", err)
	}
	if _, err := workflow.ParseYAML(yamlBytes); err != nil {
		return nil, fmt.Errorf("workflow.yaml self-check failed: %w", err)
	}

	// Derive rubric placeholder paths from the workflow.
	type rubricEntry struct {
		path  string
		stage string
	}
	var rubrics []rubricEntry
	for _, s := range wf.Stages {
		if s.Eval != nil && s.Eval.Method == "rubric" {
			rubrics = append(rubrics, rubricEntry{path: s.Eval.Rubric, stage: s.Name})
		}
	}

	var actions []initAction

	// Workflow.yaml write action.
	workflowPath := filepath.Join(etudDir, "workflow.yaml")
	actions = append(actions, writeAction(workflowPath, yamlBytes))

	// Rubric placeholder write actions.
	for _, entry := range rubrics {
		fullPath := filepath.Join(etudDir, entry.path)
		content := fmt.Sprintf("# Rubric for %s\nTODO: define evaluation criteria.\n", entry.stage)
		actions = append(actions, writeAction(fullPath, []byte(content)))
	}

	// Refspec phase — exactly one action.
	actions = append(actions, refspecAction(ctx, root, remote, remoteChanged))

	return actions, nil
}

// writeAction returns an initAction for creating a scaffold file.
func writeAction(path string, content []byte) initAction {
	return initAction{
		run: func(force, dryRun bool) ([]actionLine, error) {
			if dryRun {
				_, err := os.Stat(path)
				exists := err == nil
				if exists && !force {
					return []actionLine{{statusSkipped, fmt.Sprintf("plan: skip %s", path)}}, nil
				}
				return []actionLine{{statusCreated, fmt.Sprintf("plan: create %s", path)}}, nil
			}
			status, err := writeScaffoldFile(path, content, force)
			if err != nil {
				return nil, err
			}
			var text string
			switch status {
			case statusSkipped:
				text = fmt.Sprintf(fmtSkipped, path)
			default:
				text = fmt.Sprintf(fmtCreated, path)
			}
			return []actionLine{{status, text}}, nil
		},
	}
}

// refspecAction returns the single refspec-phase initAction.
func refspecAction(ctx context.Context, root, remote string, remoteChanged bool) initAction {
	return initAction{
		run: func(force, dryRun bool) ([]actionLine, error) {
			fetchKey := fmt.Sprintf("remote.%s.fetch", remote)
			fetchVal := "+refs/etude/*:refs/etude/*"
			pushKey := fmt.Sprintf("remote.%s.push", remote)
			pushVal := "refs/etude/*:refs/etude/*"

			// Dry-run is always read-only and NEVER errors on a missing remote,
			// even under --force with an explicit missing remote. Check dryRun
			// BEFORE the force missing-remote error path.
			if dryRun {
				present := remoteExists(ctx, root, remote)
				if !present {
					// Report the would-skip condition; for force+explicit-missing
					// note that a real run would error.
					if force && remoteChanged {
						note := fmt.Sprintf("plan: remote %s not found — a real run would error", remote)
						return []actionLine{{statusNote, note}}, nil
					}
					note := fmt.Sprintf("remote %s not found -> would skip refspec configuration", remote)
					return []actionLine{{statusNote, note}}, nil
				}
				if force {
					// force + present → zero output (silent on refspecs).
					return nil, nil
				}
				// Non-force + present: preview distinct fetch and push lines (both counted).
				return []actionLine{
					{statusConfigured, fmt.Sprintf("plan: configure fetch refspec on %s", remote)},
					{statusConfigured, fmt.Sprintf("plan: configure push refspec on %s", remote)},
				}, nil
			}

			// Normal (non-dry-run) run.

			// Force gate: --force is always silent on refspecs EXCEPT for the
			// explicit-missing-remote error case.
			if force {
				if remoteChanged && !remoteExists(ctx, root, remote) {
					return nil, remoteNotFoundErr(remote)
				}
				// Force + all other cases: zero output (silent).
				return nil, nil
			}

			// Non-force normal run.
			if !remoteExists(ctx, root, remote) {
				if remoteChanged {
					return nil, remoteNotFoundErr(remote)
				}
				note := fmt.Sprintf(fmtRemoteNotFound, remote)
				return []actionLine{{statusNote, note}}, nil
			}

			// Remote present: add refspecs if absent.
			lines, err := addRefspecIfAbsent(ctx, root, fetchKey, fetchVal)
			if err != nil {
				return nil, err
			}
			pushLines, err := addRefspecIfAbsent(ctx, root, pushKey, pushVal)
			if err != nil {
				return nil, err
			}
			return append(lines, pushLines...), nil
		},
	}
}

// apply calls run(force, dryRun) on each action, prints output, tallies
// statuses, and prints the summary. It is the sole fmt.Fprintf site for
// action output.
func apply(w io.Writer, actions []initAction, force, dryRun bool) error {
	var created, skipped, configured int

	for _, action := range actions {
		lines, err := action.run(force, dryRun)
		if err != nil {
			return err
		}
		for _, line := range lines {
			fmt.Fprintln(w, line.text)
			switch line.status {
			case statusCreated:
				created++
			case statusSkipped:
				skipped++
			case statusConfigured, statusAlreadyConfigured:
				configured++
				// statusNote is uncounted.
			}
		}
	}

	if dryRun {
		fmt.Fprintf(w, "dry-run: %d to create, %d to skip, %d to configure\n", created, skipped, configured)
	} else {
		fmt.Fprintf(w, "init: %d created, %d skipped, %d configured\n", created, skipped, configured)
	}

	return nil
}

// writeScaffoldFile writes content to path, creating parent dirs as needed.
// Returns the actionStatus and any error; the caller is responsible for printing.
func writeScaffoldFile(path string, content []byte, force bool) (actionStatus, error) {
	if _, err := os.Stat(path); err == nil && !force {
		return statusSkipped, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return statusCreated, nil
}

// addRefspecIfAbsent adds value to key only when no byte-exact match already
// exists, ensuring idempotency. Returns actionLines for the caller to print.
// Exit code 1 from --get-all means no entries (key absent); only non-zero
// codes other than 1 are treated as errors.
//
// Note: every git invocation is pinned with `git -C <root>` rather than relying
// on the process working directory, which is more robust when the cwd changes
// between calls (e.g. tests chdir). This deliberately differs from capture.go,
// which runs git relative to the current directory.
func addRefspecIfAbsent(ctx context.Context, root, key, value string) ([]actionLine, error) {
	existing, err := gitGetAll(ctx, root, key)
	if err != nil {
		return nil, fmt.Errorf("git config --get-all %s: %w", key, err)
	}
	for _, v := range existing {
		if v == value {
			text := fmt.Sprintf(fmtAlreadyConfigured, key, value)
			return []actionLine{{statusAlreadyConfigured, text}}, nil
		}
	}
	// Directive A: use --local explicitly.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "config", "--local", "--add", key, value)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git config --add %s: %w\n%s", key, err, output)
	}
	text := fmt.Sprintf(fmtConfigured, key, value)
	return []actionLine{{statusConfigured, text}}, nil
}

// gitGetAll returns all values for a git config key. Exit code 1 means the
// key is absent (zero entries) and is treated as an empty list, not an error.
func gitGetAll(ctx context.Context, root, key string) ([]string, error) {
	// Directive A: use --local explicitly.
	// Directive G (see addRefspecIfAbsent): git -C <root> for robustness.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "config", "--local", "--get-all", key)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Exit code 1 means the key is absent / zero entries — empty list.
			return nil, nil
		}
		return nil, err
	}
	raw := strings.TrimRight(string(out), "\n")
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// repoRoot resolves the repository root via git rev-parse --show-toplevel.
// A non-zero exit produces a clean "not a git repository" error.
func repoRoot(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	// Use git -C <cwd> so all subsequent calls can also use -C <root>.
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository (or any parent up to root %s)", cwd)
	}
	return strings.TrimSpace(string(out)), nil
}

// remoteNotFoundErr is the shared error for an explicitly-named remote that does
// not exist, used by both the --force and non-force paths so they cannot drift.
func remoteNotFoundErr(name string) error {
	return fmt.Errorf("remote %q not found", name)
}

// remoteExists returns true if the named remote is configured in the repo.
func remoteExists(ctx context.Context, root, remote string) bool {
	// Directive G: git -C <root> for consistency.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", remote)
	return cmd.Run() == nil
}

// validateRemoteName rejects empty or git-invalid remote names before the value
// is used in a `git -C <root> remote get-url <name>` call or composed into a
// `remote.<name>.*` config key. A leading "-" is rejected because git would
// otherwise treat the name as a flag (argument injection); the remaining rules
// mirror git's ref-name format so the name cannot produce a malformed key.
// Directive E: validate before composing remote.<name>.* keys.
func validateRemoteName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid remote name %q: must not be empty", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("invalid remote name %q: contains whitespace or control character", name)
		}
	}
	switch {
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("invalid remote name %q: must not start with '-'", name)
	case strings.HasPrefix(name, ".") || strings.HasPrefix(name, "/"):
		return fmt.Errorf("invalid remote name %q: must not start with '.' or '/'", name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("invalid remote name %q: must not contain '..'", name)
	case strings.HasSuffix(name, ".lock"):
		return fmt.Errorf("invalid remote name %q: must not end with '.lock'", name)
	}
	return nil
}
