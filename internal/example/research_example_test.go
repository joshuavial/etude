// Package example contains smoke tests that execute example walkthroughs
// end-to-end against a freshly compiled etude binary.
package example

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestResearchWalkthroughSmokeTest proves the generality claim for etude-2pc.3:
// a non-dev 5-stage research workflow runs live, captures by construction,
// the review gate passes on round 1, and all 5 stages replay forward.
//
// The test builds the binary fresh, drives examples/research/walkthrough.sh,
// and asserts stable output markers.  It is heavier than in-process CLI tests
// so it uses a generous timeout.
func TestResearchWalkthroughSmokeTest(t *testing.T) {
	// Skip if bash or git is unavailable (e.g. minimal CI image).
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found on PATH; skipping research walkthrough smoke test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping research walkthrough smoke test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh scripts not supported on Windows")
	}

	// Locate the repo root: this test lives at internal/example/research_example_test.go,
	// so walk up two directories from the file's directory.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	// Build etude into a temp directory.  The binary path is passed to the
	// walkthrough via ETUDE_BIN so the script never falls back to an ambient
	// binary on $PATH.
	binDir := t.TempDir()
	etudebin := filepath.Join(binDir, "etude")
	t.Logf("building etude -> %s", etudebin)
	buildCmd := exec.Command("go", "build", "-o", etudebin, "./cmd/etude")
	buildCmd.Dir = repoRoot
	buildOut, buildErr := buildCmd.CombinedOutput()
	if buildErr != nil {
		t.Fatalf("go build failed: %v\n%s", buildErr, buildOut)
	}

	// Generous timeout: the walkthrough git-inits, runs 5 stages + gate, shows
	// the run, and forward-replays.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	walkthroughPath := filepath.Join(repoRoot, "examples", "research", "walkthrough.sh")
	cmd := exec.CommandContext(ctx, "bash", walkthroughPath)
	cmd.Env = append(os.Environ(), "ETUDE_BIN="+etudebin)

	output, err := cmd.CombinedOutput()
	outStr := string(output)

	if err != nil {
		t.Fatalf("walkthrough.sh exited with error: %v\noutput:\n%s", err, outStr)
	}

	t.Logf("walkthrough output:\n%s", outStr)

	// Assert stable output markers — checked against substrings that are
	// unlikely to change even if formatting evolves.

	// Non-dev stage names must appear in the run show output.
	for _, stage := range []string{"stage: research", "stage: fact-check", "stage: draft", "stage: review", "stage: tone-police"} {
		assertContains(t, outStr, stage, "run show must include "+stage)
	}

	// The review gate must pass on round 1 (folded optional: catches silent RERUN regression).
	assertContains(t, outStr, "captured gate review.r1 status=pass",
		"live run must print gate pass line")

	// The run show output must include the gate pass status.
	assertContains(t, outStr, "status:   pass", "run show must show gate status=pass")

	// The approver seat must appear (registry-mechanism reuse proof).
	assertContains(t, outStr, "seat: approver", "run show must show approver seat from registry")

	// Forward replay must complete.
	assertContains(t, outStr, "Step 3: etude replay", "walkthrough must reach forward replay step")

	// Overall completion marker.
	assertContains(t, outStr, "Walkthrough complete", "walkthrough must reach completion marker")
}
