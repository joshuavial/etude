package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/registry"
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
	// statusWarn is uncounted — the repo is still exposed to run-ref loss and the
	// operator has to act. Every warning carries the exact command to run.
	statusWarn actionStatus = "warn"
)

// Shared format strings — kept here so apply output and dry-run reporting
// cannot drift from each other.
const (
	fmtCreated           = "created %s"
	fmtSkipped           = "skipped %s"
	fmtConfigured        = "configured %s = %s"
	fmtAlreadyConfigured = "already configured %s = %s"
	fmtRemoteNotFound    = "remote %s not found, skipping refspec configuration"
	fmtRemovedFetch      = "removed %s = %s (a fetch refspec into refs/etude/* makes any 'git fetch --prune' delete run refs not yet pushed)"

	// Warnings state the CONDITION and point at the docs. They deliberately do
	// NOT embed a runnable shell command any more.
	//
	// Three consecutive gate rounds produced a blocking defect in these strings
	// and nowhere else: a placeholder URL git accepts as a relative URL (so
	// pasting it created a broken remote that then silenced this very warning);
	// a preview that dropped the --remote selection (so the operator repaired
	// origin and left the selected remote exposed); and a nested-single-quote
	// command that will not parse when pasted. Each fix was correct and the next
	// round found another. The defect is not any one string — it is that a
	// setup command was trying to emit an executable, context-correct
	// remediation for every state it can observe.
	//
	// Producing correct operator-facing remediation is `etude doctor`'s entire
	// job (bead etude-ldf): it is read-only, it reports OK/WARN/FAIL per check,
	// and an exact remediation command is its deliverable rather than a
	// by-product. init's job is to not exit quietly on an exposed repo. It says
	// what is wrong and where the fix is documented; it does not hand out
	// commands it cannot guarantee.
	fmtWarnFetchRemains    = "warning: %s still has a fetch refspec into refs/etude/* (%s) — any 'git fetch --prune' will delete run refs not yet pushed. See the migration section of docs/init.md."
	fmtWarnFetchRemainsDry = "warning: %s has a fetch refspec into refs/etude/* (%s) — any 'git fetch --prune' will delete run refs not yet pushed. This is a preview; a real run of this command on remote %q removes it."
	fmtWarnNoPush          = "warning: remote %q has no %s push refspec, so run refs never reach it and stay local-only. See docs/init.md."
	fmtWarnNoRemote        = "warning: remote %q not found, so no refs/etude/* push refspec is configured and run refs stay local-only. Add the remote, then re-run etude init against it. See docs/init.md."
	fmtWarnOtherRemote     = "warning: remote %q also has a fetch refspec into refs/etude/* (%s) — any 'git fetch --prune' against it will delete run refs not yet pushed. This run only configured the remote it was pointed at; re-run etude init with --remote %q to repair that one. See docs/init.md."
)

// etudeRefPrefix is the ref namespace this tool owns.
const etudeRefPrefix = "refs/etude/"

// canonicalPushRefspec is the one push refspec etude registers and checks for.
// init compares against it EXACTLY. Deciding whether some other refspec happens
// to be equivalent needs a full model of refspec semantics — glob matching, name
// preservation, grammar — which belongs in `etude doctor` (bead etude-ldf), not
// in a setup command that would only be guessing.
const canonicalPushRefspec = "refs/etude/*:refs/etude/*"

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

	// Generate registry.yaml bytes and self-check.
	reg := registry.Default()
	registryBytes, err := reg.YAML()
	if err != nil {
		return nil, fmt.Errorf("generate registry.yaml: %w", err)
	}
	if _, err := registry.ParseYAML(registryBytes); err != nil {
		return nil, fmt.Errorf("registry.yaml self-check failed: %w", err)
	}

	var actions []initAction

	// Workflow.yaml write action.
	workflowPath := filepath.Join(etudDir, "workflow.yaml")
	actions = append(actions, writeAction(workflowPath, yamlBytes))

	// Registry.yaml write action.
	registryPath := filepath.Join(etudDir, "registry.yaml")
	actions = append(actions, writeAction(registryPath, registryBytes))

	// Rubric placeholder write actions.
	for _, entry := range rubrics {
		fullPath := filepath.Join(etudDir, entry.path)
		content := fmt.Sprintf("# Rubric for %s\nTODO: define evaluation criteria.\n", entry.stage)
		actions = append(actions, writeAction(fullPath, []byte(content)))
	}

	// Refspec phase — exactly one action.
	actions = append(actions, refspecAction(ctx, root, remote, remoteChanged))

	// Safety phase — report if the target remote is still exposed, so init never
	// exits quietly on a repo that can still lose run refs.
	actions = append(actions, refspecSafetyAction(ctx, root, remote))

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
			// No FETCH refspec is registered. One whose destination is inside
			// refs/etude/* makes every local run ref a remote-tracking ref, so
			// any `git fetch --prune` deletes every run ref not yet pushed. Any
			// left by an older etude init is removed instead. `etude sync`
			// passes the refspec explicitly, so nothing needs it in config.
			//
			// The PUSH refspec stays: it is what makes `git push` carry run refs
			// at all, and pushing cannot delete a local ref. Removing both would
			// lose the same data by another route.
			fetchKey := fmt.Sprintf("remote.%s.fetch", remote)
			pushKey := fmt.Sprintf("remote.%s.push", remote)
			pushVal := canonicalPushRefspec

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
				// A hazardous-fetch-refspec removal is previewed on BOTH paths,
				// because it happens on both.
				var lines []actionLine
				stale, err := findEtudeFetchRefspecs(ctx, root, fetchKey)
				if err != nil {
					return nil, err
				}
				for _, v := range stale {
					lines = append(lines, actionLine{statusConfigured, fmt.Sprintf("plan: remove %s = %s", fetchKey, v)})
				}
				if force {
					// force + present → silent except for the removal.
					return lines, nil
				}
				return append(lines, actionLine{statusConfigured, fmt.Sprintf("plan: configure push refspec on %s", remote)}), nil
			}

			// Normal (non-dry-run) run.

			// Force gate: --force is always silent on refspecs EXCEPT for the
			// explicit-missing-remote error case.
			if force {
				if remoteChanged && !remoteExists(ctx, root, remote) {
					return nil, remoteNotFoundErr(remote)
				}
				if !remoteExists(ctx, root, remote) {
					return nil, nil
				}
				// Force is silent on refspecs EXCEPT for removing a hazardous
				// fetch refspec — a known data-loss setting is never left in
				// place just because the caller passed --force.
				return removeEtudeFetchRefspecs(ctx, root, fetchKey)
			}

			// Non-force normal run.
			if !remoteExists(ctx, root, remote) {
				if remoteChanged {
					return nil, remoteNotFoundErr(remote)
				}
				note := fmt.Sprintf(fmtRemoteNotFound, remote)
				return []actionLine{{statusNote, note}}, nil
			}

			// Remote present. ORDER IS LOAD-BEARING and the rule is etude-i19's:
			// a legacy fetch refspec whose destination lands in the LOCAL etude
			// namespace makes every local run ref prunable, so it is removed
			// FIRST. Every later step therefore runs from a state that cannot be
			// pruned by configuration, and an interruption at any point leaves
			// the dangerous setting gone rather than the safe one missing.
			//
			// (Precisely: after this step no NEW fetch can be started with the
			// legacy refspec. A `git fetch --prune` already in flight read its
			// refspecs at startup and still holds it; removing config cannot
			// reach into a running process. That window is one-time and is
			// inherited from etude-i19, which has the same property.)
			lines, err := removeEtudeFetchRefspecs(ctx, root, fetchKey)
			if err != nil {
				return nil, err
			}
			pushLines, err := addRefspecIfAbsent(ctx, root, pushKey, pushVal)
			if err != nil {
				return nil, err
			}
			lines = append(lines, pushLines...)

			// Then install the mirrored fetch refspecs. Their destination is the
			// SIBLING namespace refs/etude-mirror/<remote>/<kind>/, so a bare
			// `git fetch --prune` can only ever delete a disposable mirror ref —
			// which is what makes a configured fetch refspec safe to have again
			// after etude-i19 had to remove it entirely.
			mirrorLines, err := addMirroredFetchRefspecs(ctx, root, remote, fetchKey)
			if err != nil {
				return nil, err
			}
			return append(lines, mirrorLines...), nil
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
	// Count rather than return on the first match. A repo carrying DUPLICATE
	// canonical entries — left by an older version of this code, which used a
	// racy read-then-add — would otherwise be reported "already configured" and
	// never collapsed. Falling through to --replace-all converges it to one,
	// so the phase self-heals instead of preserving the old bug's output.
	matches := 0
	for _, v := range existing {
		if v == value {
			matches++
		}
	}
	if matches == 1 {
		text := fmt.Sprintf(fmtAlreadyConfigured, key, value)
		return []actionLine{{statusAlreadyConfigured, text}}, nil
	}
	// --replace-all with a value-pattern matching exactly this value, rather than
	// --add. The read above is not atomic with the write, and worktree lanes
	// share one .git/config, so two concurrent inits can both observe "absent"
	// and both add — leaving duplicate entries. --replace-all collapses every
	// line matching the pattern to a single value, and adds it when none match,
	// so concurrent invocations CONVERGE on exactly one entry instead of racing.
	// The read above therefore only decides the message, never correctness.
	//
	// Directive A: use --local explicitly.
	pattern := "^" + regexp.QuoteMeta(value) + "$"
	if err := runGitConfigWithLockRetry(ctx, root, "--replace-all", key, value, pattern); err != nil {
		return nil, err
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

// refspec is a parsed git refspec: [+]<src>[:<dst>]. With no colon, one pattern
// serves as both sides.
//
// This is parsed rather than substring-matched because a refspec has several
// spellings that mean the same thing — "+refs/etude/*:refs/etude/*",
// "refs/etude/*:refs/etude/*", and a bare "refs/etude/*" all name the same
// namespace, and a substring test for ":refs/etude/" silently misses the last
// one. init only needs to answer ONE question about a refspec: did etude
// register it? Anything requiring real refspec SEMANTICS — glob coverage, name
// preservation, grammar validity — belongs to `etude doctor` (bead etude-ldf).
type refspec struct {
	force    bool
	src, dst string
	// hasDst records whether a destination was written at all. It is NOT
	// cosmetic: a colonless refspec means opposite things on the two sides.
	// For FETCH, no destination means the ref is fetched to FETCH_HEAD and no
	// local ref is updated — so it creates no local ref and cannot be pruned.
	// For PUSH, it means the same name on the remote. Treating a colonless
	// fetch refspec as "dst = src" would make init delete a harmless entry.
	hasDst bool
}

func parseRefspec(value string) refspec {
	s := strings.TrimSpace(value)
	force := strings.HasPrefix(s, "+")
	s = strings.TrimPrefix(s, "+")
	src, dst, ok := strings.Cut(s, ":")
	if !ok {
		return refspec{force: force, src: s, hasDst: false}
	}
	return refspec{force: force, src: src, dst: dst, hasDst: true}
}

// etudeOwnedFetchRefspec reports whether a fetch refspec is one etude itself
// registers: its destination lands inside refs/etude/. These, and only these,
// are init's to remove.
//
// A refspec BROADER than the namespace (e.g. "+refs/*:refs/*") prunes run refs
// just as effectively, but it is the user's own configuration and deleting it
// would break their branch fetching. Detecting and reporting that case needs
// glob semantics and is `etude doctor`'s job (etude-ldf); init does not guess.
func etudeOwnedFetchRefspec(r refspec) bool {
	// No destination means the ref goes to FETCH_HEAD only — no local ref is
	// created, so nothing is prunable and there is nothing for init to remove.
	// Deleting such an entry would destroy harmless user configuration, which is
	// a worse outcome than the bug this bead fixes.
	if !r.hasDst {
		return false
	}
	return strings.HasPrefix(strings.TrimSuffix(r.dst, "*"), etudeRefPrefix)
}

// findEtudeFetchRefspecs returns the values of key that etude registered and may
// remove. Read-only, so the dry-run preview and the real run report the same set.
func findEtudeFetchRefspecs(ctx context.Context, root, key string) ([]string, error) {
	existing, err := gitGetAll(ctx, root, key)
	if err != nil {
		return nil, fmt.Errorf("git config --get-all %s: %w", key, err)
	}
	var hazardous []string
	for _, v := range existing {
		if etudeOwnedFetchRefspec(parseRefspec(v)) {
			hazardous = append(hazardous, v)
		}
	}
	return hazardous, nil
}

// removeEtudeFetchRefspecs deletes every etude-registered fetch refspec on key.
//
// Such an entry makes every local run ref a remote-tracking ref, and per
// git-fetch(1) refs fetched due to an explicit configured refspec are subject to
// pruning — so one `git fetch --prune` anywhere in the repository deletes every
// run ref not yet pushed. That destroyed three recorded gate attempts during
// epic etude-9uf (bead etude-nad), and it fires from ordinary tooling:
// `workmux remove --gone` runs `git fetch --prune` automatically, and linked
// worktrees share one ref store.
//
// Runs on every init, including --force.
func removeEtudeFetchRefspecs(ctx context.Context, root, key string) ([]actionLine, error) {
	hazardous, err := findEtudeFetchRefspecs(ctx, root, key)
	if err != nil {
		return nil, err
	}
	if len(hazardous) == 0 {
		return nil, nil
	}

	// ONE git call removes ALL matches, via an anchored alternation of the exact
	// escaped values. Unsetting them one call at a time would not be atomic:
	// with two hazardous entries, a kill between calls leaves the second active
	// and the repo still prunable — precisely the state this exists to remove.
	// git rewrites .git/config under an exclusive lock, so a single --unset-all
	// either removes every match or changes nothing.
	alternatives := make([]string, 0, len(hazardous))
	for _, v := range hazardous {
		alternatives = append(alternatives, regexp.QuoteMeta(v))
	}
	valuePattern := "^(" + strings.Join(alternatives, "|") + ")$"

	// Two concurrency cases, both reachable because worktree lanes share one
	// .git/config and the read above is not atomic with this write:
	//   - exit 5, "unset an option which does not exist": a concurrent init
	//     already removed them. That is the outcome we wanted, so it is success,
	//     but report nothing removed — this process did not remove it.
	//   - "could not lock config file": git takes an exclusive lock and does NOT
	//     retry, so simultaneous inits collide. Retry briefly. This is detected
	//     by message, so the child runs under LC_ALL=C — a localized git would
	//     otherwise translate the string and turn a retriable collision into a
	//     hard failure. Matching an exit code instead does not work here: git
	//     exits 255 on a lock collision, which is its generic fatal status and
	//     would swallow unrelated errors (it is NOT the documented 4, "can not
	//     write config file").
	//
	// ponytail: fixed short backoff, no jitter — the contending population is a
	// handful of lanes, not a fleet. Move to jittered exponential backoff if init
	// ever runs at real concurrency.
	if err := runGitConfigWithLockRetry(ctx, root, "--unset-all", key, valuePattern); err != nil {
		if errors.Is(err, errNothingToUnset) {
			return nil, nil
		}
		return nil, err
	}

	lines := make([]actionLine, 0, len(hazardous))
	for _, v := range hazardous {
		lines = append(lines, actionLine{statusConfigured, fmt.Sprintf(fmtRemovedFetch, key, v)})
	}
	return lines, nil
}

// refspecSafetyAction reports, without changing anything, whether the remote
// init was pointed at ended up in the safe state. Exiting quietly on an exposed
// repo is how the original bug went unnoticed across two incidents.
//
// Deliberately narrow: it checks only the TARGET remote, and only by exact
// comparison — "is an etude-registered fetch refspec still present" and "is the
// canonical push refspec present". A general audit (other remotes, refspecs
// broader than the namespace, mappings that do not preserve names, invalid
// grammar) requires a full refspec-semantics model and is `etude doctor`'s job,
// tracked as bead etude-ldf. A setup command that guessed at those would report
// confidently and be wrong.
//
// Read-only: it performs no writes and runs on every path, including --force and
// --dry-run. Its OUTPUT is not identical between the two, and cannot be: a real
// run removes the hazardous entry before this action reads it, so the entry is
// reported only under --dry-run — and there, with preview-appropriate wording.
func refspecSafetyAction(ctx context.Context, root, remote string) initAction {
	return initAction{
		run: func(force, dryRun bool) ([]actionLine, error) {
			var lines []actionLine

			// A missing target remote is reported, but must NOT short-circuit
			// the sweep below: the repo can still carry a hazardous refspec on a
			// remote that does exist, and `git fetch --prune <that remote>`
			// deletes unpushed run refs regardless of whether the remote this
			// run was pointed at is present. Returning here would make the most
			// exposed configuration — no origin, a hazardous sibling — the one
			// case that reports nothing.
			targetExists := remoteExists(ctx, root, remote)
			if !targetExists {
				lines = append(lines, actionLine{statusWarn, fmt.Sprintf(fmtWarnNoRemote, remote)})
			}

			// Target-remote checks only make sense when it exists; the
			// missing-remote warning above already covers that case, and adding
			// "no push refspec" on top of "remote not found" is noise.
			if targetExists {
				fetchKey := fmt.Sprintf("remote.%s.fetch", remote)
				stale, err := findEtudeFetchRefspecs(ctx, root, fetchKey)
				if err != nil {
					return nil, err
				}
				for _, v := range stale {
					// Under --dry-run nothing has been removed yet, so the entry
					// is still there by construction and a manual unset command
					// would contradict the "plan: remove ..." line above.
					if dryRun {
						lines = append(lines, actionLine{statusWarn, fmt.Sprintf(fmtWarnFetchRemainsDry, fetchKey, v, remote)})
						continue
					}
					lines = append(lines, actionLine{statusWarn, fmt.Sprintf(fmtWarnFetchRemains, fetchKey, v)})
				}

				pushKey := fmt.Sprintf("remote.%s.push", remote)
				push, err := gitGetAll(ctx, root, pushKey)
				if err != nil {
					return nil, err
				}
				found := false
				for _, v := range push {
					if v == canonicalPushRefspec {
						found = true
						break
					}
				}
				if !found {
					lines = append(lines, actionLine{statusWarn, fmt.Sprintf(fmtWarnNoPush, remote, canonicalPushRefspec)})
				}
			}

			// Every OTHER remote is checked too, and only warned about. init
			// configures the one remote it was pointed at, so a hazardous entry
			// on a sibling remote survives it — and `git fetch --prune <that
			// remote>` deletes unpushed run refs just the same. Reporting only
			// the target remote would leave a real exposure silent, which is the
			// failure mode this phase exists to prevent.
			//
			// This needs NO refspec semantics beyond what init already has: it
			// is the same exact etudeOwnedFetchRefspec predicate applied to more
			// config keys. It is not the general audit that was cut to
			// `etude doctor` — that one needed glob coverage, name preservation
			// and grammar validation, none of which appear here.
			//
			// Warn only, never remove: --remote named ONE remote, and silently
			// editing a different one's config is not what was asked for.
			others, err := gitRemotes(ctx, root)
			if err != nil {
				return nil, err
			}
			for _, other := range others {
				if other == remote {
					continue
				}
				otherKey := fmt.Sprintf("remote.%s.fetch", other)
				otherStale, err := findEtudeFetchRefspecs(ctx, root, otherKey)
				if err != nil {
					return nil, err
				}
				for _, v := range otherStale {
					lines = append(lines, actionLine{statusWarn, fmt.Sprintf(fmtWarnOtherRemote, other, v, other)})
				}
			}
			return lines, nil
		},
	}
}

// errNothingToUnset reports git config's exit 5, "tried to unset an option which
// does not exist". For a concurrent init that is success, not failure: another
// process reached the desired end state first.
//
// Only the --unset-all caller checks for it. --replace-all cannot produce exit 5
// (it ADDS when no line matches), so the add path never sees this sentinel.
var errNothingToUnset = errors.New("nothing to unset")

// runGitConfigWithLockRetry runs one `git config --local <args...>` write,
// retrying briefly on a lock collision.
//
// Both durable config writes in this command go through here. git takes an
// EXCLUSIVE lock on .git/config and does NOT retry, so simultaneous inits — which
// are normal when worktree lanes share one config file — collide with
// "could not lock config file". The collision is detected by MESSAGE, so the
// child runs under LC_ALL=C: a localized git would otherwise translate the
// string and turn a retriable collision into a hard failure. Matching an exit
// code instead does not work here — git exits 255 on a lock collision, which is
// its generic fatal status and would swallow unrelated errors (it is NOT the
// documented 4, "can not write config file"). Verified by holding the lock file.
//
// ponytail: fixed short backoff, no jitter — the contending population is a
// handful of lanes, not a fleet. Move to jittered exponential backoff if init
// ever runs at real concurrency.
func runGitConfigWithLockRetry(ctx context.Context, root string, args ...string) error {
	full := append([]string{"-C", root, "config", "--local"}, args...)
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 20 * time.Millisecond):
			}
		}
		cmd := exec.CommandContext(ctx, "git", full...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		output, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 5 {
			return errNothingToUnset
		}
		lastErr = fmt.Errorf("git config %s: %w\n%s", strings.Join(args, " "), err, output)
		if !strings.Contains(string(output), "could not lock config file") {
			return lastErr
		}
	}
	return lastErr
}

// gitRemotes lists the repo's configured remotes.
func gitRemotes(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "remote")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git remote: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// mirroredFetchRefspec is the refspec that mirrors one kind from a remote:
//
//	+refs/etude/<kind>/*:refs/etude-mirror/<remote>/<kind>/*
//
// Forced (+) is correct here and ONLY here. The destination is a copy of the
// remote, so it should always match the remote and there is nothing of ours to
// lose in it. The local namespace is never a fetch destination, so no forced
// refspec can overwrite an authoritative local run.
func mirroredFetchRefspec(remote, kind string) string {
	return "+refs/etude/" + kind + "/*:" + refstore.MirrorPrefix(remote, kind) + "*"
}

// addMirroredFetchRefspecs installs one mirrored fetch refspec per etude ref
// kind on the target remote, driven from refstore.Kinds so a new kind cannot be
// silently omitted from one side of the fetch/push pair.
//
// Each is a separate `git config` write and therefore a separate crash point.
// That is safe in any prefix: a partially-installed set mirrors only some kinds,
// which is incomplete mirroring with no data loss and no upload path — no push
// refspec can match the mirror namespace at all.
func addMirroredFetchRefspecs(ctx context.Context, root, remote, fetchKey string) ([]actionLine, error) {
	// Validate the CONSTRUCTED mirror ref before writing any refspec: one that
	// cannot work must never be installed.
	//
	// This is not redundant with git's own remote-name check. `git remote add`
	// rejects a name like "prod:backup", but a remote written straight into
	// config as remote.<name>.url exists and `git remote get-url` succeeds for
	// it — so remoteExists returns true and we get here with a name that cannot
	// form a legal ref. Removing this check let exactly that install three
	// unusable refspecs; see the note in internal/refstore/store.go.
	if err := (refstore.Store{RepoDir: root}).ValidateMirrorRemote(ctx, remote); err != nil {
		return nil, fmt.Errorf("cannot mirror remote %q: %w", remote, err)
	}

	var lines []actionLine
	for _, kind := range refstore.Kinds {
		l, err := addRefspecIfAbsent(ctx, root, fetchKey, mirroredFetchRefspec(remote, kind))
		if err != nil {
			return nil, err
		}
		lines = append(lines, l...)
	}
	return lines, nil
}
