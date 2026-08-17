package liverun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// ---------------------------------------------------------------------------
// Stub helpers for gate tests
// ---------------------------------------------------------------------------

// stubCheckRunner is a test double for CheckRunner.
type stubCheckRunner struct {
	passed    bool
	rawOutput []byte
	detail    string
}

func (s *stubCheckRunner) RunCheck(_ context.Context, _ replay.RunRequest) (bool, []byte, string) {
	return s.passed, s.rawOutput, s.detail
}

type recordingCheckRunner struct {
	inputs []replay.RunInput
}

func (r *recordingCheckRunner) RunCheck(_ context.Context, req replay.RunRequest) (bool, []byte, string) {
	r.inputs = append([]replay.RunInput(nil), req.Inputs...)
	return true, nil, ""
}

type mutatingCheckRunner struct {
	calls int
}

func (r *mutatingCheckRunner) RunCheck(_ context.Context, req replay.RunRequest) (bool, []byte, string) {
	r.calls++
	if err := os.WriteFile(filepath.Join(req.WorktreeDir, "checkout-marker.txt"), []byte("check mutation\n"), 0o644); err != nil {
		return false, nil, err.Error()
	}
	return true, nil, ""
}

// envelopeJSON encodes a seatEnvelope to JSON bytes.
func envelopeJSON(verdict string, required []string) []byte {
	env := seatEnvelope{Verdict: verdict, Required: required}
	b, _ := json.Marshal(env)
	return b
}

func sessionEnvelopeJSON(verdict string) []byte {
	return sessionEnvelopeJSONWithPath(verdict, "transcript.txt")
}

func sessionEnvelopeJSONWithPath(verdict, transcriptPath string) []byte {
	env := seatEnvelope{
		Verdict: verdict,
		Session: &seatSessionEnvelope{
			SessionID:      "session-123",
			TranscriptPath: transcriptPath,
		},
	}
	b, _ := json.Marshal(env)
	return b
}

type transcriptSeatRunner struct {
	envelope   []byte
	path       string
	transcript []byte
}

func (r transcriptSeatRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	path := r.path
	if path == "" {
		path = "transcript.txt"
	}
	outputPath := filepath.Join(req.ScratchDir, path)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return replay.RunResult{}, err
	}
	if err := os.WriteFile(outputPath, r.transcript, 0o644); err != nil {
		return replay.RunResult{}, err
	}
	res := replay.RunResult{
		Output:    r.envelope,
		MediaType: "application/json",
		Producer:  req.Producer,
	}
	return res, nil
}

// stubSeats is a call-indexed seat stub: each call index returns the next
// entry from the responses slice (wraps at end).
type stubSeats struct {
	responses [][]byte // each entry: canned envelope JSON or nil for error
	call      int
}

func (s *stubSeats) runner() replay.Runner {
	idx := s.call
	s.call++
	var resp []byte
	if len(s.responses) > 0 {
		resp = s.responses[idx%len(s.responses)]
	}
	return &replay.StubRunner{CannedOutput: resp, CannedMediaType: "application/json"}
}

// fixedTiers returns a Tiers function for the given ladder map.
// ladder maps tier name → (seats, nextStronger).
func fixedTiers(ladder map[string][2]interface{}) func(string) ([]string, string, bool) {
	return func(name string) ([]string, string, bool) {
		v, ok := ladder[name]
		if !ok {
			return nil, "", false
		}
		seats := v[0].([]string)
		next, _ := v[1].(string)
		return seats, next, true
	}
}

// gateTestEngine returns an Engine wired with stub resolvers for gate testing.
// checkPassed: outcome for all checks.
// seatResponses: cyclic list of seat envelope responses (in order of invocation).
// tierLadder: tier name → (seats, nextStronger).
func gateTestEngine(
	repo string,
	resolveStage func(workflow.Stage) (replay.Runner, error),
	checkPassed bool,
	seatResponses [][]byte,
	tierLadder map[string][2]interface{},
) (*Engine, *stubSeats) {
	ss := &stubSeats{responses: seatResponses}
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: resolveStage,
		ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
			detail := ""
			if !checkPassed {
				detail = "check failed"
			}
			return &stubCheckRunner{passed: checkPassed, rawOutput: []byte("check output"), detail: detail}, nil
		},
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{
				HarnessName:  "stub-harness",
				ProviderName: "stub-provider",
				Model:        "stub-model",
			}, nil
		},
		Tiers: fixedTiers(tierLadder),
		Root:  repo,
		Now:   fixedClock(),
	}
	return e, ss
}

// gatedWorkflow returns a 1-stage workflow where the single stage has a gate.
func gatedWorkflow(gate *workflow.GateConfig) workflow.Workflow {
	return workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{
				Name:     "plan",
				Skill:    "sk",
				Produces: "plan",
				Inputs:   []string{"task"},
				Gate:     gate,
			},
		},
	}
}

// maxRoundsPtr returns a pointer to n for use in GateConfig.MaxRounds.
func maxRoundsPtr(n int) *int { return &n }

// passThresholdPtr returns a pointer to f for use in GateConfig.PassThreshold.
func passThresholdPtr(f float64) *float64 { return &f }

// ---------------------------------------------------------------------------
// AC1: records a GateAttempt with the synthesized verdict; manifest_version==3;
//      JSON round-trip with gates.
// ---------------------------------------------------------------------------

func TestGateAC1_RecordsGateAttempt(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// 1 check (pass) + 2 go seats → PASS on round 1.
	goEnv := envelopeJSON("go", nil)
	e, _ := gateTestEngine(repo,
		stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan output"), CannedMediaType: "text/plain; charset=utf-8"}),
		true,                   // check passes
		[][]byte{goEnv, goEnv}, // two go seats
		map[string][2]interface{}{
			"L2": {[]string{"seatA", "seatB"}, "L1"},
		},
	)

	wf := gatedWorkflow(&workflow.GateConfig{
		Checks: []workflow.Runner{{Command: "true"}},
		Tier:   "L2",
	})

	runID := "mywf-20260101T000000Z-gateac01"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, runID)

	// manifest_version == 3 because gates are present.
	raw, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON(): %v", err)
	}
	var doc struct {
		ManifestVersion int `json:"manifest_version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal manifest_version: %v", err)
	}
	if doc.ManifestVersion != 3 {
		t.Errorf("manifest_version = %d, want 3", doc.ManifestVersion)
	}

	// ParseJSON round-trip must succeed.
	m2, err := runmanifest.ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON round-trip: %v", err)
	}
	if len(m2.Gates) != 1 {
		t.Fatalf("round-trip gates = %d, want 1", len(m2.Gates))
	}

	// Check the gate attempt.
	if len(m.Gates) != 1 {
		t.Fatalf("gates = %d, want 1", len(m.Gates))
	}
	g := m.Gates[0]
	if g.GateID != "plan.r1" {
		t.Errorf("gate_id = %q, want plan.r1", g.GateID)
	}
	if g.Phase != "plan" {
		t.Errorf("phase = %q, want plan", g.Phase)
	}
	if g.Round != 1 {
		t.Errorf("round = %d, want 1", g.Round)
	}
	if g.Tier != 2 { // L2 → 2
		t.Errorf("tier = %d, want 2", g.Tier)
	}
	if g.Status != runmanifest.GateStatusPass {
		t.Errorf("status = %q, want pass", g.Status)
	}

	// reviewed_stages must bind the plan stage output.
	if len(g.ReviewedStages) != 1 {
		t.Fatalf("reviewed_stages = %d, want 1", len(g.ReviewedStages))
	}
	rs := g.ReviewedStages[0]
	if rs.Stage != "plan" {
		t.Errorf("reviewed stage = %q, want plan", rs.Stage)
	}
	if rs.Role != "plan" {
		t.Errorf("reviewed role = %q, want plan", rs.Role)
	}
	if rs.Artifact != m.Stages[0].Output.Artifact {
		t.Errorf("reviewed artifact mismatch: got %q, want %q", rs.Artifact, m.Stages[0].Output.Artifact)
	}

	// Seats: check.0, seatA, seatB.
	if len(g.Seats) != 3 {
		t.Fatalf("seats = %d, want 3", len(g.Seats))
	}
	checkSeat := g.Seats[0]
	if checkSeat.Seat != "check.0" {
		t.Errorf("seat[0].seat = %q, want check.0", checkSeat.Seat)
	}
	if checkSeat.Verdict != runmanifest.SeatVerdictGo {
		t.Errorf("seat[0].verdict = %q, want go", checkSeat.Verdict)
	}
	if checkSeat.Provider.Name != "deterministic" {
		t.Errorf("seat[0].provider.name = %q, want deterministic", checkSeat.Provider.Name)
	}
	for _, s := range g.Seats[1:] {
		if s.Verdict != runmanifest.SeatVerdictGo {
			t.Errorf("seat %q verdict = %q, want go", s.Seat, s.Verdict)
		}
		if s.Provider.Name != "stub-provider" {
			t.Errorf("seat %q provider.name = %q, want stub-provider", s.Seat, s.Provider.Name)
		}
		if s.Provider.Model != "stub-model" {
			t.Errorf("seat %q provider.model = %q, want stub-model", s.Seat, s.Provider.Model)
		}
	}
}

type checkoutPolicyRunner struct {
	wantRead     bool
	prompts      []string
	markerRead   bool
	markerDenied bool
	calls        int
}

func TestNewOutputOnlySeatDirIsAbsoluteWithRelativeTMPDIR(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeTemp, err := filepath.Rel(cwd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", relativeTemp)
	dir, cleanup, err := newOutputOnlySeatDir(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !filepath.IsAbs(dir) {
		t.Fatalf("output-only seat dir = %q, want absolute", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("neutral git repository: %v", err)
	}
}

func TestNewOutputOnlySeatDirRejectsSymlinkedTMPDIRInsideCheckout(t *testing.T) {
	checkout := t.TempDir()
	tempLink := filepath.Join(t.TempDir(), "inside-checkout")
	if err := os.Symlink(checkout, tempLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tempLink)
	if _, _, err := newOutputOnlySeatDir(context.Background(), checkout); err == nil {
		t.Fatal("accepted a symlinked TMPDIR physically inside the checkout")
	} else if !strings.Contains(err.Error(), "inside checkout") {
		t.Fatalf("rejection error = %q, want checkout containment", err)
	}
	entries, err := os.ReadDir(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected neutral directory was not cleaned up: %v", entries)
	}
}

func TestPathAtOrBelowUsesFilesystemIdentity(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	inside, err := pathAtOrBelow(alias, child)
	if err != nil {
		t.Fatal(err)
	}
	if !inside {
		t.Fatal("filesystem-identical root alias did not contain its child")
	}
	outside, err := pathAtOrBelow(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if outside {
		t.Fatal("unrelated directory reported inside root")
	}
}

func TestOutputOnlySeatRunnerStripsCheckoutGitEnvironment(t *testing.T) {
	runner := &replay.ExecRunner{EnvAllowlist: []string{
		"HOME", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CEILING_DIRECTORIES", "GIT_NAMESPACE",
	}}
	gotRunner, err := outputOnlySeatRunner(runner, t.TempDir(), t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	got := gotRunner.(*replay.ExecRunner)
	if diff := strings.Join(got.EnvAllowlist, ","); diff != "HOME" {
		t.Fatalf("filtered env allowlist = %q, want HOME", diff)
	}
	if len(runner.EnvAllowlist) != 14 {
		t.Fatal("output-only filtering mutated the configured runner")
	}
}

func TestOutputOnlySeatRunnerMaterializesCheckoutExecutable(t *testing.T) {
	checkout := t.TempDir()
	neutral := t.TempDir()
	command := filepath.Join(checkout, "seat.sh")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &replay.ExecRunner{Command: []string{command, "--flag"}}
	prepared, err := outputOnlySeatRunner(runner, checkout, neutral, "primary")
	if err != nil {
		t.Fatal(err)
	}
	got := prepared.(*replay.ExecRunner)
	if got.Command[0] != filepath.Join(neutral, "etude-seat-primary") {
		t.Fatalf("materialized command = %q", got.Command[0])
	}
	if got.Command[1] != "--flag" {
		t.Fatalf("command argument changed: %v", got.Command)
	}
	if runner.Command[0] != command {
		t.Fatal("output-only preparation mutated configured command")
	}
}

func TestOutputOnlySeatRunnerMaterializesRelativeCheckoutExecutable(t *testing.T) {
	checkout := t.TempDir()
	neutral := t.TempDir()
	command := filepath.Join(checkout, "seat.sh")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &replay.ExecRunner{Command: []string{"./seat.sh", "--flag"}}
	prepared, err := outputOnlySeatRunner(runner, checkout, neutral, "primary")
	if err != nil {
		t.Fatal(err)
	}
	got := prepared.(*replay.ExecRunner)
	if got.Command[0] != filepath.Join(neutral, "etude-seat-primary") {
		t.Fatalf("materialized command = %q", got.Command[0])
	}
	if got.Command[1] != "--flag" {
		t.Fatalf("command argument changed: %v", got.Command)
	}
	if runner.Command[0] != "./seat.sh" {
		t.Fatal("output-only preparation mutated configured command")
	}
}

func TestOutputOnlySeatRunnerLaunchesResolvedExternalSymlinkTarget(t *testing.T) {
	checkout := t.TempDir()
	neutral := t.TempDir()
	external := t.TempDir()
	writeTestFile(t, checkout, "checkout-marker.txt", "must not be visible beside argv zero\n")
	target := filepath.Join(external, "seat.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nif [ -e \"$(dirname \"$0\")/checkout-marker.txt\" ]; then verdict=block; else verdict=go; fi\nprintf '{\"verdict\":\"%s\"}' \"$verdict\" > \"$ETUDE_OUTPUT_FILE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(checkout, "seat-link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	prepared, err := outputOnlySeatRunner(&replay.ExecRunner{Command: []string{link}}, checkout, neutral, "primary")
	if err != nil {
		t.Fatal(err)
	}
	got := prepared.(*replay.ExecRunner)
	if got.Command[0] == link {
		t.Fatalf("command still launches through checkout symlink %q", link)
	}
	gotInfo, err := os.Stat(got.Command[0])
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, targetInfo) {
		t.Fatalf("resolved command %q does not identify target %q", got.Command[0], target)
	}
	res, err := got.Run(context.Background(), replay.RunRequest{WorktreeDir: neutral, ScratchDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(res.Output, []byte(`"verdict":"go"`)) {
		t.Fatalf("seat launched through checkout symlink path: %s", res.Output)
	}
}

func TestOutputOnlySeatRunnerLaunchesResolvedTargetThroughProtectedSymlinkedParent(t *testing.T) {
	checkout := t.TempDir()
	neutral := t.TempDir()
	external := t.TempDir()
	target := filepath.Join(external, "seat.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(checkout, "bin")); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(checkout, "bin", "seat.sh")
	prepared, err := outputOnlySeatRunner(&replay.ExecRunner{Command: []string{linkPath}}, checkout, neutral, "primary")
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.(*replay.ExecRunner).Command[0]; got != resolvedTarget {
		t.Fatalf("command = %q, want resolved target %q", got, resolvedTarget)
	}
}

func TestOutputOnlySeatRunnerRecognizesResolvedProtectedRootAlias(t *testing.T) {
	physicalCheckout := t.TempDir()
	aliasParent := t.TempDir()
	checkoutAlias := filepath.Join(aliasParent, "checkout-alias")
	if err := os.Symlink(physicalCheckout, checkoutAlias); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	target := filepath.Join(external, "seat.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(physicalCheckout, "seat-link.sh")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}
	prepared, err := outputOnlySeatRunner(&replay.ExecRunner{Command: []string{linkPath}}, checkoutAlias, t.TempDir(), "primary")
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.(*replay.ExecRunner).Command[0]; got != resolvedTarget {
		t.Fatalf("command = %q, want resolved target %q", got, resolvedTarget)
	}
}

func TestOutputOnlySeatRunnerLeavesExternalSymlinkPathUnchanged(t *testing.T) {
	checkout := t.TempDir()
	external := t.TempDir()
	target := filepath.Join(t.TempDir(), "python3")
	if err := os.WriteFile(target, []byte("binary placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(external, "venv-python")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	prepared, err := outputOnlySeatRunner(&replay.ExecRunner{Command: []string{link}}, checkout, t.TempDir(), "primary")
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.(*replay.ExecRunner).Command[0]; got != link {
		t.Fatalf("external symlink command = %q, want original path %q", got, link)
	}
}

func TestOutputOnlySeatRunnerCopiesProtectedSymlinkTarget(t *testing.T) {
	checkout := t.TempDir()
	neutral := t.TempDir()
	target := filepath.Join(checkout, "seat-target.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(checkout, "seat-link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	prepared, err := outputOnlySeatRunner(&replay.ExecRunner{Command: []string{link}}, checkout, neutral, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.(*replay.ExecRunner).Command[0]; got != filepath.Join(neutral, "etude-seat-primary") {
		t.Fatalf("protected symlink command = %q, want neutral copy", got)
	}
}

func TestOutputOnlySeatRunnerMaterializesExecutableFromAdditionalProtectedRoot(t *testing.T) {
	commandRoot := t.TempDir()
	protectedRoot := t.TempDir()
	neutral := t.TempDir()
	command := filepath.Join(protectedRoot, "seat.sh")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepared, err := outputOnlySeatRunner(
		&replay.ExecRunner{Command: []string{command}}, commandRoot, neutral, "primary", protectedRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.(*replay.ExecRunner).Command[0]; got != filepath.Join(neutral, "etude-seat-primary") {
		t.Fatalf("materialized command = %q", got)
	}
}

func TestValidateOutputOnlySeatScratchRejectsCheckoutScratch(t *testing.T) {
	checkout := t.TempDir()
	scratch := filepath.Join(checkout, "scratch")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateOutputOnlySeatScratch(scratch, checkout); err == nil {
		t.Fatal("accepted scratch inside checkout")
	} else if !strings.Contains(err.Error(), "inside checkout") {
		t.Fatalf("error = %q, want checkout containment", err)
	}
	if err := validateOutputOnlySeatScratch(t.TempDir(), checkout); err != nil {
		t.Fatalf("rejected scratch outside checkout: %v", err)
	}
}

func (r *checkoutPolicyRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	r.calls++
	if len(req.Inputs) != 1 || req.Inputs[0].Role != gatePromptRole {
		return replay.RunResult{}, fmt.Errorf("seat inputs = %+v, want one %q prompt", req.Inputs, gatePromptRole)
	}
	prompt := string(req.Inputs[0].Content)
	r.prompts = append(r.prompts, prompt)
	content, err := os.ReadFile(filepath.Join(req.WorktreeDir, "checkout-marker.txt"))
	if r.wantRead {
		if !strings.Contains(prompt, "CHECKOUT ACCESS: READ-ONLY") {
			return replay.RunResult{}, fmt.Errorf("read grant prompt missing READ-ONLY mode")
		}
		if err != nil {
			return replay.RunResult{}, fmt.Errorf("read checkout marker: %w", err)
		}
		if string(content) != "pinned marker\n" {
			return replay.RunResult{}, fmt.Errorf("checkout marker = %q", content)
		}
		r.markerRead = true
	} else {
		if !strings.Contains(prompt, "CHECKOUT ACCESS: OUTPUT-ONLY") {
			return replay.RunResult{}, fmt.Errorf("default prompt missing OUTPUT-ONLY mode")
		}
		if strings.Contains(prompt, "CHECKOUT ACCESS: READ-ONLY") {
			return replay.RunResult{}, fmt.Errorf("output-only prompt also authorizes READ-ONLY")
		}
		if err == nil {
			return replay.RunResult{}, fmt.Errorf("output-only seat read checkout marker %q", content)
		}
		if !os.IsNotExist(err) {
			return replay.RunResult{}, fmt.Errorf("output-only marker read error = %v, want not exist", err)
		}
		if _, err := os.Stat(filepath.Join(req.WorktreeDir, ".git")); err != nil {
			return replay.RunResult{}, fmt.Errorf("output-only seat cwd is not a neutral git repository: %w", err)
		}
		r.markerDenied = true
	}
	return replay.RunResult{Output: goEnvelope(), MediaType: "application/json"}, nil
}

func TestGateReadCheckoutPromptAndManifest(t *testing.T) {
	for _, readCheckout := range []bool{false, true} {
		t.Run(fmt.Sprintf("read=%t", readCheckout), func(t *testing.T) {
			repo := initTestRepo(t)
			writeTestFile(t, repo, "checkout-marker.txt", "pinned marker\n")
			// A stray .gitmodules file without a mode-160000 tree entry is not a
			// submodule and must not false-reject an opted-in read grant.
			writeTestFile(t, repo, ".gitmodules", "# no gitlinks\n")
			gitRun(t, repo, "add", "checkout-marker.txt", ".gitmodules")
			gitRun(t, repo, "commit", "-m", "add marker")
			sha := headSHA(t, repo)
			// The live engine must expose the detached pin, not the caller's
			// mutable source tree after that commit.
			writeTestFile(t, repo, "checkout-marker.txt", "unpinned mutation\n")

			seat := &checkoutPolicyRunner{wantRead: readCheckout}
			check := &mutatingCheckRunner{}
			e := &Engine{
				Store:         refstore.New(repo),
				ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("stage claim"), CannedMediaType: "text/plain; charset=utf-8"}),
				ResolveCheck: func(workflow.Runner) (CheckRunner, error) {
					return check, nil
				},
				ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
					return seat, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
				},
				Tiers: fixedTiers(map[string][2]interface{}{
					"L1": {[]string{"reviewer-a", "reviewer-b"}, ""},
				}),
				Root: repo,
				Now:  fixedClock(),
			}
			wf := gatedWorkflow(&workflow.GateConfig{
				Checks:       []workflow.Runner{{Command: "mutate-checkout"}},
				Tier:         "L1",
				ReadCheckout: readCheckout,
			})
			runID := fmt.Sprintf("mywf-20260101T000000Z-read%t", readCheckout)
			if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
				TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if seat.calls != 2 {
				t.Fatalf("seat calls = %d, want 2", seat.calls)
			}
			if check.calls != 1 {
				t.Fatalf("check calls = %d, want 1", check.calls)
			}
			if seat.markerRead != readCheckout {
				t.Fatalf("markerRead = %t, want %t", seat.markerRead, readCheckout)
			}
			if seat.markerDenied != !readCheckout {
				t.Fatalf("markerDenied = %t, want %t", seat.markerDenied, !readCheckout)
			}
			if len(seat.prompts) != 2 || seat.prompts[0] != seat.prompts[1] {
				t.Fatalf("model seats received different prompts: %#v", seat.prompts)
			}
			prompt := seat.prompts[0]
			if readCheckout && !strings.Contains(prompt, sha) {
				t.Errorf("read prompt does not name pinned commit %s:\n%s", sha, prompt)
			}
			assertMeasurementAuthorityPrompt(t, prompt)

			m := readLiveManifest(t, repo, runID)
			if len(m.Gates) != 1 || m.Gates[0].ReadCheckout != readCheckout {
				t.Fatalf("recorded grant = %+v, want %t", m.Gates, readCheckout)
			}
			raw, err := m.JSON()
			if err != nil {
				t.Fatalf("JSON: %v", err)
			}
			if strings.Contains(string(raw), "read_checkout") != readCheckout {
				t.Fatalf("manifest read_checkout presence mismatch for read=%t:\n%s", readCheckout, raw)
			}
		})
	}
}

type contaminatingCheckoutRunner struct {
	seat          string
	firstCheckout *string
	called        *bool
}

func TestRunGateSeatsStopsAfterPinnedCheckoutCleanupFailure(t *testing.T) {
	root := t.TempDir()
	resolveCalls := 0
	e := &Engine{
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			resolveCalls++
			return &replay.StubRunner{CannedOutput: goEnvelope(), CannedMediaType: "application/json"}, SeatMeta{
				HarnessName: "stub", ProviderName: "stub", Model: "stub",
			}, nil
		},
		Root: root,
		Now:  fixedClock(),
	}
	checkout := t.TempDir()
	factoryCalls := 0
	results, verdicts, _, _ := e.runGateSeats(
		context.Background(), root, t.TempDir(), []string{"writer", "reader"}, nil,
		artifactstore.New(), 1, false,
		func() (string, func() error, error) {
			factoryCalls++
			return checkout, func() error { return errors.New("cleanup failed") }, nil
		},
	)
	if resolveCalls != 1 || factoryCalls != 1 {
		t.Fatalf("calls after cleanup failure: resolve=%d factory=%d, want 1 each", resolveCalls, factoryCalls)
	}
	if len(results) != 1 || len(verdicts) != 1 || verdicts[0] != runmanifest.SeatVerdictFailed {
		t.Fatalf("results=%+v verdicts=%v", results, verdicts)
	}
	if !strings.Contains(results[0].FailureNote, "cleanup pinned seat checkout") {
		t.Fatalf("failure note = %q", results[0].FailureNote)
	}
}

func (r contaminatingCheckoutRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	*r.called = true
	marker := filepath.Join(req.WorktreeDir, "checkout-marker.txt")
	switch r.seat {
	case "writer":
		*r.firstCheckout = req.WorktreeDir
		if err := os.WriteFile(marker, []byte("contaminated\n"), 0o644); err != nil {
			return replay.RunResult{}, err
		}
	case "reader":
		if *r.firstCheckout == "" {
			return replay.RunResult{}, errors.New("writer checkout was not recorded")
		}
		if _, err := os.Stat(*r.firstCheckout); !os.IsNotExist(err) {
			return replay.RunResult{}, fmt.Errorf("writer checkout still exists before reader invocation: %v", err)
		}
		if req.WorktreeDir == *r.firstCheckout {
			return replay.RunResult{}, errors.New("reader reused writer checkout")
		}
		content, err := os.ReadFile(marker)
		if err != nil {
			return replay.RunResult{}, err
		}
		if string(content) != "pinned\n" {
			return replay.RunResult{}, fmt.Errorf("reader marker = %q, want pinned commit", content)
		}
	}
	return replay.RunResult{Output: goEnvelope(), MediaType: "application/json"}, nil
}

func TestGateReadCheckoutSeatsReceivePristineCheckouts(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, repo, "checkout-marker.txt", "pinned\n")
	gitRun(t, repo, "add", "checkout-marker.txt")
	gitRun(t, repo, "commit", "-m", "add pinned marker")
	pinnedSHA := headSHA(t, repo)
	writeTestFile(t, repo, "checkout-marker.txt", "new-head\n")
	gitRun(t, repo, "add", "checkout-marker.txt")
	gitRun(t, repo, "commit", "-m", "move mutable head")

	firstCheckout := ""
	writerCalled, readerCalled := false, false
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("stage claim"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			called := &writerCalled
			if seatName == "reader" {
				called = &readerCalled
			}
			return contaminatingCheckoutRunner{seat: seatName, firstCheckout: &firstCheckout, called: called}, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"writer", "reader"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", ReadCheckout: true})
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: "mywf-20260101T000000Z-pristine", GitSHA: pinnedSHA,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !writerCalled || !readerCalled {
		t.Fatalf("seat calls: writer=%t reader=%t", writerCalled, readerCalled)
	}
}

type checkoutTranscriptSeatRunner struct {
	checkoutPath *string
}

func (r checkoutTranscriptSeatRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	*r.checkoutPath = req.WorktreeDir
	if err := os.WriteFile(filepath.Join(req.WorktreeDir, "seat-transcript.md"), []byte("checkout transcript literal\n"), 0o644); err != nil {
		return replay.RunResult{}, err
	}
	return replay.RunResult{
		Output:    sessionEnvelopeJSONWithPath("block", "seat-transcript.md"),
		MediaType: "application/json",
	}, nil
}

func TestGateReadCheckoutCapturesSessionBeforeCleanup(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	checkoutPath := ""
	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("stage claim"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return checkoutTranscriptSeatRunner{checkoutPath: &checkoutPath}, SeatMeta{
				HarnessName: "codex", ProviderName: "openai", Model: "gpt-5.5", RequireSessionEvidence: true,
			}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"codex"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", ReadCheckout: true, MaxRounds: &one})
	runID := "mywf-20260101T000000Z-session-cleanup"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	})
	var gateErr *GateEscalationError
	if !errors.As(err, &gateErr) {
		t.Fatalf("Run error = %v, want terminal block", err)
	}
	m := readLiveManifest(t, repo, runID)
	seat := m.Gates[0].Seats[0]
	if seat.Session == nil || seat.Session.TranscriptArtifact == nil {
		t.Fatalf("session transcript not captured: %+v", seat.Session)
	}
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/"+runID, seat.Session.TranscriptArtifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "checkout transcript literal\n" {
		t.Fatalf("transcript = %q", content)
	}
	if _, err := os.Stat(checkoutPath); !os.IsNotExist(err) {
		t.Fatalf("seat checkout still exists after block: %v", err)
	}
}

func TestGateReadCheckoutGitlinkBoundary(t *testing.T) {
	for _, readCheckout := range []bool{false, true} {
		t.Run(fmt.Sprintf("read=%t", readCheckout), func(t *testing.T) {
			repo := initTestRepo(t)
			baseSHA := headSHA(t, repo)
			gitRun(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+baseSHA+",nested/vendor/sub")
			gitRun(t, repo, "commit", "-m", "add nested gitlink")
			sha := headSHA(t, repo)

			seatCalls := 0
			e := &Engine{
				Store:         refstore.New(repo),
				ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("stage claim"), CannedMediaType: "text/plain; charset=utf-8"}),
				ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
					seatCalls++
					return &replay.StubRunner{CannedOutput: goEnvelope(), CannedMediaType: "application/json"}, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
				},
				Tiers: fixedTiers(map[string][2]interface{}{
					"L1": {[]string{"reviewer"}, ""},
				}),
				Root: repo,
				Now:  fixedClock(),
			}
			wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", ReadCheckout: readCheckout})
			runID := fmt.Sprintf("mywf-20260101T000001Z-gitlink%t", readCheckout)
			err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
				TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
			})

			if readCheckout {
				if err == nil || !strings.Contains(err.Error(), "nested/vendor/sub") || !strings.Contains(err.Error(), "#14") {
					t.Fatalf("read grant error = %v, want recursive gitlink and GH #14 guidance", err)
				}
				if seatCalls != 0 {
					t.Fatalf("seat ran %d times before gitlink rejection", seatCalls)
				}
				if got := readLiveManifest(t, repo, runID); len(got.Gates) != 0 {
					t.Fatalf("gitlink rejection recorded %d gate attempts, want none", len(got.Gates))
				}
				return
			}
			if err != nil {
				t.Fatalf("output-only gate on gitlink repo changed behavior: %v", err)
			}
			if seatCalls != 1 {
				t.Fatalf("output-only seat calls = %d, want 1", seatCalls)
			}
		})
	}
}

func TestGateReadCheckoutChecksOnlyResolvesNoGrant(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	check := &recordingCheckRunner{}
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("stage claim"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveCheck: func(workflow.Runner) (CheckRunner, error) {
			return check, nil
		},
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{
		Checks:       []workflow.Runner{{Command: "true"}},
		ReadCheckout: true,
	})
	runID := "mywf-20260101T000002Z-checksonly"
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	if len(m.Gates) != 1 || m.Gates[0].ReadCheckout {
		t.Fatalf("checks-only gate recorded checkout-read grant: %+v", m.Gates)
	}
	if len(check.inputs) != 1 || check.inputs[0].Role != "plan" || string(check.inputs[0].Content) != "stage claim" {
		t.Fatalf("deterministic check inputs = %+v, want raw stage output", check.inputs)
	}
}

func TestGateAgenticSeatRequiresSessionEvidence(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	goEnv := envelopeJSON("go", nil)
	ss := &stubSeats{responses: [][]byte{goEnv}}
	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{
				HarnessName:            "codex",
				ProviderName:           "openai",
				Model:                  "gpt-5.5",
				RequireSessionEvidence: true,
			}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"codex"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
	runID := "mywf-20260101T000000Z-session01"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	var gateErr *GateEscalationError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected gate escalation from missing session evidence, got %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	seat := m.Gates[0].Seats[0]
	if seat.Verdict != runmanifest.SeatVerdictMalfunction {
		t.Fatalf("seat verdict = %q, want malfunction", seat.Verdict)
	}
	if seat.FailureNote != "agentic seat did not provide session evidence" {
		t.Fatalf("failure note = %q", seat.FailureNote)
	}
}

func TestGateAgenticSeatStoresTranscriptEvidence(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			runner := transcriptSeatRunner{
				envelope:   sessionEnvelopeJSONWithPath("go", "transcript.md"),
				path:       "transcript.md",
				transcript: []byte("full transcript without secrets"),
			}
			meta := SeatMeta{
				HarnessName:            "codex",
				ProviderName:           "openai",
				Model:                  "gpt-5.5",
				RequireSessionEvidence: true,
			}
			return runner, meta, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"codex"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1"})
	runID := "mywf-20260101T000000Z-session02"
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	seat := m.Gates[0].Seats[0]
	if seat.Session == nil {
		t.Fatal("session evidence missing")
	}
	if seat.Session.SessionID != "session-123" {
		t.Fatalf("session id = %q", seat.Session.SessionID)
	}
	if seat.Session.RetrievalStatus != runmanifest.SessionEvidenceRetrievalImported {
		t.Fatalf("retrieval status = %q", seat.Session.RetrievalStatus)
	}
	if seat.Session.RedactionStatus != runmanifest.SessionEvidenceRedactionPassed {
		t.Fatalf("redaction status = %q", seat.Session.RedactionStatus)
	}
	if seat.Session.TranscriptArtifact == nil {
		t.Fatal("transcript artifact missing")
	}
	if seat.Session.TranscriptArtifact.MediaType != "text/markdown; charset=utf-8" {
		t.Fatalf("transcript media type = %q, want text/markdown; charset=utf-8", seat.Session.TranscriptArtifact.MediaType)
	}
}

func TestGateAgenticSeatFailsClosedOnSecretTranscript(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			runner := transcriptSeatRunner{
				envelope:   sessionEnvelopeJSON("go"),
				transcript: []byte("token ghp_123456789012345678901234567890123456"),
			}
			meta := SeatMeta{
				HarnessName:            "codex",
				ProviderName:           "openai",
				Model:                  "gpt-5.5",
				RequireSessionEvidence: true,
			}
			return runner, meta, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"codex"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
	runID := "mywf-20260101T000000Z-session03"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	var gateErr *GateEscalationError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected gate escalation from secret transcript, got %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	seat := m.Gates[0].Seats[0]
	if seat.Verdict != runmanifest.SeatVerdictMalfunction {
		t.Fatalf("seat verdict = %q, want malfunction", seat.Verdict)
	}
	if seat.Session == nil || seat.Session.RedactionStatus != runmanifest.SessionEvidenceFailed {
		t.Fatalf("redaction status = %#v, want failed", seat.Session)
	}
}

// ---------------------------------------------------------------------------
// AC2: failing check hard-blocks regardless of seat votes.
// ---------------------------------------------------------------------------

func TestGateAC2_FailingCheckHardBlocks(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// Check fails; 2 go seats — gate must NOT pass.
	// With max_rounds=1, should end up ESCALATED (no stronger tier → error).
	goEnv := envelopeJSON("go", nil)
	e, _ := gateTestEngine(repo,
		stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan output"), CannedMediaType: "text/plain; charset=utf-8"}),
		false, // check FAILS
		[][]byte{goEnv, goEnv},
		map[string][2]interface{}{
			"L1": {[]string{"seatA", "seatB"}, ""}, // L1 = top tier, no stronger
		},
	)

	one := 1
	wf := gatedWorkflow(&workflow.GateConfig{
		Checks:    []workflow.Runner{{Command: "false"}},
		Tier:      "L1",
		MaxRounds: &one,
	})

	runID := "mywf-20260101T000000Z-gateac02"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	// Must escalate because check failed and max_rounds=1.
	var gateEscErr *GateEscalationError
	if !errors.As(err, &gateEscErr) {
		t.Fatalf("expected GateEscalationError, got: %v", err)
	}
	if gateEscErr.Phase != "plan" {
		t.Errorf("phase = %q, want plan", gateEscErr.Phase)
	}

	// The partial run is inspectable.
	m := readLiveManifest(t, repo, runID)
	if len(m.Gates) != 1 {
		t.Fatalf("gates = %d, want 1", len(m.Gates))
	}
	g := m.Gates[0]
	if g.Status != runmanifest.GateStatusEscalated {
		t.Errorf("status = %q, want escalated", g.Status)
	}

	// The failing check must be recorded as block (not go).
	checkSeat := g.Seats[0]
	if checkSeat.Seat != "check.0" {
		t.Errorf("seat[0].seat = %q, want check.0", checkSeat.Seat)
	}
	if checkSeat.Verdict != runmanifest.SeatVerdictBlock {
		t.Errorf("check seat verdict = %q, want block", checkSeat.Verdict)
	}
	// Both seats voted go but gate still didn't pass.
	for _, s := range g.Seats[1:] {
		if s.Verdict != runmanifest.SeatVerdictGo {
			t.Errorf("seat %q verdict = %q, want go despite check failure", s.Seat, s.Verdict)
		}
	}
}

// ---------------------------------------------------------------------------
// AC3: rerun re-executes stage with gate-feedback in its inputs + round bump.
// ---------------------------------------------------------------------------

func TestGateAC3_RerunWithFeedback(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// Round 1: seats block → RERUN.
	// Round 2: seats go → PASS.
	blockEnv := envelopeJSON("block", []string{"fix the plan"})
	goEnv := envelopeJSON("go", nil)

	// Seat stub returns block on first 2 calls (round 1: 2 seats), go on next 2 (round 2).
	seatResponses := [][]byte{blockEnv, blockEnv, goEnv, goEnv}

	// Stage runner: 1st call = original; 2nd call = rerun (must see gate-feedback input).
	stageCallCount := 0
	var rerunSawFeedback bool
	resolveStage := func(stage workflow.Stage) (replay.Runner, error) {
		call := stageCallCount
		stageCallCount++
		if call == 0 {
			return &replay.StubRunner{CannedOutput: []byte("plan v1"), CannedMediaType: "text/plain; charset=utf-8"}, nil
		}
		// Rerun: return a runner that inspects inputs.
		return &feedbackCheckRunner{
			output:      []byte("plan v2"),
			mediaType:   "text/plain; charset=utf-8",
			sawFeedback: &rerunSawFeedback,
		}, nil
	}

	ss := &stubSeats{responses: seatResponses}
	two := 2
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: resolveStage,
		ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
			return &stubCheckRunner{passed: true}, nil
		},
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L2": {[]string{"seatA", "seatB"}, "L1"},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	_ = e.ResolveCheck // ensure ResolveCheck is set; checks are configured but pass

	wf := gatedWorkflow(&workflow.GateConfig{
		Tier:      "L2",
		MaxRounds: &two,
	})

	runID := "mywf-20260101T000000Z-gateac03"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !rerunSawFeedback {
		t.Error("rerun stage runner did not receive gate-feedback input")
	}

	m := readLiveManifest(t, repo, runID)

	// Two gate attempts: r1 rerun, r2 pass.
	if len(m.Gates) != 2 {
		t.Fatalf("gates = %d, want 2", len(m.Gates))
	}
	if m.Gates[0].Status != runmanifest.GateStatusRerun {
		t.Errorf("gate[0].status = %q, want rerun", m.Gates[0].Status)
	}
	if m.Gates[1].Status != runmanifest.GateStatusPass {
		t.Errorf("gate[1].status = %q, want pass", m.Gates[1].Status)
	}

	// Round numbers must match.
	if m.Gates[0].Round != 1 {
		t.Errorf("gate[0].round = %d, want 1", m.Gates[0].Round)
	}
	if m.Gates[1].Round != 2 {
		t.Errorf("gate[1].round = %d, want 2", m.Gates[1].Round)
	}

	// A second Stage named "plan.r2" must exist with gate-feedback in its Inputs.
	foundRerunStage := false
	for _, s := range m.Stages {
		if s.Name == "plan.r2" {
			foundRerunStage = true
			hasFeedback := false
			for _, inp := range s.Inputs {
				if inp.Role == "gate-feedback" {
					hasFeedback = true
					break
				}
			}
			if !hasFeedback {
				t.Error("plan.r2 stage has no gate-feedback input")
			}
			// chain role unchanged: output role is still "plan".
			if s.Output.Role != "plan" {
				t.Errorf("plan.r2 output role = %q, want plan", s.Output.Role)
			}
		}
	}
	if !foundRerunStage {
		t.Errorf("no stage named plan.r2 found in manifest stages: %v",
			func() []string {
				names := make([]string, 0, len(m.Stages))
				for _, s := range m.Stages {
					names = append(names, s.Name)
				}
				return names
			}())
	}

	// Gate r2 reviewed_stages must reference plan.r2 (not the original plan).
	if m.Gates[1].ReviewedStages[0].Stage != "plan.r2" {
		t.Errorf("gate[1] reviewed stage = %q, want plan.r2", m.Gates[1].ReviewedStages[0].Stage)
	}
}

// feedbackCheckRunner is a replay.Runner that records whether it received a
// gate-feedback input.
type feedbackCheckRunner struct {
	output      []byte
	mediaType   string
	sawFeedback *bool
}

func (r *feedbackCheckRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	for _, inp := range req.Inputs {
		if inp.Role == "gate-feedback" {
			*r.sawFeedback = true
			break
		}
	}
	return replay.RunResult{Output: r.output, MediaType: r.mediaType, Producer: req.Producer}, nil
}

// ---------------------------------------------------------------------------
// AC4: escalation advances tier; terminal escalation → GateEscalationError.
// ---------------------------------------------------------------------------

func TestGateAC4_EscalationAdvancesTier(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// L3: single seat blocks → ESCALATED (max_rounds=1).
	// L2: two seats go → PASS.
	blockEnv := envelopeJSON("block", []string{"needs work"})
	goEnv := envelopeJSON("go", nil)

	// Responses: 1 block (L3 round 1), 2 go (L2 round 2).
	ss := &stubSeats{responses: [][]byte{blockEnv, goEnv, goEnv}}
	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L3": {[]string{"seatA"}, "L2"},
			"L2": {[]string{"seatB", "seatC"}, "L1"},
		}),
		Root: repo,
		Now:  fixedClock(),
	}

	wf := gatedWorkflow(&workflow.GateConfig{
		Tier:      "L3",
		MaxRounds: &one,
	})

	runID := "mywf-20260101T000000Z-gateac04a"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, runID)
	if len(m.Gates) != 2 {
		t.Fatalf("gates = %d, want 2", len(m.Gates))
	}

	// First attempt: L3 (tier=3), escalated.
	g0 := m.Gates[0]
	if g0.Tier != 3 {
		t.Errorf("gate[0].tier = %d, want 3", g0.Tier)
	}
	if g0.Status != runmanifest.GateStatusEscalated {
		t.Errorf("gate[0].status = %q, want escalated", g0.Status)
	}
	if g0.Decision.EscalationReason == "" {
		t.Error("gate[0].escalation_reason must not be empty")
	}

	// Second attempt: L2 (tier=2), pass.
	g1 := m.Gates[1]
	if g1.Tier != 2 {
		t.Errorf("gate[1].tier = %d, want 2", g1.Tier)
	}
	if g1.Status != runmanifest.GateStatusPass {
		t.Errorf("gate[1].status = %q, want pass", g1.Status)
	}

	// Rounds are monotonically increasing.
	if g0.Round >= g1.Round {
		t.Errorf("rounds not monotonic: gate[0].round=%d gate[1].round=%d", g0.Round, g1.Round)
	}
}

func TestGateAC4_TerminalEscalation(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// L1 is the top tier (no stronger). Seat blocks → ESCALATED → GateEscalationError.
	blockEnv := envelopeJSON("block", []string{"still blocked"})
	ss := &stubSeats{responses: [][]byte{blockEnv, blockEnv}}
	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"seatA", "seatB"}, ""}, // top: no stronger
		}),
		Root: repo,
		Now:  fixedClock(),
	}

	wf := gatedWorkflow(&workflow.GateConfig{
		Tier:      "L1",
		MaxRounds: &one,
	})

	runID := "mywf-20260101T000000Z-gateac04b"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})

	var gateEscErr *GateEscalationError
	if !errors.As(err, &gateEscErr) {
		t.Fatalf("expected GateEscalationError, got: %v", err)
	}
	if gateEscErr.Phase != "plan" {
		t.Errorf("phase = %q, want plan", gateEscErr.Phase)
	}
	if gateEscErr.RunID != runID {
		t.Errorf("run_id = %q, want %q", gateEscErr.RunID, runID)
	}

	// Partial run must be valid and inspectable.
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) == 0 {
		t.Error("partial run has no stages")
	}
	if len(m.Gates) != 1 {
		t.Fatalf("partial run gates = %d, want 1 (the escalated attempt)", len(m.Gates))
	}
	if m.Gates[0].Status != runmanifest.GateStatusEscalated {
		t.Errorf("gate status = %q, want escalated", m.Gates[0].Status)
	}
}

// ---------------------------------------------------------------------------
// AC5: fail-closed cases.
// ---------------------------------------------------------------------------

func TestGateAC5_FailClosed(t *testing.T) {
	t.Run("errored-seat-escalates", func(t *testing.T) {
		// 2 seats, one errors (Err set) → usable=1 < min(2,2) → ESCALATED immediately.
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		goEnv := envelopeJSON("go", nil)
		callCount := 0
		one := 1
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				callCount++
				meta := SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}
				if callCount == 1 {
					return &replay.StubRunner{Err: errors.New("seat error")}, meta, nil
				}
				return &replay.StubRunner{CannedOutput: goEnv, CannedMediaType: "application/json"}, meta, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				"L1": {[]string{"seatA", "seatB"}, ""}, // top: no stronger
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{
			Tier:      "L1",
			MaxRounds: &one,
		})

		runID := "mywf-20260101T000000Z-gateac05a"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})

		var gateEscErr *GateEscalationError
		if !errors.As(err, &gateEscErr) {
			t.Fatalf("expected GateEscalationError (insufficient usable), got: %v", err)
		}

		m := readLiveManifest(t, repo, runID)
		if len(m.Gates) != 1 {
			t.Fatalf("gates = %d, want 1", len(m.Gates))
		}
		g := m.Gates[0]
		if g.Status != runmanifest.GateStatusEscalated {
			t.Errorf("status = %q, want escalated", g.Status)
		}
		// Find the errored seat and verify it's failed with a failure_note.
		foundFailed := false
		for _, s := range g.Seats {
			if s.Verdict == runmanifest.SeatVerdictFailed {
				foundFailed = true
				if s.FailureNote == "" {
					t.Error("errored seat has no failure_note")
				}
			}
		}
		if !foundFailed {
			t.Error("no failed seat found in gate seats")
		}
	})

	t.Run("malformed-envelope-malfunction", func(t *testing.T) {
		// Seat returns non-JSON → malfunction.
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		// Two malfunction seats → usable=0 < min(2,2) → ESCALATED.
		one := 1
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				meta := SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}
				return &replay.StubRunner{CannedOutput: []byte("not json"), CannedMediaType: "text/plain"}, meta, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				"L1": {[]string{"seatA", "seatB"}, ""},
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
		runID := "mywf-20260101T000000Z-gateac05b"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})

		var gateEscErr *GateEscalationError
		if !errors.As(err, &gateEscErr) {
			t.Fatalf("expected GateEscalationError, got: %v", err)
		}
		m := readLiveManifest(t, repo, runID)
		for _, s := range m.Gates[0].Seats {
			if s.Verdict != runmanifest.SeatVerdictMalfunction {
				t.Errorf("seat %q verdict = %q, want malfunction", s.Seat, s.Verdict)
			}
			if s.FailureNote == "" {
				t.Errorf("seat %q has no failure_note for malfunction", s.Seat)
			}
		}
	})

	t.Run("threshold-0.5-two-go-one-block-passes", func(t *testing.T) {
		// 3 seats, pass_threshold=0.5, 2 go + 1 block → 0.67 >= 0.5 → PASS.
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		goEnv := envelopeJSON("go", nil)
		blockEnv := envelopeJSON("block", []string{"fix it"})
		ss := &stubSeats{responses: [][]byte{goEnv, goEnv, blockEnv}}
		pt := 0.5
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				"L3": {[]string{"seatA", "seatB", "seatC"}, "L2"},
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{Tier: "L3", PassThreshold: &pt})
		runID := "mywf-20260101T000000Z-gateac05c1"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
		if err != nil {
			t.Fatalf("expected pass with threshold 0.5 (2go/1block), got: %v", err)
		}
		m := readLiveManifest(t, repo, runID)
		if m.Gates[0].Status != runmanifest.GateStatusPass {
			t.Errorf("status = %q, want pass", m.Gates[0].Status)
		}
	})

	t.Run("threshold-1.0-two-go-one-block-reruns", func(t *testing.T) {
		// Same seats but threshold=1.0 → 0.67 < 1.0 → not pass.
		// With max_rounds=1 → ESCALATED (top tier L3 → L2 not in map → terminal).
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		goEnv := envelopeJSON("go", nil)
		blockEnv := envelopeJSON("block", []string{"fix it"})
		ss := &stubSeats{responses: [][]byte{goEnv, goEnv, blockEnv}}
		pt := 1.0
		one := 1
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				// Only L3 in map; no L2 → nextStronger = "" → terminal on escalate.
				"L3": {[]string{"seatA", "seatB", "seatC"}, ""},
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{Tier: "L3", PassThreshold: &pt, MaxRounds: &one})
		runID := "mywf-20260101T000000Z-gateac05c2"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
		var gateEscErr *GateEscalationError
		if !errors.As(err, &gateEscErr) {
			t.Fatalf("expected GateEscalationError with threshold 1.0 (2go/1block), got: %v", err)
		}
	})

	t.Run("checks-only-gate-passes", func(t *testing.T) {
		// No seats, only a passing check → PASS (checks-only gate).
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
				return &stubCheckRunner{passed: true}, nil
			},
			// ResolveSeat / Tiers not set: no seats in this gate.
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{
			Checks: []workflow.Runner{{Command: "true"}},
		})
		runID := "mywf-20260101T000000Z-gateac05d"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
		if err != nil {
			t.Fatalf("checks-only gate: expected pass, got: %v", err)
		}
		m := readLiveManifest(t, repo, runID)
		if len(m.Gates) != 1 {
			t.Fatalf("gates = %d, want 1", len(m.Gates))
		}
		if m.Gates[0].Status != runmanifest.GateStatusPass {
			t.Errorf("status = %q, want pass", m.Gates[0].Status)
		}
	})
}

// ---------------------------------------------------------------------------
// Unit tests for synthesis and helpers
// ---------------------------------------------------------------------------

func TestSynthesizeVerdict(t *testing.T) {
	tests := []struct {
		name           string
		checksPassed   []bool
		seatVerdicts   []runmanifest.SeatVerdict
		tierRound      int
		maxRounds      int
		passThreshold  float64
		expectedSeats  int
		wantStatus     runmanifest.GateStatus
		wantEscalation bool
	}{
		{
			name:         "all-checks-pass-no-seats",
			checksPassed: []bool{true},
			seatVerdicts: nil,
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 0,
			wantStatus: runmanifest.GateStatusPass,
		},
		{
			name:         "check-fails-rerun",
			checksPassed: []bool{false},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 1,
			wantStatus: runmanifest.GateStatusRerun,
		},
		{
			name:         "check-fails-max-rounds-escalates",
			checksPassed: []bool{false},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo},
			tierRound:    3, maxRounds: 3, passThreshold: 1.0, expectedSeats: 1,
			wantStatus: runmanifest.GateStatusEscalated, wantEscalation: true,
		},
		{
			name:         "insufficient-usable-escalates",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictFailed, runmanifest.SeatVerdictGo},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 2,
			wantStatus: runmanifest.GateStatusEscalated, wantEscalation: true,
		},
		{
			name:         "single-seat-one-usable-passes",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 1,
			wantStatus: runmanifest.GateStatusPass,
		},
		{
			name:         "threshold-met-passes",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo, runmanifest.SeatVerdictBlock},
			tierRound:    1, maxRounds: 3, passThreshold: 0.5, expectedSeats: 2,
			wantStatus: runmanifest.GateStatusPass,
		},
		{
			name:         "threshold-not-met-reruns",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictBlock, runmanifest.SeatVerdictBlock},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 2,
			wantStatus: runmanifest.GateStatusRerun,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			syn := synthesizeVerdict(tc.checksPassed, tc.seatVerdicts, tc.tierRound, tc.maxRounds, tc.passThreshold, tc.expectedSeats)
			if syn.status != tc.wantStatus {
				t.Errorf("status = %q, want %q", syn.status, tc.wantStatus)
			}
			if tc.wantEscalation && syn.escalationReason == "" {
				t.Error("escalation_reason must not be empty for escalated status")
			}
		})
	}
}

func TestTierToInt(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"L1", 1}, {"L2", 2}, {"L3", 3}, {"L4", 4},
		{"", 0}, {"inline", 0}, {"L", 0}, {"L10", 0},
	}
	for _, tc := range tests {
		if got := tierToInt(tc.name); got != tc.want {
			t.Errorf("tierToInt(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestSplitProvider(t *testing.T) {
	tests := []struct{ s, name, model string }{
		{"anthropic/claude-opus", "anthropic", "claude-opus"},
		{"openai/gpt-5", "openai", "gpt-5"},
		{"singlename", "singlename", "singlename"},
		{"a/b/c", "a", "b/c"}, // split on FIRST slash only
	}
	for _, tc := range tests {
		n, m := splitProvider(tc.s)
		if n != tc.name || m != tc.model {
			t.Errorf("splitProvider(%q) = (%q, %q), want (%q, %q)", tc.s, n, m, tc.name, tc.model)
		}
	}
}

// noopWriter returns an io.Writer that discards all output.
func noopWriter() *nopW { return &nopW{} }

type nopW struct{}

func (*nopW) Write(p []byte) (int, error) { return len(p), nil }

// Ensure noopWriter is used as io.Writer.
var _ interface{ Write([]byte) (int, error) } = (*nopW)(nil)

// Compile-time check that fixedClock is available (defined in engine_test.go).
var _ = fixedClock

// Compile-time check that time is imported (used in SeatResult.Timestamp).
var _ = time.Now
