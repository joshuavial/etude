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

func TestGateReadCheckoutPopulatesRecordedSubmodule(t *testing.T) {
	for _, readCheckout := range []bool{false, true} {
		t.Run(fmt.Sprintf("read=%t", readCheckout), func(t *testing.T) {
			t.Setenv("GIT_ALLOW_PROTOCOL", "file")
			submodule := initTestRepo(t)
			writeTestFile(t, submodule, "payload.txt", "pinned gate content\n")
			gitRun(t, submodule, "add", "payload.txt")
			gitRun(t, submodule, "commit", "-m", "add gate payload")
			submoduleSHA := headSHA(t, submodule)

			repo := initTestRepo(t)
			gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "nested/vendor/sub")
			gitRun(t, repo, "commit", "-m", "add nested submodule")
			sha := headSHA(t, repo)

			seatCalls := 0
			seatRunner := replay.Runner(&replay.StubRunner{CannedOutput: goEnvelope(), CannedMediaType: "application/json"})
			if readCheckout {
				seatRunner = runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
					content, err := os.ReadFile(filepath.Join(req.WorktreeDir, "nested", "vendor", "sub", "payload.txt"))
					if err != nil {
						return replay.RunResult{}, err
					}
					if got := string(content); got != "pinned gate content\n" {
						t.Fatalf("seat submodule content = %q, want pinned content", got)
					}
					return replay.RunResult{Output: goEnvelope(), MediaType: "application/json"}, nil
				})
			}
			e := &Engine{
				Store:         refstore.New(repo),
				ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("stage claim"), CannedMediaType: "text/plain; charset=utf-8"}),
				ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
					seatCalls++
					return seatRunner, SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
				},
				Tiers: fixedTiers(map[string][2]interface{}{
					"L1": {[]string{"reviewer"}, ""},
				}),
				Root: repo,
				Now:  fixedClock(),
			}
			wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", ReadCheckout: readCheckout})
			runID := fmt.Sprintf("mywf-20260101T000001Z-submodule%t", readCheckout)
			err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
				TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
			})

			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if seatCalls != 1 {
				t.Fatalf("seat calls = %d, want 1", seatCalls)
			}
			m := readLiveManifest(t, repo, runID)
			if got := m.Stages[0].Submodules["nested/vendor/sub"]; got != submoduleSHA {
				t.Fatalf("recorded submodule SHA = %q, want %q", got, submoduleSHA)
			}
			if got := m.Gates[0].ReadCheckout; got != readCheckout {
				t.Fatalf("recorded read_checkout = %t, want %t", got, readCheckout)
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
	writeTestFile(t, repo, "checkout-marker.txt", "pinned marker\n")
	gitRun(t, repo, "add", "checkout-marker.txt")
	gitRun(t, repo, "commit", "-m", "add checkout marker")
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
	var rerunInputs []replay.RunInput
	resolveStage := func(stage workflow.Stage) (replay.Runner, error) {
		call := stageCallCount
		stageCallCount++
		if call == 0 {
			return &stageEvidenceRunner{
				output: []byte("plan v1"), log: []byte("runner log"), transcript: []byte("transcript"), sessionID: "session-1",
			}, nil
		}
		// Rerun: return a runner that inspects inputs.
		return &feedbackCheckRunner{
			output:      []byte("plan v2"),
			mediaType:   "text/plain; charset=utf-8",
			sawFeedback: &rerunSawFeedback,
			captured:    &rerunInputs,
		}, nil
	}

	seatCall := 0
	var seatCheckoutDirs []string
	two := 2
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: resolveStage,
		ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
			return &stubCheckRunner{passed: true}, nil
		},
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			response := seatResponses[seatCall]
			seatCall++
			return runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
				content, err := os.ReadFile(filepath.Join(req.WorktreeDir, "checkout-marker.txt"))
				if err != nil {
					return replay.RunResult{}, err
				}
				if string(content) != "pinned marker\n" {
					return replay.RunResult{}, fmt.Errorf("checkout marker = %q", content)
				}
				seatCheckoutDirs = append(seatCheckoutDirs, req.WorktreeDir)
				return replay.RunResult{Output: response, MediaType: "application/json"}, nil
			}), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L2": {[]string{"seatA", "seatB"}, "L1"},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	_ = e.ResolveCheck // ensure ResolveCheck is set; checks are configured but pass

	wf := gatedWorkflow(&workflow.GateConfig{
		Tier:         "L2",
		MaxRounds:    &two,
		ReadCheckout: true,
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
	assertPriorAttemptInputs(t, rerunInputs)
	if len(seatCheckoutDirs) != 4 {
		t.Fatalf("seat checkout count = %d, want 4", len(seatCheckoutDirs))
	}
	seenCheckoutDirs := make(map[string]bool, len(seatCheckoutDirs))
	for _, dir := range seatCheckoutDirs {
		if seenCheckoutDirs[dir] {
			t.Fatalf("seat checkout %q was reused across isolated invocations", dir)
		}
		seenCheckoutDirs[dir] = true
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("seat checkout %q still exists after invocation: %v", dir, err)
		}
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
	if !m.Gates[0].ReadCheckout || !m.Gates[1].ReadCheckout {
		t.Fatalf("read_checkout grants = %t/%t, want true/true", m.Gates[0].ReadCheckout, m.Gates[1].ReadCheckout)
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
			priorRoles := map[string]bool{}
			for _, inp := range s.Inputs {
				if inp.Role == "gate-feedback" {
					hasFeedback = true
				}
				if workflow.IsPriorAttemptRole(inp.Role) {
					priorRoles[inp.Role] = true
				}
			}
			if !hasFeedback {
				t.Error("plan.r2 stage has no gate-feedback input")
			}
			for _, role := range []string{"prior-attempts", "prior-attempt-1-output", "prior-attempt-1-log", "prior-attempt-1-transcript"} {
				if !priorRoles[role] {
					t.Errorf("plan.r2 stage has no %s input", role)
				}
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
	captured    *[]replay.RunInput
}

func (r *feedbackCheckRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	if r.captured != nil {
		*r.captured = append([]replay.RunInput(nil), req.Inputs...)
	}
	for _, inp := range req.Inputs {
		if inp.Role == "gate-feedback" && r.sawFeedback != nil {
			*r.sawFeedback = true
			break
		}
	}
	return replay.RunResult{Output: r.output, MediaType: r.mediaType, Producer: req.Producer}, nil
}

type stageEvidenceRunner struct {
	output     []byte
	log        []byte
	transcript []byte
	sessionID  string
}

func (r *stageEvidenceRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	if err := os.WriteFile(filepath.Join(req.ScratchDir, "stage-transcript.txt"), r.transcript, 0o600); err != nil {
		return replay.RunResult{}, err
	}
	return replay.RunResult{
		Output: r.output,
		Log:    r.log,
		Producer: runmanifest.Producer{
			Harness: runmanifest.Harness{Name: "stub"},
			Skill:   req.Producer.Skill,
		},
		Session: &replay.SessionInfo{SessionID: r.sessionID, TranscriptPath: "stage-transcript.txt"},
	}, nil
}

func assertPriorAttemptInputs(t *testing.T, inputs []replay.RunInput) {
	t.Helper()
	byRole := make(map[string][]byte, len(inputs))
	for _, input := range inputs {
		byRole[input.Role] = input.Content
	}
	wantIndex := `{
  "version": 1,
  "attempts": [
    {
      "stage": "plan",
      "round": 1,
      "session_id": "session-1",
      "resumed_session": false,
      "output_digest": "7ba7dc833f225079fec28c951951783c7362bbc50857e0f71ceb6d5b71eb1041",
      "output_role": "prior-attempt-1-output",
      "log_digest": "a18c461b8e1526a691a4fa19184dd5dff12ed156f9f2b59d2c1efe0d7ff043b9",
      "log_role": "prior-attempt-1-log",
      "transcript_digest": "54e6289e14c7b0e7ad9acc2dfc4c1e3d027d0eef7f5c4c3fe7c292761d0e06a6",
      "transcript_role": "prior-attempt-1-transcript",
      "transcript_retrieval_status": "imported",
      "transcript_redaction_status": "passed",
      "killing_verdict": {
        "gate_id": "plan.r1",
        "status": "rerun",
        "seats": [
          {
            "seat": "seatA",
            "verdict": "block",
            "required": [
              "fix the plan"
            ]
          },
          {
            "seat": "seatB",
            "verdict": "block",
            "required": [
              "fix the plan"
            ]
          }
        ]
      }
    }
  ]
}
`
	if got := string(byRole["prior-attempts"]); got != wantIndex {
		t.Fatalf("prior-attempts index:\n%s\nwant:\n%s", got, wantIndex)
	}
	for role, want := range map[string]string{
		"prior-attempt-1-output":     "plan v1",
		"prior-attempt-1-log":        "runner log",
		"prior-attempt-1-transcript": "transcript",
	} {
		if got := string(byRole[role]); got != want {
			t.Fatalf("input %s = %q, want %q", role, got, want)
		}
	}
}

func TestValidatePriorTranscriptEvidenceStateMachine(t *testing.T) {
	artifact := &runmanifest.ArtifactRef{Artifact: strings.Repeat("a", 64)}
	tests := []struct {
		name    string
		session *runmanifest.SessionEvidence
		wantErr string
	}{
		{name: "missing session is no transcript", session: nil},
		{name: "no transcript", session: &runmanifest.SessionEvidence{RetrievalStatus: runmanifest.SessionEvidenceNotApplicable, RedactionStatus: runmanifest.SessionEvidenceNotApplicable}},
		{name: "retrieval failed", session: &runmanifest.SessionEvidence{RetrievalStatus: runmanifest.SessionEvidenceFailed, RedactionStatus: runmanifest.SessionEvidenceNotApplicable}},
		{name: "imported and scanned", session: &runmanifest.SessionEvidence{RetrievalStatus: runmanifest.SessionEvidenceRetrievalImported, RedactionStatus: runmanifest.SessionEvidenceRedactionPassed, TranscriptArtifact: artifact}},
		{name: "redaction failed", session: &runmanifest.SessionEvidence{RetrievalStatus: runmanifest.SessionEvidenceRetrievalImported, RedactionStatus: runmanifest.SessionEvidenceFailed}, wantErr: "redaction failed"},
		{name: "success missing artifact", session: &runmanifest.SessionEvidence{RetrievalStatus: runmanifest.SessionEvidenceRetrievalImported, RedactionStatus: runmanifest.SessionEvidenceRedactionPassed}, wantErr: "without artifact"},
		{name: "artifact retrieval failed", session: &runmanifest.SessionEvidence{RetrievalStatus: runmanifest.SessionEvidenceFailed, RedactionStatus: runmanifest.SessionEvidenceNotApplicable, TranscriptArtifact: artifact}, wantErr: "with artifact"},
		{name: "mixed absent state", session: &runmanifest.SessionEvidence{RetrievalStatus: runmanifest.SessionEvidenceNotApplicable, RedactionStatus: runmanifest.SessionEvidenceRedactionPassed}, wantErr: "without artifact"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePriorTranscriptEvidence(tc.session)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("validate: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestFilterPriorAttemptInputsPreservesOrdinaryAndFeedback(t *testing.T) {
	refs := []runmanifest.ArtifactRef{
		{Role: "task"}, {Role: "prior-attempts"}, {Role: "prior-attempt-1-output"}, {Role: "gate-feedback"},
	}
	inputs := []replay.RunInput{
		{Role: "task"}, {Role: "prior-attempts"}, {Role: "prior-attempt-1-output"}, {Role: "gate-feedback"},
	}
	gotRefs, gotInputs := filterPriorAttemptInputs(refs, inputs)
	if len(gotRefs) != 2 || gotRefs[0].Role != "task" || gotRefs[1].Role != "gate-feedback" {
		t.Fatalf("filtered refs = %#v", gotRefs)
	}
	if len(gotInputs) != 2 || gotInputs[0].Role != "task" || gotInputs[1].Role != "gate-feedback" {
		t.Fatalf("filtered inputs = %#v", gotInputs)
	}
}

func TestBuildPriorAttemptInputsMarksResumedSessionAndKeepsIdenticalOutputs(t *testing.T) {
	as := artifactstore.New()
	outputArtifact, err := as.AddContent("plan", "text/plain", []byte("same output"))
	if err != nil {
		t.Fatal(err)
	}
	output := runmanifest.ArtifactFromManifestArtifact(outputArtifact)
	noTranscriptSession := func(id string) *runmanifest.SessionEvidence {
		return &runmanifest.SessionEvidence{SessionID: id, RetrievalStatus: runmanifest.SessionEvidenceNotApplicable, RedactionStatus: runmanifest.SessionEvidenceNotApplicable}
	}
	stages := []runmanifest.Stage{
		{Name: "plan", Output: output, Producer: runmanifest.Producer{Session: noTranscriptSession("shared-session")}},
		{Name: "plan.r2", Output: output, Producer: runmanifest.Producer{Session: noTranscriptSession("shared-session")}},
		{Name: "plan.r3", Output: output, Producer: runmanifest.Producer{Session: noTranscriptSession(" shared-session ")}},
		{Name: "plan.r4", Output: output},
	}
	gates := []runmanifest.GateAttempt{
		{GateID: "plan.r1", Phase: "plan", Round: 1, Status: runmanifest.GateStatusRerun, ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan", Artifact: output.Artifact}}},
		{GateID: "plan.r2", Phase: "plan", Round: 2, Status: runmanifest.GateStatusRerun, ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan.r2", Artifact: output.Artifact}}},
		{GateID: "plan.r3", Phase: "plan", Round: 3, Status: runmanifest.GateStatusRerun, ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan.r3", Artifact: output.Artifact}}},
		{GateID: "plan.r4", Phase: "plan", Round: 4, Status: runmanifest.GateStatusRerun, ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan.r4", Artifact: output.Artifact}}},
	}
	refs, inputs, err := buildPriorAttemptInputs("plan", stages, gates, as)
	if err != nil {
		t.Fatalf("buildPriorAttemptInputs: %v", err)
	}
	if len(refs) != 5 || len(inputs) != 5 {
		t.Fatalf("refs/inputs = %d/%d, want index plus four outputs", len(refs), len(inputs))
	}
	var index priorAttemptsIndex
	if err := json.Unmarshal(inputs[0].Content, &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if len(index.Attempts) != 4 || index.Attempts[0].ResumedSession || !index.Attempts[1].ResumedSession || index.Attempts[2].ResumedSession || index.Attempts[3].ResumedSession {
		t.Fatalf("resumed flags = %#v", index.Attempts)
	}
	if index.Attempts[3].SessionID != "" {
		t.Fatalf("nil Session encoded session_id = %q", index.Attempts[3].SessionID)
	}
	for i := 1; i <= 4; i++ {
		ref := refs[i]
		if ref.Artifact != output.Artifact || ref.Role != fmt.Sprintf("prior-attempt-%d-output", i) {
			t.Fatalf("output ref[%d] = %#v", i, ref)
		}
	}
}

func TestBuildPriorAttemptInputsSeedsSessionHistoryFromPassedAttempts(t *testing.T) {
	as := artifactstore.New()
	upstreamArtifact, err := as.AddContent("upstream", "text/plain", []byte("passed output"))
	if err != nil {
		t.Fatal(err)
	}
	blockedArtifact, err := as.AddContent("plan", "text/plain", []byte("blocked output"))
	if err != nil {
		t.Fatal(err)
	}
	session := func() *runmanifest.SessionEvidence {
		return &runmanifest.SessionEvidence{
			SessionID:       "reused-session",
			RetrievalStatus: runmanifest.SessionEvidenceNotApplicable,
			RedactionStatus: runmanifest.SessionEvidenceNotApplicable,
		}
	}
	stages := []runmanifest.Stage{
		{Name: "upstream", Output: runmanifest.ArtifactFromManifestArtifact(upstreamArtifact), Producer: runmanifest.Producer{Session: session()}},
		{Name: "plan", Output: runmanifest.ArtifactFromManifestArtifact(blockedArtifact), Producer: runmanifest.Producer{Session: session()}},
	}
	gates := []runmanifest.GateAttempt{
		{GateID: "upstream.r1", Phase: "upstream", Round: 1, Status: runmanifest.GateStatusPass, ReviewedStages: []runmanifest.ReviewedRef{{Stage: "upstream", Artifact: upstreamArtifact.SHA256}}},
		{GateID: "plan.r1", Phase: "plan", Round: 1, Status: runmanifest.GateStatusRerun, ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan", Artifact: blockedArtifact.SHA256}}},
	}

	_, inputs, err := buildPriorAttemptInputs("plan", stages, gates, as)
	if err != nil {
		t.Fatalf("buildPriorAttemptInputs: %v", err)
	}
	var index priorAttemptsIndex
	if err := json.Unmarshal(inputs[0].Content, &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if len(index.Attempts) != 1 || !index.Attempts[0].ResumedSession {
		t.Fatalf("attempt index = %#v, want killed attempt marked resumed from earlier passed producer", index.Attempts)
	}
}

func TestBuildPriorAttemptInputsRequiresOneReviewedStage(t *testing.T) {
	as := artifactstore.New()
	outputArtifact, err := as.AddContent("plan", "text/plain", []byte("output"))
	if err != nil {
		t.Fatal(err)
	}
	output := runmanifest.ArtifactFromManifestArtifact(outputArtifact)
	stages := []runmanifest.Stage{{Name: "plan", Output: output}}
	gates := []runmanifest.GateAttempt{{
		GateID: "plan.r1", Phase: "plan", Round: 1, Status: runmanifest.GateStatusRerun,
		ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan"}, {Stage: "plan"}},
	}}

	_, _, err = buildPriorAttemptInputs("plan", stages, gates, as)
	if err == nil || !strings.Contains(err.Error(), "want exactly one") {
		t.Fatalf("buildPriorAttemptInputs error = %v, want reviewed-stage cardinality error", err)
	}
}

func TestBuildPriorAttemptInputsBindsReviewedNameAndArtifact(t *testing.T) {
	as := artifactstore.New()
	firstArtifact, err := as.AddContent("plan", "text/plain", []byte("first output"))
	if err != nil {
		t.Fatal(err)
	}
	secondArtifact, err := as.AddContent("plan", "text/plain", []byte("second output"))
	if err != nil {
		t.Fatal(err)
	}
	first := runmanifest.ArtifactFromManifestArtifact(firstArtifact)
	second := runmanifest.ArtifactFromManifestArtifact(secondArtifact)
	stages := []runmanifest.Stage{
		{Name: "plan", Output: first},
		{Name: "plan", Output: second},
	}
	gates := []runmanifest.GateAttempt{{
		GateID: "plan.r1", Phase: "plan", Round: 1, Status: runmanifest.GateStatusRerun,
		ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan", Artifact: first.Artifact}},
	}}

	_, inputs, err := buildPriorAttemptInputs("plan", stages, gates, as)
	if err != nil {
		t.Fatalf("buildPriorAttemptInputs: %v", err)
	}
	byRole := make(map[string][]byte, len(inputs))
	for _, input := range inputs {
		byRole[input.Role] = input.Content
	}
	if got := string(byRole["prior-attempt-1-output"]); got != "first output" {
		t.Fatalf("reviewed output = %q, want exact first artifact", got)
	}
}

func TestBuildPriorAttemptInputsOmitsEscalatedOnlyHistory(t *testing.T) {
	refs, inputs, err := buildPriorAttemptInputs("plan", nil, []runmanifest.GateAttempt{{
		GateID: "plan.r1", Phase: "plan", Round: 1, Status: runmanifest.GateStatusEscalated,
	}}, artifactstore.New())
	if err != nil {
		t.Fatalf("buildPriorAttemptInputs: %v", err)
	}
	if len(refs) != 0 || len(inputs) != 0 {
		t.Fatalf("refs/inputs = %#v/%#v, want no prior-attempt bundle", refs, inputs)
	}
}

func TestGateRerunStopsBeforeRunnerWhenPriorTranscriptRedactionFailed(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	stageCalls := 0
	block := envelopeJSON("block", []string{"fix it"})
	ss := &stubSeats{responses: [][]byte{block, block}}
	two := 2
	e := &Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			stageCalls++
			return &stageEvidenceRunner{
				output: []byte("plan"), transcript: []byte("api_key=abcdefghijklmnop"), sessionID: "unsafe-session",
			}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L2": {[]string{"seatA", "seatB"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L2", MaxRounds: &two})
	runID := "mywf-20260101T000000Z-redaction-stop"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha})
	if err == nil || !strings.Contains(err.Error(), "transcript redaction failed") {
		t.Fatalf("Run error = %v, want transcript redaction failure", err)
	}
	if stageCalls != 1 {
		t.Fatalf("stage calls = %d, want 1", stageCalls)
	}
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 || len(m.Gates) != 1 || m.Gates[0].Status != runmanifest.GateStatusRerun {
		t.Fatalf("durable state = stages:%d gates:%#v", len(m.Stages), m.Gates)
	}
	if session := m.Stages[0].Producer.Session; session == nil || session.RedactionStatus != runmanifest.SessionEvidenceFailed {
		t.Fatalf("session = %#v, want persisted redaction failure", session)
	}
}

func TestGateRerunReportsTranscriptRetrievalFailureAndContinues(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	block := envelopeJSON("block", []string{"fix it"})
	goResult := envelopeJSON("go", nil)
	ss := &stubSeats{responses: [][]byte{block, block, goResult, goResult}}
	stageCalls := 0
	var rerunInputs []replay.RunInput
	two := 2
	e := &Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			stageCalls++
			if stageCalls == 1 {
				return runnerFunc(func(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
					return replay.RunResult{
						Output:   []byte("plan v1"),
						Producer: runmanifest.Producer{Harness: runmanifest.Harness{Name: "stub"}, Skill: req.Producer.Skill},
						Session:  &replay.SessionInfo{SessionID: "missing-transcript", TranscriptPath: "missing.txt"},
					}, nil
				}), nil
			}
			return &feedbackCheckRunner{output: []byte("plan v2"), captured: &rerunInputs}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L2": {[]string{"seatA", "seatB"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L2", MaxRounds: &two})
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: "mywf-20260101T000000Z-retrieval-continues", GitSHA: sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stageCalls != 2 {
		t.Fatalf("stage calls = %d, want 2", stageCalls)
	}
	var index priorAttemptsIndex
	for _, input := range rerunInputs {
		if input.Role == "prior-attempts" {
			if err := json.Unmarshal(input.Content, &index); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(input.Role, "transcript") {
			t.Fatalf("unexpected transcript input %q after retrieval failure", input.Role)
		}
	}
	if len(index.Attempts) != 1 || index.Attempts[0].TranscriptRetrievalStatus != runmanifest.SessionEvidenceFailed || index.Attempts[0].TranscriptRole != "" {
		t.Fatalf("index attempts = %#v", index.Attempts)
	}
}

func TestGateThirdAttemptReceivesRebuiltHistoryAndResumedSession(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	block := envelopeJSON("block", []string{"fix it"})
	goResult := envelopeJSON("go", nil)
	ss := &stubSeats{responses: [][]byte{block, block, block, block, goResult, goResult}}
	stageCalls := 0
	var thirdInputs []replay.RunInput
	three := 3
	e := &Engine{
		Store: refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) {
			stageCalls++
			if stageCalls <= 2 {
				return &stageEvidenceRunner{
					output: []byte(fmt.Sprintf("plan v%d", stageCalls)),
					log:    []byte(fmt.Sprintf("log v%d", stageCalls)), transcript: []byte(fmt.Sprintf("transcript v%d", stageCalls)), sessionID: "shared-session",
				}, nil
			}
			return &feedbackCheckRunner{output: []byte("plan v3"), captured: &thirdInputs}, nil
		},
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L2": {[]string{"seatA", "seatB"}, ""}}),
		Root:  repo,
		Now:   fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L2", MaxRounds: &three})
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: "mywf-20260101T000000Z-third-attempt", GitSHA: sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var index priorAttemptsIndex
	roleCounts := make(map[string]int)
	feedbackCount := 0
	for _, input := range thirdInputs {
		roleCounts[input.Role]++
		if input.Role == "gate-feedback" {
			feedbackCount++
		}
		if input.Role == "prior-attempts" {
			if err := json.Unmarshal(input.Content, &index); err != nil {
				t.Fatal(err)
			}
		}
	}
	if feedbackCount != 2 || roleCounts["prior-attempts"] != 1 {
		t.Fatalf("feedback/prior index counts = %d/%d", feedbackCount, roleCounts["prior-attempts"])
	}
	if len(index.Attempts) != 2 || index.Attempts[0].ResumedSession || !index.Attempts[1].ResumedSession {
		t.Fatalf("attempt index = %#v", index.Attempts)
	}
	for _, role := range []string{
		"prior-attempt-1-output", "prior-attempt-1-log", "prior-attempt-1-transcript",
		"prior-attempt-2-output", "prior-attempt-2-log", "prior-attempt-2-transcript",
	} {
		if roleCounts[role] != 1 {
			t.Errorf("input role %s count = %d, want 1", role, roleCounts[role])
		}
	}
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
