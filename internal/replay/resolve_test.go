package replay

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
// Helpers
// ---------------------------------------------------------------------------

// initRepo creates a temporary git repo with identity configured and returns
// its path.
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

// seedRun writes a run manifest into a temp git repo and returns the store,
// runID, and the resulting commit OID.
func seedRun(t *testing.T, manifest runmanifest.Manifest, files map[string][]byte) (refstore.Store, string, string) {
	t.Helper()
	dir := initRepo(t)
	store := refstore.New(dir)
	ctx := context.Background()
	commit, err := runmanifest.Writer{Store: store}.Write(ctx, manifest, files, runmanifest.WriteOptions{})
	if err != nil {
		t.Fatalf("seedRun Write: %v", err)
	}
	return store, manifest.RunID, commit
}

// makeManifest builds a minimal valid Manifest.
func makeManifest(runID string, refs map[string]string, stages []runmanifest.Stage) runmanifest.Manifest {
	return runmanifest.Manifest{
		RunID:           runID,
		Workflow:        "test-workflow",
		WorkflowVersion: "v1",
		Created:         time.Now().UTC(),
		Refs:            refs,
		Stages:          stages,
	}
}

// makeStage builds a minimal valid Stage. The output artifact is built from
// the provided output content bytes.
func makeStage(name string, inputs []runmanifest.ArtifactRef, outputContent []byte) runmanifest.Stage {
	sum := sha256.Sum256(outputContent)
	sha := hex.EncodeToString(sum[:])
	outputPath := "artifacts/sha256/" + sha[:2] + "/" + sha
	return runmanifest.Stage{
		Name:       name,
		ProducedBy: "test-agent",
		GitSHA:     strings.Repeat("a", 40),
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

// contentArtifact builds a content ArtifactRef and its file bytes.
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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestResolveInputsSingleInput(t *testing.T) {
	inputContent := []byte("hello world")
	inputRef, inputBytes := contentArtifact("prompt", inputContent)
	outputContent := []byte("output-single")
	stage := makeStage("plan", []runmanifest.ArtifactRef{inputRef}, outputContent)

	files := map[string][]byte{
		inputRef.Path:     inputBytes,
		stage.Output.Path: outputContent,
	}
	manifest := makeManifest("run-single", map[string]string{"pr": "42"}, []runmanifest.Stage{stage})
	store, runID, commit := seedRun(t, manifest, files)

	result, err := ResolveInputs(context.Background(), store, runID, "plan")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}

	if result.Name != "plan" {
		t.Errorf("Name = %q, want %q", result.Name, "plan")
	}
	if result.Commit != commit {
		t.Errorf("Commit = %q, want %q", result.Commit, commit)
	}
	if result.Refs["pr"] != "42" {
		t.Errorf("Refs[pr] = %q, want %q", result.Refs["pr"], "42")
	}
	if len(result.ResolvedInputs) != 1 {
		t.Fatalf("len(ResolvedInputs) = %d, want 1", len(result.ResolvedInputs))
	}
	if result.ResolvedInputs[0].Role != "prompt" {
		t.Errorf("Role = %q, want %q", result.ResolvedInputs[0].Role, "prompt")
	}

	got, err := result.ResolvedInputs[0].ReadContent(context.Background())
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	if string(got) != string(inputContent) {
		t.Errorf("ReadContent = %q, want %q", got, inputContent)
	}
}

func TestResolveInputsMultiInput(t *testing.T) {
	content1 := []byte("input-one")
	content2 := []byte("input-two")
	ref1, bytes1 := contentArtifact("task", content1)
	ref2, bytes2 := contentArtifact("context", content2)
	outputContent := []byte("output-multi")
	stage := makeStage("implement", []runmanifest.ArtifactRef{ref1, ref2}, outputContent)

	files := map[string][]byte{
		ref1.Path:         bytes1,
		ref2.Path:         bytes2,
		stage.Output.Path: outputContent,
	}
	manifest := makeManifest("run-multi", nil, []runmanifest.Stage{stage})
	store, runID, _ := seedRun(t, manifest, files)

	result, err := ResolveInputs(context.Background(), store, runID, "implement")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}

	if len(result.ResolvedInputs) != 2 {
		t.Fatalf("len(ResolvedInputs) = %d, want 2", len(result.ResolvedInputs))
	}

	got1, err := result.ResolvedInputs[0].ReadContent(context.Background())
	if err != nil {
		t.Fatalf("ReadContent[0]: %v", err)
	}
	if string(got1) != string(content1) {
		t.Errorf("ReadContent[0] = %q, want %q", got1, content1)
	}

	got2, err := result.ResolvedInputs[1].ReadContent(context.Background())
	if err != nil {
		t.Fatalf("ReadContent[1]: %v", err)
	}
	if string(got2) != string(content2) {
		t.Errorf("ReadContent[1] = %q, want %q", got2, content2)
	}
}

func TestResolveInputsContentHashRoundTrip(t *testing.T) {
	inputContent := []byte("canonical content for hash check")
	inputRef, inputBytes := contentArtifact("data", inputContent)
	outputContent := []byte("output-hash")
	stage := makeStage("verify", []runmanifest.ArtifactRef{inputRef}, outputContent)

	files := map[string][]byte{
		inputRef.Path:     inputBytes,
		stage.Output.Path: outputContent,
	}
	manifest := makeManifest("run-hash", nil, []runmanifest.Stage{stage})
	store, runID, _ := seedRun(t, manifest, files)

	result, err := ResolveInputs(context.Background(), store, runID, "verify")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	if len(result.ResolvedInputs) != 1 {
		t.Fatalf("len(ResolvedInputs) = %d, want 1", len(result.ResolvedInputs))
	}

	got, err := result.ResolvedInputs[0].ReadContent(context.Background())
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}

	sum := sha256.Sum256(got)
	gotHash := hex.EncodeToString(sum[:])
	wantHash := result.ResolvedInputs[0].ArtifactRef.Artifact
	if gotHash != wantHash {
		t.Errorf("sha256(ReadContent) = %q, ArtifactRef.Artifact = %q: hash mismatch", gotHash, wantHash)
	}
}

func TestResolveInputsZeroInputs(t *testing.T) {
	outputContent := []byte("output-zero")
	stage := makeStage("review", nil, outputContent)

	files := map[string][]byte{stage.Output.Path: outputContent}
	manifest := makeManifest("run-zero", nil, []runmanifest.Stage{stage})
	store, runID, _ := seedRun(t, manifest, files)

	result, err := ResolveInputs(context.Background(), store, runID, "review")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	if result.ResolvedInputs == nil {
		t.Fatal("ResolvedInputs is nil, want non-nil empty slice")
	}
	if len(result.ResolvedInputs) != 0 {
		t.Errorf("len(ResolvedInputs) = %d, want 0", len(result.ResolvedInputs))
	}
}

func TestResolveInputsErrStageNotFound(t *testing.T) {
	outputContent := []byte("output-notfound")
	stage := makeStage("plan", nil, outputContent)

	files := map[string][]byte{stage.Output.Path: outputContent}
	manifest := makeManifest("run-notfound", nil, []runmanifest.Stage{stage})
	store, runID, _ := seedRun(t, manifest, files)

	_, err := ResolveInputs(context.Background(), store, runID, "nonexistent")
	if !errors.Is(err, ErrStageNotFound) {
		t.Fatalf("error = %v, want ErrStageNotFound", err)
	}
	if !strings.Contains(err.Error(), "plan") {
		t.Errorf("error %q does not list available stage name 'plan'", err.Error())
	}
}

func TestResolveInputsErrAmbiguousStage(t *testing.T) {
	outputContent1 := []byte("output-ambig-1")
	outputContent2 := []byte("output-ambig-2")
	// Two stages with the same name — deliberate, matches capture's append behavior.
	stage1 := makeStage("plan", nil, outputContent1)
	stage2 := makeStage("plan", nil, outputContent2)

	files := map[string][]byte{
		stage1.Output.Path: outputContent1,
		stage2.Output.Path: outputContent2,
	}
	manifest := makeManifest("run-ambig", nil, []runmanifest.Stage{stage1, stage2})
	store, runID, _ := seedRun(t, manifest, files)

	_, err := ResolveInputs(context.Background(), store, runID, "plan")
	if !errors.Is(err, ErrAmbiguousStage) {
		t.Fatalf("error = %v, want ErrAmbiguousStage", err)
	}
}

func TestResolveInputsErrRunNotFound(t *testing.T) {
	dir := initRepo(t)
	store := refstore.New(dir)

	_, err := ResolveInputs(context.Background(), store, "no-such-run", "plan")
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("error = %v, want ErrRunNotFound", err)
	}
}

func TestResolveInputsErrInvalidRunID(t *testing.T) {
	// Use a plain non-repo dir so that any git call would return a git error.
	// ErrInvalidRunID must be returned before any git call.
	nonRepo := t.TempDir()
	store := refstore.New(nonRepo)

	cases := []struct {
		name string
		id   string
	}{
		{"slash in id", "bad/id"},
		{"double dot", ".."},
		{"lock suffix", "x.lock"},
		{"leading dot", ".hidden"},
		{"trailing dot", "myrun."},
		{"all dots", "..."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveInputs(context.Background(), store, tc.id, "plan")
			if !errors.Is(err, ErrInvalidRunID) {
				t.Fatalf("id=%q: error = %v, want ErrInvalidRunID", tc.id, err)
			}
			// Must not leak git "not a git repository" — that would mean we hit git before validation.
			if strings.Contains(err.Error(), "not a git repository") {
				t.Fatalf("id=%q: git error leaked before validation: %v", tc.id, err)
			}
		})
	}
}

func TestResolveInputsErrPointerNotMaterialized(t *testing.T) {
	ptrRef, ptrFileBytes := pointerArtifact(t, "raw-data")
	outputContent := []byte("output-ptr")
	stage := makeStage("fetch", []runmanifest.ArtifactRef{ptrRef}, outputContent)

	files := map[string][]byte{
		ptrRef.Path:       ptrFileBytes,
		stage.Output.Path: outputContent,
	}
	manifest := makeManifest("run-ptr", nil, []runmanifest.Stage{stage})
	store, runID, _ := seedRun(t, manifest, files)

	result, err := ResolveInputs(context.Background(), store, runID, "fetch")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	if len(result.ResolvedInputs) != 1 {
		t.Fatalf("len(ResolvedInputs) = %d, want 1", len(result.ResolvedInputs))
	}

	_, err = result.ResolvedInputs[0].ReadContent(context.Background())
	if !errors.Is(err, ErrPointerNotMaterialized) {
		t.Fatalf("ReadContent error = %v, want ErrPointerNotMaterialized", err)
	}
}

func TestResolveInputsReadContentPropagatesError(t *testing.T) {
	ctx := context.Background()

	// Build an ArtifactRef whose path will NOT be present in the git tree.
	// We seed the run via store.WriteCommit directly (bypassing runmanifest.Writer
	// which would reject a missing artifact file) so we can deliberately omit the
	// input blob from the commit tree.
	inputContent := []byte("propagate-error-input")
	inputRef, _ := contentArtifact("data", inputContent)
	outputContent := []byte("output-propagate")
	stage := makeStage("process", []runmanifest.ArtifactRef{inputRef}, outputContent)

	// Build the manifest and marshal it to JSON manually so we can write it with
	// store.WriteCommit without the artifact-presence validation that Writer.Write
	// enforces.
	manifest := makeManifest("run-propagate", nil, []runmanifest.Stage{stage})
	manifestBytes, err := manifest.JSON()
	if err != nil {
		t.Fatalf("manifest.JSON: %v", err)
	}

	dir := initRepo(t)
	store := refstore.New(dir)

	// Write a commit that has the manifest and the output blob, but NOT the input
	// blob at inputRef.Path. ResolveInputs will succeed (it only reads manifest.json),
	// but the production ReadContent closure will fail when it tries to read the
	// absent input path.
	_, err = store.WriteCommit(ctx, "refs/etude/runs/"+manifest.RunID, map[string][]byte{
		"manifest.json":   manifestBytes,
		stage.Output.Path: outputContent,
		// inputRef.Path deliberately omitted
	}, refstore.WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit: %v", err)
	}

	result, err := ResolveInputs(ctx, store, manifest.RunID, "process")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	if len(result.ResolvedInputs) != 1 {
		t.Fatalf("len(ResolvedInputs) = %d, want 1", len(result.ResolvedInputs))
	}

	// Call the PRODUCTION ReadContent closure — not a hand-built copy.
	_, err = result.ResolvedInputs[0].ReadContent(ctx)
	if err == nil {
		t.Fatal("ReadContent returned nil error, want an error for nonexistent path")
	}
	if !errors.Is(err, refstore.ErrNotFound) {
		t.Errorf("ReadContent error = %v, want to wrap refstore.ErrNotFound", err)
	}
}

func TestResolveInputsTOCTOU(t *testing.T) {
	ctx := context.Background()

	inputContent := []byte("toctou-input-content")
	inputRef, inputBytes := contentArtifact("task", inputContent)
	outputContent := []byte("output-toctou")
	stage := makeStage("plan", []runmanifest.ArtifactRef{inputRef}, outputContent)

	files := map[string][]byte{
		inputRef.Path:     inputBytes,
		stage.Output.Path: outputContent,
	}
	manifest := makeManifest("run-toctou", nil, []runmanifest.Stage{stage})
	store, runID, originalCommit := seedRun(t, manifest, files)

	// Resolve before advancing the ref. result.Commit is pinned to originalCommit.
	result, err := ResolveInputs(ctx, store, runID, "plan")
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	if result.Commit != originalCommit {
		t.Fatalf("result.Commit = %q, want originalCommit %q", result.Commit, originalCommit)
	}

	// Advance the run ref via store.WriteCommit directly (bypassing
	// runmanifest.Writer validation) so we can write DIFFERENT bytes at
	// inputRef.Path in the new commit tree. This is the discriminating step: a
	// buggy implementation that reads from the ref (not the resolved commit) would
	// return these overwritten bytes instead of the original.
	overwrittenInputBytes := []byte("toctou-input-OVERWRITTEN-by-concurrent-capture")
	origManifestBytes, err := store.ReadCommitFile(ctx, originalCommit, "manifest.json")
	if err != nil {
		t.Fatalf("ReadCommitFile manifest: %v", err)
	}
	newCommit, err := store.WriteCommit(ctx, "refs/etude/runs/"+runID, map[string][]byte{
		"manifest.json":   origManifestBytes,
		inputRef.Path:     overwrittenInputBytes, // DIFFERENT bytes at the same path
		stage.Output.Path: outputContent,
	}, refstore.WriteOptions{ExpectedOld: originalCommit})
	if err != nil {
		t.Fatalf("advancing run ref: %v", err)
	}

	// The resolved commit must differ from the advanced ref HEAD.
	if result.Commit == newCommit {
		t.Fatalf("result.Commit == newCommit (%q): snapshot not pinned to original commit", newCommit)
	}

	// ReadContent must still return data from the ORIGINAL resolved commit, NOT
	// the overwritten bytes at the advanced ref. A ref-reading impl would return
	// overwrittenInputBytes and fail this assertion.
	if len(result.ResolvedInputs) != 1 {
		t.Fatalf("len(ResolvedInputs) = %d, want 1", len(result.ResolvedInputs))
	}
	got, err := result.ResolvedInputs[0].ReadContent(ctx)
	if err != nil {
		t.Fatalf("ReadContent after ref advance: %v", err)
	}
	if string(got) != string(inputContent) {
		t.Errorf("ReadContent = %q, want original %q (not overwritten %q)",
			got, inputContent, overwrittenInputBytes)
	}
}
