package liverun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// GateEscalationError is returned when a gate exhausts all tiers with no
// stronger tier to escalate to. The partial run is still valid and inspectable.
type GateEscalationError struct {
	Phase  string
	RunID  string
	Reason string
}

func (e *GateEscalationError) Error() string {
	return fmt.Sprintf("gate %q: terminal escalation: %s (run %s; resume with: etude run <workflow> --resume %s)", e.Phase, e.Reason, e.RunID, e.RunID)
}

// SeatMeta holds the harness and provider metadata for a named registry seat.
// ProviderName and Model are pre-split from the registry Seat.Provider field.
type SeatMeta struct {
	HarnessName  string // e.g. "claude-code"
	ProviderName string // e.g. "anthropic" (before "/" in seat.Provider)
	Model        string // e.g. "claude-opus" (after "/" in seat.Provider)
}

// CheckRunner executes a deterministic gate check. Unlike replay.Runner, the
// exit code IS the verdict: 0 = pass, nonzero or launch/timeout failure = block.
// Checks never require an output file.
type CheckRunner interface {
	RunCheck(ctx context.Context, req replay.RunRequest) (passed bool, rawOutput []byte, detail string)
}

// seatEnvelope is the JSON structure written to ETUDE_OUTPUT_FILE by a model
// seat runner.
type seatEnvelope struct {
	Verdict  string   `json:"verdict"`
	Required []string `json:"required,omitempty"`
	Optional []string `json:"optional,omitempty"`
}

// execCheckRunner implements CheckRunner using an external command. It
// materializes inputs and sets the strict env (PATH, ETUDE_INPUTS_DIR,
// ETUDE_OUTPUT_FILE) identically to replay.ExecRunner, but interprets exit
// code as the verdict and never requires an output file.
type execCheckRunner struct {
	command []string
	timeout time.Duration
}

// compile-time interface satisfaction.
var _ CheckRunner = (*execCheckRunner)(nil)

// checkWaitDelay mirrors replay.runnerWaitDelay.
const checkWaitDelay = 10 * time.Second

// RunCheck materializes inputs, invokes the command, and interprets exit code.
// Exit 0 = pass; nonzero, launch failure, or timeout = block (fail-closed).
func (r *execCheckRunner) RunCheck(ctx context.Context, req replay.RunRequest) (passed bool, rawOutput []byte, detail string) {
	if len(r.command) == 0 {
		return false, nil, "check runner: no command configured"
	}

	resolvedWorktree, err := resolveGateDir(req.WorktreeDir)
	if err != nil {
		return false, nil, fmt.Sprintf("check runner: invalid worktree dir: %v", err)
	}
	resolvedScratch, err := resolveGateDir(req.ScratchDir)
	if err != nil {
		return false, nil, fmt.Sprintf("check runner: invalid scratch dir: %v", err)
	}

	// Materialize inputs under <ScratchDir>/inputs/<NN>-<role>.
	outputPath := filepath.Join(resolvedScratch, "output")
	inputsDir := filepath.Join(resolvedScratch, "inputs")
	_ = os.Remove(outputPath)
	if err := os.RemoveAll(inputsDir); err != nil {
		return false, nil, fmt.Sprintf("check runner: remove inputs: %v", err)
	}
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		return false, nil, fmt.Sprintf("check runner: mkdir inputs: %v", err)
	}
	for i, inp := range req.Inputs {
		name := fmt.Sprintf("%02d-%s", i, inp.Role)
		p := filepath.Join(inputsDir, name)
		if err := os.WriteFile(p, inp.Content, 0o644); err != nil {
			return false, nil, fmt.Sprintf("check runner: write input %s: %v", name, err)
		}
	}

	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"ETUDE_INPUTS_DIR=" + inputsDir,
		"ETUDE_OUTPUT_FILE=" + outputPath,
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, r.command[0], r.command[1:]...)
	cmd.Dir = resolvedWorktree
	cmd.Env = env
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.WaitDelay = checkWaitDelay

	runErr := cmd.Run()
	combined := append(append([]byte(nil), stdoutBuf.Bytes()...), stderrBuf.Bytes()...)

	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, combined, fmt.Sprintf("check timed out after %v", r.timeout)
		}
		return false, combined, "check runner: context cancelled"
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			note := fmt.Sprintf("exit %d", exitErr.ExitCode())
			if s := strings.TrimSpace(stderrBuf.String()); s != "" {
				if len(s) > 200 {
					s = s[:200] + "..."
				}
				note += ": " + s
			}
			return false, combined, note
		}
		return false, combined, fmt.Sprintf("check runner: launch failed: %v", runErr)
	}
	return true, combined, ""
}

// resolveGateDir validates a path is non-empty, absolute, exists as a
// directory, and returns its symlink-resolved form.
func resolveGateDir(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q is not absolute", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("eval symlinks %q: %w", path, err)
	}
	return resolved, nil
}

// tierToInt maps a registry tier name to the manifest integer.
// L1→1, L2→2, L3→3; all others (L4, inline, unknown) → 0.
// The manifest requires Tier ∈ {0,1,2,3}; L4 and inline share 0 (lossy but
// valid; escalation uses tier NAMES not this integer).
func tierToInt(name string) int {
	if len(name) == 2 && name[0] == 'L' {
		switch name[1] {
		case '1':
			return 1
		case '2':
			return 2
		case '3':
			return 3
		}
	}
	return 0
}

// splitProvider splits "provider/model" into (providerName, model). If the
// string contains no "/", BOTH fields are set to the whole string so that
// validateProviderField (which requires both non-empty) is satisfied.
func splitProvider(s string) (providerName, model string) {
	idx := strings.IndexByte(s, '/')
	if idx < 0 {
		return s, s
	}
	return s[:idx], s[idx+1:]
}

// synthesisResult holds the output of the D4 fail-closed synthesis algorithm.
type synthesisResult struct {
	status           runmanifest.GateStatus
	escalationReason string
	degradedReason   string
}

// synthesizeVerdict applies the D4 fail-closed algorithm:
//
//  1. If any check failed → not-pass (step 5).
//  2. If expectedSeats == 0 → PASS (checks-only gate).
//  3. If usable < min(2, expectedSeats) → ESCALATED (seat outage, skip rerun).
//  4. If goCount/usable >= passThreshold → PASS.
//  5. Not-pass: tierRound < maxRounds → RERUN; else → ESCALATED.
func synthesizeVerdict(
	checksPassed []bool,
	seatVerdicts []runmanifest.SeatVerdict,
	tierRound, maxRounds int,
	passThreshold float64,
	expectedSeats int,
) synthesisResult {
	checkFailed := false
	for _, p := range checksPassed {
		if !p {
			checkFailed = true
			break
		}
	}

	usable, goCount, anyNonUsable := 0, 0, false
	for _, v := range seatVerdicts {
		switch v {
		case runmanifest.SeatVerdictGo:
			usable++
			goCount++
		case runmanifest.SeatVerdictBlock:
			usable++
		default:
			anyNonUsable = true
		}
	}

	degraded := ""
	if anyNonUsable {
		degraded = "one or more seats produced non-usable results"
	}

	if checkFailed {
		return notPassDecision(tierRound, maxRounds, degraded)
	}
	if expectedSeats == 0 {
		return synthesisResult{status: runmanifest.GateStatusPass, degradedReason: degraded}
	}

	minUsable := 2
	if expectedSeats < 2 {
		minUsable = expectedSeats
	}
	if usable < minUsable {
		return synthesisResult{
			status:           runmanifest.GateStatusEscalated,
			escalationReason: fmt.Sprintf("insufficient usable seats: got %d need %d", usable, minUsable),
			degradedReason:   degraded,
		}
	}

	if float64(goCount)/float64(usable) >= passThreshold {
		return synthesisResult{status: runmanifest.GateStatusPass, degradedReason: degraded}
	}
	return notPassDecision(tierRound, maxRounds, degraded)
}

func notPassDecision(tierRound, maxRounds int, degraded string) synthesisResult {
	if tierRound < maxRounds {
		return synthesisResult{status: runmanifest.GateStatusRerun, degradedReason: degraded}
	}
	return synthesisResult{
		status:           runmanifest.GateStatusEscalated,
		escalationReason: fmt.Sprintf("max rounds %d exhausted", maxRounds),
		degradedReason:   degraded,
	}
}

// buildGateFeedback constructs a markdown artifact that summarizes what blocks.
func buildGateFeedback(checkBlocks []string, seatBlockRequired map[string][]string) []byte {
	var sb strings.Builder
	sb.WriteString("# Gate Feedback\n\n")
	if len(checkBlocks) > 0 {
		sb.WriteString("## Failing Checks\n\n")
		for _, b := range checkBlocks {
			sb.WriteString("- ")
			sb.WriteString(b)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	for seatName, required := range seatBlockRequired {
		if len(required) == 0 {
			continue
		}
		sb.WriteString("## Seat ")
		sb.WriteString(seatName)
		sb.WriteString(" Required Changes\n\n")
		for _, r := range required {
			sb.WriteString("- ")
			sb.WriteString(r)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}

// storeRawOutput adds raw bytes to the artifact store and returns the artifact.
// Returns nil when content is empty (no artifact stored).
func storeRawOutput(as *artifactstore.Store, role string, content []byte) *artifactstore.ManifestArtifact {
	if len(content) == 0 {
		return nil
	}
	art, err := as.AddContent(role, "application/octet-stream", content)
	if err != nil {
		return nil
	}
	return &art
}

// classifySeatOutput maps a runner result + error to a seat verdict and, on
// success, parses the JSON envelope. Implements the D3/D4 mapping:
//
//   - ErrOutputMissing → empty
//   - ErrRunnerFailed / launch failure → failed
//   - DeadlineExceeded → failed (timeout note)
//   - success, zero bytes → malfunction (empty-but-present file)
//   - success, non-JSON or bad verdict → malfunction
//   - success, valid envelope → go / block
func classifySeatOutput(res replay.RunResult, runErr error) (runmanifest.SeatVerdict, string, *seatEnvelope) {
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			return runmanifest.SeatVerdictFailed, fmt.Sprintf("seat timed out: %v", runErr), nil
		}
		if errors.Is(runErr, replay.ErrOutputMissing) {
			return runmanifest.SeatVerdictEmpty, fmt.Sprintf("no output produced: %v", runErr), nil
		}
		return runmanifest.SeatVerdictFailed, fmt.Sprintf("runner failed: %v", runErr), nil
	}
	if len(res.Output) == 0 {
		return runmanifest.SeatVerdictMalfunction, "seat produced empty output file (expected JSON verdict envelope)", nil
	}
	var env seatEnvelope
	if err := json.Unmarshal(res.Output, &env); err != nil {
		return runmanifest.SeatVerdictMalfunction, fmt.Sprintf("invalid JSON envelope: %v", err), nil
	}
	switch env.Verdict {
	case "go":
		return runmanifest.SeatVerdictGo, "", &env
	case "block":
		return runmanifest.SeatVerdictBlock, "", &env
	default:
		return runmanifest.SeatVerdictMalfunction, fmt.Sprintf("unknown verdict %q in envelope", env.Verdict), nil
	}
}

// runGateChecks runs all configured checks. Returns SeatResults for the
// manifest, a bool slice (true=passed) for synthesis, and string details of
// failing checks for gate-feedback.
func (e *Engine) runGateChecks(
	ctx context.Context,
	worktreeDir, scratch string,
	checks []workflow.Runner,
	gateInputs []replay.RunInput,
	as *artifactstore.Store,
	globalRound int,
) (seatResults []runmanifest.SeatResult, checksPassed []bool, blockDetails []string) {
	for i, check := range checks {
		checkScratch := filepath.Join(scratch, fmt.Sprintf("gate-r%d-check%d", globalRound, i))
		_ = os.MkdirAll(checkScratch, 0o755)

		runnerName := check.Command
		if check.Name != "" {
			runnerName = check.Name
		}
		if runnerName == "" {
			runnerName = "command"
		}

		var (
			passed    bool
			raw       []byte
			detail    string
			resolveOK = true
		)
		cr, resolveErr := e.ResolveCheck(check)
		if resolveErr != nil {
			resolveOK = false
			detail = fmt.Sprintf("resolve check runner: %v", resolveErr)
		} else {
			req := replay.RunRequest{
				WorktreeDir:     worktreeDir,
				ScratchDir:      checkScratch,
				Inputs:          gateInputs,
				OutputRole:      "check-output",
				OutputMediaType: "application/octet-stream",
			}
			passed, raw, detail = cr.RunCheck(ctx, req)
		}

		rawArt := storeRawOutput(as, fmt.Sprintf("check-%d", i), raw)
		var rawRef *runmanifest.ArtifactRef
		if rawArt != nil {
			ref := runmanifest.ArtifactFromManifestArtifact(*rawArt)
			rawRef = &ref
		}

		sr := runmanifest.SeatResult{
			Seat:      fmt.Sprintf("check.%d", i),
			Harness:   runmanifest.Harness{Name: "exec"},
			Provider:  runmanifest.Provider{Name: "deterministic", Model: runnerName},
			RawOutput: rawRef,
			Timestamp: e.clock(),
		}

		if resolveOK && passed {
			sr.Verdict = runmanifest.SeatVerdictGo
		} else {
			sr.Verdict = runmanifest.SeatVerdictBlock
			if detail != "" {
				sr.Required = []string{detail}
			}
			blockDetails = append(blockDetails, detail)
		}

		seatResults = append(seatResults, sr)
		checksPassed = append(checksPassed, resolveOK && passed)
	}
	return seatResults, checksPassed, blockDetails
}

// runGateSeats runs all configured model seats. Returns SeatResults for the
// manifest, verdicts for synthesis, and a map of seatName → required[] for
// blocking seats (used to build gate-feedback).
func (e *Engine) runGateSeats(
	ctx context.Context,
	worktreeDir, scratch string,
	seatNames []string,
	gateInputs []replay.RunInput,
	as *artifactstore.Store,
	globalRound int,
) (seatResults []runmanifest.SeatResult, verdicts []runmanifest.SeatVerdict, blockRequired map[string][]string) {
	blockRequired = make(map[string][]string)

	for i, seatName := range seatNames {
		seatScratch := filepath.Join(scratch, fmt.Sprintf("gate-r%d-seat%d", globalRound, i))
		_ = os.MkdirAll(seatScratch, 0o755)

		runner, meta, resolveErr := e.ResolveSeat(seatName)
		if resolveErr != nil {
			note := fmt.Sprintf("resolve seat %q: %v", seatName, resolveErr)
			// Use seatName as both provider.name and provider.model (fallback).
			n, m := splitProvider(seatName)
			seatResults = append(seatResults, runmanifest.SeatResult{
				Seat:        seatName,
				Harness:     runmanifest.Harness{Name: "exec"},
				Provider:    runmanifest.Provider{Name: n, Model: m},
				Verdict:     runmanifest.SeatVerdictFailed,
				FailureNote: note,
				Timestamp:   e.clock(),
			})
			verdicts = append(verdicts, runmanifest.SeatVerdictFailed)
			continue
		}

		req := replay.RunRequest{
			WorktreeDir:     worktreeDir,
			ScratchDir:      seatScratch,
			Inputs:          gateInputs,
			OutputRole:      "seat-output",
			OutputMediaType: "application/json",
		}

		res, runErr := runner.Run(ctx, req)
		verdict, failureNote, env := classifySeatOutput(res, runErr)

		rawArt := storeRawOutput(as, seatName, res.Output)
		var rawRef *runmanifest.ArtifactRef
		if rawArt != nil {
			ref := runmanifest.ArtifactFromManifestArtifact(*rawArt)
			rawRef = &ref
		}

		sr := runmanifest.SeatResult{
			Seat:        seatName,
			Harness:     runmanifest.Harness{Name: meta.HarnessName},
			Provider:    runmanifest.Provider{Name: meta.ProviderName, Model: meta.Model},
			Verdict:     verdict,
			FailureNote: failureNote,
			RawOutput:   rawRef,
			Timestamp:   e.clock(),
		}
		if env != nil {
			sr.Required = env.Required
			sr.Optional = env.Optional
		}
		if verdict == runmanifest.SeatVerdictBlock && env != nil {
			blockRequired[seatName] = env.Required
		}

		seatResults = append(seatResults, sr)
		verdicts = append(verdicts, verdict)
	}
	return seatResults, verdicts, blockRequired
}

// runGate executes the full gate drive loop for a guarded stage output:
// checks → seats → synthesize → rerun/escalate. Each attempt is written to
// the CAS manifest via the engine's existing write path.
//
// Returns:
//   - allGateAttempts: existingGateAttempts + new attempts from this gate
//   - updatedStages:   completedStages extended with any rerun stages
//   - newCommit:       latest CAS commit OID after the last attempt write
//   - finalOutputRef/Content: output from the last stage run (original or rerun)
//   - error: nil on pass; GateEscalationError on terminal; infra errors otherwise
func (e *Engine) runGate(
	ctx context.Context,
	out io.Writer,
	runID, gitSHA string,
	created time.Time,
	wf workflow.Workflow,
	stage workflow.Stage,
	stageIdx int,
	baseInputRefs []runmanifest.ArtifactRef,
	baseRunInputs []replay.RunInput,
	as *artifactstore.Store,
	completedStages []runmanifest.Stage,
	existingGateAttempts []runmanifest.GateAttempt,
	prevCommit string,
	initialOutputRef runmanifest.ArtifactRef,
	initialOutputContent []byte,
	worktreeDir, scratch string,
) (allGateAttempts []runmanifest.GateAttempt, updatedStages []runmanifest.Stage, newCommit string, finalOutputRef runmanifest.ArtifactRef, finalOutputContent []byte, returnErr error) {
	gate := stage.Gate

	// Validate resolver availability once before the loop.
	if len(gate.Checks) > 0 && e.ResolveCheck == nil {
		return nil, completedStages, prevCommit, initialOutputRef, initialOutputContent,
			fmt.Errorf("gate on stage %q requires ResolveCheck to be set on Engine", stage.Name)
	}
	if (len(gate.Seats) > 0 || gate.Tier != "") && e.ResolveSeat == nil {
		return nil, completedStages, prevCommit, initialOutputRef, initialOutputContent,
			fmt.Errorf("gate on stage %q requires ResolveSeat to be set on Engine", stage.Name)
	}
	if gate.Tier != "" && e.Tiers == nil {
		return nil, completedStages, prevCommit, initialOutputRef, initialOutputContent,
			fmt.Errorf("gate on stage %q requires Tiers to be set on Engine", stage.Name)
	}

	// Mutable loop state.
	currentTierName := gate.Tier
	globalRound := 1
	tierRound := 1
	reviewedStageName := stage.Name
	reviewedOutputRef := initialOutputRef
	reviewedOutputContent := initialOutputContent
	// inputRefs/runInputs grow each RERUN as gate-feedback is appended.
	inputRefs := append([]runmanifest.ArtifactRef(nil), baseInputRefs...)
	runInputs := append([]replay.RunInput(nil), baseRunInputs...)

	thisAttempts := make([]runmanifest.GateAttempt, 0)

	for {
		// Resolve seats and next-stronger tier for this iteration.
		var seatNames []string
		var nextStronger string
		if currentTierName != "" {
			seats, next, ok := e.Tiers(currentTierName)
			if !ok {
				return nil, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
					fmt.Errorf("tier %q not found", currentTierName)
			}
			seatNames = seats
			nextStronger = next
		} else {
			// Inline seats: no escalation ladder.
			seatNames = gate.Seats
			nextStronger = ""
		}

		// The stage output this gate round is reviewing.
		gateInputs := []replay.RunInput{
			{
				Role:      stage.Produces,
				MediaType: reviewedOutputRef.MediaType,
				Content:   reviewedOutputContent,
			},
		}

		// Run checks then seats.
		checkSeatResults, checksPassed, checkBlocks := e.runGateChecks(
			ctx, worktreeDir, scratch, gate.Checks, gateInputs, as, globalRound,
		)
		modelSeatResults, seatVerdicts, seatBlockRequired := e.runGateSeats(
			ctx, worktreeDir, scratch, seatNames, gateInputs, as, globalRound,
		)

		// Synthesize verdict.
		syn := synthesizeVerdict(
			checksPassed, seatVerdicts,
			tierRound, gate.EffectiveMaxRounds(), gate.EffectivePassThreshold(),
			len(seatNames),
		)

		// Build and record the gate attempt.
		gateID := fmt.Sprintf("%s.r%d", stage.Name, globalRound)
		allSeats := append(checkSeatResults, modelSeatResults...)
		attempt := runmanifest.GateAttempt{
			GateID: gateID,
			Phase:  stage.Name,
			Round:  globalRound,
			Tier:   tierToInt(currentTierName),
			Status: syn.status,
			ReviewedStages: []runmanifest.ReviewedRef{
				{
					Stage:    reviewedStageName,
					Role:     stage.Produces,
					Artifact: reviewedOutputRef.Artifact,
				},
			},
			Seats: allSeats,
			Decision: runmanifest.GateDecision{
				EscalationReason: syn.escalationReason,
				DegradedReason:   syn.degradedReason,
			},
			Timestamp: e.clock(),
		}
		thisAttempts = append(thisAttempts, attempt)
		allAttempts := append(append([]runmanifest.GateAttempt(nil), existingGateAttempts...), thisAttempts...)

		// Write CAS commit for this gate attempt.
		manifest := runmanifest.Manifest{
			RunID:           runID,
			Workflow:        wf.Name,
			WorkflowVersion: wf.Name + "-v1",
			Created:         created,
			Refs:            map[string]string{},
			Stages:          completedStages,
			Gates:           allAttempts,
			EnvAllowlist:    e.EnvAllowlist,
		}
		newCommit2, err := runmanifest.WriteManifestTree(
			ctx, e.Store, runsPrefix, manifest,
			filesForManifest(manifest, as),
			refstore.WriteOptions{
				ExpectedOld: prevCommit,
				Message:     fmt.Sprintf("live run %s: gate %s", runID, gateID),
			},
		)
		if err != nil {
			return nil, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
				fmt.Errorf("write gate attempt %s: %w", gateID, err)
		}
		prevCommit = newCommit2
		fmt.Fprintf(out, "captured gate %s status=%s\n", gateID, syn.status)

		switch syn.status {
		case runmanifest.GateStatusPass:
			return allAttempts, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent, nil

		case runmanifest.GateStatusRerun:
			feedbackBytes := buildGateFeedback(checkBlocks, seatBlockRequired)
			feedbackArt, err := as.AddContent("gate-feedback", "text/markdown; charset=utf-8", feedbackBytes)
			if err != nil {
				return nil, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
					fmt.Errorf("store gate feedback: %w", err)
			}
			feedbackRef := runmanifest.ArtifactFromManifestArtifact(feedbackArt)
			inputRefs = append(inputRefs, feedbackRef)
			runInputs = append(runInputs, replay.RunInput{
				Role:      "gate-feedback",
				MediaType: "text/markdown; charset=utf-8",
				Content:   feedbackBytes,
			})

			globalRound++
			tierRound++

			rerunName := fmt.Sprintf("%s.r%d", stage.Name, globalRound)
			rerunScratch := fmt.Sprintf("%s/stage%02d-r%d", scratch, stageIdx, globalRound)
			if err := os.MkdirAll(rerunScratch, 0o755); err != nil {
				return nil, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
					fmt.Errorf("mkdir rerun scratch: %w", err)
			}

			newOutputRef, newOutputContent, newStages, newCommit3, err := e.runAndCaptureStage(
				ctx, out, runID, gitSHA, created, wf,
				stage, rerunName, inputRefs, runInputs,
				rerunScratch, as, completedStages, allAttempts, prevCommit, worktreeDir,
			)
			if err != nil {
				return nil, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
					fmt.Errorf("rerun stage %s: %w", rerunName, err)
			}
			completedStages = newStages
			prevCommit = newCommit3
			reviewedOutputRef = newOutputRef
			reviewedOutputContent = newOutputContent
			reviewedStageName = rerunName

		case runmanifest.GateStatusEscalated:
			if nextStronger == "" {
				return allAttempts, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
					&GateEscalationError{Phase: stage.Name, RunID: runID, Reason: syn.escalationReason}
			}
			currentTierName = nextStronger
			globalRound++
			tierRound = 1
		}
	}
}
