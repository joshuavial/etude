// Package example contains a smoke test that executes the summarize walkthrough
// end-to-end against a freshly compiled etude binary.
//
// The test builds etude into a temp directory, passes the binary path via
// ETUDE_BIN, runs walkthrough.sh, and asserts exit 0 plus the presence of
// stable output markers.  It is heavier than in-process CLI tests (it compiles
// a binary, git-inits a repo, runs bench) so it uses a generous timeout.
package example

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWalkthroughSmokeTest(t *testing.T) {
	// Skip if bash or git is unavailable (e.g. minimal CI image).
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found on PATH; skipping walkthrough smoke test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping walkthrough smoke test")
	}

	// Locate the repo root: this test lives at internal/example/example_test.go,
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

	// Generous timeout: the walkthrough git-inits, captures 3 runs, replays 1,
	// runs bench with a 3-run cohort, reindexes, and gc-reports.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	walkthroughPath := filepath.Join(repoRoot, "examples", "summarize", "walkthrough.sh")
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
	assertContains(t, outStr, "doc-alpha", "run list must include doc-alpha")
	assertContains(t, outStr, "doc-beta", "run list must include doc-beta")
	assertContains(t, outStr, "doc-gamma", "run list must include doc-gamma")
	assertContains(t, outStr, "replay (new skill) wins", "bench must print win-rate headline")
	assertContains(t, outStr, "wins 100.0%", "bench must show new skill winning all 3 runs")
	assertContains(t, outStr, "B=3", "bench must report B=3 (replay wins all)")
	assertContains(t, outStr, "reindexed", "reindex must print confirmation line")
	assertContains(t, outStr, "logical artifact bytes", "gc must print storage report")
	assertContains(t, outStr, "Walkthrough complete", "walkthrough must reach completion marker")
}

// assertContains fails the test if s does not contain substr.
func assertContains(t *testing.T, s, substr, msg string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("%s: output does not contain %q", msg, substr)
	}
}
