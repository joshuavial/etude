package liverun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/joshuavial/etude/internal/worktree"
)

const runsPrefix = "refs/etude/runs/"

var (
	ErrCallerWorkspaceDirty       = errors.New("caller workspace has uncommitted changes")
	ErrCallerWorkspaceChanged     = errors.New("caller workspace HEAD changed during provenance capture")
	ErrCallerWorkspaceUnsupported = errors.New("caller workspace repository is unsupported")
)

// StageError records a stage execution failure with the run id so callers can
// print a --resume hint.
type StageError struct {
	StageName string
	RunID     string
	Err       error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("stage %q failed: %v", e.StageName, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// roleArtifact pairs a content-addressed ArtifactRef with its raw bytes.
type roleArtifact struct {
	ref     runmanifest.ArtifactRef
	content []byte
}

func phaseRound(name, phase string) (int, bool) {
	prefix := phase + ".r"
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	round, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	return round, err == nil && round > 0
}

func nextPhaseRound(phase string, stages []runmanifest.Stage, gates []runmanifest.GateAttempt) int {
	maxRound := 0
	for _, stage := range stages {
		if round, ok := phaseRound(stage.Name, phase); ok && round > maxRound {
			maxRound = round
		}
	}
	for _, gate := range gates {
		if gate.Phase == phase && gate.Round > maxRound {
			maxRound = gate.Round
		}
	}
	return maxRound + 1
}

func latestStageForRole(stages []runmanifest.Stage, role string) (runmanifest.Stage, bool) {
	for i := len(stages) - 1; i >= 0; i-- {
		if stages[i].Output.Role == role {
			return stages[i], true
		}
	}
	return runmanifest.Stage{}, false
}

func isModelSeat(seat runmanifest.SeatResult) bool {
	return !(strings.HasPrefix(seat.Seat, "check.") && seat.Provider.Name == "deterministic")
}

func isNonUsableSeat(verdict runmanifest.SeatVerdict) bool {
	switch verdict {
	case runmanifest.SeatVerdictFailed, runmanifest.SeatVerdictEmpty,
		runmanifest.SeatVerdictMalfunction, runmanifest.SeatVerdictDisregarded:
		return true
	default:
		return false
	}
}

func recoverableGateStageIndexes(wf workflow.Workflow, manifest runmanifest.Manifest) []int {
	latestByPhase := make(map[string]runmanifest.GateAttempt)
	for _, attempt := range manifest.Gates {
		latestByPhase[attempt.Phase] = attempt
	}

	var indexes []int
	for i, stage := range wf.Stages {
		if stage.Gate == nil {
			continue
		}
		captured, ok := latestStageForRole(manifest.Stages, stage.Produces)
		if !ok || captured.Output.Role != stage.Produces {
			continue
		}
		latest, ok := latestByPhase[stage.Name]
		if !ok || latest.Status != runmanifest.GateStatusEscalated ||
			!strings.HasPrefix(latest.Decision.EscalationReason, insufficientUsableSeatsPrefix) {
			continue
		}

		modelSeats := 0
		allLatestNonUsable := true
		phaseHasSubstantiveVerdict := false
		for _, attempt := range manifest.Gates {
			if attempt.Phase != stage.Name {
				continue
			}
			for _, seat := range attempt.Seats {
				if !isModelSeat(seat) {
					continue
				}
				if seat.Verdict == runmanifest.SeatVerdictGo || seat.Verdict == runmanifest.SeatVerdictBlock {
					phaseHasSubstantiveVerdict = true
				}
			}
		}
		for _, seat := range latest.Seats {
			if !isModelSeat(seat) {
				continue
			}
			modelSeats++
			if !isNonUsableSeat(seat.Verdict) {
				allLatestNonUsable = false
			}
		}
		if modelSeats > 0 && allLatestNonUsable && !phaseHasSubstantiveVerdict {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

// RunOptions configures a call to Engine.Run.
type RunOptions struct {
	// TaskBytes is the task input content. Required unless ResumeID is set.
	TaskBytes []byte
	// TaskFile is the filename used to infer the task media type.
	TaskFile string
	// RunID is an explicit run id; auto-generated if empty.
	RunID string
	// GitSHA is the git commit SHA; defaults to HEAD if empty.
	GitSHA string
	// ResumeID, when non-empty, resumes an existing partial run.
	// TaskBytes, TaskFile, RunID, and GitSHA are ignored in resume mode.
	ResumeID string
}

// Engine executes a live workflow run.
type Engine struct {
	// Store is the refstore for CAS commits.
	Store refstore.Store
	// ResolveRunner returns a runner for the given stage.
	// Tests inject a StubRunner; production code resolves from workflow/registry config.
	ResolveRunner func(stage workflow.Stage) (replay.Runner, error)
	// ResolveCheck resolves a CheckRunner for a gate check.
	// Required when any stage has a gate with checks configured.
	// Tests inject a stub; production wires from registry.
	ResolveCheck func(r workflow.Runner) (CheckRunner, error)
	// ResolveSeat resolves a seat runner and its provider/harness metadata.
	// Required when any stage has a gate with seats or a tier configured.
	// Tests inject a stub returning canned envelope JSON.
	ResolveSeat func(seatName string) (replay.Runner, SeatMeta, error)
	// ResolveSeatCandidates, when set, returns the seat's full invocation ladder
	// in retry order and takes precedence over ResolveSeat. When nil the engine
	// falls back to the single-runner path, so every existing caller and test is
	// unchanged — which matters because a seat with no configured fallbacks (all
	// of them except `opus` today) behaves identically either way.
	ResolveSeatCandidates func(seatName string) ([]SeatCandidate, error)
	// Tiers returns the seat names and next-stronger tier name for a given
	// registry tier name. Returns ok=false when the tier is not found.
	// Required when any stage has a gate with a Tier configured.
	Tiers func(tierName string) (seats []string, nextStronger string, ok bool)
	// Root is the repository root directory used for worktree checkout and HEAD resolution.
	Root string
	// Now returns the current time. Defaults to time.Now when nil.
	Now func() time.Time
	// EnvAllowlist is the list of env var NAMES configured for passthrough to
	// live runners.  It is written to every manifest for audit (NAMES only;
	// VALUES are never stored).  The same list must drive both the runner
	// closures (ResolveRunner/ResolveSeat) and this field so audit cannot lie.
	EnvAllowlist []string
	// CallerDir is the directory from which the CLI was invoked. Caller-workspace
	// runners execute here; an empty value falls back to Root for library users.
	CallerDir string
	// ResolveCallerHEAD resolves HEAD during post-run provenance capture. Tests
	// may inject different consecutive values to prove the race fails closed;
	// production removes ambient Git overrides and replacement-object rewriting.
	ResolveCallerHEAD func(context.Context, string) (string, error)
}

func (e *Engine) clock() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) callerProvenance(ctx context.Context) (string, error) {
	resolve := e.ResolveCallerHEAD
	if resolve == nil {
		resolve = resolveCallerHEAD
	}
	return captureCallerProvenance(ctx, e.Root, resolve)
}

func callerGitCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		env = append(env, item)
	}
	cmd.Env = append(env, "GIT_NO_REPLACE_OBJECTS=1")
	return cmd
}

func resolveCallerHEAD(ctx context.Context, root string) (string, error) {
	out, err := callerGitCommand(ctx, root, "rev-parse", "--verify", "HEAD^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD as commit: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ensureCallerDescendsFrom(ctx context.Context, root, ancestor, descendant string) error {
	err := callerGitCommand(ctx, root, "merge-base", "--is-ancestor", ancestor, descendant).Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return fmt.Errorf("%w: caller history rewrite is not supported; %s is not an ancestor of %s", ErrCallerWorkspaceChanged, ancestor, descendant)
	}
	return fmt.Errorf("verify caller commit ancestry: %w", err)
}

func resolveCanonicalCallerRoot(ctx context.Context, callerDir string) (string, error) {
	out, err := callerGitCommand(ctx, callerDir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("resolve caller directory repository: %w", err)
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
}

func ensureCallerRepository(ctx context.Context, root, callerDir string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve guarded repository root: %w", err)
	}
	want, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return fmt.Errorf("resolve guarded repository root: %w", err)
	}
	got, err := resolveCanonicalCallerRoot(ctx, callerDir)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%w: caller directory repository %q does not match guarded root %q", ErrCallerWorkspaceUnsupported, got, want)
	}
	return nil
}

type callerRepositoryIdentity struct {
	root       string
	gitDir     string
	commonDir  string
	gitInfo    os.FileInfo
	commonInfo os.FileInfo
}

func captureCallerRepositoryIdentity(ctx context.Context, root string) (callerRepositoryIdentity, error) {
	resolvePath := func(args ...string) (string, os.FileInfo, error) {
		out, err := callerGitCommand(ctx, root, args...).Output()
		if err != nil {
			return "", nil, err
		}
		path := strings.TrimSpace(string(out))
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", nil, err
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return "", nil, err
		}
		info, err := os.Stat(path)
		return path, info, err
	}
	canonicalRoot, err := resolveCanonicalCallerRoot(ctx, root)
	if err != nil {
		return callerRepositoryIdentity{}, err
	}
	gitDir, gitInfo, err := resolvePath("rev-parse", "--absolute-git-dir")
	if err != nil {
		return callerRepositoryIdentity{}, fmt.Errorf("resolve caller Git directory identity: %w", err)
	}
	commonDir, commonInfo, err := resolvePath("rev-parse", "--git-common-dir")
	if err != nil {
		return callerRepositoryIdentity{}, fmt.Errorf("resolve caller common Git directory identity: %w", err)
	}
	return callerRepositoryIdentity{root: canonicalRoot, gitDir: gitDir, commonDir: commonDir, gitInfo: gitInfo, commonInfo: commonInfo}, nil
}

func sameCallerRepositoryIdentity(before, after callerRepositoryIdentity) bool {
	return before.root == after.root && before.gitDir == after.gitDir && before.commonDir == after.commonDir &&
		os.SameFile(before.gitInfo, after.gitInfo) && os.SameFile(before.commonInfo, after.commonInfo)
}

func (e *Engine) callerDir() string {
	if e.CallerDir != "" {
		return e.CallerDir
	}
	return e.Root
}

func ensureCallerWorkspaceSupported(ctx context.Context, root string) error {
	entries, err := callerGitCommand(ctx, root, "ls-files", "--stage", "-z").Output()
	if err != nil {
		return fmt.Errorf("inspect caller workspace repository shape: %w", err)
	}
	for _, entry := range bytes.Split(entries, []byte{0}) {
		if bytes.HasPrefix(entry, []byte("160000 ")) {
			return fmt.Errorf("%w: tracked submodules are not supported", ErrCallerWorkspaceUnsupported)
		}
	}
	graftsOut, err := callerGitCommand(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "info/grafts").Output()
	if err != nil {
		return fmt.Errorf("resolve caller workspace graft file: %w", err)
	}
	grafts, err := os.ReadFile(strings.TrimSpace(string(graftsOut)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read caller workspace graft file: %w", err)
	}
	if len(grafts) != 0 {
		return fmt.Errorf("%w: pre-existing info/grafts is not supported", ErrCallerWorkspaceUnsupported)
	}
	return nil
}

func inspectCallerWorkspaceClean(ctx context.Context, root string) error {
	if err := ensureCallerWorkspaceSupported(ctx, root); err != nil {
		return err
	}
	if err := callerGitCommand(ctx, root, "diff-index", "--cached", "--quiet", "HEAD", "--").Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return ErrCallerWorkspaceDirty
		}
		return fmt.Errorf("inspect caller workspace index: %w", err)
	}
	tracked, err := callerGitCommand(ctx, root, "ls-files", "-v", "-z").Output()
	if err != nil {
		return fmt.Errorf("inspect caller workspace index flags: %w", err)
	}
	for _, entry := range bytes.Split(tracked, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		tag := entry[0]
		if tag == 'S' || (tag >= 'a' && tag <= 'z') {
			return fmt.Errorf("%w: tracked path is hidden by assume-unchanged or skip-worktree", ErrCallerWorkspaceDirty)
		}
	}
	if err := inspectCallerTrackedBytes(ctx, root); err != nil {
		return err
	}
	untracked, err := callerGitCommand(ctx, root, "-c", "core.ignoreCase=false", "ls-files", "--others", "-z").Output()
	if err != nil {
		return fmt.Errorf("inspect caller workspace untracked files: %w", err)
	}
	for _, pathBytes := range bytes.Split(untracked, []byte{0}) {
		if len(pathBytes) == 0 {
			continue
		}
		checkErr := callerGitCommand(ctx, root, "-c", "core.ignoreCase=false", "check-ignore", "--quiet", "--no-index", "--", string(pathBytes)).Run()
		if checkErr == nil {
			continue
		}
		var exitErr *exec.ExitError
		if errors.As(checkErr, &exitErr) && exitErr.ExitCode() == 1 {
			return ErrCallerWorkspaceDirty
		}
		return fmt.Errorf("inspect caller workspace untracked path %q: %w", pathBytes, checkErr)
	}
	return nil
}

func inspectCallerTrackedBytes(ctx context.Context, root string) error {
	tree, err := callerGitCommand(ctx, root, "ls-tree", "-r", "-z", "--full-tree", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("inspect caller workspace HEAD tree: %w", err)
	}
	for _, entry := range bytes.Split(tree, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, gitPath, ok := bytes.Cut(entry, []byte{'\t'})
		fields := bytes.Fields(meta)
		if !ok || len(fields) != 3 || string(fields[1]) != "blob" {
			return fmt.Errorf("inspect caller workspace HEAD tree: unexpected entry %q", entry)
		}
		mode, wantOID := string(fields[0]), string(fields[2])
		if err := inspectTrackedPathAncestors(root, string(gitPath)); err != nil {
			return err
		}
		workPath := filepath.Join(root, filepath.FromSlash(string(gitPath)))
		info, err := os.Lstat(workPath)
		if errors.Is(err, os.ErrNotExist) {
			return ErrCallerWorkspaceDirty
		}
		if err != nil {
			return fmt.Errorf("inspect tracked path %q: %w", gitPath, err)
		}

		var content io.Reader
		switch mode {
		case "100644", "100755":
			if !info.Mode().IsRegular() || (info.Mode().Perm()&0o100 != 0) != (mode == "100755") {
				return ErrCallerWorkspaceDirty
			}
			file, err := os.Open(workPath)
			if err != nil {
				return fmt.Errorf("open tracked path %q: %w", gitPath, err)
			}
			content = file
		case "120000":
			if info.Mode()&os.ModeSymlink == 0 {
				return ErrCallerWorkspaceDirty
			}
			target, err := os.Readlink(workPath)
			if err != nil {
				return fmt.Errorf("read tracked symlink %q: %w", gitPath, err)
			}
			content = strings.NewReader(target)
		default:
			return fmt.Errorf("%w: unsupported tracked mode %s", ErrCallerWorkspaceUnsupported, mode)
		}
		cmd := callerGitCommand(ctx, root, "hash-object", "--stdin")
		cmd.Stdin = content
		out, err := cmd.Output()
		if closer, ok := content.(io.Closer); ok {
			_ = closer.Close()
		}
		if err != nil {
			return fmt.Errorf("hash tracked path %q: %w", gitPath, err)
		}
		if strings.TrimSpace(string(out)) != wantOID {
			return ErrCallerWorkspaceDirty
		}
	}
	return nil
}

func inspectTrackedPathAncestors(root, gitPath string) error {
	parts := strings.Split(gitPath, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return ErrCallerWorkspaceDirty
		}
		if err != nil {
			return fmt.Errorf("inspect tracked path ancestor %q: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrCallerWorkspaceDirty
		}
	}
	return nil
}

func callerGitControlSnapshot(ctx context.Context, root string) ([]byte, error) {
	var snapshot bytes.Buffer
	for _, name := range []string{"config", "config.worktree", "info/attributes", "info/exclude", "info/grafts"} {
		out, err := callerGitCommand(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", name).Output()
		if err != nil {
			return nil, fmt.Errorf("resolve caller workspace git control file %q: %w", name, err)
		}
		controlPath := strings.TrimSpace(string(out))
		info, err := os.Lstat(controlPath)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(&snapshot, "%s\x00%s\x00missing\x00", name, controlPath)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect caller workspace git control file %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: Git control file %q is not regular", ErrCallerWorkspaceUnsupported, name)
		}
		contents, err := os.ReadFile(controlPath)
		if err != nil {
			return nil, fmt.Errorf("read caller workspace git control file %q: %w", name, err)
		}
		fmt.Fprintf(&snapshot, "%s\x00%s\x00present\x00%d\x00", name, controlPath, len(contents))
		snapshot.Write(contents)
	}
	excludesOut, err := callerGitCommand(ctx, root, "config", "-z", "--path", "--get", "core.excludesFile").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("resolve caller workspace external excludes file: %w", err)
		}
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			homeDir, homeErr := os.UserHomeDir()
			if homeErr != nil {
				return nil, fmt.Errorf("resolve default caller workspace external excludes file: %w", homeErr)
			}
			configDir = filepath.Join(homeDir, ".config")
		}
		excludesOut = []byte(filepath.Join(configDir, "git", "ignore"))
	} else {
		excludesOut = bytes.TrimSuffix(excludesOut, []byte{0})
	}
	excludesPath := string(excludesOut)
	if !filepath.IsAbs(excludesPath) {
		excludesPath = filepath.Join(root, excludesPath)
	}
	info, err := os.Lstat(excludesPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(&snapshot, "core.excludesFile\x00%s\x00missing\x00", excludesPath)
	} else if err != nil {
		return nil, fmt.Errorf("inspect caller workspace external excludes file: %w", err)
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: external excludes file is not regular", ErrCallerWorkspaceUnsupported)
	} else if contents, readErr := os.ReadFile(excludesPath); readErr != nil {
		return nil, fmt.Errorf("read caller workspace external excludes file: %w", readErr)
	} else {
		fmt.Fprintf(&snapshot, "core.excludesFile\x00%s\x00present\x00%d\x00", excludesPath, len(contents))
		snapshot.Write(contents)
	}

	untrackedOut, err := callerGitCommand(ctx, root, "-c", "core.ignoreCase=false", "ls-files", "--others", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect caller workspace untracked ignore files: %w", err)
	}
	for _, pathBytes := range bytes.Split(untrackedOut, []byte{0}) {
		if len(pathBytes) == 0 || !strings.EqualFold(filepath.Base(string(pathBytes)), ".gitignore") {
			continue
		}
		path := string(pathBytes)
		info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
		if statErr != nil {
			return nil, fmt.Errorf("inspect caller workspace untracked ignore file %q: %w", path, statErr)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: untracked ignore file %q is not regular", ErrCallerWorkspaceUnsupported, path)
		}
		contents, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if readErr != nil {
			return nil, fmt.Errorf("read caller workspace untracked ignore file %q: %w", path, readErr)
		}
		fmt.Fprintf(&snapshot, ".gitignore\x00%s\x00%d\x00", path, len(contents))
		snapshot.Write(contents)
	}
	return snapshot.Bytes(), nil
}

func captureCallerProvenance(ctx context.Context, root string, resolve func(context.Context, string) (string, error)) (string, error) {
	first, err := resolve(ctx, root)
	if err != nil {
		return "", fmt.Errorf("capture caller workspace HEAD: %w", err)
	}
	if err := ensureCallerWorkspaceSupported(ctx, root); err != nil {
		return "", err
	}
	if err := inspectCallerWorkspaceClean(ctx, root); err != nil {
		return "", err
	}
	second, err := resolve(ctx, root)
	if err != nil {
		return "", fmt.Errorf("recheck caller workspace HEAD: %w", err)
	}
	if first != second {
		return "", fmt.Errorf("%w: %s -> %s", ErrCallerWorkspaceChanged, first, second)
	}
	if err := inspectCallerWorkspaceClean(ctx, root); err != nil {
		return "", err
	}
	third, err := resolve(ctx, root)
	if err != nil {
		return "", fmt.Errorf("final caller workspace HEAD check: %w", err)
	}
	if first != third {
		return "", fmt.Errorf("%w: %s -> %s", ErrCallerWorkspaceChanged, first, third)
	}
	return third, nil
}

// Run executes the workflow, capturing each stage incrementally via CAS.
// If opts.ResumeID is non-empty, resumes an existing partial run from its frontier.
func (e *Engine) Run(ctx context.Context, out io.Writer, wf workflow.Workflow, opts RunOptions) error {
	// ParseYAML calls Workflow.Validate, but Engine also accepts programmatic
	// workflows. Enforce the engine-owned rerun input namespace here, before a
	// checkout or runner invocation, so both entry paths fail at the same seam.
	if err := validatePriorAttemptRoleCollisions(wf); err != nil {
		return err
	}
	if opts.ResumeID != "" {
		return e.resume(ctx, out, wf, opts.ResumeID)
	}
	return e.startFresh(ctx, out, wf, opts)
}

func validatePriorAttemptRoleCollisions(wf workflow.Workflow) error {
	for i, stage := range wf.Stages {
		if workflow.IsPriorAttemptRole(stage.Produces) {
			return fmt.Errorf("%w: stage[%d] produces role %q is reserved for gate reruns", workflow.ErrInvalidWorkflow, i, stage.Produces)
		}
		for j, role := range stage.Inputs {
			if workflow.IsPriorAttemptRole(role) {
				return fmt.Errorf("%w: stage[%d] input[%d] role %q is reserved for gate reruns", workflow.ErrInvalidWorkflow, i, j, role)
			}
		}
	}
	return nil
}

func (e *Engine) startFresh(ctx context.Context, out io.Writer, wf workflow.Workflow, opts RunOptions) error {
	runID := opts.RunID
	if runID == "" {
		var err error
		runID, err = GenerateRunID(wf.Name)
		if err != nil {
			return err
		}
	} else if !runmanifest.IsValidRunID(runID) {
		// An explicit --run-id override must pass the same validation as a
		// generated id before it reaches any git ref path (rejects path
		// traversal, .lock, leading/trailing dots, bad charset).
		return fmt.Errorf("invalid run id %q", runID)
	}

	gitSHA := opts.GitSHA
	if gitSHA == "" {
		var err error
		gitSHA, err = resolveHEAD(ctx, e.Root)
		if err != nil {
			return err
		}
	}
	for _, stage := range wf.Stages {
		if wf.EffectiveRunnerWorkspace(stage) == workflow.RunnerWorkspaceCaller {
			if err := ensureCallerWorkspaceSupported(ctx, e.Root); err != nil {
				return err
			}
			break
		}
	}

	wt, err := worktree.Checkout(ctx, e.Root, gitSHA)
	if err != nil {
		switch {
		case errors.Is(err, worktree.ErrInvalidSHA):
			return fmt.Errorf("invalid git sha %q: %w", gitSHA, err)
		case errors.Is(err, worktree.ErrSHANotFound):
			return fmt.Errorf("git sha %q not found in repository", gitSHA)
		default:
			return fmt.Errorf("checkout %q: %w", gitSHA, err)
		}
	}
	defer wt.Close()

	scratch, err := os.MkdirTemp("", "etude-live-scratch-*")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	as := artifactstore.New()
	chain := make(map[string]roleArtifact)

	// Seed task into store and chain (if provided).
	if len(opts.TaskBytes) > 0 {
		mediaType := inferTaskMediaType(opts.TaskFile)
		taskArtifact, err := as.AddContent("task", mediaType, opts.TaskBytes)
		if err != nil {
			return fmt.Errorf("store task: %w", err)
		}
		taskRef := runmanifest.ArtifactFromManifestArtifact(taskArtifact)
		chain["task"] = roleArtifact{ref: taskRef, content: opts.TaskBytes}
	}

	return e.executeStages(ctx, out, wf, runID, gitSHA, wt.Submodules, e.clock(), as, chain, "", 0, nil, nil, false, wt.Dir, scratch)
}

func (e *Engine) resume(ctx context.Context, out io.Writer, wf workflow.Workflow, resumeID string) error {
	ref := runsPrefix + resumeID
	commit, err := e.Store.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, refstore.ErrNotFound) {
			return fmt.Errorf("run %q not found", resumeID)
		}
		return fmt.Errorf("resolve run %q: %w", resumeID, err)
	}

	manifestBytes, err := e.Store.ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		return fmt.Errorf("read manifest for run %q: %w", resumeID, err)
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return fmt.Errorf("parse manifest for run %q: %w", resumeID, err)
	}

	frontier := DeriveFrontier(wf, manifest)
	recoverable := recoverableGateStageIndexes(wf, manifest)
	if frontier >= len(wf.Stages) && len(recoverable) == 0 {
		return fmt.Errorf("run %q is already complete (%d stages done)", resumeID, len(wf.Stages))
	}
	recoverableSet := make(map[int]bool, len(recoverable))
	for _, index := range recoverable {
		recoverableSet[index] = true
	}
	latestByPhase := make(map[string]runmanifest.GateAttempt)
	for _, attempt := range manifest.Gates {
		latestByPhase[attempt.Phase] = attempt
	}
	for i := 0; i < frontier; i++ {
		stage := wf.Stages[i]
		if stage.Gate == nil || recoverableSet[i] {
			continue
		}
		attempt, ok := latestByPhase[stage.Name]
		if !ok {
			return fmt.Errorf("run %q has captured stage %q without a completed gate attempt", resumeID, stage.Name)
		}
		if attempt.Status != runmanifest.GateStatusPass {
			return fmt.Errorf("run %q has terminal gate escalation/status %q at stage %q", resumeID, attempt.Status, stage.Name)
		}
	}
	if len(manifest.Stages) == 0 {
		return fmt.Errorf("run %q has no completed stages to resume from", resumeID)
	}
	originalGitSHA := manifest.OriginalCheckout()

	wt, err := worktree.Checkout(ctx, e.Root, originalGitSHA)
	if err != nil {
		return fmt.Errorf("checkout %q for resume: %w", originalGitSHA, err)
	}
	defer wt.Close()
	for i, stage := range manifest.Stages {
		if stage.RunnerWorkspace == "" && stage.GitSHA != originalGitSHA {
			return fmt.Errorf("run %q stage[%d] git sha %q does not match original checkout %q", resumeID, i, stage.GitSHA, originalGitSHA)
		}
		if err := wt.ValidateSubmodules(stage.Submodules); err != nil {
			return fmt.Errorf("run %q stage[%d]: %w", resumeID, i, err)
		}
	}

	scratch, err := os.MkdirTemp("", "etude-live-scratch-*")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	// Re-seed store and chain from all referenced artifact paths in the manifest.
	as := artifactstore.New()
	chain := make(map[string]roleArtifact)

	refByPath := make(map[string]runmanifest.ArtifactRef)
	for _, ms := range manifest.Stages {
		for _, inp := range ms.Inputs {
			refByPath[inp.Path] = inp
		}
		refByPath[ms.Output.Path] = ms.Output
		if ms.Log != nil {
			ref := *ms.Log
			refByPath[ref.Path] = ref
		}
		if ms.Producer.Session != nil && ms.Producer.Session.TranscriptArtifact != nil {
			ref := *ms.Producer.Session.TranscriptArtifact
			refByPath[ref.Path] = ref
		}
	}
	for _, gate := range manifest.Gates {
		for _, seat := range gate.Seats {
			if seat.RawOutput != nil {
				ref := *seat.RawOutput
				refByPath[ref.Path] = ref
			}
			if seat.Session != nil && seat.Session.TranscriptArtifact != nil {
				ref := *seat.Session.TranscriptArtifact
				refByPath[ref.Path] = ref
			}
		}
	}

	rawBytes := make(map[string][]byte)
	for _, path := range runmanifest.ArtifactPaths(manifest) {
		data, err := e.Store.ReadCommitFile(ctx, commit, path)
		if err != nil {
			return fmt.Errorf("reseed artifact %q: %w", path, err)
		}
		rawBytes[path] = data
		ref := refByPath[path]
		stored, err := as.AddContent(ref.Role, ref.MediaType, data)
		if err != nil {
			return fmt.Errorf("reseed store path %q: %w", path, err)
		}
		storedRef := runmanifest.ArtifactFromManifestArtifact(stored)
		if storedRef.Artifact != ref.Artifact || storedRef.Path != ref.Path {
			return fmt.Errorf("reseed artifact %q: content does not match recorded artifact", path)
		}
	}

	// Build chain: stage outputs first, then any remaining input roles (e.g. "task").
	for _, ms := range manifest.Stages {
		chain[ms.Output.Role] = roleArtifact{ref: ms.Output, content: rawBytes[ms.Output.Path]}
	}
	for _, ms := range manifest.Stages {
		for _, inp := range ms.Inputs {
			if _, ok := chain[inp.Role]; !ok {
				chain[inp.Role] = roleArtifact{ref: inp, content: rawBytes[inp.Path]}
			}
		}
	}

	completedStages := append([]runmanifest.Stage(nil), manifest.Stages...)
	gateAttempts := append([]runmanifest.GateAttempt(nil), manifest.Gates...)
	for _, stageIdx := range recoverable {
		stage := wf.Stages[stageIdx]
		captured, ok := latestStageForRole(completedStages, stage.Produces)
		if !ok {
			return fmt.Errorf("recover gate %q: captured output role %q not found", stage.Name, stage.Produces)
		}
		inputRefs := append([]runmanifest.ArtifactRef(nil), captured.Inputs...)
		runInputs := make([]replay.RunInput, 0, len(inputRefs))
		for _, input := range inputRefs {
			content, ok := rawBytes[input.Path]
			if !ok {
				return fmt.Errorf("recover gate %q: input artifact %q not rehydrated", stage.Name, input.Path)
			}
			runInputs = append(runInputs, replay.RunInput{Role: input.Role, MediaType: input.MediaType, Content: content})
		}
		outputContent, ok := rawBytes[captured.Output.Path]
		if !ok {
			return fmt.Errorf("recover gate %q: output artifact %q not rehydrated", stage.Name, captured.Output.Path)
		}

		oldArtifact := captured.Output.Artifact
		allAttempts, updatedStages, newCommit, finalRef, finalContent, gateErr := e.runGate(
			ctx, out, manifest.RunID, originalGitSHA, wt.Submodules, manifest.Created, wf,
			stage, stageIdx, inputRefs, runInputs, as, completedStages, gateAttempts, commit,
			captured.Output, outputContent, captured.Name, wt.Dir, scratch,
		)
		if gateErr != nil {
			return gateErr
		}
		gateAttempts = allAttempts
		completedStages = updatedStages
		commit = newCommit
		chain[stage.Produces] = roleArtifact{ref: finalRef, content: finalContent}

		if finalRef.Artifact != oldArtifact {
			if stageIdx+1 >= len(wf.Stages) {
				fmt.Fprintf(out, "ref %s%s\n", runsPrefix, manifest.RunID)
				return nil
			}
			return e.executeStages(ctx, out, wf, manifest.RunID, originalGitSHA, wt.Submodules, manifest.Created, as, chain,
				commit, stageIdx+1, completedStages, gateAttempts, true, wt.Dir, scratch)
		}
	}

	if frontier < len(wf.Stages) {
		return e.executeStages(ctx, out, wf, manifest.RunID, originalGitSHA, wt.Submodules, manifest.Created, as, chain,
			commit, frontier, completedStages, gateAttempts, false, wt.Dir, scratch)
	}
	fmt.Fprintf(out, "ref %s%s\n", runsPrefix, manifest.RunID)
	return nil
}

// executeStages runs wf.Stages[frontier:], accumulating CAS commits.
// preCompleted and preGates hold the already-committed history from a resume
// (nil for fresh runs).
func (e *Engine) executeStages(
	ctx context.Context,
	out io.Writer,
	wf workflow.Workflow,
	runID, originalGitSHA string,
	submodules map[string]string,
	created time.Time,
	as *artifactstore.Store,
	chain map[string]roleArtifact,
	prevCommit string,
	frontier int,
	preCompleted []runmanifest.Stage,
	preGates []runmanifest.GateAttempt,
	forceUniqueNames bool,
	worktreeDir, scratch string,
) error {
	completedStages := append([]runmanifest.Stage(nil), preCompleted...)

	// gateAttempts accumulates across all gates in this run so each
	// subsequent manifest write carries the full history.
	gateAttempts := append([]runmanifest.GateAttempt(nil), preGates...)

	for i, stage := range wf.Stages[frontier:] {
		stageIdx := frontier + i

		// Build inputs from chain.
		var inputRefs []runmanifest.ArtifactRef
		var runInputs []replay.RunInput
		for _, role := range stage.Inputs {
			if role == "repo-state" {
				continue // implicit worktree; not materialized or recorded as ArtifactRef
			}
			ra, ok := chain[role]
			if !ok {
				return &StageError{
					StageName: stage.Name,
					RunID:     runID,
					Err:       fmt.Errorf("input role %q not available in chain", role),
				}
			}
			inputRefs = append(inputRefs, ra.ref)
			runInputs = append(runInputs, replay.RunInput{
				Role:      role,
				MediaType: ra.ref.MediaType,
				Content:   ra.content,
			})
		}

		// Per-stage scratch subdir avoids output file collision between stages.
		stageScratch := fmt.Sprintf("%s/stage%02d", scratch, stageIdx)
		if err := os.MkdirAll(stageScratch, 0o755); err != nil {
			return &StageError{StageName: stage.Name, RunID: runID, Err: fmt.Errorf("mkdir stage scratch: %w", err)}
		}

		executionName := stage.Name
		if forceUniqueNames {
			executionName = fmt.Sprintf("%s.r%d", stage.Name, nextPhaseRound(stage.Name, completedStages, gateAttempts))
		}
		outputRef, outputContent, newStages, newCommit, err := e.runAndCaptureStage(
			ctx, out, runID, originalGitSHA, submodules, created, wf,
			stage, executionName, inputRefs, runInputs,
			stageScratch, as, completedStages, gateAttempts, prevCommit, worktreeDir,
		)
		if err != nil {
			return &StageError{StageName: stage.Name, RunID: runID, Err: err}
		}
		completedStages = newStages
		prevCommit = newCommit
		chain[stage.Produces] = roleArtifact{ref: outputRef, content: outputContent}

		// Execute the gate when configured.
		if stage.Gate != nil {
			allAttempts, updatedStages, newCommit2, finalOutputRef, finalOutputContent, gateErr := e.runGate(
				ctx, out, runID, originalGitSHA, submodules, created, wf,
				stage, stageIdx, inputRefs, runInputs,
				as, completedStages, gateAttempts, prevCommit,
				outputRef, outputContent, executionName, worktreeDir, scratch,
			)
			if gateErr != nil {
				return gateErr // GateEscalationError or infra error
			}
			gateAttempts = allAttempts
			completedStages = updatedStages
			prevCommit = newCommit2
			chain[stage.Produces] = roleArtifact{ref: finalOutputRef, content: finalOutputContent}
		}
	}

	fmt.Fprintf(out, "ref %s%s\n", runsPrefix, runID)
	return nil
}

// runAndCaptureStage executes a single stage run: resolves the runner,
// invokes it, stores the output artifact, appends the Stage record to
// completedStages, and writes an incremental CAS manifest commit.
//
// stageName may differ from stage.Name for gate-rerun stages (e.g. "plan.r2").
// scratchSubDir must be pre-created by the caller.
// gateAttempts are included in the manifest write for consistency.
//
// Returns the output ArtifactRef, output bytes, updated completedStages slice,
// new CAS commit OID, and any error.
func (e *Engine) runAndCaptureStage(
	ctx context.Context,
	out io.Writer,
	runID, originalGitSHA string,
	submodules map[string]string,
	created time.Time,
	wf workflow.Workflow,
	stage workflow.Stage,
	stageName string,
	inputRefs []runmanifest.ArtifactRef,
	runInputs []replay.RunInput,
	scratchSubDir string,
	as *artifactstore.Store,
	completedStages []runmanifest.Stage,
	gateAttempts []runmanifest.GateAttempt,
	prevCommit string,
	worktreeDir string,
) (outputRef runmanifest.ArtifactRef, outputContent []byte, newCompletedStages []runmanifest.Stage, newCommit string, returnErr error) {
	runner, err := e.ResolveRunner(stage)
	if err != nil {
		return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
	}

	stageSkill := runmanifest.Skill{
		ID:      stage.Skill,
		Repo:    "manual",
		Version: "manual",
	}
	producer := runmanifest.Producer{Skill: stageSkill}
	runnerWorkspace := wf.EffectiveRunnerWorkspace(stage)
	runnerDir := worktreeDir
	stageGitSHA := originalGitSHA
	manifestWorkspace := ""
	var callerControl []byte
	var callerIdentity callerRepositoryIdentity
	callerExpectedHEAD := ""
	if runnerWorkspace == workflow.RunnerWorkspaceCaller {
		runnerDir = e.callerDir()
		manifestWorkspace = workflow.RunnerWorkspaceCaller
		if err = ensureCallerRepository(ctx, e.Root, runnerDir); err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
		callerIdentity, err = captureCallerRepositoryIdentity(ctx, e.Root)
		if err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
		if err = inspectCallerWorkspaceClean(ctx, e.Root); err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
		callerExpectedHEAD = originalGitSHA
		for i := len(completedStages) - 1; i >= 0; i-- {
			if completedStages[i].RunnerWorkspace == workflow.RunnerWorkspaceCaller {
				callerExpectedHEAD = completedStages[i].GitSHA
				break
			}
		}
		resolve := e.ResolveCallerHEAD
		if resolve == nil {
			resolve = resolveCallerHEAD
		}
		actualHEAD, resolveErr := resolve(ctx, e.Root)
		if resolveErr != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, fmt.Errorf("capture caller workspace pre-run HEAD: %w", resolveErr)
		}
		if actualHEAD != callerExpectedHEAD {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit,
				fmt.Errorf("%w: expected %s, found %s", ErrCallerWorkspaceChanged, callerExpectedHEAD, actualHEAD)
		}
		if err = ensureCallerDescendsFrom(ctx, e.Root, originalGitSHA, callerExpectedHEAD); err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
		callerControl, err = callerGitControlSnapshot(ctx, e.Root)
		if err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
	}

	res, err := runner.Run(ctx, replay.RunRequest{
		WorktreeDir:     runnerDir,
		ScratchDir:      scratchSubDir,
		Inputs:          runInputs,
		OutputRole:      stage.Produces,
		OutputMediaType: "application/octet-stream",
		Producer:        producer,
	})
	if err != nil {
		return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
	}
	if runnerWorkspace == workflow.RunnerWorkspaceCaller {
		postIdentity, identityErr := captureCallerRepositoryIdentity(ctx, e.Root)
		if identityErr != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, identityErr
		}
		if !sameCallerRepositoryIdentity(callerIdentity, postIdentity) {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit,
				fmt.Errorf("%w: repository identity changed during runner", ErrCallerWorkspaceChanged)
		}
		if err := ensureCallerWorkspaceSupported(ctx, e.Root); err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
		postControl, controlErr := callerGitControlSnapshot(ctx, e.Root)
		if controlErr != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, controlErr
		}
		if !bytes.Equal(callerControl, postControl) {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit,
				fmt.Errorf("%w: repository Git control files changed during runner", ErrCallerWorkspaceDirty)
		}
		stageGitSHA, err = e.callerProvenance(ctx)
		if err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
		if err := ensureCallerDescendsFrom(ctx, e.Root, originalGitSHA, stageGitSHA); err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
		}
		if callerExpectedHEAD != originalGitSHA {
			if err := ensureCallerDescendsFrom(ctx, e.Root, callerExpectedHEAD, stageGitSHA); err != nil {
				return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, err
			}
		}
	}
	if len(res.Log) > replay.MaxStageLogBytes {
		return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, fmt.Errorf("runner log exceeds %d bytes", replay.MaxStageLogBytes)
	}

	// An explicit runner session is sufficient evidence even for inline
	// commands whose producer harness metadata is empty. Runners without a
	// session retain the deterministic/shell behavior.
	if res.Session != nil && strings.ToLower(strings.TrimSpace(res.Producer.Harness.Name)) != "shell" {
		if err := replay.ValidateSessionInfo(res.Session); err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, fmt.Errorf("runner session: %w", err)
		}
		sess := sessionInfoFields{
			SessionID:      res.Session.SessionID,
			TranscriptURI:  res.Session.TranscriptURI,
			TranscriptPath: res.Session.TranscriptPath,
		}
		evidence, note := buildSessionEvidence(as, stageName+"-transcript", scratchSubDir, runnerDir, sess, false)
		if evidence != nil {
			producer.Session = evidence
		}
		if note != "" {
			// Non-fatal: log the note but do not fail the stage.
			fmt.Fprintf(os.Stderr, "stage %s: session evidence note: %s\n", stageName, note)
		}
	}

	var logRef *runmanifest.ArtifactRef
	if len(res.Log) > 0 {
		logArtifact, err := as.AddContent(stageName+"-log", "application/octet-stream", res.Log)
		if err != nil {
			return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, fmt.Errorf("store log: %w", err)
		}
		ref := runmanifest.ArtifactFromManifestArtifact(logArtifact)
		logRef = &ref
	}

	outputMediaType := res.MediaType
	if outputMediaType == "" {
		outputMediaType = "application/octet-stream"
	}
	outputArtifact, err := as.AddContent(stage.Produces, outputMediaType, res.Output)
	if err != nil {
		return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, fmt.Errorf("store output: %w", err)
	}
	outRef := runmanifest.ArtifactFromManifestArtifact(outputArtifact)

	newStages := append(append([]runmanifest.Stage(nil), completedStages...), runmanifest.Stage{
		Name:            stageName,
		ProducedBy:      "original",
		GitSHA:          stageGitSHA,
		Submodules:      cloneStringMap(submodules),
		RunnerWorkspace: manifestWorkspace,
		Skill:           stageSkill,
		Producer:        producer,
		Inputs:          inputRefs,
		Output:          outRef,
		Log:             logRef,
		Timestamp:       e.clock(),
	})

	manifest := runmanifest.Manifest{
		RunID:           runID,
		Workflow:        wf.Name,
		WorkflowVersion: wf.Name + "-v1",
		Created:         created,
		Refs:            map[string]string{},
		OriginalGitSHA:  manifestOriginalGitSHA(newStages, originalGitSHA),
		Stages:          newStages,
		Gates:           gateAttempts,
		EnvAllowlist:    e.EnvAllowlist,
	}

	newCommit, err = runmanifest.WriteManifestTree(
		ctx, e.Store, runsPrefix, manifest,
		filesForManifest(manifest, as),
		refstore.WriteOptions{
			ExpectedOld: prevCommit,
			Message:     fmt.Sprintf("live run %s: stage %s", runID, stageName),
		},
	)
	if err != nil {
		return runmanifest.ArtifactRef{}, nil, completedStages, prevCommit, fmt.Errorf("write manifest: %w", err)
	}
	fmt.Fprintf(out, "captured %s\n", newCommit)
	return outRef, res.Output, newStages, newCommit, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func manifestOriginalGitSHA(stages []runmanifest.Stage, originalGitSHA string) string {
	for _, stage := range stages {
		if stage.RunnerWorkspace != "" {
			return originalGitSHA
		}
	}
	return ""
}

// filesForManifest returns only the artifact files referenced by the manifest.
// WriteManifestTree rejects any unreferenced files, so we must not pass extras.
func filesForManifest(manifest runmanifest.Manifest, as *artifactstore.Store) map[string][]byte {
	paths := runmanifest.ArtifactPaths(manifest)
	allFiles := as.Files()
	files := make(map[string][]byte, len(paths))
	for _, p := range paths {
		if content, ok := allFiles[p]; ok {
			files[p] = content
		}
	}
	return files
}

// inferTaskMediaType returns a media type for a task file based on its extension.
func inferTaskMediaType(filePath string) string {
	lower := strings.ToLower(filePath)
	switch {
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown; charset=utf-8"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// resolveHEAD returns the HEAD commit SHA of the git repository at root.
func resolveHEAD(ctx context.Context, root string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: ensure the repo has at least one commit or pass --git-sha")
	}
	return strings.TrimSpace(string(out)), nil
}
