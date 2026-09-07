package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/liverun"
	"github.com/joshuavial/etude/internal/refstore"
)

// writeGatedWorkflowFiles writes a `gated` workflow (one stage `review`
// producing role `review`, gated at tier L1 with one seat) plus a registry
// wiring that seat to a script which BLOCKS on its first invocation and
// returns GO on every invocation after that, tracked via a counter file.
// This lets a test observe exactly how many times the seat actually ran,
// which matters for the etude-3343 mismatch-must-not-invoke-seats assertion.
func writeGatedWorkflowFiles(t *testing.T, repo string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh scripts not supported on Windows")
	}

	counterPath := filepath.Join(t.TempDir(), "seat-calls")
	seatScript := filepath.Join(repo, "seat.sh")
	body := fmt.Sprintf(`#!/bin/sh
if [ -s %q ]; then
  printf '{"verdict":"go"}' > "$ETUDE_OUTPUT_FILE"
else
  printf 'x' >> %q
  printf '{"verdict":"block","required":["fix the draft"]}' > "$ETUDE_OUTPUT_FILE"
fi
`, counterPath, counterPath)
	if err := os.WriteFile(seatScript, []byte(body), 0o755); err != nil {
		t.Fatalf("write seat.sh: %v", err)
	}

	etDir := filepath.Join(repo, ".etude")
	if err := os.MkdirAll(filepath.Join(etDir, "workflows"), 0o755); err != nil {
		t.Fatalf("mkdir .etude/workflows: %v", err)
	}
	workflowContent := "name: gated\n" +
		"stages:\n" +
		"  - name: review\n" +
		"    produces: review\n" +
		"    skill: review-skill\n" +
		"    gate:\n" +
		"      tier: L1\n"
	if err := os.WriteFile(filepath.Join(etDir, "workflows", "gated.yaml"), []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("write workflows/gated.yaml: %v", err)
	}

	registryContent := fmt.Sprintf(`quorum: unanimous
seats:
  reviewer:
    provider: deterministic/reviewer
    harness: shell
    invoke: %s
tiers:
  L1:
    name: Gate test tier
    seats:
      - reviewer
`, seatScript)
	if err := os.WriteFile(filepath.Join(etDir, "registry.yaml"), []byte(registryContent), 0o644); err != nil {
		t.Fatalf("write registry.yaml: %v", err)
	}
}

// TestGateRejectsMismatchThenAcceptsSameNameRecapture is the etude-3343
// end-to-end regression, driven entirely through the CLI:
//
//  1. capture `review` with file content A.
//  2. gate: bytes match the capture, so the seat runs (its first invocation
//     blocks) -> recorded as review.r1, citing digest A.
//  3. the file is edited to content B WITHOUT recapturing; gate on those bytes
//     must fail closed with ErrArtifactMismatch, and the run ref must not move.
//  4. capture `review` again (same stage name) with content B via
//     --expect append.
//  5. gate on the recaptured bytes passes as review.r2, citing digest B.
//
// Both stage occurrences and both gate digests must remain intact throughout.
func TestGateRejectsMismatchThenAcceptsSameNameRecapture(t *testing.T) {
	repo := initCaptureRepo(t)
	writeGatedWorkflowFiles(t, repo)
	chdir(t, repo)

	reviewFile := filepath.Join(repo, "review.md")
	writeFile(t, repo, "review.md", "draft A\n")

	if _, stderr, err := execute("capture", "review", "--run", "r1", "--output", "review="+reviewFile); err != nil {
		t.Fatalf("capture A: %v\nstderr: %s", err, stderr)
	}

	// The first gate reviews byte-identical content, so it reaches the seat.
	// The seat script's FIRST invocation blocks.
	_, stderr, err := execute("gate", "--run", "r1", "--stage", "review", "--artifact", reviewFile, "--workflow", "gated")
	if err == nil {
		t.Fatal("expected the first gate attempt to fail (blocked), got nil error")
	}
	var notPassed *ErrGateNotPassed
	if !errors.As(err, &notPassed) || notPassed.GateID != "review.r1" {
		t.Fatalf("first gate error = %v (stderr %q), want ErrGateNotPassed for review.r1", err, stderr)
	}

	afterFirst := readRunManifest(t, repo, "r1")
	if len(afterFirst.Gates) != 1 || afterFirst.Gates[0].GateID != "review.r1" {
		t.Fatalf("after the first gate, gates = %+v", afterFirst.Gates)
	}
	digestA := afterFirst.Gates[0].ReviewedStages[0].Artifact
	if digestA == "" || digestA != afterFirst.Stages[0].Output.Artifact {
		t.Fatalf("review.r1 reviewed digest = %q, want the first occurrence's output digest %q",
			digestA, afterFirst.Stages[0].Output.Artifact)
	}

	store := refstore.New(repo)
	refBeforeMismatch, err := store.Resolve(context.Background(), "refs/etude/runs/r1")
	if err != nil {
		t.Fatalf("resolve after first gate: %v", err)
	}

	// Change the file WITHOUT recapturing. Gating on it must refuse closed.
	writeFile(t, repo, "review.md", "draft B\n")
	_, stderr, err = execute("gate", "--run", "r1", "--stage", "review", "--artifact", reviewFile, "--workflow", "gated")
	if !errors.Is(err, liverun.ErrArtifactMismatch) {
		t.Fatalf("gate on unrecaptured bytes = %v (stderr %q), want ErrArtifactMismatch", err, stderr)
	}
	refAfterMismatch, err := store.Resolve(context.Background(), "refs/etude/runs/r1")
	if err != nil {
		t.Fatalf("resolve after rejected mismatch: %v", err)
	}
	if refAfterMismatch != refBeforeMismatch {
		t.Fatalf("run ref moved after a rejected mismatch: before %s after %s", refBeforeMismatch, refAfterMismatch)
	}
	if got := readRunManifest(t, repo, "r1"); len(got.Gates) != 1 {
		t.Fatalf("a rejected mismatch recorded an attempt: gates = %+v", got.Gates)
	}

	// Recapture the SAME stage name with the new bytes.
	if _, stderr, err := execute("capture", "review", "--run", "r1", "--output", "review="+reviewFile, "--expect", "append"); err != nil {
		t.Fatalf("recapture B: %v\nstderr: %s", err, stderr)
	}

	// The second gate reviews the recaptured bytes, so it reaches the seat.
	// The seat script's SECOND invocation returns go.
	stdout, stderr, err := execute("gate", "--run", "r1", "--stage", "review", "--artifact", reviewFile, "--workflow", "gated")
	if err != nil {
		t.Fatalf("second gate: %v\nstderr: %s\nstdout: %s", err, stderr, stdout)
	}
	if !strings.Contains(stdout, "review.r2") {
		t.Fatalf("expected review.r2 in stdout, got %q", stdout)
	}

	final := readRunManifest(t, repo, "r1")
	if len(final.Stages) != 2 || final.Stages[0].Name != "review" || final.Stages[1].Name != "review" {
		t.Fatalf("expected two same-named `review` stage occurrences, got %+v", final.Stages)
	}
	if len(final.Gates) != 2 || final.Gates[0].GateID != "review.r1" || final.Gates[1].GateID != "review.r2" {
		t.Fatalf("expected review.r1 then review.r2, got %+v", final.Gates)
	}
	digestB := final.Gates[1].ReviewedStages[0].Artifact
	if digestB == "" || digestB == digestA {
		t.Fatalf("review.r2 digest = %q, want a digest distinct from review.r1's %q", digestB, digestA)
	}
	if final.Gates[0].ReviewedStages[0].Artifact != digestA {
		t.Errorf("review.r1's digest changed after the recapture and second gate: %q, want %q",
			final.Gates[0].ReviewedStages[0].Artifact, digestA)
	}
	if digestA != final.Stages[0].Output.Artifact {
		t.Errorf("review.r1 no longer cites the FIRST occurrence's output digest")
	}
	if digestB != final.Stages[1].Output.Artifact {
		t.Errorf("review.r2 does not cite the SECOND occurrence's output digest")
	}

	// Both stage occurrences' bytes are still readable from the ref.
	contentA, err := store.ReadFile(context.Background(), "refs/etude/runs/r1", final.Stages[0].Output.Path)
	if err != nil || string(contentA) != "draft A\n" {
		t.Errorf("first occurrence bytes = %q, err %v, want %q", contentA, err, "draft A\n")
	}
	contentB, err := store.ReadFile(context.Background(), "refs/etude/runs/r1", final.Stages[1].Output.Path)
	if err != nil || string(contentB) != "draft B\n" {
		t.Errorf("second occurrence bytes = %q, err %v, want %q", contentB, err, "draft B\n")
	}
}
