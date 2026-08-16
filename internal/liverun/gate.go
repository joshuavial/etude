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

	"github.com/joshuavial/etude/internal/artifactmedia"
	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/sessionevidence"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/joshuavial/etude/internal/worktree"
)

const insufficientUsableSeatsPrefix = "insufficient usable seats:"

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
	HarnessName            string // e.g. "claude-code"
	ProviderName           string // e.g. "anthropic" (before "/" in seat.Provider)
	Model                  string // e.g. "claude-opus" (after "/" in seat.Provider)
	RequireSessionEvidence bool
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
	Verdict  string               `json:"verdict"`
	Required []string             `json:"required,omitempty"`
	Optional []string             `json:"optional,omitempty"`
	Session  *seatSessionEnvelope `json:"session,omitempty"`
}

type seatSessionEnvelope struct {
	SessionID      string `json:"session_id,omitempty"`
	TranscriptURI  string `json:"transcript_uri,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
}

type gateSeatCheckoutFactory func() (dir string, cleanup func() error, err error)

// execCheckRunner implements CheckRunner using an external command. It
// materializes inputs and sets the strict env (PATH, ETUDE_INPUTS_DIR,
// ETUDE_OUTPUT_FILE) identically to replay.ExecRunner, but interprets exit
// code as the verdict and never requires an output file.
type execCheckRunner struct {
	command []string
	timeout time.Duration
	// envAllowlist is the list of env var NAMES (never values) passed through to
	// the check, mirroring replay.ExecRunner. A check is a real build/test
	// command — `make test` needs HOME to find the Go module cache — so a
	// strictly hermetic env makes every such check fail for a reason that has
	// nothing to do with the code under review.
	envAllowlist []string
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
	// Append allowlisted NAMES only; values are read here and never stored on the
	// runner or written to a manifest. The three names above are reserved and are
	// skipped defensively even though workflow validation already rejects them.
	for _, name := range r.envAllowlist {
		switch name {
		case "PATH", "ETUDE_INPUTS_DIR", "ETUDE_OUTPUT_FILE":
			continue
		}
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
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
// L1→1, L2→2, L3→3, L4→4; inline/unknown → 0.
// The manifest requires Tier ∈ {0,1,2,3,4}, so the integer mirrors the registry
// L-number faithfully (0 is reserved for inline/unknown/backfilled gates).
func tierToInt(name string) int {
	if len(name) == 2 && name[0] == 'L' {
		switch name[1] {
		case '1':
			return 1
		case '2':
			return 2
		case '3':
			return 3
		case '4':
			return 4
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
			escalationReason: fmt.Sprintf("%s got %d need %d", insufficientUsableSeatsPrefix, usable, minUsable),
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

// sessionInfoFields holds the minimal session identity needed by buildSessionEvidence.
// Callers (seat envelope, runner result) fill this from their respective structs.
type sessionInfoFields struct {
	SessionID      string
	TranscriptURI  string
	TranscriptPath string
}

// buildSessionEvidence is the shared core for building SessionEvidence from a
// session info triple. artifactRole is used as the artifact content role
// (e.g. "seatName-transcript" or "stageName-transcript"). scratchDir and
// worktreeDir are used by resolveTranscriptPath when the transcript path is
// relative. required controls whether a missing transcript path is fatal.
func buildSessionEvidence(as *artifactstore.Store, artifactRole, scratchDir, worktreeDir string, sess sessionInfoFields, required bool) (*runmanifest.SessionEvidence, string) {
	evidence := &runmanifest.SessionEvidence{
		SessionID:       sess.SessionID,
		TranscriptURI:   sess.TranscriptURI,
		RetrievalStatus: runmanifest.SessionEvidenceNotApplicable,
		RedactionStatus: runmanifest.SessionEvidenceNotApplicable,
	}
	if strings.TrimSpace(sess.SessionID) == "" && strings.TrimSpace(sess.TranscriptURI) == "" {
		return nil, "session evidence requires session_id or transcript_uri"
	}
	if strings.TrimSpace(sess.TranscriptPath) == "" {
		if required {
			return nil, "session evidence requires transcript_path"
		}
		return evidence, ""
	}

	transcriptPath, transcriptRoot := resolveTranscriptPath(sess.TranscriptPath, scratchDir, worktreeDir)
	content, err := sessionevidence.ReadRegularFileUnder(transcriptRoot, transcriptPath)
	if err != nil {
		evidence.RetrievalStatus = runmanifest.SessionEvidenceFailed
		evidence.RedactionStatus = runmanifest.SessionEvidenceNotApplicable
		return evidence, fmt.Sprintf("read transcript %s: %v", sess.TranscriptPath, err)
	}
	evidence.RetrievalStatus = runmanifest.SessionEvidenceRetrievalImported
	if err := sessionevidence.ScanForSecrets(content); err != nil {
		evidence.RedactionStatus = runmanifest.SessionEvidenceFailed
		return evidence, fmt.Sprintf("transcript redaction scan failed: %v", err)
	}
	evidence.RedactionStatus = runmanifest.SessionEvidenceRedactionPassed

	artifact, err := as.AddContent(artifactRole, artifactmedia.Infer(transcriptPath), content)
	if err != nil {
		evidence.RetrievalStatus = runmanifest.SessionEvidenceFailed
		return evidence, fmt.Sprintf("store transcript artifact: %v", err)
	}
	ref := runmanifest.ArtifactFromManifestArtifact(artifact)
	evidence.TranscriptArtifact = &ref
	return evidence, ""
}

func storeSeatSessionEvidence(as *artifactstore.Store, seatName, seatScratch, worktreeDir string, env *seatEnvelope, required bool) (*runmanifest.SessionEvidence, string) {
	if env == nil || env.Session == nil {
		if required {
			return nil, "agentic seat did not provide session evidence"
		}
		return nil, ""
	}
	session := env.Session
	if strings.TrimSpace(session.SessionID) == "" && strings.TrimSpace(session.TranscriptURI) == "" {
		return nil, "seat session evidence requires session_id or transcript_uri"
	}
	return buildSessionEvidence(as, seatName+"-transcript", seatScratch, worktreeDir, sessionInfoFields{
		SessionID:      session.SessionID,
		TranscriptURI:  session.TranscriptURI,
		TranscriptPath: session.TranscriptPath,
	}, required)
}

func resolveTranscriptPath(pathValue, seatScratch, worktreeDir string) (string, string) {
	if filepath.IsAbs(pathValue) {
		if rel, err := filepath.Rel(seatScratch, pathValue); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return pathValue, seatScratch
		}
		return pathValue, worktreeDir
	}
	scratchPath := filepath.Join(seatScratch, pathValue)
	if _, err := os.Lstat(scratchPath); err == nil {
		return scratchPath, seatScratch
	}
	return filepath.Join(worktreeDir, pathValue), worktreeDir
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
	outputOnly bool,
	checkoutFactory gateSeatCheckoutFactory,
) (seatResults []runmanifest.SeatResult, verdicts []runmanifest.SeatVerdict, blockRequired map[string][]string, ladderNotes []string) {
	blockRequired = make(map[string][]string)

	for i, seatName := range seatNames {
		seatScratch := filepath.Join(scratch, fmt.Sprintf("gate-r%d-seat%d", globalRound, i))
		_ = os.MkdirAll(seatScratch, 0o755)

		runner, meta, candidates, resolveErr := e.resolveSeatRunner(seatName)
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
		candidates = append([]SeatCandidate(nil), candidates...)

		seatWorktreeDir := worktreeDir
		if outputOnly {
			if scratchErr := validateOutputOnlySeatScratch(scratch, worktreeDir, e.Root); scratchErr != nil {
				note := fmt.Sprintf("validate output-only seat scratch: %v", scratchErr)
				seatResults = append(seatResults, runmanifest.SeatResult{
					Seat:        seatName,
					Harness:     runmanifest.Harness{Name: meta.HarnessName},
					Provider:    runmanifest.Provider{Name: meta.ProviderName, Model: meta.Model},
					Verdict:     runmanifest.SeatVerdictFailed,
					FailureNote: note,
					Timestamp:   e.clock(),
				})
				verdicts = append(verdicts, runmanifest.SeatVerdictFailed)
				continue
			}
			neutralDir, cleanup, neutralErr := newOutputOnlySeatDir(ctx, worktreeDir, e.Root)
			if neutralErr != nil {
				note := fmt.Sprintf("create output-only seat directory: %v", neutralErr)
				seatResults = append(seatResults, runmanifest.SeatResult{
					Seat:        seatName,
					Harness:     runmanifest.Harness{Name: meta.HarnessName},
					Provider:    runmanifest.Provider{Name: meta.ProviderName, Model: meta.Model},
					Verdict:     runmanifest.SeatVerdictFailed,
					FailureNote: note,
					Timestamp:   e.clock(),
				})
				verdicts = append(verdicts, runmanifest.SeatVerdictFailed)
				continue
			}
			defer cleanup()
			seatWorktreeDir = neutralDir
			var prepareErr error
			if len(candidates) == 0 {
				runner, prepareErr = outputOnlySeatRunner(runner, worktreeDir, neutralDir, "primary", e.Root)
			} else {
				for i := range candidates {
					candidates[i].Runner, prepareErr = outputOnlySeatRunner(
						candidates[i].Runner, worktreeDir, neutralDir, fmt.Sprintf("candidate-%d", i), e.Root,
					)
					if prepareErr != nil {
						break
					}
				}
			}
			if prepareErr != nil {
				note := fmt.Sprintf("prepare output-only seat command: %v", prepareErr)
				seatResults = append(seatResults, runmanifest.SeatResult{
					Seat:        seatName,
					Harness:     runmanifest.Harness{Name: meta.HarnessName},
					Provider:    runmanifest.Provider{Name: meta.ProviderName, Model: meta.Model},
					Verdict:     runmanifest.SeatVerdictFailed,
					FailureNote: note,
					Timestamp:   e.clock(),
				})
				verdicts = append(verdicts, runmanifest.SeatVerdictFailed)
				continue
			}
		}
		var checkoutCleanup func() error
		if checkoutFactory != nil {
			checkoutDir, cleanup, checkoutErr := checkoutFactory()
			if checkoutErr != nil {
				note := fmt.Sprintf("create pinned seat checkout: %v", checkoutErr)
				seatResults = append(seatResults, runmanifest.SeatResult{
					Seat:        seatName,
					Harness:     runmanifest.Harness{Name: meta.HarnessName},
					Provider:    runmanifest.Provider{Name: meta.ProviderName, Model: meta.Model},
					Verdict:     runmanifest.SeatVerdictFailed,
					FailureNote: note,
					Timestamp:   e.clock(),
				})
				verdicts = append(verdicts, runmanifest.SeatVerdictFailed)
				continue
			}
			seatWorktreeDir = checkoutDir
			checkoutCleanup = cleanup
		}

		req := replay.RunRequest{
			WorktreeDir:     seatWorktreeDir,
			ScratchDir:      seatScratch,
			Inputs:          gateInputs,
			OutputRole:      "seat-output",
			OutputMediaType: "application/json",
		}

		res, runErr, ladderNote, ranMeta := e.runSeatLadder(ctx, candidates, runner, meta, req)
		meta = ranMeta
		verdict, failureNote, env := classifySeatOutput(res, runErr)
		// The ladder note can only ride on failure_note, which the manifest
		// FORBIDS on go/block (validated at write time — attaching it to a
		// successful fallback makes the whole attempt fail to record). So on a
		// usable verdict the rung that produced it is carried by Harness.Name,
		// which is the durable signal, and the note is surfaced at gate level
		// instead via the returned ladderNotes.
		if ladderNote != "" {
			switch verdict {
			case runmanifest.SeatVerdictGo, runmanifest.SeatVerdictBlock:
				ladderNotes = append(ladderNotes, fmt.Sprintf("seat %s: %s", seatName, ladderNote))
			default:
				if failureNote == "" {
					failureNote = ladderNote
				} else {
					failureNote = ladderNote + "; " + failureNote
				}
			}
		}

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
			session, sessionFailure := storeSeatSessionEvidence(as, seatName, seatScratch, seatWorktreeDir, env, meta.RequireSessionEvidence)
			sr.Session = session
			if sessionFailure != "" {
				sr.Verdict = runmanifest.SeatVerdictMalfunction
				// Keep the ladder note: overwriting it here would discard which
				// rungs were tried, and a downgrade is exactly where that history
				// is most useful to whoever reads the record.
				if ladderNote != "" {
					sr.FailureNote = ladderNote + "; " + sessionFailure
				} else {
					sr.FailureNote = sessionFailure
				}
				verdict = sr.Verdict
			}
		}
		if verdict == runmanifest.SeatVerdictBlock && env != nil {
			blockRequired[seatName] = env.Required
		}
		if checkoutCleanup != nil {
			if cleanupErr := checkoutCleanup(); cleanupErr != nil {
				delete(blockRequired, seatName)
				verdict = runmanifest.SeatVerdictFailed
				sr.Verdict = verdict
				sr.FailureNote = fmt.Sprintf("cleanup pinned seat checkout: %v", cleanupErr)
				seatResults = append(seatResults, sr)
				verdicts = append(verdicts, verdict)
				break
			}
		}

		seatResults = append(seatResults, sr)
		verdicts = append(verdicts, verdict)
	}
	return seatResults, verdicts, blockRequired, ladderNotes
}

func newOutputOnlySeatDir(ctx context.Context, protectedDirs ...string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "etude-output-only-seat-*")
	if err != nil {
		return "", nil, err
	}
	originalDir := dir
	dir, err = filepath.Abs(dir)
	if err != nil {
		_ = os.RemoveAll(originalDir)
		return "", nil, fmt.Errorf("resolve output-only seat directory: %w", err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		_ = os.RemoveAll(originalDir)
		return "", nil, fmt.Errorf("resolve physical output-only seat directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for _, protectedDir := range protectedDirs {
		resolvedProtected, err := resolveGateDir(protectedDir)
		if err != nil {
			cleanup()
			return "", nil, fmt.Errorf("resolve checkout boundary: %w", err)
		}
		insideCheckout, err := pathAtOrBelow(resolvedProtected, dir)
		if err != nil {
			cleanup()
			return "", nil, fmt.Errorf("compare checkout boundary: %w", err)
		}
		if insideCheckout {
			cleanup()
			return "", nil, fmt.Errorf("temporary directory %q is inside checkout %q", dir, resolvedProtected)
		}
	}
	cmd := exec.CommandContext(ctx, "git", "init", "--quiet", "--template=", dir)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if output, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return dir, cleanup, nil
}

func validateOutputOnlySeatScratch(scratch string, protectedDirs ...string) error {
	for _, protectedDir := range protectedDirs {
		inside, err := pathAtOrBelow(protectedDir, scratch)
		if err != nil {
			return fmt.Errorf("compare scratch boundary: %w", err)
		}
		if inside {
			return fmt.Errorf("scratch directory %q is inside checkout %q", scratch, protectedDir)
		}
	}
	return nil
}

func pathAtOrBelow(root, path string) (bool, error) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false, err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil {
			return false, err
		}
		if os.SameFile(rootInfo, info) {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
	}
}

func lexicalPathAtOrBelow(root, path string) (bool, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func outputOnlySeatRunner(runner replay.Runner, commandRoot, neutralDir, commandName string, protectedDirs ...string) (replay.Runner, error) {
	execRunner, ok := runner.(*replay.ExecRunner)
	if !ok || execRunner == nil {
		return runner, nil
	}
	clone := *execRunner
	clone.Command = append([]string(nil), execRunner.Command...)
	clone.EnvAllowlist = clone.EnvAllowlist[:0:0]
	for _, name := range execRunner.EnvAllowlist {
		if !strings.HasPrefix(name, "GIT_") {
			clone.EnvAllowlist = append(clone.EnvAllowlist, name)
		}
	}
	if len(clone.Command) == 0 {
		return &clone, nil
	}
	if !filepath.IsAbs(clone.Command[0]) {
		if !strings.ContainsRune(clone.Command[0], filepath.Separator) {
			return &clone, nil
		}
		candidate := filepath.Join(commandRoot, clone.Command[0])
		if _, err := os.Stat(candidate); err != nil {
			return &clone, nil
		}
		clone.Command[0] = candidate
	}
	originalCommandPath := clone.Command[0]
	resolvedOriginParent, err := filepath.EvalSymlinks(filepath.Dir(originalCommandPath))
	if err != nil {
		return nil, fmt.Errorf("resolve seat executable origin parent: %w", err)
	}
	resolvedOriginPath := filepath.Join(resolvedOriginParent, filepath.Base(originalCommandPath))
	originalInsideCheckout := false
	for _, protectedDir := range append([]string{commandRoot}, protectedDirs...) {
		insideConfigured, err := lexicalPathAtOrBelow(protectedDir, originalCommandPath)
		if err != nil {
			return nil, fmt.Errorf("compare seat executable origin boundary: %w", err)
		}
		resolvedProtected, err := resolveGateDir(protectedDir)
		if err != nil {
			return nil, fmt.Errorf("resolve seat executable origin boundary: %w", err)
		}
		insideResolved, err := lexicalPathAtOrBelow(resolvedProtected, originalCommandPath)
		if err != nil {
			return nil, fmt.Errorf("compare resolved seat executable origin boundary: %w", err)
		}
		insidePhysicalOrigin, err := lexicalPathAtOrBelow(resolvedProtected, resolvedOriginPath)
		if err != nil {
			return nil, fmt.Errorf("compare physical seat executable origin boundary: %w", err)
		}
		if insideConfigured || insideResolved || insidePhysicalOrigin {
			originalInsideCheckout = true
			break
		}
	}
	commandPath, err := filepath.EvalSymlinks(originalCommandPath)
	if err != nil {
		return nil, fmt.Errorf("resolve seat executable: %w", err)
	}
	insideCheckout := false
	for _, protectedDir := range append([]string{commandRoot}, protectedDirs...) {
		inside, err := pathAtOrBelow(protectedDir, commandPath)
		if err != nil {
			return nil, fmt.Errorf("compare seat executable boundary: %w", err)
		}
		if inside {
			insideCheckout = true
			break
		}
	}
	if !insideCheckout {
		if originalInsideCheckout {
			clone.Command[0] = commandPath
		}
		return &clone, nil
	}
	info, err := os.Stat(commandPath)
	if err != nil {
		return nil, fmt.Errorf("stat seat executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("seat executable %q is not a regular file", commandPath)
	}
	content, err := os.ReadFile(commandPath)
	if err != nil {
		return nil, fmt.Errorf("read seat executable: %w", err)
	}
	materialized := filepath.Join(neutralDir, "etude-seat-"+commandName)
	if err := os.WriteFile(materialized, content, info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("materialize seat executable: %w", err)
	}
	clone.Command[0] = materialized
	return &clone, nil
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
	initialOutputStage string,
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
	globalRound := nextPhaseRound(stage.Name, completedStages, existingGateAttempts)
	tierRound := 1
	reviewedStageName := initialOutputStage
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

		// The workflow configuration is the authorization source. The manifest
		// records this resolved decision after the fact; it is never read back as
		// authority. A checks-only gate has no model seat to grant, so its resolved
		// grant remains false even if the inert workflow leaf is set.
		readCheckout := gate.ReadCheckout && len(seatNames) > 0
		var checkoutFactory gateSeatCheckoutFactory
		if readCheckout {
			gitlink, err := firstCheckoutGitlink(ctx, e.Root, gitSHA)
			if err != nil {
				return nil, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
					fmt.Errorf("inspect pinned checkout for read_checkout: %w", err)
			}
			if gitlink != "" {
				return nil, completedStages, prevCommit, reviewedOutputRef, reviewedOutputContent,
					fmt.Errorf("gate on stage %q: read_checkout cannot inspect submodule gitlink %q until GitHub issue #14 populates submodules and records their SHAs", stage.Name, gitlink)
			}
			checkoutFactory = func() (string, func() error, error) {
				checkout, checkoutErr := worktree.Checkout(ctx, e.Root, gitSHA)
				if checkoutErr != nil {
					return "", nil, checkoutErr
				}
				return checkout.Dir, checkout.Close, nil
			}
		}

		// The stage output this gate round is reviewing.
		checkInputs := []replay.RunInput{
			{
				Role:      stage.Produces,
				MediaType: reviewedOutputRef.MediaType,
				Content:   reviewedOutputContent,
			},
		}
		promptGate := *gate
		promptGate.Tier = currentTierName
		modelInputs := []replay.RunInput{{
			Role:      gatePromptRole,
			MediaType: "text/markdown; charset=utf-8",
			Content:   buildGatePrompt(stage, &promptGate, seatNames, globalRound, reviewedStageName, reviewedOutputContent, readCheckout, gitSHA),
		}}

		// Run checks then seats.
		checkSeatResults, checksPassed, checkBlocks := e.runGateChecks(
			ctx, worktreeDir, scratch, gate.Checks, checkInputs, as, globalRound,
		)
		outputOnly := !readCheckout
		modelSeatResults, seatVerdicts, seatBlockRequired, gateLadderNotes := e.runGateSeats(
			ctx, worktreeDir, scratch, seatNames, modelInputs, as, globalRound,
			outputOnly, checkoutFactory,
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
			GateID:       gateID,
			Phase:        stage.Name,
			Round:        globalRound,
			Tier:         tierToInt(currentTierName),
			Status:       syn.status,
			ReadCheckout: readCheckout,
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
				DegradedReason:   joinDegraded(syn.degradedReason, gateLadderNotes),
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

// firstCheckoutGitlink returns the first recursive mode-160000 entry in the
// pinned tree. Until GH #14 populates submodules and records their identities,
// granting reads in such a checkout would expose an empty directory as if it
// were evidence, so callers fail closed before invoking a model seat.
func firstCheckoutGitlink(ctx context.Context, worktreeDir, gitSHA string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreeDir, "ls-tree", "-r", "-z", "--full-tree", gitSHA, "--")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if detail := strings.TrimSpace(string(exitErr.Stderr)); detail != "" {
				return "", fmt.Errorf("git ls-tree %s: %w: %s", gitSHA, err, detail)
			}
		}
		return "", fmt.Errorf("git ls-tree %s: %w", gitSHA, err)
	}
	for _, record := range bytes.Split(out, []byte{0}) {
		meta, path, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			continue
		}
		fields := bytes.Fields(meta)
		if len(fields) >= 1 && bytes.Equal(fields[0], []byte("160000")) {
			return string(path), nil
		}
	}
	return "", nil
}

// resolveSeatRunner returns the runner for a seat's PRIMARY invocation, used
// when no candidate ladder is configured. Kept separate so the ladder path and
// the legacy path share one call site in runGateSeats.
// It also returns the resolved ladder so runSeatLadder does not resolve a second
// time: a resolver is a caller-supplied closure and may well be stateful (test
// doubles here are), so calling it twice per seat is both wasteful and surprising.
func (e *Engine) resolveSeatRunner(seatName string) (replay.Runner, SeatMeta, []SeatCandidate, error) {
	if e.ResolveSeatCandidates == nil {
		runner, meta, err := e.ResolveSeat(seatName)
		return runner, meta, nil, err
	}
	candidates, err := e.ResolveSeatCandidates(seatName)
	if err != nil {
		return nil, SeatMeta{}, nil, err
	}
	if len(candidates) == 0 {
		return nil, SeatMeta{}, nil, fmt.Errorf("seat %q resolved to no invocation candidates", seatName)
	}
	// The primary's identity is the seat's declared one, and is what an exhausted
	// ladder records — runmanifest rejects a seat result with an empty
	// harness.name, so this must never come back blank.
	return candidates[0].Runner, candidates[0].Meta, candidates, nil
}

// runSeatLadder walks a seat's invocation candidates in order and returns the
// first USABLE outcome, together with a machine-readable note describing every
// rung that did not produce one.
//
// A `go` or `block` is a real verdict and stops the ladder — only an OUTAGE
// (failed/empty/malfunction) moves to the next rung. An in-harness candidate is
// SKIPPED, never exec'd, and never terminates the walk: etude cannot know whether
// its caller is able to run one, so it must not decide on the caller's behalf by
// stopping short of a rung it could have run itself.
//
// With no ladder configured this is exactly one Run call on the primary, which is
// the pre-existing behaviour.
func (e *Engine) runSeatLadder(
	ctx context.Context,
	candidates []SeatCandidate,
	primary replay.Runner,
	primaryMeta SeatMeta,
	req replay.RunRequest,
) (replay.RunResult, error, string, SeatMeta) {
	// No ladder configured: exactly one Run on the primary, the pre-existing path.
	if len(candidates) == 0 {
		res, err := primary.Run(ctx, req)
		return res, err, "", primaryMeta
	}

	var (
		notes    []string
		lastRes  replay.RunResult
		lastErr  error
		lastMeta = primaryMeta
		ran      bool
	)
	for _, c := range candidates {
		if c.InHarness {
			// Stable marker + the candidate verbatim, so a supervisor can detect
			// this programmatically instead of string-matching English.
			notes = append(notes, fmt.Sprintf("IN_HARNESS_CANDIDATE_SKIPPED harness=%s invoke=%s", c.Harness, c.Invoke))
			continue
		}
		if c.Runner == nil {
			continue
		}
		res, err := c.Runner.Run(ctx, req)
		ran = true
		lastRes, lastErr, lastMeta = res, err, c.Meta
		verdict, _, _ := classifySeatOutput(res, err)
		if verdict == runmanifest.SeatVerdictGo || verdict == runmanifest.SeatVerdictBlock {
			// A real verdict from this rung; record it against THIS harness.
			return res, err, strings.Join(notes, "; "), c.Meta
		}
		notes = append(notes, fmt.Sprintf("CANDIDATE_FAILED harness=%s invoke=%s verdict=%s", c.Harness, c.Invoke, verdict))
	}

	if !ran {
		// Every candidate was in-harness, so etude ran nothing. Report it as a
		// runner failure rather than an empty result: "produced no output" would
		// imply something was attempted. The seat's declared identity is used, so
		// harness.name is never blank (runmanifest rejects that outright).
		return replay.RunResult{}, fmt.Errorf("%w: no candidate for this seat could be run by etude",
			replay.ErrRunnerNotConfigured), strings.Join(notes, "; "), primaryMeta
	}

	// Every exec-able rung was tried and none produced a usable verdict. Return
	// the LAST one's outcome — already executed, not re-run — so the caller has a
	// concrete result to classify.
	return lastRes, lastErr, strings.Join(notes, "; "), lastMeta
}
