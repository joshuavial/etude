package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
)

// ---------------------------------------------------------------------------
// Positive registration tests
// ---------------------------------------------------------------------------

func TestRunIsRegisteredSubcommand(t *testing.T) {
	stdout, stderr, err := execute("run", "--help")
	if err != nil {
		t.Fatalf("run --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "run") {
		t.Fatalf("run --help output does not mention 'run':\n%s", stdout)
	}
}

func TestRunListIsRegisteredSubcommand(t *testing.T) {
	stdout, stderr, err := execute("run", "list", "--help")
	if err != nil {
		t.Fatalf("run list --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "list") {
		t.Fatalf("run list --help output does not mention 'list':\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// run list
// ---------------------------------------------------------------------------

func TestRunListZeroRuns(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "no runs found") {
		t.Fatalf("expected 'no runs found', got: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRunListOneRun(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "content")
	chdir(t, repo)

	// Use a run id with no digits so no assertion can be satisfied incidentally.
	_, stderr, err := execute("capture", "plan", "--run", "myrun", "--output", "output=out.md")
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list returned error: %v\nstderr: %s", err, stderr)
	}

	// Header row must contain all column names.
	for _, col := range []string{"RUN ID", "WORKFLOW", "CREATED", "STAGES"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("expected column %q in header:\n%s", col, stdout)
		}
	}

	// Find the data row for "myrun" and split with Fields to collapse tabwriter padding.
	var dataRow string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "myrun") {
			dataRow = line
			break
		}
	}
	if dataRow == "" {
		t.Fatalf("could not find data row for 'myrun' in output:\n%s", stdout)
	}
	fields := strings.Fields(dataRow)
	// fields: [<run id>, <workflow>, <created>, <stages>]
	if len(fields) < 4 {
		t.Fatalf("expected at least 4 fields in data row, got %d: %v", len(fields), fields)
	}
	if fields[0] != "myrun" {
		t.Fatalf("fields[0] = %q, want %q", fields[0], "myrun")
	}
	if fields[1] != "manual" {
		t.Fatalf("fields[1] = %q, want %q", fields[1], "manual")
	}
	if _, err := time.Parse(time.RFC3339, fields[2]); err != nil {
		t.Fatalf("fields[2] = %q is not a valid RFC3339 timestamp: %v", fields[2], err)
	}
	if !strings.HasSuffix(fields[2], "Z") {
		t.Fatalf("fields[2] = %q does not end with Z (UTC)", fields[2])
	}
	if fields[len(fields)-1] != "1" {
		t.Fatalf("last field (stage count) = %q, want %q", fields[len(fields)-1], "1")
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRunListMultipleRunsDeterministicOrder(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "content")
	chdir(t, repo)

	// Seed run-b first, then run-a — output must be run-a before run-b (lexical order).
	_, stderr, err := execute("capture", "plan", "--run", "run-b", "--output", "output=out.md")
	if err != nil {
		t.Fatalf("capture run-b returned error: %v\nstderr: %s", err, stderr)
	}
	_, stderr, err = execute("capture", "plan", "--run", "run-a", "--output", "output=out.md")
	if err != nil {
		t.Fatalf("capture run-a returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "run-a") {
		t.Fatalf("run-a missing from output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "run-b") {
		t.Fatalf("run-b missing from output:\n%s", stdout)
	}
	idxA := strings.Index(stdout, "run-a")
	idxB := strings.Index(stdout, "run-b")
	if idxA >= idxB {
		t.Fatalf("expected run-a before run-b in output:\n%s", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRunListCorruptManifestFails(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "content")
	chdir(t, repo)

	// Seed a good run so the list is non-empty.
	_, stderr, err := execute("capture", "plan", "--run", "good-run", "--output", "output=out.md")
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	// Write a bad manifest under a separate run ref directly via refstore.
	seedRunWithBadManifest(t, repo, "bad-run")

	stdout, stderr, err := execute("run", "list")
	if err == nil {
		t.Fatal("run list with corrupt manifest returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "bad-run") {
		t.Fatalf("error does not name the offending run id 'bad-run': %q", combined)
	}
	// tabwriter only flushes on success, so a mid-list failure must leave stdout empty.
	if stdout != "" {
		t.Fatalf("expected empty stdout on corrupt-manifest failure, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// run show
// ---------------------------------------------------------------------------

func TestRunShowExistingRun(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "task.txt", "task content")
	writeFile(t, repo, "out.md", "output content")
	chdir(t, repo)

	// Capture stage 1 with an input and two refs (lexically issue < pr, tests sorting).
	_, stderr, err := execute("capture", "plan",
		"--run", "show-run",
		"--output", "output=out.md",
		"--input", "task=task.txt",
		"--ref", "pr=42",
		"--ref", "issue=7",
	)
	if err != nil {
		t.Fatalf("capture stage 1 returned error: %v\nstderr: %s", err, stderr)
	}

	// Capture stage 2.
	writeFile(t, repo, "review.md", "review content")
	_, stderr, err = execute("capture", "review",
		"--run", "show-run",
		"--output", "output=review.md",
	)
	if err != nil {
		t.Fatalf("capture stage 2 returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "show", "show-run")
	if err != nil {
		t.Fatalf("run show returned error: %v\nstderr: %s", err, stderr)
	}

	// Run metadata lines — pin label+value with regexps to avoid padding-width dependence.
	for _, pattern := range []string{
		`(?m)^run id:\s+show-run$`,
		`(?m)^workflow:\s+manual$`,
		`(?m)^workflow version:\s+manual-v1$`,
	} {
		if !regexp.MustCompile(pattern).MatchString(stdout) {
			t.Fatalf("pattern %q not found in run show output:\n%s", pattern, stdout)
		}
	}

	// Both ref values must be present.
	if !strings.Contains(stdout, "issue: 7") {
		t.Fatalf("expected ref 'issue: 7' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "pr: 42") {
		t.Fatalf("expected ref 'pr: 42' in output:\n%s", stdout)
	}

	// Refs must appear in sorted order: issue before pr.
	idxIssue := strings.Index(stdout, "issue: 7")
	idxPr := strings.Index(stdout, "pr: 42")
	if idxIssue >= idxPr {
		t.Fatalf("expected 'issue' ref before 'pr' ref in output:\n%s", stdout)
	}

	// Both stage headers must appear.
	if !strings.Contains(stdout, "stage: plan") {
		t.Fatalf("expected 'stage: plan' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "stage: review") {
		t.Fatalf("expected 'stage: review' in output:\n%s", stdout)
	}

	// produced_by and skill fields.
	if !strings.Contains(stdout, "produced_by: original") {
		t.Fatalf("expected 'produced_by: original' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "skill:       plan@manual (manual)") {
		t.Fatalf("expected 'skill:       plan@manual (manual)' in output:\n%s", stdout)
	}
	// Legacy capture (no --harness/--model) must not print harness or model lines.
	if strings.Contains(stdout, "harness:") {
		t.Fatalf("did not expect 'harness:' line in output for legacy capture:\n%s", stdout)
	}
	if strings.Contains(stdout, "  model:") {
		t.Fatalf("did not expect 'model:' line in output for legacy capture:\n%s", stdout)
	}

	// Input line: role + exact path + size + storage + media-type all in one substring.
	// "task content" = 12 bytes; content-addressed path is deterministic.
	wantInput := "role=task path=artifacts/sha256/1c/1c29caf59466263bf1f7597a05979915dfc32bff9739ebcbbb21ccdf2497a5dd size=12 storage=content media-type=text/plain; charset=utf-8"
	if !strings.Contains(stdout, wantInput) {
		t.Fatalf("expected input line %q in output:\n%s", wantInput, stdout)
	}

	// Output line for plan stage: "output content" = 14 bytes, markdown.
	wantPlanOutput := "role=output path=artifacts/sha256/18/187c2ea88be13f390a8e4e3124f934c9a74100b192e79fc8d492ddde9f5f686f size=14 storage=content media-type=text/markdown; charset=utf-8"
	if !strings.Contains(stdout, wantPlanOutput) {
		t.Fatalf("expected plan output line %q in output:\n%s", wantPlanOutput, stdout)
	}

	// Output line for review stage: "review content" = 14 bytes, markdown.
	wantReviewOutput := "role=output path=artifacts/sha256/fe/fedc04b3f1b3b91e320f773e621a38d01e2515e98875d96aa22b1cc793bb26d9 size=14 storage=content media-type=text/markdown; charset=utf-8"
	if !strings.Contains(stdout, wantReviewOutput) {
		t.Fatalf("expected review output line %q in output:\n%s", wantReviewOutput, stdout)
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRunShowCorruptManifestFails(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// A ref that resolves but holds an unparseable manifest must fail at the
	// show entry point, naming the run id, with nothing written to stdout.
	seedRunWithBadManifest(t, repo, "corrupt-show")

	stdout, stderr, err := execute("run", "show", "corrupt-show")
	if err == nil {
		t.Fatal("run show with corrupt manifest returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "corrupt-show") {
		t.Fatalf("error does not name the offending run id 'corrupt-show': %q", combined)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout on corrupt-manifest failure, got: %q", stdout)
	}
}

func TestRunShowUnknownRunID(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("run", "show", "no-such-run")
	if err == nil {
		t.Fatal("run show unknown run returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "not found") {
		t.Fatalf("error does not mention 'not found': %q", combined)
	}
	if !strings.Contains(combined, "no-such-run") {
		t.Fatalf("error does not mention the run id 'no-such-run': %q", combined)
	}
	if stdout != "" {
		t.Fatalf("stdout not empty: %q", stdout)
	}
}

func TestRunShowInvalidRunIDBeforeGit(t *testing.T) {
	// Validation must happen before any git call — prove it by running in a non-repo dir.
	dir := t.TempDir()
	chdir(t, dir)

	cases := []struct {
		name string
		id   string
		want string
	}{
		{"slash in id", "bad/id", "invalid run id"},
		{"double dot", "..", "invalid run id"},
		{"lock suffix", "x.lock", "invalid run id"},
		{"leading dot", ".hidden", "invalid run id"},
		{"trailing dot", "myrun.", "invalid run id"},
		{"all dots", "...", "invalid run id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := execute("run", "show", tc.id)
			if err == nil {
				t.Fatalf("run show %q returned nil error", tc.id)
			}
			combined := err.Error() + " " + stderr
			if !strings.Contains(combined, tc.want) {
				t.Fatalf("error %q does not contain %q", combined, tc.want)
			}
			if stdout != "" {
				t.Fatalf("stdout not empty: %q", stdout)
			}
			// Must NOT say "not a git repository" — that would mean we hit git before validation.
			if strings.Contains(combined, "not a git repository") {
				t.Fatalf("validation ran after git check: %q", combined)
			}
		})
	}
}

func TestRunShowFullProducer(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output content")
	chdir(t, repo)

	_, stderr, err := execute("capture", "plan",
		"--run", "full-producer-run",
		"--output", "output=out.md",
		"--harness", "claude-code",
		"--harness-version", "2.1.150",
		"--model", "claude-opus-4-7",
		"--skill-id", "dev-planner",
		"--skill-repo", "codewithjv-agent-skills",
		"--skill-version", "v3",
	)
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "show", "full-producer-run")
	if err != nil {
		t.Fatalf("run show returned error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "harness:     claude-code 2.1.150") {
		t.Fatalf("expected 'harness:     claude-code 2.1.150' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "model:       claude-opus-4-7") {
		t.Fatalf("expected 'model:       claude-opus-4-7' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "skill:       dev-planner@v3 (codewithjv-agent-skills)") {
		t.Fatalf("expected 'skill:       dev-planner@v3 (codewithjv-agent-skills)' in output:\n%s", stdout)
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRunShowPartialProducer(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output content")
	chdir(t, repo)

	_, stderr, err := execute("capture", "plan",
		"--run", "partial-producer-run",
		"--output", "output=out.md",
		"--harness", "claude-code",
		"--harness-version", "1.0",
		"--skill-id", "dev-planner",
		"--skill-repo", "codewithjv-agent-skills",
		"--skill-version", "v2",
	)
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "show", "partial-producer-run")
	if err != nil {
		t.Fatalf("run show returned error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "harness:     claude-code 1.0") {
		t.Fatalf("expected 'harness:     claude-code 1.0' in output:\n%s", stdout)
	}
	if strings.Contains(stdout, "  model:") {
		t.Fatalf("did not expect 'model:' line when --model not provided:\n%s", stdout)
	}
	if !strings.Contains(stdout, "skill:       dev-planner@v2 (codewithjv-agent-skills)") {
		t.Fatalf("expected 'skill:       dev-planner@v2 (codewithjv-agent-skills)' in output:\n%s", stdout)
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func seedRunWithBadManifest(t *testing.T, repo, runID string) {
	t.Helper()
	store := refstore.New(repo)
	bad := []byte(`not valid json`)
	if _, err := store.WriteCommit(context.Background(), fmt.Sprintf("refs/etude/runs/%s", runID), map[string][]byte{"manifest.json": bad}, refstore.WriteOptions{}); err != nil {
		t.Fatalf("WriteCommit bad manifest returned error: %v", err)
	}
}
