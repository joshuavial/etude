package liverun

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// createRunForReplay creates a run with 3 stages using a stub runner
// and returns the run id and the repo.
func createRunForReplay(t *testing.T) (repo, runID string) {
	t.Helper()
	repo = initTestRepo(t)
	sha := headSHA(t, repo)

	// Stub runner that returns the input content as output so we can verify chaining.
	stub := &replay.StubRunner{CannedOutput: []byte("stub-out"), CannedMediaType: "application/octet-stream"}
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
			{Name: "stage-b", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
			{Name: "stage-c", Skill: "sk", Produces: "review", Inputs: []string{"diff"}},
		},
	}
	runID = "mywf-20260101T000000Z-ffreplay"
	e := Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now: func() time.Time {
			return time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
		},
	}
	if err := e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{
		TaskBytes: []byte("my task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return repo, runID
}

// AC2: ReplayForward re-executes all stages with recorded inputs.
func TestReplayForwardThreeStages(t *testing.T) {
	repo, runID := createRunForReplay(t)

	// Replay runner returns "replayed" for each stage.
	resolveRunner := func(stageName string) (replay.Runner, error) {
		return &replay.StubRunner{CannedOutput: []byte("replayed-" + stageName), CannedMediaType: "text/plain; charset=utf-8"}, nil
	}

	var out bytes.Buffer
	err := ReplayForward(context.Background(), refstore.New(repo), repo, &out, runID, resolveRunner)
	if err != nil {
		t.Fatalf("ReplayForward: %v", err)
	}

	// All 3 stage outputs should be concatenated to out.
	got := out.String()
	for _, name := range []string{"stage-a", "stage-b", "stage-c"} {
		want := "replayed-" + name
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("output missing %q; got %q", want, got)
		}
	}
}

func TestReplayForwardCallerStageUsesOriginalCheckout(t *testing.T) {
	repo := initTestRepo(t)
	originalSHA := headSHA(t, repo)
	caller := runnerFunc(func(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
		writeTestFile(t, repo, "README.md", "caller commit\n")
		gitRun(t, repo, "add", "README.md")
		gitRun(t, repo, "commit", "-m", "caller commit")
		return replay.RunResult{Output: []byte("caller output")}, nil
	})
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "produce", Skill: "sk", Produces: "plan",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	runID := "callerwf-20260101T000000Z-forwardpin"
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(caller), Root: repo, Now: fixedClock()}
	if err := e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{RunID: runID, GitSHA: originalSHA}); err != nil {
		t.Fatalf("create caller run: %v", err)
	}
	postRunSHA := headSHA(t, repo)
	if postRunSHA == originalSHA {
		t.Fatal("caller runner did not commit")
	}

	replayed := false
	err := ReplayForward(context.Background(), refstore.New(repo), repo, &bytes.Buffer{}, runID,
		func(string) (replay.Runner, error) {
			return runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
				replayed = true
				if got := headSHA(t, req.WorktreeDir); got != originalSHA {
					t.Fatalf("forward replay HEAD = %q, want original %q (caller post-run %q)", got, originalSHA, postRunSHA)
				}
				return replay.RunResult{Output: []byte("replayed")}, nil
			}), nil
		})
	if err != nil {
		t.Fatalf("ReplayForward: %v", err)
	}
	if !replayed {
		t.Fatal("forward replay runner did not execute")
	}
}

func TestReplayForwardDoesNotRequireCallerCommit(t *testing.T) {
	repo := initTestRepo(t)
	originalSHA := headSHA(t, repo)
	wf := workflow.Workflow{Name: "callerwf", Stages: []workflow.Stage{{
		Name: "produce", Skill: "sk", Produces: "plan",
		Runner: &workflow.Runner{Command: "unused", Workspace: workflow.RunnerWorkspaceCaller},
	}}}
	runID := "callerwf-20260101T000000Z-forwardmissing"
	e := Engine{Store: refstore.New(repo), ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("caller")}), Root: repo, Now: fixedClock()}
	if err := e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{RunID: runID, GitSHA: originalSHA}); err != nil {
		t.Fatalf("create caller run: %v", err)
	}
	rewriteLiveManifest(t, repo, runID, func(m *runmanifest.Manifest) {
		m.Stages[0].GitSHA = strings.Repeat("b", 40)
	})
	runnerCalls := 0
	err := ReplayForward(context.Background(), refstore.New(repo), repo, &bytes.Buffer{}, runID,
		func(string) (replay.Runner, error) {
			runnerCalls++
			return &replay.StubRunner{}, nil
		})
	if err != nil {
		t.Fatalf("ReplayForward with pruned caller provenance: %v", err)
	}
	if runnerCalls != 1 {
		t.Fatalf("resolve runner calls = %d, want 1", runnerCalls)
	}
}

func TestReplayForwardMissingRunErrors(t *testing.T) {
	repo := initTestRepo(t)

	err := ReplayForward(
		context.Background(),
		refstore.New(repo),
		repo,
		&bytes.Buffer{},
		"mywf-20260101T000000Z-notfound",
		func(string) (replay.Runner, error) { return &replay.StubRunner{}, nil },
	)
	if err == nil {
		t.Fatal("expected error for missing run")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("not found")) {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}
