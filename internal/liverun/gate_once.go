package liverun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// gatePromptRole is the input role under which the shared gate prompt is
// materialized for every seat and check. Seats read it from
// $ETUDE_INPUTS_DIR/00-gate-prompt.
const gatePromptRole = "gate-prompt"

// ErrRunNotFound is returned by GateStage when the run ref does not exist. A
// gate attaches to work that was already recorded, so this is a hard error and
// nothing is written.
var ErrRunNotFound = errors.New("run not found")

// ErrNoReviewableStage is returned when the run exists but carries no stage
// producing the gated phase's role — there is nothing for the gate to review.
var ErrNoReviewableStage = errors.New("no reviewable stage on run")

// GateRequest describes one supervised gate invocation.
//
// Unlike runGate, there is no stage runner to re-invoke: a not-pass returns to
// the caller, who returns it to whoever produced the artifact.
type GateRequest struct {
	// RunID is the run ref to gate against. It must already exist.
	RunID string
	// Stage is the workflow stage whose gate block drives this attempt.
	Stage workflow.Stage
	// Artifact is the reviewed content, inlined into the shared prompt.
	Artifact []byte
	// WorktreeDir is where checks and seats execute. For a supervised gate this
	// is the caller's repo root, NOT a pristine checkout: the work under review
	// is uncommitted, so `make test` must see the real tree.
	WorktreeDir string
	// ScratchDir must NOT be at or under WorktreeDir (ExecRunner rejects that).
	ScratchDir string
}

// GateOutcome is the result of one supervised gate attempt.
type GateOutcome struct {
	GateID  string
	Round   int
	Status  runmanifest.GateStatus
	Attempt runmanifest.GateAttempt
	// Commit is the new run-ref commit after the attempt was appended.
	Commit string
}

// Passed reports whether the gate may advance. Only "pass" advances; both
// "rerun" and "escalated" are non-pass, so a caller cannot accidentally treat a
// seat outage as success.
func (o GateOutcome) Passed() bool {
	return o.Status == runmanifest.GateStatusPass
}

// GateStage runs one stage's gate against an already-recorded run and appends
// the attempt to the run ref.
//
// It reuses runGateChecks/runGateSeats unchanged — this is deliberately not a
// second seat path. The record is written through runmanifest.Writer with an
// ExpectedOld CAS, the same call capture-gate uses, so an attempt written here
// and one written by capture-gate are interchangeable on the run.
func (e *Engine) GateStage(ctx context.Context, out io.Writer, req GateRequest) (GateOutcome, error) {
	gate := req.Stage.Gate
	if gate == nil {
		return GateOutcome{}, fmt.Errorf("stage %q has no gate configured", req.Stage.Name)
	}
	if len(gate.Checks) > 0 && e.ResolveCheck == nil {
		return GateOutcome{}, fmt.Errorf("gate on stage %q requires ResolveCheck to be set on Engine", req.Stage.Name)
	}
	if (len(gate.Seats) > 0 || gate.Tier != "") && e.ResolveSeat == nil {
		return GateOutcome{}, fmt.Errorf("gate on stage %q requires ResolveSeat to be set on Engine", req.Stage.Name)
	}

	ref := runsPrefix + req.RunID
	commit, err := e.Store.Resolve(ctx, ref)
	if errors.Is(err, refstore.ErrNotFound) {
		return GateOutcome{}, fmt.Errorf("%w: %s; capture the stage first (etude capture <stage> --run %s ...)",
			ErrRunNotFound, ref, req.RunID)
	}
	if err != nil {
		return GateOutcome{}, err
	}

	manifest, priorFiles, err := readRunAt(ctx, e.Store, commit)
	if err != nil {
		return GateOutcome{}, err
	}
	if manifest.RunID != req.RunID {
		return GateOutcome{}, fmt.Errorf("existing manifest run_id %q does not match --run %q", manifest.RunID, req.RunID)
	}

	// The gate reviews a stage that was already captured. Resolving it here (not
	// just "the run exists") is what enforces capture-before-gate per phase.
	reviewed, ok := latestStageForRole(manifest.Stages, req.Stage.Produces)
	if !ok {
		return GateOutcome{}, fmt.Errorf("%w: run %s has no stage producing role %q; capture it before gating stage %q",
			ErrNoReviewableStage, req.RunID, req.Stage.Produces, req.Stage.Name)
	}

	// Resolve seats for the configured tier. No flag can widen or narrow this.
	var seatNames []string
	if gate.Tier != "" {
		if e.Tiers == nil {
			return GateOutcome{}, fmt.Errorf("gate on stage %q requires Tiers to be set on Engine", req.Stage.Name)
		}
		seats, _, found := e.Tiers(gate.Tier)
		if !found {
			return GateOutcome{}, fmt.Errorf("tier %q not found in registry", gate.Tier)
		}
		seatNames = seats
	} else {
		seatNames = gate.Seats
	}

	round := nextPhaseRound(req.Stage.Name, manifest.Stages, manifest.Gates)

	prompt := buildGatePrompt(req.Stage, gate, seatNames, round, reviewed.Name, req.Artifact)
	gateInputs := []replay.RunInput{{
		Role:      gatePromptRole,
		MediaType: "text/markdown; charset=utf-8",
		Content:   prompt,
	}}

	if err := os.MkdirAll(req.ScratchDir, 0o755); err != nil {
		return GateOutcome{}, fmt.Errorf("create gate scratch: %w", err)
	}

	as := artifactstore.New()
	checkSeats, checksPassed, _ := e.runGateChecks(ctx, req.WorktreeDir, req.ScratchDir, gate.Checks, gateInputs, as, round)
	modelSeats, verdicts, _ := e.runGateSeats(ctx, req.WorktreeDir, req.ScratchDir, seatNames, gateInputs, as, round)

	syn := synthesizeVerdict(
		checksPassed, verdicts,
		round, gate.EffectiveMaxRounds(), gate.EffectivePassThreshold(),
		len(seatNames),
	)

	gateID := fmt.Sprintf("%s.r%d", req.Stage.Name, round)
	attempt := runmanifest.GateAttempt{
		GateID: gateID,
		Phase:  req.Stage.Name,
		Round:  round,
		Tier:   tierToInt(gate.Tier),
		Status: syn.status,
		ReviewedStages: []runmanifest.ReviewedRef{{
			Stage:    reviewed.Name,
			Role:     req.Stage.Produces,
			Artifact: reviewed.Output.Artifact,
		}},
		Seats: append(checkSeats, modelSeats...),
		Decision: runmanifest.GateDecision{
			EscalationReason: syn.escalationReason,
			DegradedReason:   syn.degradedReason,
		},
		Timestamp: e.clock(),
	}

	manifest.Gates = append(manifest.Gates, attempt)

	files := as.Files()
	for path, content := range priorFiles {
		files[path] = content
	}

	written, err := (runmanifest.Writer{Store: e.Store}).Write(ctx, manifest, files, runmanifest.WriteOptions{
		ExpectedOld: commit,
		Message:     fmt.Sprintf("gate: run %s gate %s", req.RunID, gateID),
	})
	if err != nil {
		return GateOutcome{}, fmt.Errorf("write gate attempt %s: %w", gateID, err)
	}

	printGateOutcome(out, gateID, syn.status, attempt.Seats)

	return GateOutcome{
		GateID:  gateID,
		Round:   round,
		Status:  syn.status,
		Attempt: attempt,
		Commit:  written,
	}, nil
}

// readRunAt loads the manifest and its artifact files from a run commit.
func readRunAt(ctx context.Context, store refstore.Store, commit string) (runmanifest.Manifest, map[string][]byte, error) {
	manifestBytes, err := store.ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		return runmanifest.Manifest{}, nil, err
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return runmanifest.Manifest{}, nil, err
	}
	files := make(map[string][]byte)
	for _, path := range runmanifest.ArtifactPaths(manifest) {
		content, err := store.ReadCommitFile(ctx, commit, path)
		if err != nil {
			return runmanifest.Manifest{}, nil, err
		}
		files[path] = content
	}
	return manifest, files, nil
}

// buildGatePrompt composes the ONE text every seat receives. Seats are model
// identities voting on an identical prompt, never role personas — so this is
// built once and handed to all of them unmodified.
//
// The prompt asks for a four-line RETURN block on stdout. It deliberately does
// NOT mention ETUDE_OUTPUT_FILE: writing the JSON envelope is the seat adapter's
// job, so a model cannot emit a passing envelope without actually stating a
// verdict the adapter can parse.
func buildGatePrompt(stage workflow.Stage, gate *workflow.GateConfig, seats []string, round int, reviewedStage string, artifact []byte) []byte {
	var sb strings.Builder
	sb.WriteString("ROLE: You are an independent reviewer seat on an etude phase gate.\n")
	sb.WriteString("You are a model identity voting on this shared prompt, not a role persona.\n")
	sb.WriteString("Review ONLY this message. Do not edit files; review only.\n\n")

	fmt.Fprintf(&sb, "PHASE: %s\n", stage.Name)
	fmt.Fprintf(&sb, "ROUND: %d\n", round)
	if gate.Tier != "" {
		fmt.Fprintf(&sb, "TIER: %s (seats: %s; every seat must return GO for the gate to pass)\n",
			gate.Tier, strings.Join(seats, ", "))
	}
	sb.WriteString("\nDECISION STANDARD: return GO when the artifact can advance as-is,\n")
	sb.WriteString("BLOCK when a required change must land first.\n")

	if abstraction := strings.TrimSpace(gate.Abstraction); abstraction != "" {
		sb.WriteString("\nABSTRACTION LEVEL (the altitude to review at):\n")
		sb.WriteString(abstraction)
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "\nARTIFACT UNDER REVIEW (role=%s, produced by stage %s):\n", stage.Produces, reviewedStage)
	sb.WriteString("=== BEGIN ARTIFACT ===\n")
	sb.Write(artifact)
	if len(artifact) > 0 && artifact[len(artifact)-1] != '\n' {
		sb.WriteString("\n")
	}
	sb.WriteString("=== END ARTIFACT ===\n")

	sb.WriteString("\nRETURN (exactly four lines, nothing else):\n")
	sb.WriteString("VERDICT: <GO|BLOCK>\n")
	sb.WriteString("BLOCKING: <numbered blocking findings, or \"none\">\n")
	sb.WriteString("OPTIONAL: <numbered non-blocking follow-ups, or \"none\">\n")
	sb.WriteString("CONFIDENCE: <high|medium|low>\n")
	return []byte(sb.String())
}

// printGateOutcome renders the synthesized verdict and every blocking seat's
// required changes, so a supervisor can hand the feedback straight back.
func printGateOutcome(out io.Writer, gateID string, status runmanifest.GateStatus, seats []runmanifest.SeatResult) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, "gate %s: %s\n", gateID, status)
	for _, s := range seats {
		fmt.Fprintf(out, "  %s (%s/%s): %s\n", s.Seat, s.Provider.Name, s.Provider.Model, s.Verdict)
		if s.FailureNote != "" {
			fmt.Fprintf(out, "    note: %s\n", s.FailureNote)
		}
		for _, r := range s.Required {
			fmt.Fprintf(out, "    required: %s\n", r)
		}
	}
}
