package cli

// migration_test.go is a durable proof guard for the etude-2pc.2 migration:
// gates.yaml seats/tiers/quorum → .etude/registry.yaml; phase_gates →
// per-stage gate blocks in .etude/workflow.yaml. These tests run against the
// REAL files in the repo (no secrets required) and fail immediately if the
// migration regresses critical field values or if gates.yaml is re-introduced.

import (
	"bytes"
	"os"
	"path/filepath"
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

// TestMigrationRegistryParsesAndValidates asserts that .etude/registry.yaml
// is accepted by registry.ParseYAML and carries exactly 4 seats and 4 tiers
// with unanimous quorum.
func TestMigrationRegistryParsesAndValidates(t *testing.T) {
	root := repoRootForMigration(t)
	content, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
	if err != nil {
		t.Fatalf("read .etude/registry.yaml: %v", err)
	}
	reg, err := registry.ParseYAML(content)
	if err != nil {
		t.Fatalf("registry.ParseYAML: %v", err)
	}
	if got := reg.EffectiveQuorum(); got != "unanimous" {
		t.Errorf("quorum = %q, want %q", got, "unanimous")
	}
	if got := len(reg.Seats); got != 4 {
		t.Errorf("seat count = %d, want 4 (opus/codex/gemini/dev)", got)
	}
	if got := len(reg.Tiers); got != 4 {
		t.Errorf("tier count = %d, want 4 (L1/L2/L3/L4)", got)
	}
	for _, name := range []string{"opus", "codex", "gemini", "dev"} {
		if _, ok := reg.Seats[name]; !ok {
			t.Errorf("seat %q missing from registry", name)
		}
	}
	for _, tier := range []string{"L1", "L2", "L3", "L4"} {
		if _, ok := reg.Tiers[tier]; !ok {
			t.Errorf("tier %q missing from registry", tier)
		}
	}
}

// TestMigrationCriticalSeatFieldsPreserved guards against data-loss during
// the port from gates.yaml. Asserts the exact invoke substrings that the
// etude-review skill and live-run engine rely on are present verbatim.
func TestMigrationCriticalSeatFieldsPreserved(t *testing.T) {
	root := repoRootForMigration(t)
	content, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
	if err != nil {
		t.Fatalf("read .etude/registry.yaml: %v", err)
	}
	reg, err := registry.ParseYAML(content)
	if err != nil {
		t.Fatalf("registry.ParseYAML: %v", err)
	}

	gemini, ok := reg.Seats["gemini"]
	if !ok {
		t.Fatal("gemini seat missing")
	}
	if !strings.Contains(gemini.Invoke, "--skip-trust") {
		t.Errorf("gemini invoke missing --skip-trust: %q", gemini.Invoke)
	}

	codex, ok := reg.Seats["codex"]
	if !ok {
		t.Fatal("codex seat missing")
	}
	if !strings.Contains(codex.Invoke, `model_reasoning_effort="high"`) {
		t.Errorf("codex invoke missing model_reasoning_effort=\"high\": %q", codex.Invoke)
	}
	if !strings.Contains(codex.Invoke, "-s read-only") {
		t.Errorf("codex invoke missing -s read-only: %q", codex.Invoke)
	}
	if len(codex.ModelFallbacks) == 0 {
		t.Error("codex model_fallbacks must be non-empty")
	}
}

// TestMigrationWorkflowParsesAndCrossResolves asserts that .etude/workflow.yaml
// is accepted by workflow.ParseYAML with 5 stages, every stage runner name
// resolves to a registry seat, every stage gate tier resolves to a registry
// tier whose seats are all defined, and the verify stage has exactly 2 checks.
func TestMigrationWorkflowParsesAndCrossResolves(t *testing.T) {
	root := repoRootForMigration(t)

	regContent, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
	if err != nil {
		t.Fatalf("read .etude/registry.yaml: %v", err)
	}
	reg, err := registry.ParseYAML(regContent)
	if err != nil {
		t.Fatalf("registry.ParseYAML: %v", err)
	}

	wfContent, err := os.ReadFile(filepath.Join(root, ".etude", "workflow.yaml"))
	if err != nil {
		t.Fatalf("read .etude/workflow.yaml: %v", err)
	}
	wf, err := workflow.ParseYAML(wfContent)
	if err != nil {
		t.Fatalf("workflow.ParseYAML: %v", err)
	}

	if got := len(wf.Stages); got != 5 {
		t.Errorf("stage count = %d, want 5", got)
	}

	for _, s := range wf.Stages {
		if s.Runner == nil {
			t.Errorf("stage %q: runner is nil, every stage must have a runner", s.Name)
			continue
		}
		if s.Runner.Name == "" {
			t.Errorf("stage %q: runner.name is empty", s.Name)
			continue
		}
		if _, ok := reg.Seats[s.Runner.Name]; !ok {
			t.Errorf("stage %q: runner name %q not found in registry seats", s.Name, s.Runner.Name)
		}
	}

	for _, s := range wf.Stages {
		if s.Gate == nil {
			t.Errorf("stage %q: gate is nil, every stage must have a gate", s.Name)
			continue
		}
		if s.Gate.Tier == "" {
			t.Errorf("stage %q: gate.tier is empty", s.Name)
			continue
		}
		tier, ok := reg.Tiers[s.Gate.Tier]
		if !ok {
			t.Errorf("stage %q: gate tier %q not found in registry tiers", s.Name, s.Gate.Tier)
			continue
		}
		for _, seatName := range tier.Seats {
			if _, ok := reg.Seats[seatName]; !ok {
				t.Errorf("stage %q: gate tier %q references undefined seat %q", s.Name, s.Gate.Tier, seatName)
			}
		}
	}

	// verify stage must have exactly 2 deterministic checks
	var verifyStage *workflow.Stage
	for i := range wf.Stages {
		if wf.Stages[i].Name == "verify" {
			verifyStage = &wf.Stages[i]
			break
		}
	}
	if verifyStage == nil {
		t.Fatal("verify stage not found")
	}
	if verifyStage.Gate == nil {
		t.Fatal("verify stage has no gate")
	}
	if got := len(verifyStage.Gate.Checks); got != 2 {
		t.Errorf("verify gate check count = %d, want 2", got)
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
