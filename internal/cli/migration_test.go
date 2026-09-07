package cli

// migration_test.go is a durable proof guard for the etude-2pc.2 migration:
// gates.yaml seats/tiers/quorum → .etude/registry.yaml; phase_gates →
// per-stage gate blocks in .etude/workflow.yaml. These tests run against the
// REAL files in the repo (no secrets required). Current profile contracts replace
// historical fixed model and five-gate expectations; schema migration guards remain.

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/workflow"
)

// repoRootForMigration resolves the repository root relative to this test
// file's location. Uses runtime.Caller so the path is compile-time stable
// regardless of the working directory when tests are invoked.
func repoRootForMigration(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// thisFile: .../internal/cli/migration_test.go — two levels up is repo root.
	return filepath.Join(filepath.Dir(thisFile), "../..")
}

// The repository is allowed to customize init scaffolding. Its default profile
// must instead agree with the named dev-codex profile used by explicit callers.
func TestDevelopmentDefaultMatchesNamedProfile(t *testing.T) {
	root := repoRootForMigration(t)
	read := func(path string) workflow.Workflow {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		wf, err := workflow.ParseYAML(data)
		if err != nil {
			t.Fatal(err)
		}
		return wf
	}
	if !reflect.DeepEqual(read(".etude/workflow.yaml"), read(".etude/workflows/dev-codex.yaml")) {
		t.Fatal("default must match named dev-codex profile")
	}
}

func TestDevelopmentReviewerBoundaries(t *testing.T) {
	root := repoRootForMigration(t)
	data, err := os.ReadFile(filepath.Join(root, ".etude/registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.ParseYAML(data)
	if err != nil {
		t.Fatal(err)
	}
	if reg.EffectiveQuorum() != "unanimous" {
		t.Fatal("review requires unanimity")
	}
	for name, model := range map[string]string{"astra": "gpt-6-astra", "fable": "claude-fable-5-1", "sol": "gpt-5.6-sol", "sonnet": "claude-sonnet-5"} {
		seat, ok := reg.Seats[name]
		if !ok {
			t.Fatalf("missing reviewer %s", name)
		}
		if !strings.HasSuffix(seat.Provider, "/"+model) || !strings.Contains(seat.Invoke, model) {
			t.Fatalf("reviewer %s identity mismatch", name)
		}
		if !strings.Contains(seat.Invoke, "seat-adapter.sh") {
			t.Fatalf("reviewer %s missing output adapter", name)
		}
		if len(seat.ModelFallbacks) != 0 || len(seat.InvocationFallbacks) != 0 {
			t.Fatalf("reviewer %s has unvalidated fallbacks", name)
		}
		if seat.Harness == "codex" && !strings.Contains(seat.Invoke, "-s read-only") {
			t.Fatalf("reviewer %s lacks read-only sandbox", name)
		}
		if seat.Harness == "claude-code" && !strings.Contains(seat.Invoke, "--tools=") {
			t.Fatalf("reviewer %s must review inline evidence without tools", name)
		}
	}
	if !reflect.DeepEqual(reg.Tiers["L2"].Seats, []string{"astra", "fable"}) {
		t.Fatal("strong gate must require Astra and Fable")
	}
}

func TestDevelopmentWorkflowCaptureAndGateContracts(t *testing.T) {
	root := repoRootForMigration(t)
	for _, profile := range []string{"dev-claude", "dev-codex"} {
		data, err := os.ReadFile(filepath.Join(root, ".etude/workflows", profile+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		wf, err := workflow.ParseYAML(data)
		if err != nil {
			t.Fatal(err)
		}
		if wf.DefaultRunner != nil {
			t.Fatal("external worker profile must not claim an autonomous runner")
		}
		found := map[string]bool{}
		for _, stage := range wf.Stages {
			found[stage.Name] = true
			if stage.Runner != nil {
				t.Fatalf("%s unexpectedly has autonomous runner", stage.Name)
			}
			wantGate := stage.Name == "plan" || stage.Name == "review" || stage.Name == "routine-review"
			if (stage.Gate != nil) != wantGate {
				t.Fatalf("%s gate violates proportional review policy", stage.Name)
			}
			if stage.Name == "docs" && stage.Optional {
				t.Fatal("documentation assessment must be mandatory")
			}
		}
		for _, name := range []string{"plan", "implement", "verify", "docs", "review", "routine-review"} {
			if !found[name] {
				t.Fatalf("missing stage %s", name)
			}
		}
	}
}

// TestMigrationRegistryRoundTrips asserts that ParseYAML(reg.YAML()) is
// byte-stable (defect #5). Encodes the parsed registry, parses that output,
// encodes again, and compares the two encodings.
func TestMigrationRegistryRoundTrips(t *testing.T) {
	root := repoRootForMigration(t)
	content, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
	if err != nil {
		t.Fatalf("read .etude/registry.yaml: %v", err)
	}
	reg, err := registry.ParseYAML(content)
	if err != nil {
		t.Fatalf("registry.ParseYAML: %v", err)
	}

	first, err := reg.YAML()
	if err != nil {
		t.Fatalf("reg.YAML() first encode: %v", err)
	}
	reg2, err := registry.ParseYAML(first)
	if err != nil {
		t.Fatalf("registry.ParseYAML on first encode: %v", err)
	}
	second, err := reg2.YAML()
	if err != nil {
		t.Fatalf("reg2.YAML() second encode: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("registry YAML round-trip is not byte-stable:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestMigrationGatesYAMLDeleted asserts that .etude/gates.yaml no longer
// exists (AC4: the file must be deleted in this migration, never re-introduced).
func TestMigrationGatesYAMLDeleted(t *testing.T) {
	root := repoRootForMigration(t)
	gatesPath := filepath.Join(root, ".etude", "gates.yaml")
	_, err := os.Stat(gatesPath)
	if err == nil {
		t.Errorf(".etude/gates.yaml still exists at %s — it must be deleted by the etude-2pc.2 migration", gatesPath)
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected error checking for .etude/gates.yaml: %v", err)
	}
}
