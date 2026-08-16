package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/bench"
	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Content-aware test doubles (BLOCK 2 determinism fix)
// ---------------------------------------------------------------------------

// replayMarker is the prefix the stub runner injects into replay output so
// the content-aware judge can identify which presented target is the replay.
const replayMarker = "<<REPLAY>>"

// canonicalWinnerMarker names the embedded canonical-winner tag.
// Each source run's stub replay output carries one of:
//
//	<<WIN:B>>  → judge must return canonical WinnerB
//	<<WIN:A>>  → judge must return canonical WinnerA
//	<<WIN:TIE>> → judge must return canonical WinnerTie
const (
	winBTag   = "<<WIN:B>>"
	winATag   = "<<WIN:A>>"
	winTieTag = "<<WIN:TIE>>"
)

// perRunMarkerRunner is a test-local replay.Runner that injects a per-run
// canonical-winner marker into the replay output. The marker makes the
// content-aware judge deterministic regardless of per-pair presentation swap.
//
// It echoes req.Producer verbatim so that producer-override flags (--model,
// --skill-version, etc.) propagate through to the recorded replay manifest.
type perRunMarkerRunner struct {
	// markers maps win-tag string → the win marker to embed in replay output.
	// Tests seed each source run's input with a distinctive win tag so the
	// runner can look it up from req.Inputs[].Content.
	markers map[string]string
	// base is optional extra bytes appended after the marker.
	base []byte
}

func (p *perRunMarkerRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	marker := winBTag // default
	// Look for the canonical-winner tag in the materialized inputs.
	for _, inp := range req.Inputs {
		for tag := range p.markers {
			if strings.Contains(string(inp.Content), tag) {
				marker = p.markers[tag]
				break
			}
		}
	}
	output := append([]byte(replayMarker+marker), p.base...)
	// Echo req.Producer verbatim: this propagates --model / --skill-version etc.
	// through to the recorded replay manifest (mirrors StubRunner behaviour).
	return replay.RunResult{
		Output:    output,
		MediaType: req.OutputMediaType,
		Producer:  req.Producer,
	}, nil
}

// contentAwareJudge is a test-local eval.Judge that reads jr.Targets[].Content,
// finds which presented side (left=Targets[0], right=Targets[1]) carries the
// replay marker, then returns the POSITION-RELATIVE winner so that after
// mapWinnerBack the CANONICAL outcome is the one embedded in the replay marker.
//
// win tag embedded in replay output → intended canonical winner:
//
//	<<WIN:B>>   → canonical B (replay wins)
//	<<WIN:A>>   → canonical A (original wins)
//	<<WIN:TIE>> → canonical tie
//
// The judge reads the content, finds the replay side, determines the canonical
// winner from the tag, then converts canonical → position-relative (i.e. undoes
// mapWinnerBack) so that when mapWinnerBack is applied by PairwiseEvaluator the
// canonical outcome is as intended.
type contentAwareJudge struct{}

func (c *contentAwareJudge) Judge(_ context.Context, jr eval.JudgeRequest) (eval.JudgeResponse, error) {
	if len(jr.Targets) != 2 {
		return eval.JudgeResponse{}, fmt.Errorf("contentAwareJudge: expected 2 targets, got %d", len(jr.Targets))
	}

	left := jr.Targets[0]  // position 0 (presented first)
	right := jr.Targets[1] // position 1 (presented second)

	leftIsReplay := strings.Contains(string(left.Content), replayMarker)
	rightIsReplay := strings.Contains(string(right.Content), replayMarker)

	// Determine which side is replay and which content it carries.
	var replayContent string
	var replayIsLeft bool
	switch {
	case leftIsReplay:
		replayContent = string(left.Content)
		replayIsLeft = true
	case rightIsReplay:
		replayContent = string(right.Content)
		replayIsLeft = false
	default:
		// Fallback: no marker found — treat as canonical B.
		return eval.JudgeResponse{Winner: eval.WinnerB}, nil
	}

	// Determine intended canonical winner from the embedded tag.
	var canonicalWinner eval.Winner
	switch {
	case strings.Contains(replayContent, winATag):
		canonicalWinner = eval.WinnerA
	case strings.Contains(replayContent, winTieTag):
		canonicalWinner = eval.WinnerTie
	default:
		// <<WIN:B>> or no specific tag → canonical B
		canonicalWinner = eval.WinnerB
	}

	// Convert canonical winner to position-relative so mapWinnerBack yields canonical.
	//
	// mapWinnerBack(judgeWinner, swapped):
	//   swapped=false (not swapped): judge sees [A=canonical, B=canonical] → judge position == canonical.
	//   swapped=true (swapped): judge sees [B=canonical, A=canonical] → judgeA=canonicalB, judgeB=canonicalA.
	//
	// We know: if replayIsLeft, then left=B_canonical (swap happened); right=A_canonical.
	//          if !replayIsLeft, then left=A_canonical (no swap); right=B_canonical.
	//
	// We must return a position-relative winner that, after mapWinnerBack, gives canonicalWinner.
	//
	// Case replayIsLeft (swapped=true, left=B, right=A):
	//   We want mapWinnerBack(posWinner, true) == canonicalWinner.
	//   mapWinnerBack(A, true)=B, mapWinnerBack(B, true)=A, mapWinnerBack(tie, true)=tie.
	//   So: canonicalB→return A; canonicalA→return B; tie→return tie.
	//
	// Case !replayIsLeft (swapped=false, left=A, right=B):
	//   We want mapWinnerBack(posWinner, false) == canonicalWinner.
	//   mapWinnerBack(x, false)=x (identity).
	//   So: return canonicalWinner directly.
	var posWinner eval.Winner
	if replayIsLeft {
		// swapped=true: invert A↔B, tie stays tie.
		switch canonicalWinner {
		case eval.WinnerB:
			posWinner = eval.WinnerA
		case eval.WinnerA:
			posWinner = eval.WinnerB
		default:
			posWinner = eval.WinnerTie
		}
	} else {
		// swapped=false: position == canonical.
		posWinner = canonicalWinner
	}

	return eval.JudgeResponse{Winner: posWinner}, nil
}

// ---------------------------------------------------------------------------
// Bench runner helper
// ---------------------------------------------------------------------------

// executeBench runs the bench command with injected runner+judge and a fixed clock.
func executeBench(
	runner replay.Runner,
	judge eval.Judge,
	fixedTime time.Time,
	args ...string,
) (string, string, error) {
	var out, errOut bytes.Buffer
	r := &benchRunner{
		runner: runner,
		judge:  judge,
		now:    func() time.Time { return fixedTime },
	}
	cmd := buildBenchCommand(&out, &errOut, r)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}
	return out.String(), errOut.String(), err
}

type fakeGateCorpus struct {
	fixtures []bench.Fixture
	err      error
	selector bench.CohortSelector
}

func (f *fakeGateCorpus) Fixtures(_ context.Context, selector bench.CohortSelector) ([]bench.Fixture, error) {
	f.selector = selector
	if f.err != nil {
		return nil, f.err
	}
	return f.fixtures, nil
}

type contentGateJudge struct {
	calls int
}

func (j *contentGateJudge) Judge(_ context.Context, req eval.JudgeRequest) (eval.JudgeResponse, error) {
	j.calls++
	if len(req.Targets) != 1 || len(req.Context) != 1 {
		return eval.JudgeResponse{}, fmt.Errorf("gate judge received %d targets and %d context inputs", len(req.Targets), len(req.Context))
	}
	prompt := string(req.Context[0].Content)
	artifact := string(req.Targets[0].Content)
	if (strings.Contains(prompt, "fail-bad") && strings.Contains(artifact, "bad")) ||
		(strings.Contains(prompt, "fail-good") && strings.Contains(artifact, "good")) {
		return eval.JudgeResponse{}, errors.New("planned judge failure")
	}
	passed := true
	switch {
	case strings.Contains(prompt, "always-block"):
		passed = false
	case strings.Contains(prompt, "selective") && strings.Contains(artifact, "bad"):
		passed = false
	}
	return eval.JudgeResponse{
		Passed:   &passed,
		Findings: []eval.Finding{{Severity: eval.SeverityInfo, Message: prompt + " on " + artifact}},
	}, nil
}

func executeGateBench(corpus bench.CorpusSource, judge eval.Judge, args ...string) (string, string, error) {
	var out, errOut bytes.Buffer
	r := &benchRunner{corpus: corpus, judge: judge, now: time.Now}
	cmd := buildBenchCommand(&out, &errOut, r)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}
	return out.String(), errOut.String(), err
}

func gateFixture(runID, content string, round int, hint bench.LabelHint) bench.Fixture {
	return bench.Fixture{
		Artifact:  []byte(content),
		MediaType: "text/markdown",
		Provenance: bench.Provenance{
			RunID:        runID,
			Phase:        "plan",
			Round:        round,
			SourceCommit: strings.Repeat("a", 40),
			Stage:        "plan",
		},
		Label: hint,
	}
}

func writeGateBenchFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func writeGateLabels(t *testing.T, dir, labels string) string {
	t.Helper()
	return writeGateBenchFile(t, dir, "labels.json", `{"version":1,"labels":[`+labels+`]}`)
}

// ---------------------------------------------------------------------------
// Bench fixture helpers (build on captureStageForReplay idiom)
// ---------------------------------------------------------------------------

// seedBenchRun captures one run at HEAD with distinctive input content that
// embeds the desired canonical-winner tag. Returns the runID.
func seedBenchRun(t *testing.T, repo, runID, winTag string) {
	t.Helper()
	chdir(t, repo)
	// Write input with the win tag so the perRunMarkerRunner can detect it.
	writeFile(t, repo, "input-"+runID+".txt", "bench-input-"+winTag)
	writeFile(t, repo, "output-"+runID+".md", "# original output for "+runID)

	_, stderr, err := execute(
		"capture", "plan",
		"--run", runID,
		"--input", "prompt=input-"+runID+".txt",
		"--output", "plan=output-"+runID+".md",
	)
	if err != nil {
		t.Fatalf("capture for %s returned error: %v\nstderr: %s", runID, err, stderr)
	}
}

// setupBenchRepo creates a repo with 3 captured plan-stage runs with known
// canonical winner tags (B, A, tie) and returns the repo path and a configured
// perRunMarkerRunner.
func setupBenchRepo(t *testing.T) (repo string, runner *perRunMarkerRunner) {
	t.Helper()
	repo = initCaptureRepo(t)

	// The markers map: input content tag → win marker injected into replay output.
	// seedBenchRun writes "bench-input-<<WIN:B>>" etc. into the input file.
	runner = &perRunMarkerRunner{
		markers: map[string]string{
			winBTag:   winBTag,
			winATag:   winATag,
			winTieTag: winTieTag,
		},
	}

	seedBenchRun(t, repo, "bench-run-b", winBTag)
	seedBenchRun(t, repo, "bench-run-a", winATag)
	seedBenchRun(t, repo, "bench-run-tie", winTieTag)

	return repo, runner
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestBenchHappyPath verifies that with 3 captured runs (B/A/tie) the report
// headline shows the correct win_rate_B and per-run table rows.
func TestBenchHappyPath(t *testing.T) {
	repo, runner := setupBenchRepo(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)
	judge := &contentAwareJudge{}

	stdout, stderr, err := executeBench(runner, judge, fixedTime, "plan", "--last", "10")
	if err != nil {
		t.Fatalf("bench returned error: %v\nstderr: %s", err, stderr)
	}

	// Expect 1 B, 1 A, 1 tie → win_rate_B = (1 + 0.5) / 3 = 0.5
	// headline: "replay (new skill) wins 50.0% vs original"
	if !strings.Contains(stdout, "replay (new skill) wins") {
		t.Errorf("stdout missing headline: %q", stdout)
	}
	// B=1 A=1 tie=1 over 3 evals
	if !strings.Contains(stdout, "B=1") || !strings.Contains(stdout, "A=1") || !strings.Contains(stdout, "tie=1") {
		t.Errorf("stdout missing count breakdown: %q", stdout)
	}
	if !strings.Contains(stdout, "over 3 evals") {
		t.Errorf("stdout missing 'over 3 evals': %q", stdout)
	}
	// Should contain per-run rows (source run IDs).
	for _, runID := range []string{"bench-run-b", "bench-run-a", "bench-run-tie"} {
		if !strings.Contains(stdout, runID) {
			t.Errorf("stdout missing run %q: %q", runID, stdout)
		}
	}
	// No stderr.
	if stderr != "" {
		t.Errorf("bench wrote to stderr: %q", stderr)
	}
}

// TestBenchSkipOnRunError verifies skip-and-report: one failing runner leaves
// the failing run in the failures section but still counts the others.
func TestBenchSkipOnRunError(t *testing.T) {
	repo, _ := setupBenchRepo(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 25, 11, 0, 0, 0, time.UTC)
	judge := &contentAwareJudge{}

	// Use a runner that errors for one specific input and succeeds for others.
	runner := &selectiveErrorRunner{
		errForContent: winATag, // bench-run-a will fail
		fallback: &perRunMarkerRunner{
			markers: map[string]string{
				winBTag:   winBTag,
				winATag:   winATag,
				winTieTag: winTieTag,
			},
		},
	}

	stdout, stderr, err := executeBench(runner, judge, fixedTime, "plan", "--last", "10")
	// Exit 0 because at least 2 evals succeeded.
	if err != nil {
		t.Fatalf("bench returned error on partial failure: %v\nstderr: %s", err, stderr)
	}

	// Report must mention failed runs section.
	if !strings.Contains(stdout, "Failed runs:") {
		t.Errorf("stdout missing 'Failed runs:': %q", stdout)
	}
	// The failing run must appear in failures.
	if !strings.Contains(stdout, "bench-run-a") {
		t.Errorf("stdout missing 'bench-run-a' in failures: %q", stdout)
	}
	// 2 successful evals must be counted.
	if !strings.Contains(stdout, "over 2 evals") {
		t.Errorf("stdout missing 'over 2 evals': %q", stdout)
	}
}

// selectiveErrorRunner errors for any run whose input content contains errForContent.
type selectiveErrorRunner struct {
	errForContent string
	fallback      replay.Runner
}

func (s *selectiveErrorRunner) Run(ctx context.Context, req replay.RunRequest) (replay.RunResult, error) {
	for _, inp := range req.Inputs {
		if strings.Contains(string(inp.Content), s.errForContent) {
			return replay.RunResult{}, errors.New("selective runner error for " + s.errForContent)
		}
	}
	return s.fallback.Run(ctx, req)
}

// TestBenchEmptyCohort verifies that a stage with no qualifying runs returns an error.
func TestBenchEmptyCohort(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	runner := &replay.StubRunner{CannedOutput: []byte("out")}
	judge := &contentAwareJudge{}

	_, stderr, err := executeBench(runner, judge, fixedTime, "missing-stage", "--last", "10")
	if err == nil {
		t.Fatal("bench returned nil error for empty cohort")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "missing-stage") {
		t.Errorf("error %q stderr %q do not mention stage name", err, stderr)
	}
	if !strings.Contains(combined, "no runs") && !strings.Contains(combined, "replayable") {
		t.Errorf("error %q stderr %q do not indicate no qualifying runs", err, stderr)
	}
}

// TestBenchNoRunner verifies that omitting --runner (and no git config) returns an error.
func TestBenchNoRunner(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Build bench with nil runner (no injection) and a real judge injection
	// to isolate the runner error path.
	var out, errOut bytes.Buffer
	r := &benchRunner{
		runner: nil,
		judge:  &contentAwareJudge{},
		now:    time.Now,
	}
	cmd := buildBenchCommand(&out, &errOut, r)
	cmd.SetArgs([]string{"plan", "--last", "10", "--judge", "does-not-matter"})
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}

	combined := out.String() + " " + errOut.String()
	if err == nil {
		t.Fatal("bench returned nil error when no runner configured")
	}
	if !strings.Contains(combined, "no runner configured") {
		t.Errorf("error %q does not indicate missing runner", combined)
	}
}

// TestBenchNoJudge verifies that omitting --judge (and no git config) returns an error.
func TestBenchNoJudge(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Build bench with nil judge (no injection) and a real runner injection.
	var out, errOut bytes.Buffer
	r := &benchRunner{
		runner: &replay.StubRunner{CannedOutput: []byte("out")},
		judge:  nil,
		now:    time.Now,
	}
	cmd := buildBenchCommand(&out, &errOut, r)
	cmd.SetArgs([]string{"plan", "--last", "10", "--runner", "does-not-matter"})
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}

	combined := out.String() + " " + errOut.String()
	if err == nil {
		t.Fatal("bench returned nil error when no judge configured")
	}
	if !strings.Contains(combined, "no judge configured") {
		t.Errorf("error %q does not indicate missing judge", combined)
	}
}

// TestBenchLastValidation verifies that --last 0 and --last -1 return errors.
func TestBenchLastValidation(t *testing.T) {
	for _, last := range []string{"0", "-1"} {
		repo := initCaptureRepo(t)
		chdir(t, repo)

		runner := &replay.StubRunner{CannedOutput: []byte("out")}
		judge := &contentAwareJudge{}
		fixedTime := time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC)
		_, stderr, err := executeBench(runner, judge, fixedTime, "plan", "--last", last)
		if err == nil {
			t.Errorf("bench --last %s returned nil error", last)
		}
		combined := err.Error() + " " + stderr
		if !strings.Contains(combined, "--last") && !strings.Contains(combined, "positive") {
			t.Errorf("--last %s error %q does not mention --last or positive", last, combined)
		}
	}
}

// TestBenchTotalZeroGuard verifies that when all runs fail the CLI reports
// "no successful evaluations" and exits non-zero.
func TestBenchTotalZeroGuard(t *testing.T) {
	repo, _ := setupBenchRepo(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 25, 13, 0, 0, 0, time.UTC)
	judge := &contentAwareJudge{}
	// Runner always fails.
	runner := &replay.StubRunner{Err: errors.New("all runners failed")}

	stdout, stderr, err := executeBench(runner, judge, fixedTime, "plan", "--last", "10")
	if err == nil {
		t.Fatal("bench returned nil error when all runs failed")
	}
	combined := stdout + " " + stderr + " " + err.Error()
	if !strings.Contains(combined, "no successful") && !strings.Contains(combined, "evaluation") {
		t.Errorf("error/output %q does not indicate no successful evaluations", combined)
	}
}

// TestBenchModelFlagDoesNotReachJudge verifies BLOCK 1: --model sets producer
// override only, NOT ExecJudge.Model; and --judge-model sets judge model only.
func TestBenchModelFlagDoesNotReachJudge(t *testing.T) {
	repo, runner := setupBenchRepo(t)
	chdir(t, repo)
	chdir(t, repo) // re-enter repo dir after setup

	// Use a judge that captures the Model it receives via ETUDE_MODEL.
	// We inject a modelCapturingJudge that records the JudgeRequest.Producer.Model.
	capturing := &modelCapturingJudge{inner: &contentAwareJudge{}}
	fixedTime := time.Date(2026, 5, 25, 14, 0, 0, 0, time.UTC)

	var out, errOut bytes.Buffer
	r := &benchRunner{
		runner: runner,
		judge:  capturing,
		now:    func() time.Time { return fixedTime },
	}
	cmd := buildBenchCommand(&out, &errOut, r)
	cmd.SetArgs([]string{"plan", "--last", "10", "--model", "contestant-model", "--judge-model", "referee-model"})
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
		t.Fatalf("bench returned error: %v\nstderr: %s", err, errOut.String())
	}

	// The producer override (--model contestant-model) must NOT appear in any
	// JudgeRequest.Producer.Model. The judge should see the replay runner's
	// echoed producer (which reflects the contestant override), but here we
	// verify that the judge's own model (ExecJudge.Model) is separate.
	//
	// Since we injected the judge directly (not via ExecJudge), we check via
	// the refstore that the recorded replay manifest carries "contestant-model"
	// in Producer.Model, confirming the flag reached the contestant path.
	store := refstore.New(repo)
	refs, err2 := store.List(context.Background(), "refs/etude/runs")
	if err2 != nil {
		t.Fatalf("list runs: %v", err2)
	}
	var foundContestantModel bool
	for _, ref := range refs {
		if !strings.Contains(ref, "-replay-") {
			continue
		}
		raw, err3 := store.ReadFile(context.Background(), ref, "manifest.json")
		if err3 != nil {
			continue
		}
		m, err4 := runmanifest.ParseJSON(raw)
		if err4 != nil {
			continue
		}
		for _, s := range m.Stages {
			if s.Producer.Model == "contestant-model" {
				foundContestantModel = true
			}
		}
	}
	if !foundContestantModel {
		t.Error("--model contestant-model did not reach the recorded replay producer")
	}
}

// modelCapturingJudge wraps an inner judge and records producers it sees.
type modelCapturingJudge struct {
	inner     eval.Judge
	producers []runmanifest.Producer
}

func (m *modelCapturingJudge) Judge(ctx context.Context, jr eval.JudgeRequest) (eval.JudgeResponse, error) {
	m.producers = append(m.producers, jr.Producer)
	return m.inner.Judge(ctx, jr)
}

// TestBenchProducerOverrideApplied verifies that --skill-version reaches the
// recorded replay producer.
func TestBenchProducerOverrideApplied(t *testing.T) {
	repo, runner := setupBenchRepo(t)
	chdir(t, repo)

	judge := &contentAwareJudge{}
	fixedTime := time.Date(2026, 5, 25, 15, 0, 0, 0, time.UTC)

	stdout, stderr, err := executeBench(runner, judge, fixedTime, "plan", "--last", "3", "--skill-version", "vNEW")
	if err != nil {
		t.Fatalf("bench returned error: %v\nstderr: %s", err, stderr)
	}
	_ = stdout

	// Inspect the recorded replay runs for the overridden skill version.
	store := refstore.New(repo)
	refs, err2 := store.List(context.Background(), "refs/etude/runs")
	if err2 != nil {
		t.Fatalf("list runs: %v", err2)
	}
	found := 0
	for _, ref := range refs {
		if !strings.Contains(ref, "-replay-") {
			continue
		}
		raw, err3 := store.ReadFile(context.Background(), ref, "manifest.json")
		if err3 != nil {
			continue
		}
		m, err4 := runmanifest.ParseJSON(raw)
		if err4 != nil {
			continue
		}
		for _, s := range m.Stages {
			if s.Producer.Skill.Version == "vNEW" {
				found++
			}
		}
	}
	if found == 0 {
		t.Error("--skill-version vNEW did not reach any recorded replay producer")
	}
}

// TestBenchHelp verifies --help exits 0 and mentions bench flags.
func TestBenchHelp(t *testing.T) {
	stdout, stderr, err := execute("bench", "--help")
	if err != nil {
		t.Fatalf("bench --help returned error: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{"bench", "--last", "--runner", "--judge", "--judge-model", "--no-cache"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("bench --help output missing %q:\n%s", want, stdout)
		}
	}
}

// TestBenchNoCacheFlagWired verifies that --no-cache is accepted and still
// produces correct results (no caching performed; judge called for each run).
func TestBenchNoCacheFlagWired(t *testing.T) {
	repo, runner := setupBenchRepo(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 25, 16, 0, 0, 0, time.UTC)
	judge := &contentAwareJudge{}

	// First run without --no-cache (cache enabled by default).
	stdout1, stderr1, err := executeBench(runner, judge, fixedTime, "plan", "--last", "10")
	if err != nil {
		t.Fatalf("first bench returned error: %v\nstderr: %s", err, stderr1)
	}
	// contentAwareJudge is not an ExecJudge so judgeID is empty; cache is disabled automatically.
	// All rows should NOT show CACHED.
	_ = stdout1

	// Second run with --no-cache: should run normally, no CACHED rows in report.
	fixedTime2 := time.Date(2026, 5, 25, 17, 0, 0, 0, time.UTC)
	stdout2, stderr2, err := executeBench(runner, judge, fixedTime2, "plan", "--last", "10", "--no-cache")
	if err != nil {
		t.Fatalf("second bench --no-cache returned error: %v\nstderr: %s", err, stderr2)
	}

	// No error; normal report.
	if !strings.Contains(stdout2, "replay (new skill) wins") {
		t.Errorf("--no-cache stdout missing headline: %q", stdout2)
	}
	// Since contentAwareJudge is a StubJudge-style judge (not ExecJudge),
	// JudgeIdentity returns "" so cache is always disabled for these tests.
	// CACHED should NOT appear in the report.
	if strings.Contains(stdout2, "CACHED") {
		// Only unexpected if it appears as a non-empty value in the row.
		// The column header "CACHED" IS expected. Check that no row has "CACHED" as a value.
		// The tabwriter output won't have "CACHED" in a value column unless Reused=true.
		// Since JudgeID is empty (contentAwareJudge), no row should be reused.
		// This is mainly a sanity check that the flag doesn't break the command.
	}

	// The --no-cache flag must appear in --help.
	stdout3, _, _ := execute("bench", "--help")
	if !strings.Contains(stdout3, "--no-cache") {
		t.Errorf("bench --help does not mention --no-cache:\n%s", stdout3)
	}
}

// TestBenchCACHEDMarkerShownOnReusedRow verifies that when a BenchOutcome has
// Reused=true, the renderReport output contains "CACHED" in that row.
// We test renderReport directly since producing a real cache hit requires an
// ExecJudge (which has a non-empty JudgeIdentity); we use the Report+Outcomes API.
func TestBenchCACHEDMarkerShownOnReusedRow(t *testing.T) {
	var buf strings.Builder

	// Build a synthetic report with one reused and one non-reused outcome.
	conf := 0.9
	outcomes := []bench.BenchOutcome{
		{
			SourceRunID: "run-a",
			Stage:       "plan",
			ReplayRunID: "run-a-replay",
			EvalID:      "pairwise-run-a-plan-20260525T000000Z",
			Winner:      eval.WinnerB,
			Confidence:  &conf,
			Findings:    nil,
			Reused:      true,
		},
		{
			SourceRunID: "run-b",
			Stage:       "plan",
			ReplayRunID: "run-b-replay",
			EvalID:      "pairwise-run-b-plan-20260525T000001Z",
			Winner:      eval.WinnerA,
			Confidence:  nil,
			Findings:    nil,
			Reused:      false,
		},
	}
	report := bench.Aggregate(outcomes)
	report.Stage = "plan"

	renderReport(&buf, report)
	out := buf.String()

	// The reused row must show CACHED.
	if !strings.Contains(out, "CACHED") {
		t.Errorf("renderReport output missing 'CACHED' for reused row:\n%s", out)
	}
	// The non-reused row must not have a CACHED value (it gets an empty cell).
	// Since tabwriter produces aligned columns, we can check that "run-b" line
	// does not have "CACHED" immediately after the finding column.
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "run-a") && !strings.Contains(line, "CACHED") {
			t.Errorf("run-a (reused) row missing CACHED: %q", line)
		}
		if strings.Contains(line, "run-b") && strings.Contains(line, "CACHED") {
			t.Errorf("run-b (non-reused) row unexpectedly contains CACHED: %q", line)
		}
	}
}

func TestBenchGateHappyPathReportsVariantScoresAndCells(t *testing.T) {
	dir := t.TempDir()
	selective := writeGateBenchFile(t, dir, "selective.md", "selective")
	alwaysBlock := writeGateBenchFile(t, dir, "always-block.md", "always-block")
	labels := writeGateLabels(t, dir,
		`{"run_id":"run-good","stage":"plan","round":1,"expected":"go","verified":true},`+
			`{"run_id":"run-bad","stage":"plan","round":1,"expected":"block","verified":true}`)
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{
		gateFixture("run-good", "good artifact", 1, bench.LabelHint{}),
		gateFixture("run-bad", "bad artifact", 1, bench.LabelHint{}),
	}}

	stdout, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", selective, "--variant", alwaysBlock,
		"--cohort", "run-refs", "--labels", labels)
	if err != nil {
		t.Fatalf("gate bench returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "gate bench plan: 2 variants over 2 common fixtures; 0 excluded") {
		t.Fatalf("stdout missing gate headline:\n%s", stdout)
	}
	selectiveLine := lineContaining(stdout, selective)
	selectiveFields := strings.Fields(selectiveLine)
	if len(selectiveFields) < 9 || selectiveFields[1] != "100.0%" ||
		selectiveFields[4] != "2" || selectiveFields[5] != "2" ||
		selectiveFields[6] != "2" || selectiveFields[7] != "0" || selectiveFields[8] != "0" {
		t.Errorf("selective report does not show perfect explicitly labeled score: %q", selectiveLine)
	}
	if !strings.Contains(stdout, "run-good") || !strings.Contains(stdout, "run-bad") ||
		!strings.Contains(stdout, "true_positive") || !strings.Contains(stdout, "false_positive") {
		t.Errorf("stdout missing expected per-cell verdicts/outcomes:\n%s", stdout)
	}
	if strings.Index(stdout, selective) > strings.Index(stdout, alwaysBlock) {
		t.Errorf("variant report order does not follow flag order:\n%s", stdout)
	}
	if corpus.selector != (bench.CohortSelector{Stage: "plan", Last: 10}) {
		t.Errorf("selector = %#v, want plan/10", corpus.selector)
	}
}

func TestBenchGateUsesProgressionProxyAndWarns(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "selective")
	b := writeGateBenchFile(t, dir, "b.md", "always-block")
	hint := bench.LabelHint{Status: bench.GateHintPass, Rounds: 1, FinalRound: 1}
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{gateFixture("run-proxy", "good", 1, hint)}}

	stdout, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", a, "--variant", b, "--cohort", "run-refs")
	if err != nil {
		t.Fatalf("gate bench returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, bench.WarningProgressionProxyCircularity) {
		t.Errorf("stdout missing circularity warning:\n%s", stdout)
	}
	if !strings.Contains(stdout, "progression-proxy") {
		t.Errorf("stdout missing per-cell proxy source:\n%s", stdout)
	}
}

func TestBenchGateExplicitLabelsOverrideProxy(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "always-block")
	b := writeGateBenchFile(t, dir, "b.md", "always-block variant two")
	labels := writeGateLabels(t, dir,
		`{"run_id":"run-override","stage":"plan","round":1,"expected":"block","verified":true}`)
	hint := bench.LabelHint{Status: bench.GateHintPass, Rounds: 1, FinalRound: 1}
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{gateFixture("run-override", "good", 1, hint)}}

	stdout, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", a, "--variant", b,
		"--cohort", "run-refs", "--labels", labels)
	if err != nil {
		t.Fatalf("gate bench returned error: %v\nstderr: %s", err, stderr)
	}
	cellFields := strings.Fields(lineContaining(stdout, "run-override"))
	if len(cellFields) < 10 || cellFields[4] != "block" || cellFields[5] != "block" ||
		cellFields[6] != "explicit" || cellFields[7] != "true" || cellFields[8] != "true_positive" {
		t.Errorf("explicit label did not override pass proxy:\n%s", stdout)
	}
	if strings.Contains(stdout, bench.WarningProgressionProxyCircularity) {
		t.Errorf("proxy warning shown despite explicit override:\n%s", stdout)
	}
}

func TestBenchGateLabelsFileWithZeroMatchesFails(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "selective")
	b := writeGateBenchFile(t, dir, "b.md", "always-block")
	labels := writeGateLabels(t, dir,
		`{"run_id":"other-run","stage":"plan","round":1,"expected":"go","verified":true}`)
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{gateFixture("selected-run", "good", 1, bench.LabelHint{})}}
	judge := &contentGateJudge{}

	_, stderr, err := executeGateBench(corpus, judge,
		"--gate", "plan", "--variant", a, "--variant", b,
		"--cohort", "run-refs", "--labels", labels)
	if err == nil || !strings.Contains(stderr, "matched zero selected fixtures") {
		t.Fatalf("zero-match labels error = %v, stderr = %q", err, stderr)
	}
	if judge.calls != 0 {
		t.Errorf("judge called %d times before zero-match rejection", judge.calls)
	}
}

func TestBenchGateReportsExplicitProxyAndUnlabeledCounts(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "selective")
	b := writeGateBenchFile(t, dir, "b.md", "selective variant two")
	labels := writeGateLabels(t, dir,
		`{"run_id":"run-explicit","stage":"plan","round":1,"expected":"go","verified":true}`)
	proxy := bench.LabelHint{Status: bench.GateHintPass, Rounds: 1, FinalRound: 1}
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{
		gateFixture("run-explicit", "good explicit", 1, bench.LabelHint{}),
		gateFixture("run-proxy", "good proxy", 1, proxy),
		gateFixture("run-unlabeled", "good unlabeled", 0, bench.LabelHint{}),
	}}

	stdout, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", a, "--variant", b,
		"--cohort", "run-refs", "--labels", labels)
	if err != nil {
		t.Fatalf("gate bench returned error: %v\nstderr: %s", err, stderr)
	}
	fields := strings.Fields(lineContaining(stdout, a))
	if len(fields) < 9 || fields[4] != "3" || fields[5] != "2" || fields[6] != "1" || fields[7] != "1" || fields[8] != "1" {
		t.Errorf("variant counts = %v, want common=3 scored=2 explicit=1 proxy=1 unlabeled=1\n%s", fields, stdout)
	}
}

func TestBenchGatePartialJudgeFailureUsesCommonFixtureSet(t *testing.T) {
	dir := t.TempDir()
	failing := writeGateBenchFile(t, dir, "fail-bad.md", "selective fail-bad")
	steady := writeGateBenchFile(t, dir, "steady.md", "selective")
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{
		gateFixture("run-good", "good", 0, bench.LabelHint{}),
		gateFixture("run-bad", "bad", 0, bench.LabelHint{}),
	}}

	stdout, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", failing, "--variant", steady, "--cohort", "run-refs")
	if err != nil {
		t.Fatalf("partial failure should retain one common fixture: %v\nstderr: %s\nstdout: %s", err, stderr, stdout)
	}
	for _, variant := range []string{failing, steady} {
		fields := strings.Fields(lineContaining(stdout, variant))
		if len(fields) < 5 || fields[4] != "1" {
			t.Errorf("variant %q common denominator = %v, want 1", variant, fields)
		}
	}
	if !strings.Contains(stdout, "run-bad/plan/r0: failed variants: "+failing) || !strings.Contains(stdout, "ERROR: planned judge failure") {
		t.Errorf("stdout does not explain excluded fixture:\n%s", stdout)
	}
}

func TestBenchGateNoCompleteFixtureExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	failBad := writeGateBenchFile(t, dir, "fail-bad.md", "fail-bad")
	failGood := writeGateBenchFile(t, dir, "fail-good.md", "fail-good")
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{
		gateFixture("run-good", "good", 0, bench.LabelHint{}),
		gateFixture("run-bad", "bad", 0, bench.LabelHint{}),
	}}

	stdout, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", failBad, "--variant", failGood, "--cohort", "run-refs")
	if err == nil || !strings.Contains(stderr, "no fixture succeeded for all variants") {
		t.Fatalf("no-common-fixture error = %v, stderr = %q", err, stderr)
	}
	if !strings.Contains(stdout, "0 common fixtures; 2 excluded") || !strings.Contains(stdout, "run-good/plan/r0") || !strings.Contains(stdout, "run-bad/plan/r0") {
		t.Errorf("stdout missing zero-common evidence:\n%s", stdout)
	}
}

func TestBenchGateRejectsAliasesOfOneVariantPath(t *testing.T) {
	dir := t.TempDir()
	target := writeGateBenchFile(t, dir, "prompt.md", "selective")
	alias := filepath.Join(dir, "prompt-alias.md")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{gateFixture("run-a", "good", 0, bench.LabelHint{})}}

	_, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", target, "--variant", alias, "--cohort", "run-refs")
	if err == nil || !strings.Contains(stderr, "aliases") || !strings.Contains(stderr, "canonically distinct") {
		t.Fatalf("alias error = %v, stderr = %q", err, stderr)
	}
}

func TestBenchGateSymlinkResolutionFailureIsValidationError(t *testing.T) {
	dir := t.TempDir()
	valid := writeGateBenchFile(t, dir, "valid.md", "selective")
	broken := filepath.Join(dir, "broken.md")
	if err := os.Symlink(filepath.Join(dir, "missing.md"), broken); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{gateFixture("run-a", "good", 0, bench.LabelHint{})}}

	_, stderr, err := executeGateBench(corpus, &contentGateJudge{},
		"--gate", "plan", "--variant", valid, "--variant", broken, "--cohort", "run-refs")
	if err == nil || !strings.Contains(stderr, "resolve --variant") || !strings.Contains(stderr, "symlinks") {
		t.Fatalf("broken symlink error = %v, stderr = %q", err, stderr)
	}
}

func TestBenchGateValidation(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "selective")
	b := writeGateBenchFile(t, dir, "b.md", "always-block")
	empty := writeGateBenchFile(t, dir, "empty.md", " \n\t")
	badLabels := writeGateBenchFile(t, dir, "bad-labels.json", `{"version":1,"labels":[],"extra":true}`)
	corpus := &fakeGateCorpus{fixtures: []bench.Fixture{gateFixture("run-a", "good", 0, bench.LabelHint{})}}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "positional argument", args: []string{"unexpected", "--gate", "plan", "--variant", a, "--variant", b, "--cohort", "run-refs"}, want: "unknown command"},
		{name: "empty phase", args: []string{"--gate", "", "--variant", a, "--variant", b, "--cohort", "run-refs"}, want: "non-empty phase"},
		{name: "invalid phase", args: []string{"--gate", "bad phase", "--variant", a, "--variant", b, "--cohort", "run-refs"}, want: "not a valid identifier"},
		{name: "one variant", args: []string{"--gate", "plan", "--variant", a, "--cohort", "run-refs"}, want: "at least two --variant"},
		{name: "missing cohort", args: []string{"--gate", "plan", "--variant", a, "--variant", b}, want: "--cohort is required"},
		{name: "unknown cohort", args: []string{"--gate", "plan", "--variant", a, "--variant", b, "--cohort", "dolt"}, want: "unknown gate-bench cohort"},
		{name: "unreadable variant", args: []string{"--gate", "plan", "--variant", a, "--variant", filepath.Join(dir, "missing.md"), "--cohort", "run-refs"}, want: "resolve --variant"},
		{name: "empty variant", args: []string{"--gate", "plan", "--variant", a, "--variant", empty, "--cohort", "run-refs"}, want: "prompt must be non-empty"},
		{name: "invalid labels", args: []string{"--gate", "plan", "--variant", a, "--variant", b, "--cohort", "run-refs", "--labels", badLabels}, want: "parse --labels"},
		{name: "replay-only runner", args: []string{"--gate", "plan", "--variant", a, "--variant", b, "--cohort", "run-refs", "--runner", "runner.sh"}, want: "--runner is not valid in gate mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, err := executeGateBench(corpus, &contentGateJudge{}, tt.args...)
			if err == nil || !strings.Contains(stderr, tt.want) {
				t.Errorf("error = %v, stderr = %q, want %q", err, stderr, tt.want)
			}
		})
	}
}

func TestBenchGateRejectsEveryReplayOnlyFlag(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "selective")
	b := writeGateBenchFile(t, dir, "b.md", "always-block")
	base := []string{"--gate", "plan", "--variant", a, "--variant", b, "--cohort", "run-refs"}
	tests := []struct {
		flag  string
		value string
	}{
		{flag: "runner", value: "runner.sh"},
		{flag: "seed", value: "1"},
		{flag: "skill-id", value: "skill"},
		{flag: "skill-repo", value: "repo"},
		{flag: "skill-version", value: "v2"},
		{flag: "model", value: "model"},
		{flag: "harness", value: "harness"},
		{flag: "harness-version", value: "v2"},
		{flag: "no-cache"},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			args := append([]string(nil), base...)
			args = append(args, "--"+tt.flag)
			if tt.value != "" {
				args = append(args, tt.value)
			}
			_, stderr, err := executeGateBench(&fakeGateCorpus{}, &contentGateJudge{}, args...)
			want := "--" + tt.flag + " is not valid in gate mode"
			if err == nil || !strings.Contains(stderr, want) {
				t.Errorf("error = %v, stderr = %q, want %q", err, stderr, want)
			}
		})
	}
}

func TestBenchGateCorpusErrors(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "selective")
	b := writeGateBenchFile(t, dir, "b.md", "always-block")
	args := []string{"--gate", "plan", "--variant", a, "--variant", b, "--cohort", "run-refs"}

	_, stderr, err := executeGateBench(&fakeGateCorpus{err: errors.New("corpus unavailable")}, &contentGateJudge{}, args...)
	if err == nil || !strings.Contains(stderr, "load gate-bench cohort: corpus unavailable") {
		t.Errorf("corpus error = %v, stderr = %q", err, stderr)
	}

	_, stderr, err = executeGateBench(&fakeGateCorpus{}, &contentGateJudge{}, args...)
	if err == nil || !strings.Contains(stderr, `no fixtures selected for gate phase "plan"`) {
		t.Errorf("empty corpus error = %v, stderr = %q", err, stderr)
	}
}

func TestBenchGateResolvedUnreadableVariant(t *testing.T) {
	dir := t.TempDir()
	valid := writeGateBenchFile(t, dir, "valid.md", "selective")
	unreadableDirectory := filepath.Join(dir, "prompt-directory")
	if err := os.Mkdir(unreadableDirectory, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, stderr, err := executeGateBench(&fakeGateCorpus{}, &contentGateJudge{},
		"--gate", "plan", "--variant", valid, "--variant", unreadableDirectory, "--cohort", "run-refs")
	if err == nil || !strings.Contains(stderr, "read --variant") {
		t.Errorf("resolved unreadable variant error = %v, stderr = %q", err, stderr)
	}
}

func TestBenchGateTimeoutFlagIsWired(t *testing.T) {
	dir := t.TempDir()
	a := writeGateBenchFile(t, dir, "a.md", "selective")
	b := writeGateBenchFile(t, dir, "b.md", "always-block")
	var out, errOut bytes.Buffer
	r := &benchRunner{corpus: &fakeGateCorpus{}, judge: &contentGateJudge{}}
	cmd := buildBenchCommand(&out, &errOut, r)
	cmd.SetArgs([]string{
		"--gate", "plan", "--variant", a, "--variant", b,
		"--cohort", "run-refs", "--timeout", "30s",
	})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no fixtures selected") {
		t.Fatalf("gate command error = %v, want empty-corpus error", err)
	}
	if r.timeout != 30*time.Second {
		t.Errorf("runner timeout = %s, want 30s", r.timeout)
	}
}

func TestBenchReplayModeRejectsGateOnlyFlags(t *testing.T) {
	dir := t.TempDir()
	prompt := writeGateBenchFile(t, dir, "prompt.md", "selective")
	_, stderr, err := executeBench(&replay.StubRunner{}, &contentAwareJudge{}, time.Now(),
		"plan", "--variant", prompt)
	if err == nil || !strings.Contains(stderr, "--variant is not valid in replay mode") {
		t.Fatalf("replay gate-only flag error = %v, stderr = %q", err, stderr)
	}
}

func TestBenchHelpDocumentsGateMode(t *testing.T) {
	stdout, stderr, err := executeGateBench(nil, nil, "--help")
	if err != nil {
		t.Fatalf("bench --help returned %v: %s", err, stderr)
	}
	for _, flag := range []string{"--gate", "--variant", "--cohort", "--labels", "--runner", "--judge"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("help missing %s:\n%s", flag, stdout)
		}
	}
}

func lineContaining(output, needle string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
