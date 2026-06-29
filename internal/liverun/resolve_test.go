package liverun

import (
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/registry"
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
	runner, err := ResolveStageRunner(wf, registry.Registry{}, wf.Stages[0], 10*time.Second)
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
	runner, err := ResolveStageRunner(wf, registry.Registry{}, wf.Stages[0], 10*time.Second)
	if err != nil {
		t.Fatalf("ResolveStageRunner with default: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner from default_runner")
	}
}

func TestResolveStageRunnerNoRunnerError(t *testing.T) {
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "my-skill", Produces: "plan"},
		},
	}
	_, err := ResolveStageRunner(wf, registry.Registry{}, wf.Stages[0], 10*time.Second)
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
	runner, err := ResolveStageRunner(wf, reg, wf.Stages[0], 10*time.Second)
	if err != nil {
		t.Fatalf("ResolveStageRunner by name: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner")
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
