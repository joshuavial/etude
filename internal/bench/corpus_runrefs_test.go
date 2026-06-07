package bench

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Gate-building helpers (synthetic tests only)
// ---------------------------------------------------------------------------

// makeGate builds a minimal valid GateAttempt for the given phase and round.
// It references the supplied stageNames for reviewed_stages (required by Validate).
func makeGate(phase string, round int, status runmanifest.GateStatus, stageNames []string, seats []runmanifest.SeatResult) runmanifest.GateAttempt {
	refs := make([]runmanifest.ReviewedRef, 0, len(stageNames))
	for _, s := range stageNames {
		refs = append(refs, runmanifest.ReviewedRef{Stage: s})
	}
	return runmanifest.GateAttempt{
		GateID:         phase + "-gate-r" + itoa(round),
		Phase:          phase,
		Round:          round,
		Tier:           1,
		Status:         status,
		ReviewedStages: refs,
		Seats:          seats,
		Timestamp:      time.Now().UTC(),
	}
}

// makeGoSeat returns a seat with a "go" verdict (no required changes).
func makeGoSeat(seat string) runmanifest.SeatResult {
	return runmanifest.SeatResult{
		Seat:      seat,
		Harness:   runmanifest.Harness{Name: "claude-code", Version: "1.0"},
		Provider:  runmanifest.Provider{Name: "anthropic", Model: "claude-opus-4"},
		Verdict:   runmanifest.SeatVerdictGo,
		Timestamp: time.Now().UTC(),
	}
}

// makeBlockSeat returns a seat with a "block" verdict and the supplied required items.
func makeBlockSeat(seat string, required []string) runmanifest.SeatResult {
	return runmanifest.SeatResult{
		Seat:      seat,
		Harness:   runmanifest.Harness{Name: "claude-code", Version: "1.0"},
		Provider:  runmanifest.Provider{Name: "anthropic", Model: "claude-opus-4"},
		Verdict:   runmanifest.SeatVerdictBlock,
		Required:  required,
		Timestamp: time.Now().UTC(),
	}
}

// makeManifestWithGates extends makeManifest by appending gate attempts.
func makeManifestWithGates(runID string, created time.Time, stages []runmanifest.Stage, gates []runmanifest.GateAttempt) runmanifest.Manifest {
	m := makeManifest(runID, created, stages)
	m.Gates = gates
	return m
}

// ---------------------------------------------------------------------------
// Synthetic tests
// ---------------------------------------------------------------------------

// TestRunRefsSourceSingleRunNoGates verifies that a run with a plan stage but
// no plan gates produces one fixture with an empty (zero-valued) LabelHint.
func TestRunRefsSourceSingleRunNoGates(t *testing.T) {
	planText := []byte("# Plan\nDo something.")
	stage := makeStage("plan", nil, planText)
	files := map[string][]byte{stage.Output.Path: planText}
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	manifest := makeManifest("run-nogates", ts, []runmanifest.Stage{stage})
	store, _ := seedRun(t, manifest, files)

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("len(fixtures) = %d, want 1", len(fixtures))
	}
	f := fixtures[0]
	if string(f.Artifact) != string(planText) {
		t.Errorf("Artifact = %q, want %q", f.Artifact, planText)
	}
	if f.Provenance.RunID != "run-nogates" {
		t.Errorf("Provenance.RunID = %q, want %q", f.Provenance.RunID, "run-nogates")
	}
	if f.Provenance.Phase != "plan" {
		t.Errorf("Provenance.Phase = %q, want %q", f.Provenance.Phase, "plan")
	}
	if f.Provenance.Stage != "plan" {
		t.Errorf("Provenance.Stage = %q, want %q", f.Provenance.Stage, "plan")
	}
	if f.Provenance.Round != 0 {
		t.Errorf("Provenance.Round = %d, want 0 (no gates)", f.Provenance.Round)
	}
	// LabelHint should be zero-valued when no gates exist.
	if f.Label.Status != "" {
		t.Errorf("Label.Status = %q, want empty (no gates)", f.Label.Status)
	}
	if f.Label.Rounds != 0 {
		t.Errorf("Label.Rounds = %d, want 0", f.Label.Rounds)
	}
}

// TestRunRefsSourceSingleRunWithGates verifies provenance and label hint for a
// run with a multi-round plan gate (2 reruns → 1 pass).
func TestRunRefsSourceSingleRunWithGates(t *testing.T) {
	planText := []byte("# Plan\nV3 plan text.")
	stage := makeStage("plan", nil, planText)
	files := map[string][]byte{stage.Output.Path: planText}
	ts := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)

	gates := []runmanifest.GateAttempt{
		makeGate("plan", 1, runmanifest.GateStatusRerun, []string{"plan"}, []runmanifest.SeatResult{
			makeBlockSeat("opus", []string{"fix A", "fix B"}),
			makeGoSeat("codex"),
		}),
		makeGate("plan", 2, runmanifest.GateStatusRerun, []string{"plan"}, []runmanifest.SeatResult{
			makeBlockSeat("opus", []string{"fix A"}),
			makeBlockSeat("codex", []string{"fix C"}),
		}),
		makeGate("plan", 3, runmanifest.GateStatusPass, []string{"plan"}, []runmanifest.SeatResult{
			makeGoSeat("opus"),
			makeGoSeat("codex"),
		}),
	}

	manifest := makeManifestWithGates("run-gates", ts, []runmanifest.Stage{stage}, gates)
	store, commit := seedRun(t, manifest, files)

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("len(fixtures) = %d, want 1", len(fixtures))
	}
	f := fixtures[0]

	// Artifact bytes.
	if string(f.Artifact) != string(planText) {
		t.Errorf("Artifact = %q, want %q", f.Artifact, planText)
	}
	if f.MediaType != stage.Output.MediaType {
		t.Errorf("MediaType = %q, want %q", f.MediaType, stage.Output.MediaType)
	}

	// Provenance.
	if f.Provenance.RunID != "run-gates" {
		t.Errorf("Provenance.RunID = %q", f.Provenance.RunID)
	}
	if f.Provenance.Phase != "plan" {
		t.Errorf("Provenance.Phase = %q", f.Provenance.Phase)
	}
	if f.Provenance.Stage != "plan" {
		t.Errorf("Provenance.Stage = %q", f.Provenance.Stage)
	}
	if f.Provenance.SourceCommit != commit {
		t.Errorf("Provenance.SourceCommit = %q, want %q", f.Provenance.SourceCommit, commit)
	}
	// Round is the highest recorded GateAttempt.Round (= 3, the final round number).
	if f.Provenance.Round != 3 {
		t.Errorf("Provenance.Round = %d, want 3 (highest gate round number)", f.Provenance.Round)
	}

	// LabelHint: derived from the final (round 3 = pass) gate attempt.
	if f.Label.Status != GateHintPass {
		t.Errorf("Label.Status = %q, want %q", f.Label.Status, GateHintPass)
	}
	if f.Label.Rounds != 3 {
		t.Errorf("Label.Rounds = %d, want 3 (attempt count)", f.Label.Rounds)
	}
	if f.Label.FinalRound != 3 {
		t.Errorf("Label.FinalRound = %d, want 3 (highest round number)", f.Label.FinalRound)
	}
	if len(f.Label.Seats) != 2 {
		t.Fatalf("len(Label.Seats) = %d, want 2", len(f.Label.Seats))
	}
	for _, seat := range f.Label.Seats {
		if seat.Verdict != "go" {
			t.Errorf("seat %q: Verdict = %q, want go", seat.Seat, seat.Verdict)
		}
		if seat.RequiredCount != 0 {
			t.Errorf("seat %q: RequiredCount = %d, want 0", seat.Seat, seat.RequiredCount)
		}
	}
}

// TestRunRefsSourceMultipleRuns verifies one fixture per eligible run and
// correct ordering (most-recent first, same as SelectCohort).
func TestRunRefsSourceMultipleRuns(t *testing.T) {
	store := newMultiRunStore(t)

	times := []time.Time{
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
	}
	runIDs := []string{"run-multi-a", "run-multi-c", "run-multi-b"}

	for i, id := range runIDs {
		text := []byte("plan for " + id)
		stage := makeStage("plan", nil, text)
		files := map[string][]byte{stage.Output.Path: text}
		manifest := makeManifest(id, times[i], []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 3 {
		t.Fatalf("len(fixtures) = %d, want 3", len(fixtures))
	}

	// Expect newest first: run-multi-c (Mar), run-multi-b (Feb), run-multi-a (Jan).
	wantOrder := []string{"run-multi-c", "run-multi-b", "run-multi-a"}
	for i, want := range wantOrder {
		if fixtures[i].Provenance.RunID != want {
			t.Errorf("fixtures[%d].Provenance.RunID = %q, want %q", i, fixtures[i].Provenance.RunID, want)
		}
	}
}

// TestRunRefsSourceNoStageMissing verifies that runs without the requested
// stage are excluded (SelectCohort skips them) — no fixture is produced.
func TestRunRefsSourceNoStageMissing(t *testing.T) {
	out := []byte("other output")
	stage := makeStage("other-stage", nil, out)
	files := map[string][]byte{stage.Output.Path: out}
	manifest := makeManifest("run-noplan", time.Now().UTC(), []runmanifest.Stage{stage})
	store, _ := seedRun(t, manifest, files)

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 0 {
		t.Errorf("len(fixtures) = %d, want 0 (stage missing run excluded)", len(fixtures))
	}
}

// TestRunRefsSourcePointerOutputSkipped verifies that runs whose plan stage
// uses pointer output are excluded by SelectCohort — no fixture produced.
func TestRunRefsSourcePointerOutputSkipped(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	store := newStore(dir)

	as := artifactstore.New()
	size := int64(512)
	ma, err := as.AddPointer("output", "application/octet-stream", artifactstore.Pointer{
		URI:    "https://example.com/out",
		SHA256: strings.Repeat("c", 64),
		Size:   &size,
	})
	if err != nil {
		t.Fatalf("AddPointer: %v", err)
	}
	ptrRef := runmanifest.ArtifactFromManifestArtifact(ma)
	ptrFile := as.Files()[ma.Path]

	stage := runmanifest.Stage{
		Name:       "plan",
		ProducedBy: "test-agent",
		GitSHA:     strings.Repeat("a", 40),
		Skill:      runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		Timestamp:  time.Now().UTC(),
		Output:     ptrRef,
	}
	manifest := makeManifest("run-ptrout-corpus", time.Now().UTC(), []runmanifest.Stage{stage})
	_, err = runmanifest.Writer{Store: store}.Write(ctx, manifest, map[string][]byte{ptrRef.Path: ptrFile}, runmanifest.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(ctx, CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 0 {
		t.Errorf("len(fixtures) = %d, want 0 (pointer output run excluded)", len(fixtures))
	}
}

// TestRunRefsSourceAmbiguousStageSkipped verifies that runs with two plan
// stages (ambiguous) are excluded — no fixture produced.
func TestRunRefsSourceAmbiguousStageSkipped(t *testing.T) {
	out1 := []byte("output-ambig-1")
	out2 := []byte("output-ambig-2")
	stage1 := makeStage("plan", nil, out1)
	stage2 := makeStage("plan", nil, out2)
	files := map[string][]byte{
		stage1.Output.Path: out1,
		stage2.Output.Path: out2,
	}
	manifest := makeManifest("run-ambig-corpus", time.Now().UTC(), []runmanifest.Stage{stage1, stage2})
	store, _ := seedRun(t, manifest, files)

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 0 {
		t.Errorf("len(fixtures) = %d, want 0 (ambiguous stage run excluded)", len(fixtures))
	}
}

// TestRunRefsSourceLastTruncation verifies that the Last selector is honoured.
func TestRunRefsSourceLastTruncation(t *testing.T) {
	store := newMultiRunStore(t)

	for i := 0; i < 5; i++ {
		ts := time.Date(2024, time.Month(i+1), 1, 0, 0, 0, 0, time.UTC)
		id := "run-corpus-trunc-" + string(rune('a'+i))
		text := []byte("plan for " + id)
		stage := makeStage("plan", nil, text)
		files := map[string][]byte{stage.Output.Path: text}
		manifest := makeManifest(id, ts, []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 2})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 2 {
		t.Errorf("len(fixtures) = %d, want 2", len(fixtures))
	}
	// The two most-recent: run-corpus-trunc-e (May) and run-corpus-trunc-d (Apr).
	if fixtures[0].Provenance.RunID != "run-corpus-trunc-e" {
		t.Errorf("fixtures[0].RunID = %q, want run-corpus-trunc-e", fixtures[0].Provenance.RunID)
	}
	if fixtures[1].Provenance.RunID != "run-corpus-trunc-d" {
		t.Errorf("fixtures[1].RunID = %q, want run-corpus-trunc-d", fixtures[1].Provenance.RunID)
	}
}

// TestRunRefsSourceEmptyStore verifies that an empty store returns no fixtures
// without error.
func TestRunRefsSourceEmptyStore(t *testing.T) {
	dir := initRepo(t)
	store := newStore(dir)

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 0 {
		t.Errorf("len(fixtures) = %d, want 0", len(fixtures))
	}
}

// ---------------------------------------------------------------------------
// harvestLabelHint unit tests
// ---------------------------------------------------------------------------

// TestHarvestLabelHintNoGates verifies zero-valued hint when no gates match.
func TestHarvestLabelHintNoGates(t *testing.T) {
	hint := harvestLabelHint("plan", nil)
	if hint.Status != "" {
		t.Errorf("Status = %q, want empty", hint.Status)
	}
	if hint.Rounds != 0 {
		t.Errorf("Rounds = %d, want 0", hint.Rounds)
	}
}

// TestHarvestLabelHintPhaseFilter verifies that only gates matching the
// requested phase contribute to the hint.
func TestHarvestLabelHintPhaseFilter(t *testing.T) {
	gates := []runmanifest.GateAttempt{
		makeGate("implement", 1, runmanifest.GateStatusPass, []string{"implement"}, []runmanifest.SeatResult{
			makeGoSeat("opus"),
		}),
		makeGate("plan", 1, runmanifest.GateStatusRerun, []string{"plan"}, []runmanifest.SeatResult{
			makeBlockSeat("opus", []string{"fix X"}),
		}),
		makeGate("plan", 2, runmanifest.GateStatusPass, []string{"plan"}, []runmanifest.SeatResult{
			makeGoSeat("opus"),
		}),
	}

	hint := harvestLabelHint("plan", gates)
	if hint.Status != GateHintPass {
		t.Errorf("Status = %q, want pass", hint.Status)
	}
	if hint.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2", hint.Rounds)
	}
}

// TestHarvestLabelHintSeatSummary verifies that RequiredCount reflects the
// number of Required items on the final round's seat results.
func TestHarvestLabelHintSeatSummary(t *testing.T) {
	gates := []runmanifest.GateAttempt{
		makeGate("plan", 1, runmanifest.GateStatusRerun, []string{"plan"}, []runmanifest.SeatResult{
			makeBlockSeat("opus", []string{"req1", "req2", "req3"}),
			makeGoSeat("codex"),
		}),
	}
	hint := harvestLabelHint("plan", gates)
	if hint.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1", hint.Rounds)
	}
	if len(hint.Seats) != 2 {
		t.Fatalf("len(Seats) = %d, want 2", len(hint.Seats))
	}
	opusSeat := hint.Seats[0]
	if opusSeat.Verdict != "block" {
		t.Errorf("opus Verdict = %q, want block", opusSeat.Verdict)
	}
	if opusSeat.RequiredCount != 3 {
		t.Errorf("opus RequiredCount = %d, want 3", opusSeat.RequiredCount)
	}
	codexSeat := hint.Seats[1]
	if codexSeat.Verdict != "go" {
		t.Errorf("codex Verdict = %q, want go", codexSeat.Verdict)
	}
	if codexSeat.RequiredCount != 0 {
		t.Errorf("codex RequiredCount = %d, want 0", codexSeat.RequiredCount)
	}
}

// TestRunRefsSourceNonContiguousRounds verifies that Provenance.Round is the
// highest recorded GateAttempt.Round — NOT the attempt count — so gapped or
// non-contiguous round sequences (e.g. {2, 5}) are handled correctly.
// With rounds {2, 5}: FinalRound=5 (highest Round field), Rounds=2 (count).
func TestRunRefsSourceNonContiguousRounds(t *testing.T) {
	planText := []byte("# Plan\nNon-contiguous rounds.")
	stage := makeStage("plan", nil, planText)
	files := map[string][]byte{stage.Output.Path: planText}
	ts := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)

	// Deliberately non-contiguous: rounds 2 and 5 (skipping 1, 3, 4).
	gates := []runmanifest.GateAttempt{
		makeGate("plan", 2, runmanifest.GateStatusRerun, []string{"plan"}, []runmanifest.SeatResult{
			makeBlockSeat("opus", []string{"fix A"}),
		}),
		makeGate("plan", 5, runmanifest.GateStatusPass, []string{"plan"}, []runmanifest.SeatResult{
			makeGoSeat("opus"),
		}),
	}

	manifest := makeManifestWithGates("run-noncontig", ts, []runmanifest.Stage{stage}, gates)
	store, _ := seedRun(t, manifest, files)

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("len(fixtures) = %d, want 1", len(fixtures))
	}
	f := fixtures[0]

	// FinalRound must be the highest Round field (5), not the attempt count (2).
	if f.Provenance.Round != 5 {
		t.Errorf("Provenance.Round = %d, want 5 (highest Round field, not attempt count)", f.Provenance.Round)
	}
	if f.Label.FinalRound != 5 {
		t.Errorf("Label.FinalRound = %d, want 5", f.Label.FinalRound)
	}
	// Rounds is the attempt count (2), distinct from FinalRound (5).
	if f.Label.Rounds != 2 {
		t.Errorf("Label.Rounds = %d, want 2 (attempt count, not highest round number)", f.Label.Rounds)
	}
	if f.Label.Status != GateHintPass {
		t.Errorf("Label.Status = %q, want pass", f.Label.Status)
	}
}

// TestHarvestLabelHintNonContiguousRounds is a direct unit test of the
// harvestLabelHint function with gapped rounds {2, 5}.
func TestHarvestLabelHintNonContiguousRounds(t *testing.T) {
	gates := []runmanifest.GateAttempt{
		makeGate("plan", 2, runmanifest.GateStatusRerun, []string{"plan"}, []runmanifest.SeatResult{
			makeBlockSeat("opus", []string{"fix A"}),
		}),
		makeGate("plan", 5, runmanifest.GateStatusPass, []string{"plan"}, []runmanifest.SeatResult{
			makeGoSeat("opus"),
		}),
	}
	hint := harvestLabelHint("plan", gates)
	if hint.FinalRound != 5 {
		t.Errorf("FinalRound = %d, want 5 (highest Round field)", hint.FinalRound)
	}
	if hint.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2 (attempt count)", hint.Rounds)
	}
	if hint.Status != GateHintPass {
		t.Errorf("Status = %q, want pass", hint.Status)
	}
}

// ---------------------------------------------------------------------------
// Real ref assertion (loose: the one-plan-per-run limitation is the invariant)
// ---------------------------------------------------------------------------

// TestRunRefsSourceRealRef_EtudeRun asserts against the real captured run
// refs/etude/runs/etude-2bm.1, which has one plan stage and four plan gate
// rounds (3 rerun → 1 pass). This locks in the documented one-plan-per-run
// limitation as an explicit test.
//
// The assertion is intentionally LOOSE: we assert exactly ONE fixture and that
// the hint surfaces a multi-round progression (Rounds >= 2 followed by a final
// pass), so that future captures with more rounds do not break this test.
func TestRunRefsSourceRealRef_EtudeRun(t *testing.T) {
	// Use the actual repo's refstore (same path pattern as other real-ref tests).
	store := newStore("../..") // internal/bench/../../ = repo root

	ctx := context.Background()

	// Verify the ref exists; skip if not (e.g. fresh clone without run refs).
	if _, err := store.Resolve(ctx, "refs/etude/runs/etude-2bm.1"); err != nil {
		t.Skipf("refs/etude/runs/etude-2bm.1 not found in repo: %v", err)
	}

	src := RunRefsSource{Store: store}
	fixtures, err := src.Fixtures(ctx, CohortSelector{Stage: "plan", Last: 100})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}

	// Find the fixture for etude-2bm.1 specifically (other runs may also be present).
	var found *Fixture
	for i := range fixtures {
		if fixtures[i].Provenance.RunID == "etude-2bm.1" {
			found = &fixtures[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no fixture found for etude-2bm.1")
	}

	// The real run has 4 plan gate rounds (3 rerun + 1 pass).
	// Assert: at least 2 rounds (multi-round progression), final status is pass.
	if found.Label.Rounds < 2 {
		t.Errorf("Label.Rounds = %d, want >= 2 (multi-round progression)", found.Label.Rounds)
	}
	if found.Label.Status != GateHintPass {
		t.Errorf("Label.Status = %q, want pass (final gate passed)", found.Label.Status)
	}

	// The artifact must be non-empty (a real plan text).
	if len(found.Artifact) == 0 {
		t.Error("Artifact is empty, want non-empty plan text")
	}

	// Provenance sanity.
	if found.Provenance.RunID != "etude-2bm.1" {
		t.Errorf("Provenance.RunID = %q, want etude-2bm.1", found.Provenance.RunID)
	}
	if found.Provenance.Phase != "plan" {
		t.Errorf("Provenance.Phase = %q, want plan", found.Provenance.Phase)
	}
	if found.Provenance.SourceCommit == "" {
		t.Error("Provenance.SourceCommit is empty")
	}
}
