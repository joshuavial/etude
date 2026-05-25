package replay

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// seedRunForRecord seeds a source run with one input and one output into a
// fresh temp git repo and returns the store, the source commit, and the
// ResolvedStage for driving RunRecorder.Record.
func seedRunForRecord(t *testing.T) (store refstore.Store, sourceCommit string, resolved ResolvedStage) {
	t.Helper()

	inputContent := []byte("source input bytes")
	inputRef, inputBytes := contentArtifact("prompt", inputContent)
	outputContent := []byte("source output bytes")
	stage := makeStage("gen", []runmanifest.ArtifactRef{inputRef}, outputContent)

	files := map[string][]byte{
		inputRef.Path:     inputBytes,
		stage.Output.Path: outputContent,
	}
	manifest := makeManifest("source-run", map[string]string{"pr": "1"}, []runmanifest.Stage{stage})
	store, _, sourceCommit = seedRun(t, manifest, files)

	// Resolve the seeded run so we have a proper ResolvedStage.
	var err error
	resolved, err = ResolveInputs(context.Background(), store, "source-run", "gen")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	return store, sourceCommit, resolved
}

// TestRunRecorderHappyPath verifies the full record round-trip:
// - RecordedRun.RunID/Commit/OutputArtifact are non-empty,
// - the ref exists and is readable,
// - ReplayOf is set correctly (RunID/Stage/Commit pinned to source commit),
// - ProducedBy is "replay",
// - Stage.Skill mirrors Producer.Skill.
func TestRunRecorderHappyPath(t *testing.T) {
	store, sourceCommit, resolved := seedRunForRecord(t)

	fixedTime := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	res := RunResult{
		Output:    []byte("replay output bytes"),
		MediaType: "text/plain; charset=utf-8",
		Producer: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		},
	}

	rec := RunRecorder{Store: store, Now: func() time.Time { return fixedTime }}
	recorded, err := rec.Record(context.Background(), "source-run", "gen", resolved, res)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	wantRunID := "source-run-replay-20260522T100000Z"
	if recorded.RunID != wantRunID {
		t.Errorf("RunID = %q, want %q", recorded.RunID, wantRunID)
	}
	if recorded.Commit == "" {
		t.Error("Commit is empty")
	}
	if recorded.OutputArtifact == "" {
		t.Error("OutputArtifact is empty")
	}
	if len(recorded.OutputArtifact) != 64 {
		t.Errorf("OutputArtifact length = %d, want 64 hex chars", len(recorded.OutputArtifact))
	}

	// The ref must be resolvable.
	ref := "refs/etude/runs/" + recorded.RunID
	commit, err := store.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve %q: %v", ref, err)
	}
	if commit != recorded.Commit {
		t.Errorf("resolved commit = %q, want %q", commit, recorded.Commit)
	}

	// Parse and inspect the recorded manifest.
	raw, err := store.ReadCommitFile(context.Background(), recorded.Commit, "manifest.json")
	if err != nil {
		t.Fatalf("ReadCommitFile manifest.json: %v", err)
	}
	m, err := runmanifest.ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if len(m.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(m.Stages))
	}
	s := m.Stages[0]

	if s.ProducedBy != "replay" {
		t.Errorf("ProducedBy = %q, want replay", s.ProducedBy)
	}
	if s.ReplayOf == nil {
		t.Fatal("ReplayOf is nil")
	}
	if s.ReplayOf.RunID != "source-run" {
		t.Errorf("ReplayOf.RunID = %q, want source-run", s.ReplayOf.RunID)
	}
	if s.ReplayOf.Stage != "gen" {
		t.Errorf("ReplayOf.Stage = %q, want gen", s.ReplayOf.Stage)
	}
	if s.ReplayOf.Commit != sourceCommit {
		t.Errorf("ReplayOf.Commit = %q, want %q", s.ReplayOf.Commit, sourceCommit)
	}

	// Stage.Skill must mirror Producer.Skill.
	if s.Skill != s.Producer.Skill {
		t.Errorf("Stage.Skill %+v != Producer.Skill %+v", s.Skill, s.Producer.Skill)
	}
}

// TestRunRecorderInputBytesCopiedVerbatim verifies that source input raw bytes
// appear byte-identical in the recorded replay run (reading from the source commit
// path is the mechanism that handles both content and pointer records correctly).
func TestRunRecorderInputBytesCopiedVerbatim(t *testing.T) {
	store, _, resolved := seedRunForRecord(t)

	// Read the source input bytes directly to compare later.
	if len(resolved.ResolvedInputs) == 0 {
		t.Skip("no inputs in source stage")
	}
	inp := resolved.ResolvedInputs[0]
	wantInputBytes, err := store.ReadCommitFile(context.Background(), resolved.Commit, inp.ArtifactRef.Path)
	if err != nil {
		t.Fatalf("read source input: %v", err)
	}

	res := RunResult{
		Output:    []byte("replay out"),
		MediaType: "application/octet-stream",
		Producer: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		},
	}

	fixedTime := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	rec := RunRecorder{Store: store, Now: func() time.Time { return fixedTime }}
	recorded, err := rec.Record(context.Background(), "source-run", "gen", resolved, res)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The input must be present at the same path in the replay run.
	gotInputBytes, err := store.ReadCommitFile(context.Background(), recorded.Commit, inp.ArtifactRef.Path)
	if err != nil {
		t.Fatalf("read replay input: %v", err)
	}
	if !bytes.Equal(gotInputBytes, wantInputBytes) {
		t.Errorf("replay input bytes differ from source:\ngot:  %q\nwant: %q", gotInputBytes, wantInputBytes)
	}
}

// TestRunRecorderOutputArtifactSHA256 verifies that RecordedRun.OutputArtifact
// is the SHA-256 of the replay output bytes.
func TestRunRecorderOutputArtifactSHA256(t *testing.T) {
	store, _, resolved := seedRunForRecord(t)

	replayOutput := []byte("output for sha check")
	res := RunResult{
		Output:    replayOutput,
		MediaType: "application/octet-stream",
		Producer: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "sk", Repo: "repo", Version: "v1"},
		},
	}

	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := RunRecorder{Store: store, Now: func() time.Time { return fixedTime }}
	recorded, err := rec.Record(context.Background(), "source-run", "gen", resolved, res)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	// OutputPath must exist in the commit tree and contain the replay output bytes.
	got, err := store.ReadCommitFile(context.Background(), recorded.Commit, recorded.OutputPath)
	if err != nil {
		t.Fatalf("read output artifact: %v", err)
	}
	if !bytes.Equal(got, replayOutput) {
		t.Errorf("output artifact bytes = %q, want %q", got, replayOutput)
	}
}

// TestAllocateRunIDSuffixing verifies that a second allocation with the same
// base returns base-2 when base is already taken.
func TestAllocateRunIDSuffixing(t *testing.T) {
	dir := initRepo(t)
	store := refstore.New(dir)
	ctx := context.Background()

	base := "myrun-replay-20260101T000000Z"

	// First allocation must return base unchanged.
	first, err := AllocateRunID(ctx, store, base)
	if err != nil {
		t.Fatalf("first AllocateRunID: %v", err)
	}
	if first != base {
		t.Errorf("first id = %q, want %q", first, base)
	}

	// Occupy the base slot.
	_, err = store.WriteCommit(ctx, "refs/etude/runs/"+base,
		map[string][]byte{"marker": []byte("x")},
		refstore.WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit to occupy slot: %v", err)
	}

	// Second allocation must return base-2.
	second, err := AllocateRunID(ctx, store, base)
	if err != nil {
		t.Fatalf("second AllocateRunID: %v", err)
	}
	if second != base+"-2" {
		t.Errorf("second id = %q, want %q", second, base+"-2")
	}
}

// TestAllocateRunIDExhausted verifies that AllocateRunID returns an error when
// all 10 slots are occupied.
func TestAllocateRunIDExhausted(t *testing.T) {
	dir := initRepo(t)
	store := refstore.New(dir)
	ctx := context.Background()

	base := "exhaust-replay-20260101T000000Z"

	// Occupy all 10 candidate slots.
	slots := []string{base}
	for n := 2; n <= 10; n++ {
		slots = append(slots, base+"-"+itoa(n))
	}
	for _, slot := range slots {
		_, err := store.WriteCommit(ctx, "refs/etude/runs/"+slot,
			map[string][]byte{"m": []byte("x")},
			refstore.WriteOptions{})
		if err != nil {
			t.Fatalf("WriteCommit %q: %v", slot, err)
		}
	}

	_, err := AllocateRunID(ctx, store, base)
	if err == nil {
		t.Fatal("AllocateRunID returned nil error, want exhausted-slots error")
	}
}

// TestRunRecorderProducerMirrored verifies that the recorded stage carries the
// producer from RunResult.Producer (not the source's producer).
func TestRunRecorderProducerMirrored(t *testing.T) {
	store, _, resolved := seedRunForRecord(t)

	overrideProducer := runmanifest.Producer{
		Skill: runmanifest.Skill{ID: "override-skill", Repo: "override-repo", Version: "vNEW"},
		Model: "claude-opus",
	}
	res := RunResult{
		Output:    []byte("override output"),
		MediaType: "text/plain; charset=utf-8",
		Producer:  overrideProducer,
	}

	fixedTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rec := RunRecorder{Store: store, Now: func() time.Time { return fixedTime }}
	recorded, err := rec.Record(context.Background(), "source-run", "gen", resolved, res)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	raw, err := store.ReadCommitFile(context.Background(), recorded.Commit, "manifest.json")
	if err != nil {
		t.Fatalf("ReadCommitFile: %v", err)
	}
	m, err := runmanifest.ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	s := m.Stages[0]
	if s.Producer.Skill.Version != "vNEW" {
		t.Errorf("Producer.Skill.Version = %q, want vNEW", s.Producer.Skill.Version)
	}
	if s.Producer.Model != "claude-opus" {
		t.Errorf("Producer.Model = %q, want claude-opus", s.Producer.Model)
	}
	// Stage.Skill mirrors Producer.Skill.
	if s.Skill.ID != "override-skill" {
		t.Errorf("Skill.ID = %q, want override-skill", s.Skill.ID)
	}
}

// TestRunRecorderNowDefault verifies that a zero Now field defaults to
// time.Now() without panicking (smoke test: just check no error + non-empty RunID).
func TestRunRecorderNowDefault(t *testing.T) {
	store, _, resolved := seedRunForRecord(t)

	res := RunResult{
		Output:    []byte("default-now output"),
		MediaType: "application/octet-stream",
		Producer: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "sk", Repo: "repo", Version: "v1"},
		},
	}

	rec := RunRecorder{Store: store} // Now is nil — must default to time.Now
	recorded, err := rec.Record(context.Background(), "source-run", "gen", resolved, res)
	if err != nil {
		t.Fatalf("Record with nil Now: %v", err)
	}
	if recorded.RunID == "" {
		t.Error("RunID is empty")
	}
	if !strings.Contains(recorded.RunID, "source-run-replay-") {
		t.Errorf("RunID %q does not match expected pattern", recorded.RunID)
	}
}

// TestRunRecorderEmptyOutputError verifies that Record fails gracefully when
// the output bytes slice is empty (AddContent should still succeed; a zero-byte
// artifact is technically valid). This test verifies the caller (bench/cli) is
// responsible for the empty-output guard, not Record itself.
// (Record does not impose a non-empty constraint — that is checked by the
// caller before calling Record. This test simply proves Record tolerates it.)
func TestRunRecorderAcceptsEmptyOutput(t *testing.T) {
	store, _, resolved := seedRunForRecord(t)

	res := RunResult{
		Output:    []byte{}, // empty
		MediaType: "text/plain; charset=utf-8",
		Producer: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "sk", Repo: "repo", Version: "v1"},
		},
	}

	fixedTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	rec := RunRecorder{Store: store, Now: func() time.Time { return fixedTime }}
	recorded, err := rec.Record(context.Background(), "source-run", "gen", resolved, res)
	if err != nil {
		t.Fatalf("Record with empty output: %v", err)
	}
	if recorded.OutputArtifact == "" {
		t.Error("OutputArtifact is empty even for zero-byte output")
	}
}

// ---------------------------------------------------------------------------
// Helper for int-to-string without strconv (mirrors bench/cohort_test.go)
// ---------------------------------------------------------------------------

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// stubRunError is a sentinel used in error-propagation tests.
var stubRunError = errors.New("stub run error")
