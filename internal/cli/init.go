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

func newInitCommand(out, errOut io.Writer) *cobra.Command {
	var force bool
	var remote string

	cmd := &cobra.Command{
		Use:           "init",
		Short:         "Scaffold .etude/ config and register refs/etude/* refspecs",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := initRunner{stdout: out, stderr: errOut}
			return runner.run(cmd.Context(), force, remote, cmd.Flags().Changed("remote"))
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing scaffolded files with fresh generated content")
	cmd.Flags().StringVar(&remote, "remote", "origin", "git remote to configure refspecs on (default: origin)")
	return cmd
}

type initRunner struct {
	stdout io.Writer
	stderr io.Writer
}

func (r initRunner) run(ctx context.Context, force bool, remote string, remoteChanged bool) error {
	// Validate the --remote value before ANY git invocation so a malformed
	// value fails fast and never reaches a git subprocess or a config key.
	if err := validateRemoteName(remote); err != nil {
		return err
	}

	// Resolve repo root using git rev-parse --show-toplevel.
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	// Scaffold .etude/ working-tree files.
	if err := r.scaffoldFiles(root, force); err != nil {
		return err
	}

	// Directive D: --force regenerates scaffolded files only; it must NOT modify
	// git config. But the explicit-remote invariant still holds under --force: an
	// explicitly named remote that does not exist is an error regardless, so a
	// typo in `--force --remote <name>` surfaces instead of silently succeeding.
	// This check is read-only (no config write), so directive D is preserved.
	if force {
		if remoteChanged && !remoteExists(ctx, root, remote) {
			return remoteNotFoundErr(remote)
		}
		return nil
	}

	return r.configureRefspecs(ctx, root, remote, remoteChanged)
}

// scaffoldFiles writes .etude/workflow.yaml and rubric placeholder files into root.
func (r initRunner) scaffoldFiles(root string, force bool) error {
	etudDir := filepath.Join(root, ".etude")

	// Directive C: guard against .etude/ existing as a regular file.
	if info, err := os.Stat(etudDir); err == nil && !info.IsDir() {
		return fmt.Errorf(".etude exists as a regular file, not a directory: %s", etudDir)
	}

	// Generate workflow.yaml bytes from the canonical default.
	wf := workflow.Default()
	yamlBytes, err := wf.YAML()
	if err != nil {
		return fmt.Errorf("generate workflow.yaml: %w", err)
	}

	// Self-check: round-trip parse before writing anything.
	if _, err := workflow.ParseYAML(yamlBytes); err != nil {
		return fmt.Errorf("workflow.yaml self-check failed: %w", err)
	}

	// Derive rubric placeholder paths dynamically from Default().Stages.
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

	// Write workflow.yaml.
	workflowPath := filepath.Join(etudDir, "workflow.yaml")
	if err := writeScaffoldFile(r.stdout, workflowPath, yamlBytes, force); err != nil {
		return err
	}

	// Write rubric placeholders.
	for _, entry := range rubrics {
		fullPath := filepath.Join(etudDir, entry.path)
		// Directive F: self-documenting placeholder content.
		content := fmt.Sprintf("# Rubric for %s\nTODO: define evaluation criteria.\n", entry.stage)
		if err := writeScaffoldFile(r.stdout, fullPath, []byte(content), force); err != nil {
			return err
		}
	}

	return nil
}

// writeScaffoldFile writes content to path, creating parent dirs as needed.
// If force is false and the file already exists it prints "skipped <path>".
// If force is true or the file does not exist it writes and prints "created <path>".
func writeScaffoldFile(out io.Writer, path string, content []byte, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		fmt.Fprintf(out, "skipped %s\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(out, "created %s\n", path)
	return nil
}

// configureRefspecs sets up fetch and push refspecs on the named remote.
// If the remote does not exist and was not explicitly named, it skips with a
// printed note (init still succeeds). If --remote was passed explicitly and
// the remote is absent it returns an error.
func (r initRunner) configureRefspecs(ctx context.Context, root, remote string, remoteChanged bool) error {
	// Check remote existence.
	if !remoteExists(ctx, root, remote) {
		if remoteChanged {
			return remoteNotFoundErr(remote)
		}
		fmt.Fprintf(r.stdout, "remote %s not found, skipping refspec configuration\n", remote)
		return nil
	}

	fetchKey := fmt.Sprintf("remote.%s.fetch", remote)
	fetchVal := "+refs/etude/*:refs/etude/*"
	pushKey := fmt.Sprintf("remote.%s.push", remote)
	pushVal := "refs/etude/*:refs/etude/*"

	if err := addRefspecIfAbsent(ctx, r.stdout, root, fetchKey, fetchVal); err != nil {
		return err
	}
	if err := addRefspecIfAbsent(ctx, r.stdout, root, pushKey, pushVal); err != nil {
		return err
	}
	return nil
}

// addRefspecIfAbsent adds value to key only when no byte-exact match already
// exists, ensuring idempotency. Exit code 1 from --get-all means no entries
// (key absent); only non-zero codes other than 1 are treated as errors.
//
// Note: every git invocation is pinned with `git -C <root>` rather than relying
// on the process working directory, which is more robust when the cwd changes
// between calls (e.g. tests chdir). This deliberately differs from capture.go,
// which runs git relative to the current directory.
func addRefspecIfAbsent(ctx context.Context, out io.Writer, root, key, value string) error {
	existing, err := gitGetAll(ctx, root, key)
	if err != nil {
		return fmt.Errorf("git config --get-all %s: %w", key, err)
	}
	for _, v := range existing {
		if v == value {
			fmt.Fprintf(out, "already configured %s = %s\n", key, value)
			return nil
		}
	}
	// Directive A: use --local explicitly.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "config", "--local", "--add", key, value)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config --add %s: %w\n%s", key, err, output)
	}
	fmt.Fprintf(out, "configured %s = %s\n", key, value)
	return nil
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
