package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/gc"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func gcContentRef(role string, content []byte) (runmanifest.ArtifactRef, []byte) {
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	path := "artifacts/sha256/" + sha[:2] + "/" + sha
	return runmanifest.ArtifactRef{
		Role:      role,
		Artifact:  sha,
		Path:      path,
		MediaType: "application/octet-stream",
		Storage:   artifactstore.StorageContent,
		Size:      int64(len(content)),
	}, content
}

func gcPointerRef(role, uri string) (runmanifest.ArtifactRef, []byte) {
	payload, _ := json.Marshal(map[string]any{"version": 1, "uri": uri})
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	path := "artifacts/pointers/sha256/" + sha[:2] + "/" + sha + ".json"
	return runmanifest.ArtifactRef{
		Role:      role,
		Artifact:  sha,
		Path:      path,
		MediaType: "application/octet-stream",
		Storage:   artifactstore.StoragePointer,
		Size:      0,
	}, payload
}

func gcMakeStage(name string, output runmanifest.ArtifactRef, inputs []runmanifest.ArtifactRef) runmanifest.Stage {
	return runmanifest.Stage{
		Name:       name,
		ProducedBy: "test-agent",
		GitSHA:     strings.Repeat("a", 40),
		Skill:      runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		Timestamp:  time.Now().UTC(),
		Inputs:     inputs,
		Output:     output,
	}
}

func gcMakeManifest(runID string, stages []runmanifest.Stage) runmanifest.Manifest {
	return runmanifest.Manifest{
		RunID:           runID,
		Workflow:        "test-workflow",
		WorkflowVersion: "v1",
		Created:         time.Now().UTC(),
		Refs:            map[string]string{"pr": "1"},
		Stages:          stages,
	}
}

func gcWriteRun(t *testing.T, store refstore.Store, manifest runmanifest.Manifest, files map[string][]byte) {
	t.Helper()
	w := runmanifest.Writer{Store: store}
	if _, err := w.Write(context.Background(), manifest, files, runmanifest.WriteOptions{}); err != nil {
		t.Fatalf("gcWriteRun %s: %v", manifest.RunID, err)
	}
}

func gcWriteEval(t *testing.T, store refstore.Store, result eval.EvalResult) {
	t.Helper()
	w := eval.Writer{Store: store}
	if _, err := w.Write(context.Background(), result, eval.WriteOptions{}); err != nil {
		t.Fatalf("gcWriteEval %s: %v", result.EvalID, err)
	}
}

func gcRubricEval(evalID, targetRunID, targetStage string, targetArtifact runmanifest.ArtifactRef) eval.EvalResult {
	val := 8.0
	max := 10.0
	return eval.EvalResult{
		EvalResultVersion: 1,
		EvalID:            evalID,
		Method:            "rubric",
		Score:             eval.Score{Kind: "rubric", Value: &val, Max: &max},
		Rubric:            &eval.RubricRef{Path: "rubric.md", Version: "v1"},
		Targets: []eval.ArtifactSource{{
			RunID:    targetRunID,
			Stage:    targetStage,
			Commit:   strings.Repeat("b", 40),
			Artifact: targetArtifact.Artifact,
		}},
		Context: []eval.ArtifactSource{},
		Created: time.Now().UTC(),
	}
}

// executeGC runs the gc command using an injectable gcRunner backed by a real store.
func executeGC(store refstore.Store, args ...string) (string, string, error) {
	var out, errOut bytes.Buffer
	runner := &gcRunner{store: store, stdout: &out, stderr: &errOut}
	cmd := buildGCCommand(&out, &errOut, runner)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// ---------------------------------------------------------------------------
// Registration and --help
// ---------------------------------------------------------------------------

func TestGCCommandIsRegistered(t *testing.T) {
	stdout, stderr, err := execute("gc", "--help")
	if err != nil {
		t.Fatalf("gc --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "gc") {
		t.Fatalf("gc --help output does not mention 'gc':\n%s", stdout)
	}
}

func TestGCHelpMentionsPruneAndMaxSize(t *testing.T) {
	stdout, stderr, err := execute("gc", "--help")
	if err != nil {
		t.Fatalf("gc --help: %v\nstderr: %s", err, stderr)
	}
	for _, flag := range []string{"--prune", "--max-size"} {
		if !strings.Contains(stdout, flag) {
			t.Fatalf("gc --help does not mention %q:\n%s", flag, stdout)
		}
	}
}

// ---------------------------------------------------------------------------
// Report mode (no --prune)
// ---------------------------------------------------------------------------

func TestGCReportDefaultNoDelete(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	outRef, outBytes := gcContentRef("output", []byte("some content here"))
	gcWriteRun(t, store, gcMakeManifest("run-leaf", []runmanifest.Stage{gcMakeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	out, stderr, err := executeGC(store)
	if err != nil {
		t.Fatalf("gc report returned error: %v\nstderr: %s", err, stderr)
	}
	// Ref must still exist — report mode never deletes
	if _, resolveErr := store.Resolve(context.Background(), "refs/etude/runs/run-leaf"); resolveErr != nil {
		t.Fatalf("run-leaf was deleted by report mode: %v", resolveErr)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Fatalf("expected TOTAL in report output:\n%s", out)
	}
	if !strings.Contains(out, "pre-dedup") {
		t.Fatalf("expected 'pre-dedup' label in report output:\n%s", out)
	}
}

func TestGCReportTotalBytesDisplayed(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	content := []byte("exactly 17 bytes!") // 17 bytes
	outRef, outBytes := gcContentRef("output", content)
	gcWriteRun(t, store, gcMakeManifest("run-1", []runmanifest.Stage{gcMakeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	out, _, err := executeGC(store)
	if err != nil {
		t.Fatalf("gc report: %v", err)
	}
	if !strings.Contains(out, "17") {
		t.Fatalf("expected 17 in report output:\n%s", out)
	}
}

func TestGCReportOversizedSectionPresentWithMaxSize(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	bigContent := []byte("this is bigger than ten bytes")
	outRef, outBytes := gcContentRef("output", bigContent)
	gcWriteRun(t, store, gcMakeManifest("big-run", []runmanifest.Stage{gcMakeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	out, _, err := executeGC(store, "--max-size", "10")
	if err != nil {
		t.Fatalf("gc --max-size 10: %v", err)
	}
	if !strings.Contains(out, "OVERSIZED") {
		t.Fatalf("expected OVERSIZED section with --max-size:\n%s", out)
	}
	if !strings.Contains(out, "big-run") {
		t.Fatalf("expected big-run in OVERSIZED section:\n%s", out)
	}
}

func TestGCReportOversizedSectionAbsentWithoutMaxSize(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	bigContent := []byte("this is much bigger than ten bytes here")
	outRef, outBytes := gcContentRef("output", bigContent)
	gcWriteRun(t, store, gcMakeManifest("big-run", []runmanifest.Stage{gcMakeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	out, _, err := executeGC(store) // no --max-size
	if err != nil {
		t.Fatalf("gc report: %v", err)
	}
	if strings.Contains(out, "OVERSIZED") {
		t.Fatalf("OVERSIZED section should be absent without --max-size:\n%s", out)
	}
}

func TestGCReportExternalListsPointers(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	pRef, pBytes := gcPointerRef("output", "s3://bucket/object.bin")
	gcWriteRun(t, store, gcMakeManifest("ptr-run", []runmanifest.Stage{gcMakeStage("gen", pRef, nil)}), map[string][]byte{pRef.Path: pBytes})

	out, _, err := executeGC(store)
	if err != nil {
		t.Fatalf("gc report: %v", err)
	}
	if !strings.Contains(out, "EXTERNAL") {
		t.Fatalf("expected EXTERNAL section:\n%s", out)
	}
	if !strings.Contains(out, "s3://bucket/object.bin") {
		t.Fatalf("expected pointer URI in EXTERNAL section:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Prune mode (--prune)
// ---------------------------------------------------------------------------

func TestGCPruneNoArgsError(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	_, _, err := executeGC(store, "--prune")
	if err == nil {
		t.Fatal("gc --prune with no args should return error")
	}
	if !strings.Contains(err.Error(), "--prune requires one or more run ids") {
		t.Fatalf("error = %q, want '--prune requires one or more run ids'", err.Error())
	}
}

func TestGCPruneLeafRunDeleted(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	outRef, outBytes := gcContentRef("output", []byte("run-1 content"))
	gcWriteRun(t, store, gcMakeManifest("run-1", []runmanifest.Stage{gcMakeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	out, _, err := executeGC(store, "--prune", "run-1")
	if err != nil {
		t.Fatalf("gc --prune run-1: %v", err)
	}
	if !strings.Contains(out, "pruned run-1") {
		t.Fatalf("expected 'pruned run-1' in output:\n%s", out)
	}
	if _, resolveErr := store.Resolve(context.Background(), "refs/etude/runs/run-1"); resolveErr == nil {
		t.Fatal("run-1 should be deleted after prune")
	}
}

func TestGCPrunePinnedRunRefusedNonZeroExit(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	outRef, outBytes := gcContentRef("output", []byte("pinned content"))
	gcWriteRun(t, store, gcMakeManifest("pinned-run", []runmanifest.Stage{gcMakeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})
	gcWriteEval(t, store, gcRubricEval("eval-1", "pinned-run", "gen", outRef))

	_, errStr, err := executeGC(store, "--prune", "pinned-run")
	if err == nil {
		t.Fatal("gc --prune of pinned run should exit non-zero")
	}
	if !strings.Contains(errStr, "refused") {
		t.Fatalf("expected 'refused' in stderr:\n%s", errStr)
	}
	// ref must survive
	if _, resolveErr := store.Resolve(context.Background(), "refs/etude/runs/pinned-run"); resolveErr != nil {
		t.Fatalf("pinned-run should survive refusal: %v", resolveErr)
	}
}

func TestGCPruneUnknownRunNonZeroExit(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	_, errStr, err := executeGC(store, "--prune", "ghost-run")
	if err == nil {
		t.Fatal("gc --prune of unknown run should exit non-zero")
	}
	if !strings.Contains(errStr, "refused") {
		t.Fatalf("expected 'refused' in stderr:\n%s", errStr)
	}
}

func TestGCPruneMixedBatchPrunesEligibleRefusesPinned(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	outRefLeaf, outBytesLeaf := gcContentRef("output", []byte("leaf content"))
	gcWriteRun(t, store, gcMakeManifest("leaf-run", []runmanifest.Stage{gcMakeStage("gen", outRefLeaf, nil)}), map[string][]byte{outRefLeaf.Path: outBytesLeaf})

	outRefPinned, outBytesPinned := gcContentRef("output", []byte("pinned content"))
	gcWriteRun(t, store, gcMakeManifest("pinned-run", []runmanifest.Stage{gcMakeStage("gen", outRefPinned, nil)}), map[string][]byte{outRefPinned.Path: outBytesPinned})
	gcWriteEval(t, store, gcRubricEval("eval-1", "pinned-run", "gen", outRefPinned))

	out, errStr, err := executeGC(store, "--prune", "leaf-run", "pinned-run", "ghost-run")
	// Must exit non-zero (refusals)
	if err == nil {
		t.Fatal("gc --prune mixed batch should exit non-zero when some are refused")
	}
	// leaf-run pruned
	if !strings.Contains(out, "pruned leaf-run") {
		t.Fatalf("expected 'pruned leaf-run' in output:\n%s", out)
	}
	// refusals in stderr
	if !strings.Contains(errStr, "refused") {
		t.Fatalf("expected 'refused' in stderr:\n%s", errStr)
	}
	// leaf-run deleted, pinned-run alive
	if _, resolveErr := store.Resolve(context.Background(), "refs/etude/runs/leaf-run"); resolveErr == nil {
		t.Fatal("leaf-run should be deleted")
	}
	if _, resolveErr := store.Resolve(context.Background(), "refs/etude/runs/pinned-run"); resolveErr != nil {
		t.Fatalf("pinned-run should survive: %v", resolveErr)
	}
}

// ---------------------------------------------------------------------------
// validateCLIIdentifier (gc package-level) used in gc
// ---------------------------------------------------------------------------

// TestGCCollectOptionsZeroMaxSizeNoOversized ensures MaxSize=0 never reports oversized.
func TestGCCollectOptionsZeroMaxSizeNoOversized(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)

	outRef, outBytes := gcContentRef("output", []byte("huge content that should not trigger oversized report"))
	gcWriteRun(t, store, gcMakeManifest("big-run", []runmanifest.Stage{gcMakeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	rpt, err := gc.Collect(context.Background(), store, gc.CollectOptions{MaxSize: 0})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(rpt.Oversized) != 0 {
		t.Fatalf("Oversized should be empty when MaxSize=0, got %v", rpt.Oversized)
	}
}
