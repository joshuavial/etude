package liverun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

func TestGenerateRunID(t *testing.T) {
	id, err := GenerateRunID("myworkflow")
	if err != nil {
		t.Fatalf("GenerateRunID: %v", err)
	}
	if !strings.HasPrefix(id, "myworkflow-") {
		t.Errorf("run id %q does not start with workflow prefix", id)
	}
	if !runmanifest.IsValidRunID(id) {
		t.Errorf("generated id %q is not a valid run id", id)
	}
}

func TestResolvedOutputOnlySeatRunnerSupportsGitEnvironmentFiltering(t *testing.T) {
	reg := registry.Registry{Seats: map[string]registry.Seat{
		"reviewer": {
			Provider: "example/model",
			Harness:  "shell",
			Invoke:   "review-seat",
		},
	}}
	runner, _, err := ResolveGateSeat(reg, "reviewer", time.Minute, []string{"HOME", "GIT_DIR"})
	if err != nil {
		t.Fatal(err)
	}
	filteredRunner, err := outputOnlySeatRunner(runner, t.TempDir(), t.TempDir(), "resolved")
	if err != nil {
		t.Fatal(err)
	}
	filtered, ok := filteredRunner.(*replay.ExecRunner)
	if !ok {
		t.Fatalf("resolved output-only seat runner = %T, want *replay.ExecRunner", runner)
	}
	if got := strings.Join(filtered.EnvAllowlist, ","); got != "HOME" {
		t.Fatalf("filtered env allowlist = %q, want HOME", got)
	}
}

func TestGenerateRunIDUniqueness(t *testing.T) {
	a, _ := GenerateRunID("wf")
	b, _ := GenerateRunID("wf")
	// Collision is astronomically unlikely; two calls at the same second differ by rand suffix.
	if a == b {
		t.Errorf("two consecutive run ids are identical: %q", a)
	}
}

func TestResolveStageRunnerInlineCommand(t *testing.T) {
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "my-skill", Produces: "plan",
				Runner: &workflow.Runner{Command: "echo hello"}},
		},
	}
	runner, err := ResolveStageRunner(wf, registry.Registry{}, wf.Stages[0], 10*time.Second, nil)
	if err != nil {
		t.Fatalf("ResolveStageRunner: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestResolveStageRunnerDefaultRunner(t *testing.T) {
	wf := workflow.Workflow{
		Name:          "mywf",
		DefaultRunner: &workflow.Runner{Command: "cat"},
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "my-skill", Produces: "plan"},
		},
	}
	runner, err := ResolveStageRunner(wf, registry.Registry{}, wf.Stages[0], 10*time.Second, nil)
	if err != nil {
		t.Fatalf("ResolveStageRunner with default: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner from default_runner")
	}
}

func TestResolveStageRunnerWorkspaceOnlyUsesDefaultBinding(t *testing.T) {
	wf := workflow.Workflow{
		Name:          "mywf",
		DefaultRunner: &workflow.Runner{Command: "cat"},
		Stages: []workflow.Stage{{
			Name: "plan", Skill: "my-skill", Produces: "plan",
			Runner: &workflow.Runner{Workspace: workflow.RunnerWorkspaceCaller},
		}},
	}
	if _, err := ResolveStageRunner(wf, registry.Registry{}, wf.Stages[0], 10*time.Second, nil); err != nil {
		t.Fatalf("ResolveStageRunner workspace override: %v", err)
	}
}

func TestResolveStageRunnerNoRunnerError(t *testing.T) {
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "my-skill", Produces: "plan"},
		},
	}
	_, err := ResolveStageRunner(wf, registry.Registry{}, wf.Stages[0], 10*time.Second, nil)
	if err == nil {
		t.Fatal("expected error when no runner configured")
	}
}

func TestResolveStageRunnerByName(t *testing.T) {
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "my-skill", Produces: "plan",
				Runner: &workflow.Runner{Name: "myrunner"}},
		},
	}
	reg := registry.Registry{
		Seats: map[string]registry.Seat{
			"myrunner": {Provider: "test", Harness: "test", Invoke: "cat"},
		},
	}
	runner, err := ResolveStageRunner(wf, reg, wf.Stages[0], 10*time.Second, nil)
	if err != nil {
		t.Fatalf("ResolveStageRunner by name: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
}

// ---------------------------------------------------------------------------
// EnvAllowlist propagation tests
// ---------------------------------------------------------------------------

// TestResolveGateSeatPropagatesEnvAllowlist verifies that the envAllowlist passed
// to ResolveGateSeat is set on the returned ExecRunner.
func TestResolveGateSeatPropagatesEnvAllowlist(t *testing.T) {
	reg := registry.Registry{
		Seats: map[string]registry.Seat{
			"myseat": {Provider: "test/model", Harness: "test", Invoke: "cat"},
		},
	}
	wantAllowlist := []string{"ETUDE_TEST_MARKER"}
	runner, _, err := ResolveGateSeat(reg, "myseat", 10*time.Second, wantAllowlist)
	if err != nil {
		t.Fatalf("ResolveGateSeat: %v", err)
	}
	er, ok := runner.(*replay.ExecRunner)
	if !ok {
		t.Fatalf("runner is %T, want *replay.ExecRunner", runner)
	}
	if len(er.EnvAllowlist) != 1 || er.EnvAllowlist[0] != "ETUDE_TEST_MARKER" {
		t.Errorf("EnvAllowlist = %v, want [ETUDE_TEST_MARKER]", er.EnvAllowlist)
	}
}

func TestResolveGateSeatSessionEvidenceRequirement(t *testing.T) {
	reg := registry.Registry{
		Seats: map[string]registry.Seat{
			"agentic":       {Provider: "openai/gpt-5.5", Harness: "codex", Invoke: "cat"},
			"deterministic": {Provider: "deterministic/approver", Harness: "codex", Invoke: "cat"},
			"shell":         {Provider: "openai/gpt-5.5", Harness: "shell", Invoke: "cat"},
			"empty":         {Provider: "empty/model", Harness: "", Invoke: "cat"},
		},
	}
	tests := []struct {
		name string
		want bool
	}{
		{"agentic", true},
		{"deterministic", false},
		{"shell", false},
		{"empty", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, meta, err := ResolveGateSeat(reg, tt.name, 10*time.Second, nil)
			if err != nil {
				t.Fatalf("ResolveGateSeat: %v", err)
			}
			if meta.RequireSessionEvidence != tt.want {
				t.Fatalf("RequireSessionEvidence = %v, want %v", meta.RequireSessionEvidence, tt.want)
			}
		})
	}
}

// TestResolveCheckRunnerIsHermetic verifies that ResolveCheckRunner returns an
// execCheckRunner, which has no allowlist and never propagates parent env vars.
func TestResolveCheckRunnerIsHermetic(t *testing.T) {
	r := workflow.Runner{Command: "echo test"}
	cr, err := ResolveCheckRunner(registry.Registry{}, r, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("ResolveCheckRunner: %v", err)
	}
	if _, ok := cr.(*execCheckRunner); !ok {
		t.Errorf("ResolveCheckRunner returned %T, want *execCheckRunner", cr)
	}
}

func TestDeriveFrontier(t *testing.T) {
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "a", Skill: "sk", Produces: "plan"},
			{Name: "b", Skill: "sk", Produces: "diff"},
			{Name: "c", Skill: "sk", Produces: "review"},
		},
	}

	// Empty manifest → frontier = 0.
	frontier := DeriveFrontier(wf, runmanifest.Manifest{})
	if frontier != 0 {
		t.Errorf("empty manifest: frontier = %d, want 0", frontier)
	}

	// After stage a → frontier = 1.
	partial := runmanifest.Manifest{
		Stages: []runmanifest.Stage{
			{Output: runmanifest.ArtifactRef{Role: "plan"}},
		},
	}
	frontier = DeriveFrontier(wf, partial)
	if frontier != 1 {
		t.Errorf("after stage a: frontier = %d, want 1", frontier)
	}

	// After all stages → frontier = 3 (complete).
	full := runmanifest.Manifest{
		Stages: []runmanifest.Stage{
			{Output: runmanifest.ArtifactRef{Role: "plan"}},
			{Output: runmanifest.ArtifactRef{Role: "diff"}},
			{Output: runmanifest.ArtifactRef{Role: "review"}},
		},
	}
	frontier = DeriveFrontier(wf, full)
	if frontier != 3 {
		t.Errorf("complete run: frontier = %d, want 3", frontier)
	}
}

func TestReviewerEnvironmentDoesNotReachChecksOrStages(t *testing.T) {
	t.Setenv("REVIEW_ONLY_TOKEN", "test-secret")
	script := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nvalue=${REVIEW_ONLY_TOKEN-unset}\nprintf '%s' \"$value\"\nif [ -n \"${ETUDE_OUTPUT_FILE-}\" ]; then printf '%s' \"$value\" > \"$ETUDE_OUTPUT_FILE\"; fi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	reg := registry.Registry{Seats: map[string]registry.Seat{"probe": {Provider: "deterministic/test", Harness: "shell", Invoke: script, EnvAllowlist: []string{"REVIEW_ONLY_TOKEN"}, InvocationFallbacks: []registry.SeatInvocation{{Harness: "shell", Invoke: script}}}}}
	req := func() replay.RunRequest {
		return replay.RunRequest{WorktreeDir: t.TempDir(), ScratchDir: t.TempDir(), OutputRole: "result"}
	}
	runner, _, err := ResolveGateSeat(reg, "probe", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), req())
	if err != nil || string(result.Output) != "test-secret" {
		t.Fatalf("seat did not receive credential: %v", err)
	}
	candidates, err := ResolveGateSeatCandidates(reg, "probe", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		result, err := c.Runner.Run(context.Background(), req())
		if err != nil || string(result.Output) != "test-secret" {
			t.Fatalf("candidate credential missing: %v", err)
		}
	}
	stage, err := ResolveStageRunner(workflow.Workflow{}, reg, workflow.Stage{Name: "work", Runner: &workflow.Runner{Name: "probe"}}, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = stage.Run(context.Background(), req())
	if err != nil || string(result.Output) != "unset" {
		t.Fatalf("stage credential isolation failed: %v", err)
	}
	check, err := ResolveCheckRunner(reg, workflow.Runner{Name: "probe"}, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	passed, raw, detail := check.RunCheck(context.Background(), req())
	if !passed || strings.Contains(string(raw), "test-secret") || !strings.Contains(string(raw), "unset") {
		t.Fatalf("check isolation failed: %s", detail)
	}
}
