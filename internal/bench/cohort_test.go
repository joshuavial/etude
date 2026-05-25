package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Seed harness (adapted from internal/replay/resolve_test.go)
// ---------------------------------------------------------------------------

// initRepo creates a temporary git repo with identity configured and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@example.invalid")
	return dir
}

// newStore creates a refstore.Store for the given repo directory.
func newStore(dir string) refstore.Store {
	return refstore.New(dir)
}

// makeManifest builds a minimal valid Manifest with the given Created timestamp.
func makeManifest(runID string, created time.Time, stages []runmanifest.Stage) runmanifest.Manifest {
	return runmanifest.Manifest{
		RunID:           runID,
		Workflow:        "test-workflow",
		WorkflowVersion: "v1",
		Created:         created,
		Refs:            map[string]string{"pr": "1"},
		Stages:          stages,
	}
}

// makeStage builds a minimal valid Stage with content storage on inputs and output.
// gitSHA defaults to a valid 40-char lowercase hex string.
func makeStage(name string, inputs []runmanifest.ArtifactRef, outputContent []byte) runmanifest.Stage {
	return makeStageWithSHA(name, strings.Repeat("a", 40), inputs, outputContent)
}

// makeStageWithSHA builds a stage with an explicit GitSHA (may be invalid for skip tests).
func makeStageWithSHA(name, gitSHA string, inputs []runmanifest.ArtifactRef, outputContent []byte) runmanifest.Stage {
	sum := sha256.Sum256(outputContent)
	sha := hex.EncodeToString(sum[:])
	outputPath := "artifacts/sha256/" + sha[:2] + "/" + sha
	return runmanifest.Stage{
		Name:       name,
		ProducedBy: "test-agent",
		GitSHA:     gitSHA,
		Skill:      runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		Timestamp:  time.Now().UTC(),
		Inputs:     inputs,
		Output: runmanifest.ArtifactRef{
			Role:      "output",
			Artifact:  sha,
			Path:      outputPath,
			MediaType: "application/octet-stream",
			Storage:   artifactstore.StorageContent,
			Size:      int64(len(outputContent)),
		},
	}
}

// contentArtifact returns a content ArtifactRef and the corresponding file bytes.
func contentArtifact(role string, content []byte) (runmanifest.ArtifactRef, []byte) {
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	path := "artifacts/sha256/" + sha[:2] + "/" + sha
	ref := runmanifest.ArtifactRef{
		Role:      role,
		Artifact:  sha,
		Path:      path,
		MediaType: "application/octet-stream",
		Storage:   artifactstore.StorageContent,
		Size:      int64(len(content)),
	}
	return ref, content
}

// pointerArtifact builds a pointer ArtifactRef and its pointer-record file bytes.
func pointerArtifact(t *testing.T, role string) (runmanifest.ArtifactRef, []byte) {
	t.Helper()
	as := artifactstore.New()
	size := int64(1024)
	ma, err := as.AddPointer(role, "application/octet-stream", artifactstore.Pointer{
		URI:    "https://example.com/obj",
		SHA256: strings.Repeat("b", 64),
		Size:   &size,
	})
	if err != nil {
		t.Fatalf("AddPointer: %v", err)
	}
	files := as.Files()
	return runmanifest.ArtifactFromManifestArtifact(ma), files[ma.Path]
}

// seedRun writes a single manifest into a NEW temp git repo and returns the store and commit OID.
func seedRun(t *testing.T, manifest runmanifest.Manifest, files map[string][]byte) (refstore.Store, string) {
	t.Helper()
	dir := initRepo(t)
	store := newStore(dir)
	commit, err := runmanifest.Writer{Store: store}.Write(context.Background(), manifest, files, runmanifest.WriteOptions{})
	if err != nil {
		t.Fatalf("seedRun Write: %v", err)
	}
	return store, commit
}

// seedRunInto writes a manifest into an EXISTING store (for multi-run tests).
func seedRunInto(t *testing.T, store refstore.Store, manifest runmanifest.Manifest, files map[string][]byte) string {
	t.Helper()
	commit, err := runmanifest.Writer{Store: store}.Write(context.Background(), manifest, files, runmanifest.WriteOptions{})
	if err != nil {
		t.Fatalf("seedRunInto Write: %v", err)
	}
	return commit
}

// newMultiRunStore creates a store with multiple runs already seeded.
func newMultiRunStore(t *testing.T) refstore.Store {
	t.Helper()
	dir := initRepo(t)
	return newStore(dir)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestSelectCohortQualifyingSingleRun(t *testing.T) {
	inputRef, inputBytes := contentArtifact("prompt", []byte("input content"))
	outputContent := []byte("output content")
	stage := makeStage("plan", []runmanifest.ArtifactRef{inputRef}, outputContent)

	files := map[string][]byte{
		inputRef.Path:     inputBytes,
		stage.Output.Path: outputContent,
	}
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	manifest := makeManifest("run-qualify", ts, []runmanifest.Stage{stage})
	store, commit := seedRun(t, manifest, files)

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if result.Stage != "plan" {
		t.Errorf("Stage = %q, want %q", result.Stage, "plan")
	}
	if len(result.Selected) != 1 {
		t.Fatalf("len(Selected) = %d, want 1", len(result.Selected))
	}
	if len(result.Skipped) != 0 {
		t.Errorf("len(Skipped) = %d, want 0", len(result.Skipped))
	}

	run := result.Selected[0]
	if run.RunID != "run-qualify" {
		t.Errorf("RunID = %q, want %q", run.RunID, "run-qualify")
	}
	if run.Commit != commit {
		t.Errorf("Commit = %q, want %q", run.Commit, commit)
	}
	if !run.Created.Equal(ts) {
		t.Errorf("Created = %v, want %v", run.Created, ts)
	}
	if run.Stage.Name != "plan" {
		t.Errorf("Stage.Name = %q, want %q", run.Stage.Name, "plan")
	}
	if run.Stage.GitSHA != strings.Repeat("a", 40) {
		t.Errorf("Stage.GitSHA = %q", run.Stage.GitSHA)
	}
}

func TestSelectCohortOrderingByCreatedDesc(t *testing.T) {
	store := newMultiRunStore(t)

	// Seed three runs with out-of-order timestamps.
	times := []time.Time{
		time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
	}
	runIDs := []string{"run-order-c", "run-order-a", "run-order-b"}

	for i, id := range runIDs {
		out := []byte("output-" + id)
		stage := makeStage("plan", nil, out)
		files := map[string][]byte{stage.Output.Path: out}
		manifest := makeManifest(id, times[i], []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 3 {
		t.Fatalf("len(Selected) = %d, want 3", len(result.Selected))
	}

	// Expect newest first: March, February, January.
	wantOrder := []string{"run-order-c", "run-order-b", "run-order-a"}
	for i, want := range wantOrder {
		if result.Selected[i].RunID != want {
			t.Errorf("Selected[%d].RunID = %q, want %q", i, result.Selected[i].RunID, want)
		}
	}
}

func TestSelectCohortTieBreakRunIDDesc(t *testing.T) {
	store := newMultiRunStore(t)
	ts := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	// Two runs with identical Created timestamps; tie-break must be RunID DESC.
	for _, id := range []string{"run-tie-aaa", "run-tie-zzz"} {
		out := []byte("output-" + id)
		stage := makeStage("plan", nil, out)
		files := map[string][]byte{stage.Output.Path: out}
		manifest := makeManifest(id, ts, []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 2 {
		t.Fatalf("len(Selected) = %d, want 2", len(result.Selected))
	}
	// "run-tie-zzz" > "run-tie-aaa" lexicographically.
	if result.Selected[0].RunID != "run-tie-zzz" {
		t.Errorf("Selected[0].RunID = %q, want run-tie-zzz", result.Selected[0].RunID)
	}
	if result.Selected[1].RunID != "run-tie-aaa" {
		t.Errorf("Selected[1].RunID = %q, want run-tie-aaa", result.Selected[1].RunID)
	}
}

func TestSelectCohortLastTruncation(t *testing.T) {
	store := newMultiRunStore(t)

	for i := 0; i < 5; i++ {
		ts := time.Date(2024, time.Month(i+1), 1, 0, 0, 0, 0, time.UTC)
		id := "run-trunc-" + string(rune('a'+i))
		out := []byte("output-" + id)
		stage := makeStage("plan", nil, out)
		files := map[string][]byte{stage.Output.Path: out}
		manifest := makeManifest(id, ts, []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	result, err := SelectCohort(context.Background(), store, "plan", 2)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 2 {
		t.Errorf("len(Selected) = %d, want 2", len(result.Selected))
	}
	// The two most-recent: run-trunc-e (May) and run-trunc-d (April).
	if result.Selected[0].RunID != "run-trunc-e" {
		t.Errorf("Selected[0].RunID = %q, want run-trunc-e", result.Selected[0].RunID)
	}
	if result.Selected[1].RunID != "run-trunc-d" {
		t.Errorf("Selected[1].RunID = %q, want run-trunc-d", result.Selected[1].RunID)
	}
}

func TestSelectCohortLastLargerThanAvailable(t *testing.T) {
	store := newMultiRunStore(t)

	for _, id := range []string{"run-large-a", "run-large-b"} {
		ts := time.Now().UTC()
		out := []byte("output-" + id)
		stage := makeStage("plan", nil, out)
		files := map[string][]byte{stage.Output.Path: out}
		manifest := makeManifest(id, ts, []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	result, err := SelectCohort(context.Background(), store, "plan", 100)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 2 {
		t.Errorf("len(Selected) = %d, want 2 (all qualifying)", len(result.Selected))
	}
}

func TestSelectCohortErrInvalidLast(t *testing.T) {
	dir := initRepo(t)
	store := newStore(dir)

	for _, last := range []int{0, -1, -100} {
		_, err := SelectCohort(context.Background(), store, "plan", last)
		if !errors.Is(err, ErrInvalidLast) {
			t.Errorf("last=%d: error = %v, want ErrInvalidLast", last, err)
		}
	}
}

func TestSelectCohortSkipStageMissing(t *testing.T) {
	out := []byte("output-missing")
	stage := makeStage("other-stage", nil, out)
	files := map[string][]byte{stage.Output.Path: out}
	ts := time.Now().UTC()
	manifest := makeManifest("run-missing", ts, []runmanifest.Stage{stage})
	store, _ := seedRun(t, manifest, files)

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 0 {
		t.Errorf("len(Selected) = %d, want 0", len(result.Selected))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("len(Skipped) = %d, want 1", len(result.Skipped))
	}
	if result.Skipped[0].Reason != SkipStageMissing {
		t.Errorf("Reason = %q, want %q", result.Skipped[0].Reason, SkipStageMissing)
	}
	if result.Skipped[0].RunID != "run-missing" {
		t.Errorf("RunID = %q, want run-missing", result.Skipped[0].RunID)
	}
}

func TestSelectCohortSkipStageAmbiguous(t *testing.T) {
	out1 := []byte("output-ambig-1")
	out2 := []byte("output-ambig-2")
	stage1 := makeStage("plan", nil, out1)
	stage2 := makeStage("plan", nil, out2)
	files := map[string][]byte{
		stage1.Output.Path: out1,
		stage2.Output.Path: out2,
	}
	ts := time.Now().UTC()
	manifest := makeManifest("run-ambig", ts, []runmanifest.Stage{stage1, stage2})
	store, _ := seedRun(t, manifest, files)

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 0 {
		t.Errorf("len(Selected) = %d, want 0", len(result.Selected))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("len(Skipped) = %d, want 1", len(result.Skipped))
	}
	if result.Skipped[0].Reason != SkipStageAmbiguous {
		t.Errorf("Reason = %q, want %q", result.Skipped[0].Reason, SkipStageAmbiguous)
	}
}

func TestSelectCohortSkipNoGitSHAParseError(t *testing.T) {
	// runmanifest.Validate rejects empty git_sha, so ParseJSON will fail on any
	// stored manifest whose git_sha is "". SelectCohort must return an error
	// naming the run (same as TestSelectCohortParseError), not a skip.
	//
	// SkipNoGitSHA in classify() is defensive code for future callers that invoke
	// classify() directly (e.g. if Validate is relaxed); it is not reachable via
	// the SelectCohort path as long as runmanifest.Validate enforces non-empty.
	ctx := context.Background()
	dir := initRepo(t)
	store := newStore(dir)

	out := []byte("output-nosha")
	sum := sha256.Sum256(out)
	shaHex := hex.EncodeToString(sum[:])
	outputPath := "artifacts/sha256/" + shaHex[:2] + "/" + shaHex

	// Write a manifest with an empty git_sha directly via WriteCommit (bypassing
	// runmanifest.Writer which would reject it via Validate).
	manifestJSON := `{
  "manifest_version": 2,
  "run_id": "run-nosha",
  "workflow": "test-workflow",
  "workflow_version": "v1",
  "created": "2024-01-01T00:00:00Z",
  "refs": {"pr": "1"},
  "stages": [{
    "stage": "plan",
    "produced_by": "test-agent",
    "git_sha": "",
    "producer": {"skill": {"id": "test-skill", "repo": "test-repo", "version": "v1"}},
    "inputs": [],
    "output": {
      "role": "output",
      "artifact": "` + shaHex + `",
      "path": "` + outputPath + `",
      "media_type": "application/octet-stream",
      "storage": "content",
      "size": ` + itoa(len(out)) + `
    },
    "timestamp": "2024-01-01T00:00:01Z"
  }]
}`

	_, err := store.WriteCommit(ctx, "refs/etude/runs/run-nosha", map[string][]byte{
		"manifest.json": []byte(manifestJSON),
		outputPath:      out,
	}, refstore.WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit: %v", err)
	}

	// ParseJSON calls Validate which requires non-empty git_sha → parse error.
	_, err = SelectCohort(ctx, store, "plan", 10)
	if err == nil {
		t.Fatal("SelectCohort: expected parse error for empty git_sha manifest, got nil")
	}
	if !strings.Contains(err.Error(), "run-nosha") {
		t.Errorf("error %q does not name the run", err.Error())
	}
}

func TestSelectCohortSkipInvalidGitSHA(t *testing.T) {
	// Test three malformed SHA variants per the plan's subcases.
	cases := []struct {
		name   string
		gitSHA string
	}{
		{"non-hex", "xyz" + strings.Repeat("a", 37)},                    // contains non-hex chars
		{"39-char-hex", strings.Repeat("a", 39)},                        // wrong length (39 not 40/64)
		{"uppercase-40-char", strings.ToUpper(strings.Repeat("a", 40))}, // uppercase rejected
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := initRepo(t)
			store := newStore(dir)

			out := []byte("output-" + tc.name)
			sum := sha256.Sum256(out)
			shaHex := hex.EncodeToString(sum[:])
			outputPath := "artifacts/sha256/" + shaHex[:2] + "/" + shaHex

			manifestJSON := `{
  "manifest_version": 2,
  "run_id": "run-badsha",
  "workflow": "test-workflow",
  "workflow_version": "v1",
  "created": "2024-01-01T00:00:00Z",
  "refs": {"pr": "1"},
  "stages": [{
    "stage": "plan",
    "produced_by": "test-agent",
    "git_sha": "` + tc.gitSHA + `",
    "producer": {"skill": {"id": "test-skill", "repo": "test-repo", "version": "v1"}},
    "inputs": [],
    "output": {
      "role": "output",
      "artifact": "` + shaHex + `",
      "path": "` + outputPath + `",
      "media_type": "application/octet-stream",
      "storage": "content",
      "size": ` + itoa(len(out)) + `
    },
    "timestamp": "2024-01-01T00:00:01Z"
  }]
}`

			_, err := store.WriteCommit(ctx, "refs/etude/runs/run-badsha", map[string][]byte{
				"manifest.json": []byte(manifestJSON),
				outputPath:      out,
			}, refstore.WriteOptions{})
			if err != nil {
				t.Fatalf("WriteCommit: %v", err)
			}

			result, err := SelectCohort(ctx, store, "plan", 10)
			if err != nil {
				t.Fatalf("SelectCohort: %v", err)
			}
			if len(result.Selected) != 0 {
				t.Errorf("len(Selected) = %d, want 0", len(result.Selected))
			}
			if len(result.Skipped) != 1 {
				t.Fatalf("len(Skipped) = %d, want 1", len(result.Skipped))
			}
			if result.Skipped[0].Reason != SkipInvalidGitSHA {
				t.Errorf("Reason = %q, want %q", result.Skipped[0].Reason, SkipInvalidGitSHA)
			}
		})
	}
}

func TestSelectCohortSkipPointerInput(t *testing.T) {
	ptrRef, ptrFileBytes := pointerArtifact(t, "raw-data")
	out := []byte("output-ptr-input")
	stage := makeStage("plan", []runmanifest.ArtifactRef{ptrRef}, out)
	files := map[string][]byte{
		ptrRef.Path:       ptrFileBytes,
		stage.Output.Path: out,
	}
	ts := time.Now().UTC()
	manifest := makeManifest("run-ptrin", ts, []runmanifest.Stage{stage})
	store, _ := seedRun(t, manifest, files)

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 0 {
		t.Errorf("len(Selected) = %d, want 0", len(result.Selected))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("len(Skipped) = %d, want 1", len(result.Skipped))
	}
	skip := result.Skipped[0]
	if skip.Reason != SkipPointerInput {
		t.Errorf("Reason = %q, want %q", skip.Reason, SkipPointerInput)
	}
	if !strings.Contains(skip.Detail, "raw-data") {
		t.Errorf("Detail = %q, want it to name the offending role", skip.Detail)
	}
}

func TestSelectCohortSkipPointerOutput(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	store := newStore(dir)

	// Build a pointer output artifact.
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
		Inputs:     nil,
		Output:     ptrRef,
	}

	ts := time.Now().UTC()
	manifest := makeManifest("run-ptrout", ts, []runmanifest.Stage{stage})
	files := map[string][]byte{ptrRef.Path: ptrFile}

	_, err = runmanifest.Writer{Store: store}.Write(ctx, manifest, files, runmanifest.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	result, err := SelectCohort(ctx, store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 0 {
		t.Errorf("len(Selected) = %d, want 0", len(result.Selected))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("len(Skipped) = %d, want 1", len(result.Skipped))
	}
	if result.Skipped[0].Reason != SkipPointerOutput {
		t.Errorf("Reason = %q, want %q", result.Skipped[0].Reason, SkipPointerOutput)
	}
}

func TestSelectCohortSkipReplayRun(t *testing.T) {
	// A replay-produced stage (produced_by:"replay") must be excluded so bench
	// does not re-bench its own recorded replays (recursive cohort growth).
	outputContent := []byte("replayed plan output")
	stage := makeStage("plan", nil, outputContent)
	stage.ProducedBy = "replay"
	stage.ReplayOf = &runmanifest.ReplayLink{
		RunID:  "source-run",
		Stage:  "plan",
		Commit: strings.Repeat("b", 40),
	}

	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	manifest := makeManifest("run-replay", ts, []runmanifest.Stage{stage})
	files := map[string][]byte{stage.Output.Path: outputContent}
	store, _ := seedRun(t, manifest, files)

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 0 {
		t.Errorf("len(Selected) = %d, want 0 (replay run excluded)", len(result.Selected))
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != SkipReplayRun {
		t.Fatalf("Skipped = %+v, want one SkipReplayRun", result.Skipped)
	}
}

func TestSelectCohortZeroRuns(t *testing.T) {
	dir := initRepo(t)
	store := newStore(dir)

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 0 {
		t.Errorf("len(Selected) = %d, want 0", len(result.Selected))
	}
	if len(result.Skipped) != 0 {
		t.Errorf("len(Skipped) = %d, want 0", len(result.Skipped))
	}
	if result.Stage != "plan" {
		t.Errorf("Stage = %q, want plan", result.Stage)
	}
}

func TestSelectCohortZeroQualifying(t *testing.T) {
	// All runs exist but none match the requested stage name.
	store := newMultiRunStore(t)

	for _, id := range []string{"run-zq-a", "run-zq-b"} {
		out := []byte("output-" + id)
		stage := makeStage("other", nil, out)
		files := map[string][]byte{stage.Output.Path: out}
		manifest := makeManifest(id, time.Now().UTC(), []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 0 {
		t.Errorf("len(Selected) = %d, want 0", len(result.Selected))
	}
	if len(result.Skipped) != 2 {
		t.Errorf("len(Skipped) = %d, want 2", len(result.Skipped))
	}
	for _, skip := range result.Skipped {
		if skip.Reason != SkipStageMissing {
			t.Errorf("RunID=%q: Reason = %q, want %q", skip.RunID, skip.Reason, SkipStageMissing)
		}
	}
}

func TestSelectCohortParseError(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	store := newStore(dir)

	// Write a corrupt manifest.json directly via WriteCommit.
	_, err := store.WriteCommit(ctx, "refs/etude/runs/run-corrupt", map[string][]byte{
		"manifest.json": []byte("this is not valid json }{"),
	}, refstore.WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit: %v", err)
	}

	_, err = SelectCohort(ctx, store, "plan", 10)
	if err == nil {
		t.Fatal("SelectCohort: expected error for corrupt manifest, got nil")
	}
	if !strings.Contains(err.Error(), "run-corrupt") {
		t.Errorf("error %q does not name the run", err.Error())
	}
}

func TestSelectCohortMixedCohort(t *testing.T) {
	// Two qualifying runs + two skipped (stage-missing) — only qualifying selected.
	store := newMultiRunStore(t)

	qualifying := []string{"run-mix-good-a", "run-mix-good-b"}
	for _, id := range qualifying {
		ts := time.Now().UTC()
		out := []byte("output-" + id)
		stage := makeStage("plan", nil, out)
		files := map[string][]byte{stage.Output.Path: out}
		manifest := makeManifest(id, ts, []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	skipped := []string{"run-mix-bad-a", "run-mix-bad-b"}
	for _, id := range skipped {
		ts := time.Now().UTC()
		out := []byte("output-" + id)
		stage := makeStage("other", nil, out)
		files := map[string][]byte{stage.Output.Path: out}
		manifest := makeManifest(id, ts, []runmanifest.Stage{stage})
		seedRunInto(t, store, manifest, files)
	}

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 2 {
		t.Errorf("len(Selected) = %d, want 2", len(result.Selected))
	}
	if len(result.Skipped) != 2 {
		t.Errorf("len(Skipped) = %d, want 2", len(result.Skipped))
	}
	for _, sel := range result.Selected {
		for _, skipID := range skipped {
			if sel.RunID == skipID {
				t.Errorf("Selected contains skipped run %q", skipID)
			}
		}
	}
}

func TestSelectCohortValidSHA64(t *testing.T) {
	// A 64-char SHA-256 OID should also qualify (SHA-256 repos).
	out := []byte("output-sha256")
	stage := makeStageWithSHA("plan", strings.Repeat("a", 64), nil, out)
	files := map[string][]byte{stage.Output.Path: out}
	ts := time.Now().UTC()
	manifest := makeManifest("run-sha256", ts, []runmanifest.Stage{stage})
	store, _ := seedRun(t, manifest, files)

	result, err := SelectCohort(context.Background(), store, "plan", 10)
	if err != nil {
		t.Fatalf("SelectCohort: %v", err)
	}
	if len(result.Selected) != 1 {
		t.Errorf("len(Selected) = %d, want 1 (64-char SHA-256 OID should qualify)", len(result.Selected))
	}
}

// ---------------------------------------------------------------------------
// isValidGitSHA unit tests
// ---------------------------------------------------------------------------

func TestIsValidGitSHA(t *testing.T) {
	cases := []struct {
		sha   string
		valid bool
	}{
		{strings.Repeat("a", 40), true},
		{strings.Repeat("f", 40), true},
		{strings.Repeat("0", 40), true},
		{strings.Repeat("a", 64), true},
		{"", false},
		{strings.Repeat("a", 39), false},
		{strings.Repeat("a", 41), false},
		{strings.Repeat("a", 63), false},
		{strings.Repeat("a", 65), false},
		{strings.ToUpper(strings.Repeat("a", 40)), false},
		{"xyz" + strings.Repeat("a", 37), false},
		{strings.Repeat("g", 40), false},       // 'g' is not hex
		{"-" + strings.Repeat("a", 39), false}, // leading dash
	}

	for _, tc := range cases {
		got := isValidGitSHA(tc.sha)
		if got != tc.valid {
			t.Errorf("isValidGitSHA(%q) = %v, want %v", tc.sha, got, tc.valid)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// itoa converts an int to a decimal string without importing strconv/fmt.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
