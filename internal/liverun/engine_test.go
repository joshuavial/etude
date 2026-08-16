package liverun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.name", "Test User")
	gitRun(t, dir, "config", "user.email", "test@example.invalid")
	writeTestFile(t, dir, "README.md", "test\n")
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "initial")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	return gitRun(t, dir, "rev-parse", "HEAD")
}

func readLiveManifest(t *testing.T, repo, runID string) runmanifest.Manifest {
	t.Helper()
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/"+runID, "manifest.json")
	if err != nil {
		t.Fatalf("ReadFile manifest: %v", err)
	}
	m, err := runmanifest.ParseJSON(content)
	if err != nil {
		t.Fatalf("ParseJSON manifest: %v", err)
	}
	return m
}

func rewriteLiveManifest(t *testing.T, repo, runID string, mutate func(*runmanifest.Manifest)) {
	t.Helper()
	ctx := context.Background()
	store := refstore.New(repo)
	ref := runsPrefix + runID
	commit, err := store.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("resolve run: %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	files := make(map[string][]byte)
	for _, artifactPath := range runmanifest.ArtifactPaths(m) {
		files[artifactPath], err = store.ReadCommitFile(ctx, commit, artifactPath)
		if err != nil {
			t.Fatalf("read run artifact %q: %v", artifactPath, err)
		}
	}
	mutate(&m)
	if _, err := runmanifest.WriteManifestTree(ctx, store, runsPrefix, m, files, refstore.WriteOptions{
		ExpectedOld: commit,
		Message:     "rewrite live-run test manifest",
	}); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
}

// stubResolveRunner returns a ResolveRunner factory that always returns stub.
func stubResolveRunner(stub replay.Runner) func(workflow.Stage) (replay.Runner, error) {
	return func(workflow.Stage) (replay.Runner, error) { return stub, nil }
}

type runnerFunc func(context.Context, replay.RunRequest) (replay.RunResult, error)

func (f runnerFunc) Run(ctx context.Context, req replay.RunRequest) (replay.RunResult, error) {
	return f(ctx, req)
}

type checkRunnerFunc func(context.Context, replay.RunRequest) (bool, []byte, string)

func (f checkRunnerFunc) RunCheck(ctx context.Context, req replay.RunRequest) (bool, []byte, string) {
	return f(ctx, req)
}

// threeStageWorkflow returns a 3-stage workflow where each stage chains the previous.
func threeStageWorkflow() workflow.Workflow {
	return workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
			{Name: "stage-b", Skill: "sk", Produces: "diff", Inputs: []string{"task", "plan"}},
			{Name: "stage-c", Skill: "sk", Produces: "review", Inputs: []string{"diff"}},
		},
	}
}

// fixedClock returns a Now function that increments by 1 second each call.
func fixedClock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHermeticRunnerRemainsDefault(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
		if req.WorktreeDir == repo {
			t.Fatal("default runner executed in caller workspace")
		}
		return replay.RunResult{Output: []byte("output")}, nil
	})
	wf := workflow.Workflow{Name: "hermeticwf", Stages: []workflow.Stage{{
		Name: "plan", Skill: "sk", Produces: "plan",
		Runner: &workflow.Runner{Command: "unused"},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "hermeticwf-20260101T000000Z-aabbccdd"
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	if m.ManifestVersion != 2 {
		t.Fatalf("ManifestVersion = %d, want unchanged version 2", m.ManifestVersion)
	}
	if got := m.Stages[0].RunnerWorkspace; got != "" {
		t.Fatalf("RunnerWorkspace = %q, want omitted hermetic default", got)
	}
	if m.OriginalGitSHA != "" {
		t.Fatalf("OriginalGitSHA = %q, want omitted for hermetic v2", m.OriginalGitSHA)
	}
}

func TestCallerWorkspaceRunnerCommitsAndRecordsPostRunProvenance(t *testing.T) {
	repo := initTestRepo(t)
	callerDir := filepath.Join(repo, "nested")
	if err := os.Mkdir(callerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initialSHA := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
		if req.WorktreeDir != callerDir {
			t.Fatalf("WorktreeDir = %q, want caller directory %q", req.WorktreeDir, callerDir)
		}
		writeTestFile(t, repo, "README.md", "produced\n")
		gitRun(t, repo, "add", "README.md")
		gitRun(t, repo, "commit", "-m", "produce output")
		return replay.RunResult{Output: []byte("review this commit"), MediaType: "text/plain"}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, CallerDir: callerDir, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-aabbccdd"
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: initialSHA}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	postSHA := headSHA(t, repo)
	if postSHA == initialSHA {
		t.Fatal("runner did not create a new commit")
	}
	m := readLiveManifest(t, repo, runID)
	if m.ManifestVersion != 4 {
		t.Fatalf("ManifestVersion = %d, want 4", m.ManifestVersion)
	}
	if got := m.Stages[0].GitSHA; got != postSHA {
		t.Fatalf("stage git_sha = %q, want post-run HEAD %q", got, postSHA)
	}
	if got := m.OriginalGitSHA; got != initialSHA {
		t.Fatalf("original_git_sha = %q, want invocation HEAD %q", got, initialSHA)
	}
	if got := m.Stages[0].RunnerWorkspace; got != workflow.RunnerWorkspaceCaller {
		t.Fatalf("runner_workspace = %q, want caller", got)
	}
}

func TestCallerWorkspaceRecordsCleanHistoryRewrite(t *testing.T) {
	repo := initTestRepo(t)
	baseSHA := headSHA(t, repo)
	writeTestFile(t, repo, "README.md", "invocation commit\n")
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "invocation commit")
	invocationSHA := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		gitRun(t, repo, "reset", "--hard", "HEAD~1")
		writeTestFile(t, repo, "README.md", "replacement commit\n")
		gitRun(t, repo, "add", "README.md")
		gitRun(t, repo, "commit", "-m", "replacement commit")
		return replay.RunResult{Output: []byte("replacement output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-rewrite"
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: invocationSHA}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	postSHA := headSHA(t, repo)
	if got := gitRun(t, repo, "rev-parse", postSHA+"^"); got != baseSHA {
		t.Fatalf("rewritten commit parent = %q, want base %q", got, baseSHA)
	}
	if got := readLiveManifest(t, repo, runID).Stages[0].GitSHA; got != postSHA {
		t.Fatalf("stage git_sha = %q, want rewritten post-run HEAD %q", got, postSHA)
	}
}

func TestCallerWorkspaceMayCommitDeletionOfInvocationDirectory(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, repo, "nested/work.txt", "work\n")
	gitRun(t, repo, "add", "nested/work.txt")
	gitRun(t, repo, "commit", "-m", "add nested work")
	callerDir := filepath.Join(repo, "nested")
	initialSHA := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
		if req.WorktreeDir != callerDir {
			t.Fatalf("WorktreeDir = %q, want caller directory %q", req.WorktreeDir, callerDir)
		}
		gitRun(t, repo, "rm", "nested/work.txt")
		gitRun(t, repo, "commit", "-m", "remove invocation directory")
		return replay.RunResult{Output: []byte("review deletion")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{
		Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner),
		Root: repo, CallerDir: callerDir, Now: fixedClock(),
	}
	runID := "callerwf-20260101T000000Z-deletecwd"
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: initialSHA}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(callerDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("caller directory still exists or stat failed unexpectedly: %v", err)
	}
	if got := readLiveManifest(t, repo, runID).Stages[0].GitSHA; got != headSHA(t, repo) {
		t.Fatalf("stage git_sha = %q, want post-deletion HEAD", got)
	}
}

func TestCallerWorkspaceGateRemainsPinnedToOriginalWorktree(t *testing.T) {
	repo := initTestRepo(t)
	initialSHA := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		writeTestFile(t, repo, "README.md", "produced\n")
		gitRun(t, repo, "add", "README.md")
		gitRun(t, repo, "commit", "-m", "produce output")
		return replay.RunResult{Output: []byte("review this commit")}, nil
	})
	checkRan := false
	check := checkRunnerFunc(func(_ context.Context, req replay.RunRequest) (bool, []byte, string) {
		checkRan = true
		if req.WorktreeDir == repo {
			t.Fatal("gate check executed in caller workspace")
		}
		if got := headSHA(t, req.WorktreeDir); got != initialSHA {
			t.Fatalf("gate check HEAD = %q, want original run SHA %q", got, initialSHA)
		}
		return true, []byte("pass"), ""
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
		Gate:   &workflow.GateConfig{Checks: []workflow.Runner{{Command: "unused"}}},
	}}}
	e := Engine{
		Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock(),
		ResolveCheck: func(workflow.Runner) (CheckRunner, error) { return check, nil },
	}
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: "callerwf-20260101T000000Z-gatepin", GitSHA: initialSHA}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !checkRan {
		t.Fatal("gate check did not run")
	}
	m := readLiveManifest(t, repo, "callerwf-20260101T000000Z-gatepin")
	if m.ManifestVersion != 4 || len(m.Gates) != 1 {
		t.Fatalf("caller gated manifest version/gates = %d/%d, want 4/1", m.ManifestVersion, len(m.Gates))
	}
	if m.OriginalGitSHA != initialSHA {
		t.Fatalf("gate rewrite original_git_sha = %q, want %q", m.OriginalGitSHA, initialSHA)
	}
}

func TestCallerWorkspaceDirtyTreeFailsBeforeCapture(t *testing.T) {
	t.Setenv("GIT_LITERAL_PATHSPECS", "1")
	tests := []struct {
		name   string
		setup  func(*testing.T, string)
		mutate func(*testing.T, string)
	}{
		{"unstaged tracked", nil, func(t *testing.T, repo string) { writeTestFile(t, repo, "README.md", "dirty\n") }},
		{"staged tracked", nil, func(t *testing.T, repo string) {
			writeTestFile(t, repo, "README.md", "staged\n")
			gitRun(t, repo, "add", "README.md")
		}},
		{"untracked", nil, func(t *testing.T, repo string) {
			writeTestFile(t, repo, "new.go", "package new\n")
		}},
		{"untracked hidden by pre-run config", func(t *testing.T, repo string) {
			gitRun(t, repo, "config", "status.showUntrackedFiles", "no")
		}, func(t *testing.T, repo string) {
			writeTestFile(t, repo, "new.go", "package new\n")
		}},
		{"assume unchanged tracked", nil, func(t *testing.T, repo string) {
			gitRun(t, repo, "update-index", "--assume-unchanged", "README.md")
			writeTestFile(t, repo, "README.md", "hidden dirty\n")
		}},
		{"skip worktree tracked", nil, func(t *testing.T, repo string) {
			gitRun(t, repo, "update-index", "--skip-worktree", "README.md")
			writeTestFile(t, repo, "README.md", "hidden dirty\n")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := initTestRepo(t)
			if tc.setup != nil {
				tc.setup(t, repo)
			}
			sha := headSHA(t, repo)
			runner := runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
				if req.WorktreeDir != repo {
					t.Fatalf("WorktreeDir = %q, want caller root %q", req.WorktreeDir, repo)
				}
				tc.mutate(t, repo)
				return replay.RunResult{Output: []byte("stale output")}, nil
			})
			wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
				Name: "implement", Skill: "sk", Produces: "diff",
				Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
			}}}
			e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
			runID := "callerwf-20260101T000000Z-aabbccdd"
			err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
			if !errors.Is(err, ErrCallerWorkspaceDirty) {
				t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
			}
			if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
				t.Fatalf("stage was captured despite dirty caller tree: Resolve error = %v", resolveErr)
			}
		})
	}
}

func TestCallerWorkspaceRejectsDirtyTreeBeforeRunner(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	writeTestFile(t, repo, "README.md", "valuable uncommitted work\n")
	runnerCalled := false
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		runnerCalled = true
		gitRun(t, repo, "reset", "--hard", "HEAD")
		return replay.RunResult{Output: []byte("output after destroying work")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-preexistingdirty"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceDirty) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
	}
	if runnerCalled {
		t.Fatal("runner was invoked with a dirty caller workspace")
	}
	got, readErr := os.ReadFile(filepath.Join(repo, "README.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "valuable uncommitted work\n" {
		t.Fatalf("README.md = %q, want preexisting work preserved", got)
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured despite preexisting dirty caller tree: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceRejectsHiddenIndexStateBeforeRunner(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			repo := initTestRepo(t)
			sha := headSHA(t, repo)
			gitRun(t, repo, "update-index", flag, "README.md")
			writeTestFile(t, repo, "README.md", "valuable hidden work\n")
			runnerCalled := false
			runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
				runnerCalled = true
				gitRun(t, repo, "reset", "--hard", "HEAD")
				return replay.RunResult{Output: []byte("output after destroying hidden work")}, nil
			})
			wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
				Name: "implement", Skill: "sk", Produces: "diff",
				Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
			}}}
			e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
			runID := "callerwf-20260101T000000Z-prehidden" + strings.TrimPrefix(flag, "--")
			err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
			if !errors.Is(err, ErrCallerWorkspaceDirty) {
				t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
			}
			if runnerCalled {
				t.Fatal("runner was invoked with hidden caller workspace state")
			}
			got, readErr := os.ReadFile(filepath.Join(repo, "README.md"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != "valuable hidden work\n" {
				t.Fatalf("README.md = %q, want preexisting hidden work preserved", got)
			}
			if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
				t.Fatalf("stage was captured despite hidden caller state: Resolve error = %v", resolveErr)
			}
		})
	}
}

func TestCallerWorkspaceIgnoresIgnoredFiles(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, repo, ".gitignore", "ignored.log\n")
	gitRun(t, repo, "add", ".gitignore")
	gitRun(t, repo, "commit", "-m", "ignore runner log")
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		writeTestFile(t, repo, "ignored.log", "diagnostic\n")
		return replay.RunResult{Output: []byte("clean output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "verify", Skill: "sk", Produces: "verify",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: "callerwf-20260101T000000Z-aabbccdd", GitSHA: sha}); err != nil {
		t.Fatalf("Run with ignored output: %v", err)
	}
}

func TestCallerWorkspaceSubdirectoryDetectsIndexFlagsAcrossRepository(t *testing.T) {
	repo := initTestRepo(t)
	callerDir := filepath.Join(repo, "nested")
	if err := os.Mkdir(callerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		gitRun(t, repo, "update-index", "--assume-unchanged", "README.md")
		writeTestFile(t, repo, "README.md", "hidden outside caller directory\n")
		return replay.RunResult{Output: []byte("stale output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{
		Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner),
		Root: repo, CallerDir: callerDir, Now: fixedClock(),
	}
	runID := "callerwf-20260101T000000Z-subdirflag"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceDirty) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured despite hidden path outside caller directory: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceRejectsMismatchedCallerRepositoryBeforeRunner(t *testing.T) {
	repo := initTestRepo(t)
	callerRepo := initTestRepo(t)
	sha := headSHA(t, repo)
	runnerCalled := false
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		runnerCalled = true
		return replay.RunResult{Output: []byte("output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, CallerDir: callerRepo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-rootmismatch"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceUnsupported) || !strings.Contains(err.Error(), "does not match guarded root") {
		t.Fatalf("Run error = %v, want mismatched repository failure", err)
	}
	if runnerCalled {
		t.Fatal("runner was invoked for a caller directory from another repository")
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured despite caller/root mismatch: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceAmbientWorktreeCannotRedirectGuard(t *testing.T) {
	repo := initTestRepo(t)
	cleanMirror := t.TempDir()
	writeTestFile(t, cleanMirror, "README.md", "test\n")
	t.Setenv("GIT_WORK_TREE", cleanMirror)
	writeTestFile(t, repo, "README.md", "dirty\n")
	if err := inspectCallerWorkspaceClean(context.Background(), repo); !errors.Is(err, ErrCallerWorkspaceDirty) {
		t.Fatalf("inspectCallerWorkspaceClean = %v, want ErrCallerWorkspaceDirty", err)
	}
}

func TestCallerWorkspaceReplacementRefCannotFalsifyProvenance(t *testing.T) {
	repo := initTestRepo(t)
	originalSHA := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		writeTestFile(t, repo, "README.md", "replacement state\n")
		gitRun(t, repo, "add", "README.md")
		gitRun(t, repo, "commit", "-m", "replacement state")
		replacementSHA := headSHA(t, repo)
		gitRun(t, repo, "replace", originalSHA, replacementSHA)
		gitRun(t, repo, "reset", "--soft", originalSHA)
		if status := gitRun(t, repo, "status", "--porcelain=v1"); status != "" {
			t.Fatalf("test setup did not hide replacement state: %q", status)
		}
		return replay.RunResult{Output: []byte("stale output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-replacement"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: originalSHA})
	if !errors.Is(err, ErrCallerWorkspaceDirty) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured through replacement ref: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceRejectsTrackedSubmoduleBeforeRunner(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	submodule := initTestRepo(t)
	writeTestFile(t, submodule, "tracked.txt", "original\n")
	gitRun(t, submodule, "add", "tracked.txt")
	gitRun(t, submodule, "commit", "-m", "add tracked file")

	repo := initTestRepo(t)
	gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "deps/sub")
	gitRun(t, repo, "commit", "-m", "add submodule")
	sha := headSHA(t, repo)
	runnerCalled := false
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		runnerCalled = true
		return replay.RunResult{Output: []byte("output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-submodule"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceUnsupported) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceUnsupported", err)
	}
	if runnerCalled {
		t.Fatal("runner was invoked for an unsupported submodule repository")
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured despite unsupported submodule: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceRejectsTrackedSubmoduleAddedByRunner(t *testing.T) {
	submodule := initTestRepo(t)
	writeTestFile(t, submodule, "tracked.txt", "original\n")
	gitRun(t, submodule, "add", "tracked.txt")
	gitRun(t, submodule, "commit", "-m", "add tracked file")

	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "deps/sub")
		gitRun(t, repo, "commit", "-m", "add submodule")
		return replay.RunResult{Output: []byte("output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-addedsubmodule"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceUnsupported) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceUnsupported", err)
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured after runner added a submodule: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceChangingHEADFailsClosed(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	calls := 0
	e := Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("output")}),
		Root:          repo,
		Now:           fixedClock(),
		ResolveCallerHEAD: func(context.Context, string) (string, error) {
			calls++
			if calls == 1 {
				return sha, nil
			}
			if calls == 2 {
				return strings.Repeat("a", 40), nil
			}
			return strings.Repeat("b", 40), nil
		},
	}
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: "callerwf-20260101T000000Z-aabbccdd", GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceChanged) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceChanged", err)
	}
	if calls != 3 {
		t.Fatalf("ResolveCallerHEAD calls = %d, want 3", calls)
	}
}

func TestCallerWorkspaceRejectsFileModeMutationDespiteConfigChange(t *testing.T) {
	repo := initTestRepo(t)
	script := filepath.Join(repo, "tracked.sh")
	writeTestFile(t, repo, "tracked.sh", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "tracked.sh")
	gitRun(t, repo, "commit", "-m", "add executable")
	gitRun(t, repo, "config", "core.fileMode", "false")
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		if err := os.Chmod(script, 0o654); err != nil {
			t.Fatal(err)
		}
		return replay.RunResult{Output: []byte("stale output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-filemode"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceDirty) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
	}
	if strings.Contains(err.Error(), "Git control files changed") {
		t.Fatalf("Run error = %v, want file-mode detection rather than config drift", err)
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured after mode mutation: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceRejectsSameSizeRewriteDespiteStatConfig(t *testing.T) {
	repo := initTestRepo(t)
	tracked := filepath.Join(repo, "README.md")
	info, err := os.Stat(tracked)
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.trustctime", "false")
	gitRun(t, repo, "config", "core.checkStat", "minimal")
	gitRun(t, repo, "config", "core.ignoreStat", "true")
	gitRun(t, repo, "config", "core.fsmonitor", "true")
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		writeTestFile(t, repo, "README.md", "best\n")
		if err := os.Chtimes(tracked, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		return replay.RunResult{Output: []byte("stale output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-statconfig"
	err = e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceDirty) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
	}
	if strings.Contains(err.Error(), "Git control files changed") {
		t.Fatalf("Run error = %v, want content detection rather than config drift", err)
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured after same-size rewrite: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceRejectsStagedIndexOnlyMutation(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		cmd := exec.Command("git", "-C", repo, "hash-object", "-w", "--stdin")
		cmd.Stdin = strings.NewReader("staged-index-only\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("hash replacement blob: %v", err)
		}
		blob := strings.TrimSpace(string(out))
		gitRun(t, repo, "update-index", "--cacheinfo", "100644,"+blob+",README.md")
		worktreeOut, err := exec.Command("git", "-C", repo, "hash-object", "README.md").Output()
		if err != nil {
			t.Fatalf("hash working file: %v", err)
		}
		headOut, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD:README.md").Output()
		if err != nil {
			t.Fatalf("resolve HEAD blob: %v", err)
		}
		worktreeBlob := strings.TrimSpace(string(worktreeOut))
		headBlob := strings.TrimSpace(string(headOut))
		if worktreeBlob != headBlob {
			t.Fatalf("working file blob = %s, want HEAD blob %s", worktreeBlob, headBlob)
		}
		return replay.RunResult{Output: []byte("stale output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-indexonly"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceDirty) {
		t.Fatalf("Run error = %v, want ErrCallerWorkspaceDirty", err)
	}
	if err := exec.Command("git", "-C", repo, "diff", "--cached", "--quiet", "HEAD", "--", "README.md").Run(); err == nil {
		t.Fatal("test did not leave an index-only mutation")
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured after index-only mutation: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceRejectsGitInfoAttributesChange(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	gitRun(t, repo, "config", "filter.mask.clean", "sed s/best/test/")
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		writeTestFile(t, repo, ".git/info/attributes", "README.md filter=mask\n")
		writeTestFile(t, repo, "README.md", "best\n")
		return replay.RunResult{Output: []byte("stale output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-attributes"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceDirty) || !strings.Contains(err.Error(), "Git control files changed") {
		t.Fatalf("Run error = %v, want Git control-file mutation rejection", err)
	}
	if _, resolveErr := e.Store.Resolve(context.Background(), runsPrefix+runID); !errors.Is(resolveErr, refstore.ErrNotFound) {
		t.Fatalf("stage was captured after attributes mutation: Resolve error = %v", resolveErr)
	}
}

func TestCallerWorkspaceAllowsBranchConditionalConfigEffectChange(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	branchOut, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	branch := strings.TrimSpace(string(branchOut))
	writeTestFile(t, repo, ".git/main-branch.inc", "[qa]\n\tbranch = included\n")
	gitRun(t, repo, "config", "includeIf.onbranch:"+branch+".path", "main-branch.inc")
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		gitRun(t, repo, "switch", "-c", "caller-branch")
		if err := exec.Command("git", "-C", repo, "config", "--get", "qa.branch").Run(); err == nil {
			t.Fatal("branch-conditional configuration remained active after branch switch")
		}
		writeTestFile(t, repo, "README.md", "caller branch commit\n")
		gitRun(t, repo, "add", "README.md")
		gitRun(t, repo, "commit", "-m", "caller branch commit")
		return replay.RunResult{Output: []byte("clean output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-branchconfig"
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	manifest := readLiveManifest(t, repo, runID)
	if got, want := manifest.Stages[0].GitSHA, headSHA(t, repo); got != want || got == sha {
		t.Fatalf("caller stage git sha = %q, want clean branch commit %q distinct from %q", got, want, sha)
	}
}

func TestCallerWorkspaceRejectsRepositoryMetadataReplacement(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(repo, ".git-original")); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "init")
		return replay.RunResult{Output: []byte("redirected output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-replacedgit"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceChanged) || !strings.Contains(err.Error(), "repository identity changed") {
		t.Fatalf("Run error = %v, want repository identity rejection", err)
	}
}

func TestCallerWorkspaceRejectsBranchActivatedCleanFilter(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, repo, ".gitattributes", "README.md filter=mask\n")
	gitRun(t, repo, "add", ".gitattributes")
	gitRun(t, repo, "commit", "-m", "add filter attributes")
	sha := headSHA(t, repo)
	writeTestFile(t, repo, ".git/caller-branch.inc", "[filter \"mask\"]\n\tclean = sed s/best/test/\n")
	gitRun(t, repo, "config", "includeIf.onbranch:caller-filter.path", "caller-branch.inc")
	runner := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		gitRun(t, repo, "switch", "-c", "caller-filter")
		gitRun(t, repo, "commit", "--allow-empty", "-m", "activate branch filter")
		writeTestFile(t, repo, "README.md", "best\n")
		statusOut, err := exec.Command("git", "-C", repo, "status", "--porcelain=v1").Output()
		if err != nil {
			t.Fatal(err)
		}
		if status := strings.TrimSpace(string(statusOut)); status != "" {
			t.Fatalf("crafted filtered mutation is visible to ordinary Git status: %q", status)
		}
		return replay.RunResult{Output: []byte("stale filtered output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: repo, Now: fixedClock()}
	runID := "callerwf-20260101T000000Z-branchfilter"
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: runID, GitSHA: sha})
	if !errors.Is(err, ErrCallerWorkspaceDirty) || strings.Contains(err.Error(), "control files changed") {
		t.Fatalf("Run error = %v, want raw tracked-byte rejection", err)
	}
}

func TestCallerWorkspaceRelativeRootUsesLibraryFallback(t *testing.T) {
	repo := initTestRepo(t)
	t.Chdir(repo)
	sha := headSHA(t, repo)
	runnerCalled := false
	runner := runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
		runnerCalled = true
		if req.WorktreeDir != "." {
			t.Fatalf("caller runner dir = %q, want relative fallback root", req.WorktreeDir)
		}
		return replay.RunResult{Output: []byte("output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "implement", Skill: "sk", Produces: "diff",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(runner), Root: ".", Now: fixedClock()}
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{RunID: "callerwf-20260101T000000Z-relroot", GitSHA: sha}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !runnerCalled {
		t.Fatal("caller runner did not execute")
	}
}

func TestCallerWorkspaceResumeRequiresPriorCallerHead(t *testing.T) {
	repo := initTestRepo(t)
	originalSHA := headSHA(t, repo)
	runID := "callerwf-20260101T000000Z-lineage"
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{
		{Name: "first", Skill: "sk", Produces: "plan", Inputs: []string{"task"}, Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller}},
		{Name: "second", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}, Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller}},
	}}
	e := Engine{
		Store: refstore.New(repo), Root: repo, Now: fixedClock(),
		ResolveRunner: func(stage workflow.Stage) (replay.Runner, error) {
			if stage.Name == "first" {
				return runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
					writeTestFile(t, repo, "README.md", "first caller commit\n")
					gitRun(t, repo, "add", "README.md")
					gitRun(t, repo, "commit", "-m", "first caller commit")
					return replay.RunResult{Output: []byte("first")}, nil
				}), nil
			}
			return &replay.StubRunner{Err: errors.New("stop after first caller")}, nil
		},
	}
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: originalSHA}); err == nil {
		t.Fatal("initial run succeeded, want second-stage failure")
	}
	firstCallerSHA := readLiveManifest(t, repo, runID).Stages[0].GitSHA
	if firstCallerSHA == originalSHA {
		t.Fatal("first caller stage did not advance HEAD")
	}
	gitRun(t, repo, "reset", "--hard", originalSHA)

	runnerCalls := 0
	e.ResolveRunner = func(workflow.Stage) (replay.Runner, error) {
		return runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
			runnerCalls++
			return replay.RunResult{Output: []byte("must not run")}, nil
		}), nil
	}
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{ResumeID: runID})
	if !errors.Is(err, ErrCallerWorkspaceChanged) || !strings.Contains(err.Error(), firstCallerSHA) {
		t.Fatalf("resume error = %v, want prior caller HEAD mismatch", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("second caller runner calls = %d, want 0", runnerCalls)
	}
}

func TestResumeHermeticStageDoesNotRequireCompletedCallerCommit(t *testing.T) {
	repo := initTestRepo(t)
	originalSHA := headSHA(t, repo)
	runID := "callerwf-20260101T000000Z-missingcommit"
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{
		{Name: "produce", Skill: "sk", Produces: "plan", Inputs: []string{"task"}, Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller}},
		{Name: "verify", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
	}}
	e := Engine{
		Store: refstore.New(repo), Root: repo, Now: fixedClock(),
		ResolveRunner: func(stage workflow.Stage) (replay.Runner, error) {
			if stage.Name == "verify" {
				return &replay.StubRunner{Err: errors.New("stop after caller")}, nil
			}
			return &replay.StubRunner{CannedOutput: []byte("caller")}, nil
		},
	}
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: originalSHA}); err == nil {
		t.Fatal("initial run succeeded, want verify failure")
	}
	rewriteLiveManifest(t, repo, runID, func(m *runmanifest.Manifest) {
		m.Stages[0].GitSHA = strings.Repeat("b", 40)
	})
	runnerCalls := 0
	e.ResolveRunner = func(workflow.Stage) (replay.Runner, error) {
		runnerCalls++
		return &replay.StubRunner{CannedOutput: []byte("resumed at original checkout")}, nil
	}
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{ResumeID: runID}); err != nil {
		t.Fatalf("resume hermetic stage with pruned caller provenance: %v", err)
	}
	if runnerCalls != 1 {
		t.Fatalf("ResolveRunner calls = %d, want 1", runnerCalls)
	}
}

// AC1: 3-stage deterministic workflow run writes a growing manifest chain.
func TestEngineRunThreeStages(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	stub := &replay.StubRunner{CannedOutput: []byte("output"), CannedMediaType: "text/plain; charset=utf-8"}
	wf := threeStageWorkflow()

	var out bytes.Buffer
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}
	err := e.Run(context.Background(), &out, wf, RunOptions{
		TaskBytes: []byte("my task"),
		TaskFile:  "task.txt",
		RunID:     "mywf-20260101T000000Z-aabbccdd",
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify output contains captured lines and final ref.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 output lines (3 captured + 1 ref), got %d:\n%s", len(lines), out.String())
	}
	for i := 0; i < 3; i++ {
		if !strings.HasPrefix(lines[i], "captured ") {
			t.Errorf("line %d = %q, want 'captured <oid>'", i, lines[i])
		}
	}
	if !strings.Contains(lines[3], "refs/etude/runs/mywf-20260101T000000Z-aabbccdd") {
		t.Errorf("line 3 = %q, want ref line", lines[3])
	}

	// Verify manifest has 3 stages with correct roles.
	m := readLiveManifest(t, repo, "mywf-20260101T000000Z-aabbccdd")
	if len(m.Stages) != 3 {
		t.Fatalf("stages = %d, want 3", len(m.Stages))
	}
	wantRoles := []string{"plan", "diff", "review"}
	for i, s := range m.Stages {
		if s.Output.Role != wantRoles[i] {
			t.Errorf("stage[%d].output.role = %q, want %q", i, s.Output.Role, wantRoles[i])
		}
		if s.ProducedBy != "original" {
			t.Errorf("stage[%d].produced_by = %q, want original", i, s.ProducedBy)
		}
		if s.GitSHA != sha {
			t.Errorf("stage[%d].git_sha = %q, want %q", i, s.GitSHA, sha)
		}
	}
}

func TestEngineRunCheckReadsPinnedSubmoduleAndManifestRecordsSHA(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	submodule := initTestRepo(t)
	writeTestFile(t, submodule, "payload.txt", "pinned-content\n")
	gitRun(t, submodule, "add", "payload.txt")
	gitRun(t, submodule, "commit", "-m", "pinned payload")
	pinnedSubmoduleOID := headSHA(t, submodule)

	repo := initTestRepo(t)
	gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "modules/lib")
	checkScript := "#!/bin/sh\nset -eu\nactual=$(cat modules/lib/payload.txt)\nprintf '%s\\n' \"$actual\"\ntest \"$actual\" = pinned-content\nif test ! -f .check-rerun-seen; then touch .check-rerun-seen; exit 1; fi\n"
	writeTestFile(t, repo, "check-submodule.sh", checkScript)
	if err := os.Chmod(filepath.Join(repo, "check-submodule.sh"), 0o755); err != nil {
		t.Fatalf("chmod check script: %v", err)
	}
	gitRun(t, repo, "add", ".gitmodules", "modules/lib", "check-submodule.sh")
	gitRun(t, repo, "commit", "-m", "pin submodule and check")
	superprojectOID := headSHA(t, repo)

	// Advance the source after the superproject pin. A correct run must still
	// expose the older content selected by the recorded gitlink.
	writeTestFile(t, submodule, "payload.txt", "newer-source-content\n")
	gitRun(t, submodule, "add", "payload.txt")
	gitRun(t, submodule, "commit", "-m", "newer payload")

	wf := workflow.Workflow{
		Name: "submodule-check",
		Stages: []workflow.Stage{
			{
				Name:     "verify",
				Skill:    "sk",
				Produces: "plan",
				Inputs:   []string{"task"},
				Gate: &workflow.GateConfig{
					Checks:    []workflow.Runner{{Command: "./check-submodule.sh"}},
					MaxRounds: maxRoundsPtr(2),
				},
			},
			{Name: "review", Skill: "sk", Produces: "review", Inputs: []string{"plan"}},
		},
	}
	runID := "submodule-check-20260101T000000Z-aabbccdd"
	store := refstore.New(repo)
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("stage output"), CannedMediaType: "text/plain"}),
		ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
			return &execCheckRunner{command: strings.Fields(r.Command), timeout: 10 * time.Second}, nil
		},
		Root: repo,
		Now:  fixedClock(),
	}
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: superprojectOID,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	manifest := readLiveManifest(t, repo, runID)
	if len(manifest.Stages) != 3 {
		t.Fatalf("stages = %d, want original, gate rerun, and review", len(manifest.Stages))
	}
	for i, stage := range manifest.Stages {
		if stage.GitSHA != superprojectOID {
			t.Errorf("stage[%d].git_sha = %q, want %q", i, stage.GitSHA, superprojectOID)
		}
		if got := stage.Submodules["modules/lib"]; got != pinnedSubmoduleOID {
			t.Errorf("stage[%d].submodules[modules/lib] = %q, want %q", i, got, pinnedSubmoduleOID)
		}
	}
	if len(manifest.Gates) != 2 || len(manifest.Gates[1].Seats) != 1 {
		t.Fatalf("gate check record missing: %+v", manifest.Gates)
	}
	if manifest.Gates[0].Status != runmanifest.GateStatusRerun || manifest.Gates[1].Status != runmanifest.GateStatusPass {
		t.Fatalf("gate statuses = %q, %q, want rerun then pass", manifest.Gates[0].Status, manifest.Gates[1].Status)
	}
	rawRef := manifest.Gates[1].Seats[0].RawOutput
	if rawRef == nil {
		t.Fatal("check raw output was not recorded")
	}
	raw, err := store.ReadFile(context.Background(), "refs/etude/runs/"+runID, rawRef.Path)
	if err != nil {
		t.Fatalf("read check raw output: %v", err)
	}
	if string(raw) != "pinned-content\n" {
		t.Fatalf("check raw output = %q, want pinned content", raw)
	}
}

// AC4: Stage B's input ArtifactRef equals Stage A's output ArtifactRef.
func TestEngineArtifactRefChaining(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	stub := &replay.StubRunner{CannedOutput: []byte("chained-output"), CannedMediaType: "application/octet-stream"}
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
			{Name: "stage-b", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
		},
	}

	var out bytes.Buffer
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}
	if err := e.Run(context.Background(), &out, wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     "mywf-20260101T000000Z-aabbccdd",
		GitSHA:    sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, "mywf-20260101T000000Z-aabbccdd")
	if len(m.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(m.Stages))
	}

	// AC4: stage-b's first input ref must match stage-a's output ref.
	aOutput := m.Stages[0].Output
	bInput := m.Stages[1].Inputs[0]
	if bInput.Artifact != aOutput.Artifact {
		t.Errorf("stage-b input artifact %q != stage-a output artifact %q", bInput.Artifact, aOutput.Artifact)
	}
	if bInput.Path != aOutput.Path {
		t.Errorf("stage-b input path %q != stage-a output path %q", bInput.Path, aOutput.Path)
	}
	if bInput.Role != "plan" {
		t.Errorf("stage-b input role = %q, want plan", bInput.Role)
	}
}

// AC3: Stop-and-capture on failure + resume completes the run.
func TestEngineResumeAfterFailure(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	repo := initTestRepo(t)
	submodule := initTestRepo(t)
	gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "modules/lib")
	gitRun(t, repo, "commit", "-am", "add submodule")
	submoduleOID := headSHA(t, submodule)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	// Stage-a succeeds; stage-b fails.
	runID := "mywf-20260101T000000Z-aabbccdd"
	wf := threeStageWorkflow()

	callCount := 0
	failRunner := func(stage workflow.Stage) (replay.Runner, error) {
		callCount++
		if stage.Name == "stage-b" {
			return &replay.StubRunner{Err: errors.New("stage-b error")}, nil
		}
		return &replay.StubRunner{CannedOutput: []byte("ok"), CannedMediaType: "application/octet-stream"}, nil
	}

	var out bytes.Buffer
	e := Engine{
		Store:         store,
		ResolveRunner: failRunner,
		Root:          repo,
		Now:           fixedClock(),
	}
	err := e.Run(context.Background(), &out, wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})

	// Verify failure is reported as StageError.
	var stageErr *StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("expected StageError, got: %v", err)
	}
	if stageErr.StageName != "stage-b" {
		t.Errorf("StageError.StageName = %q, want stage-b", stageErr.StageName)
	}
	if stageErr.RunID != runID {
		t.Errorf("StageError.RunID = %q, want %q", stageErr.RunID, runID)
	}

	// Partial manifest must have stage-a only.
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 {
		t.Fatalf("partial manifest stages = %d, want 1", len(m.Stages))
	}
	if m.Stages[0].Name != "stage-a" {
		t.Errorf("partial manifest stage[0].name = %q, want stage-a", m.Stages[0].Name)
	}

	// A gate attempt can add artifacts after the last completed stage. Resume
	// must preserve those artifacts even though they are not stage inputs or
	// outputs.
	ctx := context.Background()
	partialCommit, err := store.Resolve(ctx, runsPrefix+runID)
	if err != nil {
		t.Fatalf("resolve partial run: %v", err)
	}
	files := make(map[string][]byte)
	for _, path := range runmanifest.ArtifactPaths(m) {
		files[path], err = store.ReadCommitFile(ctx, partialCommit, path)
		if err != nil {
			t.Fatalf("read partial artifact %q: %v", path, err)
		}
	}
	gateStore := artifactstore.New()
	rawArtifact, err := gateStore.AddContent("check-0", "application/octet-stream", []byte("check output"))
	if err != nil {
		t.Fatalf("store gate raw output: %v", err)
	}
	gateTranscript, err := gateStore.AddContent("seat-transcript", "application/json", []byte(`{"seat":"check.0"}`))
	if err != nil {
		t.Fatalf("store gate transcript: %v", err)
	}
	stageTranscript, err := gateStore.AddContent("stage-transcript", "application/json", []byte(`{"stage":"stage-a"}`))
	if err != nil {
		t.Fatalf("store stage transcript: %v", err)
	}
	for path, content := range gateStore.Files() {
		files[path] = content
	}
	rawRef := runmanifest.ArtifactFromManifestArtifact(rawArtifact)
	gateTranscriptRef := runmanifest.ArtifactFromManifestArtifact(gateTranscript)
	stageTranscriptRef := runmanifest.ArtifactFromManifestArtifact(stageTranscript)
	m.Stages[0].Producer.Session = &runmanifest.SessionEvidence{
		SessionID:          "stage-session",
		TranscriptArtifact: &stageTranscriptRef,
		RetrievalStatus:    runmanifest.SessionEvidenceRetrievalImported,
		RedactionStatus:    runmanifest.SessionEvidenceRedactionPassed,
	}
	m.Gates = append(m.Gates, runmanifest.GateAttempt{
		GateID: "stage-a.r1",
		Phase:  "stage-a",
		Round:  1,
		Tier:   1,
		Status: runmanifest.GateStatusEscalated,
		ReviewedStages: []runmanifest.ReviewedRef{{
			Stage: "stage-a", Artifact: m.Stages[0].Output.Artifact, Role: m.Stages[0].Output.Role,
		}},
		Seats: []runmanifest.SeatResult{{
			Seat:      "check.0",
			Harness:   runmanifest.Harness{Name: "exec"},
			Provider:  runmanifest.Provider{Name: "deterministic", Model: "check"},
			Verdict:   runmanifest.SeatVerdictGo,
			RawOutput: &rawRef,
			Session: &runmanifest.SessionEvidence{
				SessionID:          "seat-session",
				TranscriptArtifact: &gateTranscriptRef,
				RetrievalStatus:    runmanifest.SessionEvidenceRetrievalImported,
				RedactionStatus:    runmanifest.SessionEvidenceRedactionPassed,
			},
			Timestamp: time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC),
		}},
		Decision:  runmanifest.GateDecision{EscalationReason: "insufficient usable seats"},
		Timestamp: time.Date(2026, 1, 1, 0, 0, 4, 0, time.UTC),
	})
	if _, err := runmanifest.WriteManifestTree(ctx, store, runsPrefix, m, files, refstore.WriteOptions{
		ExpectedOld: partialCommit,
		Message:     "capture gate output",
	}); err != nil {
		t.Fatalf("write partial run with gate output: %v", err)
	}

	// Now resume: stage-b and stage-c succeed.
	successRunner := func(stage workflow.Stage) (replay.Runner, error) {
		return &replay.StubRunner{CannedOutput: []byte("resumed"), CannedMediaType: "application/octet-stream"}, nil
	}
	e.ResolveRunner = successRunner
	e.Now = fixedClock()

	var out2 bytes.Buffer
	err = e.Run(context.Background(), &out2, wf, RunOptions{ResumeID: runID})
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}

	// Final manifest must have all 3 stages.
	m = readLiveManifest(t, repo, runID)
	if len(m.Stages) != 3 {
		t.Fatalf("final manifest stages = %d, want 3", len(m.Stages))
	}
	for i, stage := range m.Stages {
		if got := stage.Submodules["modules/lib"]; got != submoduleOID {
			t.Errorf("resumed stage[%d] submodule OID = %q, want %q", i, got, submoduleOID)
		}
	}

	// Explicit reseed byte-presence: the resumed stage-b CAS append could only
	// succeed if the task input blob AND stage-a's output blob were reseeded with
	// correct bytes from the partial run commit (WriteManifestTree rejects any
	// referenced-but-missing artifact). Assert both blobs are byte-present in the
	// final run commit, not merely referenced.
	rs := refstore.New(repo)
	taskPath := ""
	for _, in := range m.Stages[0].Inputs {
		if in.Role == "task" {
			taskPath = in.Path
		}
	}
	if taskPath == "" {
		t.Fatal("stage-a has no task input role in final manifest")
	}
	for _, p := range []string{
		taskPath,
		m.Stages[0].Output.Path,
		rawRef.Path,
		gateTranscriptRef.Path,
		stageTranscriptRef.Path,
	} {
		b, err := rs.ReadFile(context.Background(), "refs/etude/runs/"+runID, p)
		if err != nil {
			t.Fatalf("reseeded blob %q not present in resumed run commit: %v", p, err)
		}
		if len(b) == 0 {
			t.Errorf("reseeded blob %q is empty", p)
		}
	}
}

func TestEngineResumeSubmoduleMismatchDoesNotResolveRunner(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	repo := initTestRepo(t)
	submodule := initTestRepo(t)
	gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "modules/lib")
	gitRun(t, repo, "commit", "-am", "add submodule")

	const runID = "mywf-20260101T000000Z-submodule-mismatch"
	wf := threeStageWorkflow()
	e := Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(stage workflow.Stage) (replay.Runner, error) {
			if stage.Name == "stage-b" {
				return &replay.StubRunner{Err: errors.New("stop after stage-a")}, nil
			}
			return &replay.StubRunner{CannedOutput: []byte("ok")}, nil
		},
		Root: repo,
		Now:  fixedClock(),
	}
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: headSHA(t, repo),
	}); err == nil {
		t.Fatal("initial run succeeded, want stage-b failure")
	}
	rewriteLiveManifest(t, repo, runID, func(m *runmanifest.Manifest) {
		m.Stages[0].Submodules = map[string]string{"modules/lib": strings.Repeat("a", 40)}
	})

	resolveCalls := 0
	e.ResolveRunner = func(workflow.Stage) (replay.Runner, error) {
		resolveCalls++
		return &replay.StubRunner{CannedOutput: []byte("unexpected")}, nil
	}
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{ResumeID: runID})
	if err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("resume error = %v, want submodule mismatch", err)
	}
	if resolveCalls != 0 {
		t.Fatalf("ResolveRunner calls = %d, want 0", resolveCalls)
	}
}

func TestEngineResumeAfterCallerCommitUsesOriginalCheckout(t *testing.T) {
	repo := initTestRepo(t)
	originalSHA := headSHA(t, repo)
	postRunSHA := ""
	runID := "callerwf-20260101T000000Z-resumepin"
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{
		{
			Name: "produce", Skill: "sk", Produces: "plan", Inputs: []string{"task"},
			Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
		},
		{Name: "verify", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
	}}

	callerRunner := runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
		if req.WorktreeDir != repo {
			t.Fatalf("caller runner dir = %q, want %q", req.WorktreeDir, repo)
		}
		writeTestFile(t, repo, "README.md", "caller commit\n")
		gitRun(t, repo, "add", "README.md")
		gitRun(t, repo, "commit", "-m", "caller commit")
		postRunSHA = headSHA(t, repo)
		return replay.RunResult{Output: []byte("caller output")}, nil
	})

	e := Engine{
		Store: refstore.New(repo), Root: repo, Now: fixedClock(),
		ResolveRunner: func(stage workflow.Stage) (replay.Runner, error) {
			if stage.Name == "produce" {
				return callerRunner, nil
			}
			return &replay.StubRunner{Err: errors.New("fail once")}, nil
		},
	}
	err := e.Run(context.Background(), io.Discard, wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: originalSHA,
	})
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.StageName != "verify" {
		t.Fatalf("initial run error = %v, want verify StageError", err)
	}
	if postRunSHA == "" || postRunSHA == originalSHA {
		t.Fatalf("caller post-run SHA = %q, want commit after %q", postRunSHA, originalSHA)
	}
	partial := readLiveManifest(t, repo, runID)
	if partial.OriginalGitSHA != originalSHA || partial.Stages[0].GitSHA != postRunSHA {
		t.Fatalf("partial provenance original/stage = %q/%q, want %q/%q", partial.OriginalGitSHA, partial.Stages[0].GitSHA, originalSHA, postRunSHA)
	}

	resumed := false
	e.ResolveRunner = func(stage workflow.Stage) (replay.Runner, error) {
		if stage.Name != "verify" {
			t.Fatalf("resume reran completed caller stage %q", stage.Name)
		}
		return runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
			resumed = true
			got := headSHA(t, req.WorktreeDir)
			if got != originalSHA {
				t.Fatalf("resumed hermetic HEAD = %q, want original %q (caller post-run %q)", got, originalSHA, postRunSHA)
			}
			return replay.RunResult{Output: []byte("resumed")}, nil
		}), nil
	}
	if err := e.Run(context.Background(), io.Discard, wf, RunOptions{ResumeID: runID}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed {
		t.Fatal("resume runner did not execute")
	}
}

// blockingRunner signals `started` when its Run begins and blocks until
// `release` is closed — lets a test inspect the run ref mid-execution.
type blockingRunner struct {
	output  []byte
	started chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, req replay.RunRequest) (replay.RunResult, error) {
	if r.started != nil {
		close(r.started)
	}
	if r.release != nil {
		<-r.release
	}
	return replay.RunResult{Output: r.output, MediaType: "application/octet-stream", Producer: req.Producer}, nil
}

// AC1 (incl. mid-run): while a later stage is still executing, the run ref is a
// valid snapshot inspectable by `run show` and lists already-captured stages.
func TestEngineMidRunInspectable(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)
	wf := threeStageWorkflow() // stage-a(plan) -> stage-b(diff) -> stage-c(review)
	runID := "mywf-20260101T000000Z-midrun01"

	started := make(chan struct{})
	release := make(chan struct{})
	resolve := func(stage workflow.Stage) (replay.Runner, error) {
		if stage.Name == "stage-b" {
			return &blockingRunner{output: []byte("b-out"), started: started, release: release}, nil
		}
		return &replay.StubRunner{CannedOutput: []byte("out"), CannedMediaType: "application/octet-stream"}, nil
	}
	e := Engine{Store: store, ResolveRunner: resolve, Root: repo, Now: fixedClock()}

	done := make(chan error, 1)
	go func() {
		done <- e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{
			TaskBytes: []byte("t"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
	}()

	// stage-b is now executing; stage-a has been CAS-captured.
	<-started
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 || m.Stages[0].Name != "stage-a" {
		close(release)
		<-done
		t.Fatalf("mid-run manifest = %d stages, want exactly [stage-a]", len(m.Stages))
	}

	close(release) // let stage-b + stage-c finish
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	m = readLiveManifest(t, repo, runID)
	if len(m.Stages) != 3 {
		t.Fatalf("final stages = %d, want 3", len(m.Stages))
	}
}

// TestEngineInvalidExplicitRunID: an explicit --run-id override must be validated
// via runmanifest.IsValidRunID before any git ref path is touched (gate round-1
// BLOCK: prevents path traversal / .lock / bad-charset ids reaching the ref).
func TestEngineInvalidExplicitRunID(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)
	stub := &replay.StubRunner{CannedOutput: []byte("ok"), CannedMediaType: "application/octet-stream"}
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}
	for _, bad := range []string{"../evil", "bad/id", "x.lock", "has space", ".hidden"} {
		err := e.Run(context.Background(), &bytes.Buffer{}, threeStageWorkflow(), RunOptions{
			TaskBytes: []byte("t"),
			TaskFile:  "task.txt",
			RunID:     bad,
			GitSHA:    sha,
		})
		if err == nil || !strings.Contains(err.Error(), "invalid run id") {
			t.Errorf("run id %q: expected 'invalid run id' error, got: %v", bad, err)
		}
		// No ref must have been created for the rejected id.
		if _, rerr := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/"+bad, "manifest.json"); rerr == nil {
			t.Errorf("run id %q: a ref was created despite validation failure", bad)
		}
	}
}

// TestEngineReservedNamesPreventedAtCLILevel verifies DeriveFrontier handles
// already-complete runs (no "etude run" execution needed here; the guard is CLI-level).
func TestEngineAlreadyCompleteResumeErrors(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	runID := "mywf-20260101T000000Z-aabbccdd"
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
		},
	}

	stub := &replay.StubRunner{CannedOutput: []byte("ok"), CannedMediaType: "application/octet-stream"}
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}

	// Complete the run.
	if err := e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Resume of complete run must error.
	err := e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{ResumeID: runID})
	if err == nil || !strings.Contains(err.Error(), "already complete") {
		t.Errorf("expected 'already complete' error, got: %v", err)
	}
}

func TestEngineResumeRetriesZeroSeatEscalationWithoutRerunningProducer(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runID := "mywf-20260101T000000Z-zero-seat"
	one := 1
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})

	producerCalls := 0
	e := Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			producerCalls++
			return &replay.StubRunner{
				CannedOutput:    []byte("captured plan"),
				CannedMediaType: "text/plain; charset=utf-8",
			}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return &replay.StubRunner{CannedMediaType: "application/json"}, SeatMeta{
				HarnessName: "stub", ProviderName: "stub", Model: "stub",
			}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"reviewer"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}

	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	})
	var gateErr *GateEscalationError
	if !errors.As(err, &gateErr) {
		t.Fatalf("first run: expected GateEscalationError, got %v", err)
	}

	e.ResolveSeat = func(string) (replay.Runner, SeatMeta, error) {
		return &replay.StubRunner{
			CannedOutput: envelopeJSON("go", nil), CannedMediaType: "application/json",
		}, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
	}

	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{ResumeID: runID}); err != nil {
		t.Fatalf("resume zero-seat escalation: %v", err)
	}

	m := readLiveManifest(t, repo, runID)
	if producerCalls != 1 {
		t.Fatalf("producer calls = %d, want 1", producerCalls)
	}
	if len(m.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(m.Stages))
	}
	if len(m.Gates) != 2 {
		t.Fatalf("gates = %d, want 2", len(m.Gates))
	}
	if m.Gates[1].GateID != "plan.r2" || m.Gates[1].Status != runmanifest.GateStatusPass {
		t.Fatalf("resumed gate = %s status=%s, want plan.r2 pass", m.Gates[1].GateID, m.Gates[1].Status)
	}
}

func TestEngineResumeAppendsRepeatedZeroSeatEscalations(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runID := "mywf-20260101T000000Z-repeat-outage"
	one := 1
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
	producerCalls := 0
	e := Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			producerCalls++
			return &replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain"}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return &replay.StubRunner{CannedMediaType: "application/json"}, SeatMeta{
				HarnessName: "stub", ProviderName: "stub", Model: "stub",
			}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L1": {[]string{"reviewer"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	for attempt := 1; attempt <= 2; attempt++ {
		opts := RunOptions{ResumeID: runID}
		if attempt == 1 {
			opts = RunOptions{TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha}
		}
		var gateErr *GateEscalationError
		if err := e.Run(context.Background(), noopWriter(), wf, opts); !errors.As(err, &gateErr) {
			t.Fatalf("attempt %d: expected GateEscalationError, got %v", attempt, err)
		}
	}

	m := readLiveManifest(t, repo, runID)
	if producerCalls != 1 {
		t.Fatalf("producer calls = %d, want 1", producerCalls)
	}
	if len(m.Gates) != 2 || m.Gates[1].GateID != "plan.r2" {
		t.Fatalf("gate history = %#v, want appended plan.r2", m.Gates)
	}
}

func TestEngineResumeDoesNotRetryPhaseWithSubstantiveVerdict(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runID := "mywf-20260101T000000Z-substantive"
	two := 2
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &two})
	seatCalls := 0
	producerCalls := 0
	e := Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			producerCalls++
			return &replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain"}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			seatCalls++
			if seatCalls == 1 {
				return &replay.StubRunner{
					CannedOutput: envelopeJSON("block", []string{"change it"}), CannedMediaType: "application/json",
				}, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
			}
			return &replay.StubRunner{CannedMediaType: "application/json"}, SeatMeta{
				HarnessName: "stub", ProviderName: "stub", Model: "stub",
			}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L1": {[]string{"reviewer"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	var gateErr *GateEscalationError
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	}); !errors.As(err, &gateErr) {
		t.Fatalf("first run: expected GateEscalationError, got %v", err)
	}

	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{ResumeID: runID})
	if err == nil || !strings.Contains(err.Error(), "already complete") {
		t.Fatalf("resume after substantive verdict: expected already complete, got %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	if producerCalls != 2 {
		t.Fatalf("producer calls = %d, want 2", producerCalls)
	}
	if len(m.Gates) != 2 {
		t.Fatalf("gates = %d, want unchanged history of 2", len(m.Gates))
	}
}

func TestEngineResumeRetriesInlineZeroSeatEscalation(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runID := "mywf-20260101T000000Z-inline-outage"
	wf := gatedWorkflow(&workflow.GateConfig{Seats: []string{"reviewer"}})
	e := Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain"}),
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return &replay.StubRunner{CannedMediaType: "application/json"}, SeatMeta{
				HarnessName: "stub", ProviderName: "stub", Model: "stub",
			}, nil
		},
		Root: repo,
		Now:  fixedClock(),
	}

	var gateErr *GateEscalationError
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	}); !errors.As(err, &gateErr) {
		t.Fatalf("first run: expected GateEscalationError, got %v", err)
	}
	e.ResolveSeat = func(string) (replay.Runner, SeatMeta, error) {
		return &replay.StubRunner{CannedOutput: envelopeJSON("go", nil), CannedMediaType: "application/json"},
			SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
	}
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{ResumeID: runID}); err != nil {
		t.Fatalf("resume inline outage: %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	if len(m.Gates) != 2 || m.Gates[1].Tier != 0 || m.Gates[1].Status != runmanifest.GateStatusPass {
		t.Fatalf("resumed inline gate = %#v, want tier-0 pass", m.Gates)
	}
}

func TestEngineResumeDoesNotRetryMixedBlockAndFailedSeats(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runID := "mywf-20260101T000000Z-mixed-outage"
	one := 1
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
	seatCalls := 0
	e := Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain"}),
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			seatCalls++
			meta := SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}
			if seatCalls == 1 {
				return &replay.StubRunner{CannedOutput: envelopeJSON("block", []string{"change it"}), CannedMediaType: "application/json"}, meta, nil
			}
			return &replay.StubRunner{CannedMediaType: "application/json"}, meta, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L1": {[]string{"reviewer-a", "reviewer-b"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	var gateErr *GateEscalationError
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	}); !errors.As(err, &gateErr) {
		t.Fatalf("first run: expected GateEscalationError, got %v", err)
	}
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{ResumeID: runID})
	if err == nil || !strings.Contains(err.Error(), "already complete") {
		t.Fatalf("resume mixed decision: expected already complete, got %v", err)
	}
	if got := len(readLiveManifest(t, repo, runID).Gates); got != 1 {
		t.Fatalf("gates = %d, want unchanged history of 1", got)
	}
}

func TestEngineResumeDoesNotAdvancePastSubstantiveEscalation(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runID := "mywf-20260101T000000Z-blocked-frontier"
	one := 1
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "sk", Produces: "plan", Inputs: []string{"task"}, Gate: &workflow.GateConfig{Tier: "L1", MaxRounds: &one}},
			{Name: "build", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
		},
	}
	producerCalls := 0
	e := Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			producerCalls++
			return &replay.StubRunner{CannedOutput: []byte("output"), CannedMediaType: "text/plain"}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return &replay.StubRunner{
				CannedOutput: envelopeJSON("block", []string{"change it"}), CannedMediaType: "application/json",
			}, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L1": {[]string{"reviewer"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	var gateErr *GateEscalationError
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	}); !errors.As(err, &gateErr) {
		t.Fatalf("first run: expected GateEscalationError, got %v", err)
	}
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{ResumeID: runID})
	if err == nil || !strings.Contains(err.Error(), "terminal gate escalation") {
		t.Fatalf("resume substantive escalation: expected terminal error, got %v", err)
	}
	if producerCalls != 1 {
		t.Fatalf("producer calls = %d, want 1", producerCalls)
	}
}

func TestEngineResumeDoesNotAdvancePastInterruptedGateRerun(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	runID := "mywf-20260101T000000Z-rerun-frontier"
	two := 2
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "sk", Produces: "plan", Inputs: []string{"task"}, Gate: &workflow.GateConfig{Tier: "L1", MaxRounds: &two}},
			{Name: "build", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
		},
	}
	producerCalls := 0
	e := Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			producerCalls++
			if producerCalls == 2 {
				return &replay.StubRunner{Err: errors.New("rerun interrupted")}, nil
			}
			return &replay.StubRunner{CannedOutput: []byte("output"), CannedMediaType: "text/plain"}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return &replay.StubRunner{
				CannedOutput: envelopeJSON("block", []string{"change it"}), CannedMediaType: "application/json",
			}, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L1": {[]string{"reviewer"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	}); err == nil || !strings.Contains(err.Error(), "rerun interrupted") {
		t.Fatalf("first run: expected interrupted rerun, got %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 || len(m.Gates) != 1 || m.Gates[0].Status != runmanifest.GateStatusRerun {
		t.Fatalf("interrupted history = stages:%d gates:%#v", len(m.Stages), m.Gates)
	}

	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{ResumeID: runID})
	if err == nil || !strings.Contains(err.Error(), "terminal gate") {
		t.Fatalf("resume interrupted rerun: expected terminal gate error, got %v", err)
	}
	if producerCalls != 2 {
		t.Fatalf("producer calls = %d, want 2", producerCalls)
	}
}

// ---------------------------------------------------------------------------
// Producer Session Evidence tests (etude-7ri.2)
// ---------------------------------------------------------------------------

// sessionStubRunner is a runner that writes a transcript file and returns a
// RunResult with a Session field populated.
type sessionStubRunner struct {
	output          []byte
	transcriptName  string
	transcriptBytes []byte
	sessionID       string
	harnessName     string
}

func (r *sessionStubRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	path := filepath.Join(req.ScratchDir, r.transcriptName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return replay.RunResult{}, err
	}
	if err := os.WriteFile(path, r.transcriptBytes, 0o644); err != nil {
		return replay.RunResult{}, err
	}
	return replay.RunResult{
		Output:    r.output,
		MediaType: "text/plain; charset=utf-8",
		Producer: runmanifest.Producer{
			Harness: runmanifest.Harness{Name: r.harnessName},
			Skill:   req.Producer.Skill,
		},
		Session: &replay.SessionInfo{
			SessionID:      r.sessionID,
			TranscriptPath: r.transcriptName,
		},
	}, nil
}

func TestProducerSession_AgenticStagePopulatesSession(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
		},
	}

	stub := &sessionStubRunner{
		output:          []byte("plan output"),
		transcriptName:  "transcript.txt",
		transcriptBytes: []byte("this is the transcript"),
		sessionID:       "session-abc",
		harnessName:     "claude-code",
	}

	runID := "mywf-20260101T000000Z-sesstest1"
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) { return stub, nil },
		Root:          repo,
		Now:           fixedClock(),
	}

	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(m.Stages))
	}
	sess := m.Stages[0].Producer.Session
	if sess == nil {
		t.Fatal("expected non-nil producer.session for agentic stage")
	}
	if sess.SessionID != "session-abc" {
		t.Errorf("session_id = %q, want session-abc", sess.SessionID)
	}
	if sess.RetrievalStatus != runmanifest.SessionEvidenceRetrievalImported {
		t.Errorf("retrieval_status = %q, want imported", sess.RetrievalStatus)
	}
	if sess.RedactionStatus != runmanifest.SessionEvidenceRedactionPassed {
		t.Errorf("redaction_status = %q, want passed", sess.RedactionStatus)
	}
	if sess.TranscriptArtifact == nil {
		t.Error("expected non-nil transcript_artifact")
	}
}

func TestProducerSession_DeterministicStageNilSession(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
		},
	}

	// Stub with harnessName="shell" — should skip session evidence.
	stub := &sessionStubRunner{
		output:          []byte("plan output"),
		transcriptName:  "transcript.txt",
		transcriptBytes: []byte("transcript"),
		sessionID:       "should-be-ignored",
		harnessName:     "shell",
	}

	runID := "mywf-20260101T000000Z-sesstest2"
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) { return stub, nil },
		Root:          repo,
		Now:           fixedClock(),
	}

	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, runID)
	if m.Stages[0].Producer.Session != nil {
		t.Error("expected nil producer.session for shell/deterministic stage")
	}
}
