package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
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

func TestRunShowJSON(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output content")
	chdir(t, repo)

	if _, stderr, err := execute("capture", "plan",
		"--run", "json-run",
		"--output", "output=out.md",
	); err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "show", "json-run", "--json")
	if err != nil {
		t.Fatalf("run show --json returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// Output must be the canonical on-disk manifest schema (snake_case,
	// re-ingestible), NOT the bare Go struct (PascalCase, lossy).
	var manifest map[string]any
	if err := json.Unmarshal([]byte(stdout), &manifest); err != nil {
		t.Fatalf("run show --json did not emit valid JSON: %v\noutput:\n%s", err, stdout)
	}
	if strings.Contains(stdout, "run id:") {
		t.Fatalf("--json must not emit the human detail view:\n%s", stdout)
	}
	if manifest["run_id"] != "json-run" {
		t.Fatalf("--json missing snake_case run_id=json-run:\n%s", stdout)
	}
	if _, ok := manifest["stages"]; !ok {
		t.Fatalf("--json missing 'stages' key:\n%s", stdout)
	}
	if strings.Contains(stdout, `"RunID"`) || strings.Contains(stdout, `"ManifestVersion"`) {
		t.Fatalf("--json leaked PascalCase Go struct fields (bare marshal):\n%s", stdout)
	}
	// Decisive contract check: the output must be re-ingestible by etude's own
	// manifest parser (the buggy bare-struct marshal failed this).
	if _, err := runmanifest.ParseJSON([]byte(stdout)); err != nil {
		t.Fatalf("run show --json output not parseable by ParseJSON: %v\noutput:\n%s", err, stdout)
	}
}

func TestRunShowJSONIncludesGates(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output content")
	chdir(t, repo)

	if _, stderr, err := execute("capture", "plan",
		"--run", "gate-run", "--output", "output=out.md",
	); err != nil {
		t.Fatalf("capture: %v\nstderr: %s", err, stderr)
	}
	gateFile := writeGateJSON(t, repo, "gate.json", "plan.r1", "plan", 1, "")
	if _, stderr, err := execute("capture-gate",
		"--run", "gate-run", "--gate-file", gateFile,
	); err != nil {
		t.Fatalf("capture-gate: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "show", "gate-run", "--json")
	if err != nil {
		t.Fatalf("run show --json: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// Gate attempts must survive into --json in the snake_case wire schema and
	// stay re-ingestible — this is the bead's core "incl gate attempts" claim.
	parsed, err := runmanifest.ParseJSON([]byte(stdout))
	if err != nil {
		t.Fatalf("--json (with gate) not parseable by ParseJSON: %v\noutput:\n%s", err, stdout)
	}
	if len(parsed.Gates) != 1 || parsed.Gates[0].GateID != "plan.r1" {
		t.Fatalf("--json did not carry the gate attempt faithfully; gates=%+v\noutput:\n%s", parsed.Gates, stdout)
	}
	if !strings.Contains(stdout, `"gate_id"`) {
		t.Fatalf("--json gates not in snake_case wire schema:\n%s", stdout)
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

// TestRunShowReplayOfLine verifies that run show prints a "replay of:" line
// for a stage with ReplayOf set (produced_by:replay).
func TestRunShowReplayOfLine(t *testing.T) {
	repo, sourceRunID := captureStageForReplay(t)
	chdir(t, repo)

	// Record a replay run using the record path.
	fixedTime := time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)
	replayRunID := sourceRunID + "-replay-20260522T103000Z"
	stub := &replay.StubRunner{CannedOutput: []byte("show-replay-output")}

	r := &replayRunner{runner: stub, now: func() time.Time { return fixedTime }}
	var out, errOut bytes.Buffer
	cmd := buildReplayCommand(&out, &errOut, r)
	cmd.SetArgs([]string{sourceRunID, "gen", "--record"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("replay --record returned error: %v\nstderr: %s", err, errOut.String())
	}

	// run show on the replay run must include "replay of:".
	stdout, stderr, err := execute("run", "show", replayRunID)
	if err != nil {
		t.Fatalf("run show returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "replay of:") {
		t.Fatalf("expected 'replay of:' in run show output:\n%s", stdout)
	}
	if !strings.Contains(stdout, sourceRunID+"/gen") {
		t.Fatalf("expected source run id %q in replay of line:\n%s", sourceRunID+"/gen", stdout)
	}
	if !strings.Contains(stdout, "produced_by: replay") {
		t.Fatalf("expected 'produced_by: replay' in output:\n%s", stdout)
	}
}

// TestRunShowRendersGates verifies run show prints a gates section: per-attempt
// header + status, per-seat provider/verdict, conditional skill/required/note/
// degraded lines, manifest ordering, and that a go seat prints no note line.
func TestRunShowRendersGates(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output content")
	chdir(t, repo)

	if _, stderr, err := execute("capture", "plan",
		"--run", "gates-run", "--output", "output=out.md",
		"--skill-id", "dev-planner", "--skill-repo", "r", "--skill-version", "v1",
	); err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	// A rich first attempt: a block seat with required feedback, a go seat that
	// carries a skill identity, and a disregarded seat with a failure note +
	// a decision degraded reason.
	rich := `{
  "gate_id": "plan.r1", "phase": "plan", "round": 1, "tier": 2, "status": "rerun",
  "reviewed_stages": [{"stage":"plan","role":"plan"}],
  "seats": [
    {"seat":"gemini","harness":{"name":"gemini-cli","version":"3.1"},"provider":{"name":"google","model":"gemini-3.1-pro-preview"},"verdict":"block","required":["specify the validation rule"],"timestamp":"2026-05-26T01:00:00Z"},
    {"seat":"opus","harness":{"name":"claude-code","version":"o47"},"provider":{"name":"anthropic","model":"claude-opus-4-7"},"skill":{"id":"dev-pr-reviewer","repo":"codewithjv-agent-skills","version":"v3"},"verdict":"go","timestamp":"2026-05-26T01:01:00Z"},
    {"seat":"pilms","harness":{"name":"pi","version":"x"},"provider":{"name":"lmstudio","model":"qwen/qwen3.6-35b-a3b"},"verdict":"disregarded","failure_note":"0-CPU client hang","timestamp":"2026-05-26T01:02:00Z"}
  ],
  "decision": {"degraded_reason":"pilms outage; 3 substantive GO"},
  "timestamp": "2026-05-26T01:05:00Z"
}`
	writeFile(t, repo, "g1.json", rich)
	if _, stderr, err := execute("capture-gate", "--run", "gates-run", "--gate-file", "g1.json"); err != nil {
		t.Fatalf("capture-gate r1 error: %v\nstderr: %s", err, stderr)
	}
	// A second, simpler attempt (pass) to exercise ordering + multiple gates.
	g2 := writeGateJSON(t, repo, "g2.json", "plan.r2", "plan", 2, "")
	if _, stderr, err := execute("capture-gate", "--run", "gates-run", "--gate-file", g2); err != nil {
		t.Fatalf("capture-gate r2 error: %v\nstderr: %s", err, stderr)
	}
	// A third attempt exercising the remaining render branches: escalated status
	// + escalation_reason + deferred_beads (decision), a go seat with optional
	// feedback, and a failed seat with a note + a raw_output transcript artifact.
	writeFile(t, repo, "codex-transcript.txt", "codex transcript bytes")
	esc := `{
  "gate_id": "plan.r3", "phase": "plan", "round": 3, "tier": 1, "status": "escalated",
  "reviewed_stages": [{"stage":"plan","role":"plan"}],
  "seats": [
    {"seat":"opus","harness":{"name":"claude-code","version":"o47"},"provider":{"name":"anthropic","model":"claude-opus-4-7"},"verdict":"go","optional":["consider caching the result"],"timestamp":"2026-05-26T04:00:00Z"},
    {"seat":"codex","harness":{"name":"codex","version":"x"},"provider":{"name":"openai","model":"gpt-5.5"},"verdict":"failed","failure_note":"401 auth error","raw_output":{"role":"codex-transcript","path":"codex-transcript.txt","media_type":"text/plain"},"timestamp":"2026-05-26T04:01:00Z"}
  ],
  "decision": {"escalation_reason":"codex auth failure; no autonomous fallback","deferred_beads":["etude-followup"]},
  "timestamp": "2026-05-26T04:05:00Z"
}`
	writeFile(t, repo, "g3.json", esc)
	if _, stderr, err := execute("capture-gate", "--run", "gates-run", "--gate-file", "g3.json"); err != nil {
		t.Fatalf("capture-gate r3 error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "show", "gates-run")
	if err != nil {
		t.Fatalf("run show returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	wants := []string{
		"gate: plan.r1", "gate: plan.r2", "gate: plan.r3",
		"  status:   rerun", "  status:   pass", "  status:   escalated",
		"  reviewed: plan (role=plan)",
		"  degraded: pilms outage; 3 substantive GO",
		"  escalation: codex auth failure; no autonomous fallback",
		"  deferred: etude-followup",
		"    provider: google / gemini-3.1-pro-preview",
		"    provider: lmstudio / qwen/qwen3.6-35b-a3b",
		"    harness:  gemini-cli 3.1",
		"    harness:  claude-code o47",
		"    verdict:  block", "    verdict:  go", "    verdict:  disregarded", "    verdict:  failed",
		"    required:",
		"      - specify the validation rule",
		"    optional:",
		"      - consider caching the result",
		"    note:     0-CPU client hang",
		"    note:     401 auth error",
		"    raw_output: artifacts/sha256/",
		"    skill:    dev-pr-reviewer@v3 (codewithjv-agent-skills)",
	}
	for _, w := range wants {
		if !strings.Contains(stdout, w) {
			t.Fatalf("expected %q in run show output:\n%s", w, stdout)
		}
	}
	// Manifest order: plan.r1 before plan.r2 before plan.r3.
	if !(strings.Index(stdout, "gate: plan.r1") < strings.Index(stdout, "gate: plan.r2") &&
		strings.Index(stdout, "gate: plan.r2") < strings.Index(stdout, "gate: plan.r3")) {
		t.Fatalf("expected gates in manifest order plan.r1 < plan.r2 < plan.r3:\n%s", stdout)
	}
	// Exactly the two notes that should exist render (pilms disregarded + codex
	// failed); no go/block seat renders a note. Count-based so it is not fooled
	// by field ordering within a seat block.
	if n := strings.Count(stdout, "    note:     "); n != 2 {
		t.Fatalf("expected exactly 2 seat note lines (disregarded + failed), got %d:\n%s", n, stdout)
	}
	// skill line renders only for the one seat that carries a skill (opus on r1).
	if n := strings.Count(stdout, "    skill:    "); n != 1 {
		t.Fatalf("expected exactly 1 seat skill line, got %d:\n%s", n, stdout)
	}
}

// TestRunShowNoGatesUnchanged verifies a gate-less run prints no gate section.
func TestRunShowNoGatesUnchanged(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output content")
	chdir(t, repo)

	if _, stderr, err := execute("capture", "plan",
		"--run", "no-gates-run", "--output", "output=out.md",
		"--skill-id", "dev-planner", "--skill-repo", "r", "--skill-version", "v1",
	); err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("run", "show", "no-gates-run")
	if err != nil {
		t.Fatalf("run show returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
	if strings.Contains(stdout, "gate:") {
		t.Fatalf("did not expect any gate section for a gate-less run:\n%s", stdout)
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
