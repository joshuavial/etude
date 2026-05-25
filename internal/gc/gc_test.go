package gc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Repo / manifest harness
// ---------------------------------------------------------------------------

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

func makeManifest(runID string, stages []runmanifest.Stage) runmanifest.Manifest {
	return runmanifest.Manifest{
		RunID:           runID,
		Workflow:        "test-workflow",
		WorkflowVersion: "v1",
		Created:         time.Now().UTC(),
		Refs:            map[string]string{"pr": "1"},
		Stages:          stages,
	}
}

func contentRef(role string, content []byte) (runmanifest.ArtifactRef, []byte) {
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

// pointerRef builds a pointer ArtifactRef and a fake pointer JSON file.
func pointerRef(role, uri string) (runmanifest.ArtifactRef, []byte) {
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

func makeStage(name string, output runmanifest.ArtifactRef, inputs []runmanifest.ArtifactRef) runmanifest.Stage {
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

func writeRun(t *testing.T, store refstore.Store, manifest runmanifest.Manifest, files map[string][]byte) {
	t.Helper()
	w := runmanifest.Writer{Store: store}
	if _, err := w.Write(context.Background(), manifest, files, runmanifest.WriteOptions{}); err != nil {
		t.Fatalf("write run %s: %v", manifest.RunID, err)
	}
}

func writeEval(t *testing.T, store refstore.Store, result eval.EvalResult) {
	t.Helper()
	w := eval.Writer{Store: store}
	if _, err := w.Write(context.Background(), result, eval.WriteOptions{}); err != nil {
		t.Fatalf("write eval %s: %v", result.EvalID, err)
	}
}

func artifactSource(runID, stage string, artifact runmanifest.ArtifactRef) eval.ArtifactSource {
	return eval.ArtifactSource{
		RunID:    runID,
		Stage:    stage,
		Commit:   strings.Repeat("b", 40),
		Artifact: artifact.Artifact,
	}
}

func rubricEval(evalID string, target eval.ArtifactSource) eval.EvalResult {
	val := 8.0
	max := 10.0
	return eval.EvalResult{
		EvalResultVersion: 1,
		EvalID:            evalID,
		Method:            "rubric",
		Score: eval.Score{
			Kind:  "rubric",
			Value: &val,
			Max:   &max,
		},
		Rubric:  &eval.RubricRef{Path: "rubric.md", Version: "v1"},
		Targets: []eval.ArtifactSource{target},
		Context: []eval.ArtifactSource{},
		Created: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// BuildPinSet tests
// ---------------------------------------------------------------------------

func TestBuildPinSetEmptyStore(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	pins, err := BuildPinSet(ctx, store)
	if err != nil {
		t.Fatalf("BuildPinSet: %v", err)
	}
	if len(pins) != 0 {
		t.Fatalf("expected empty pin set, got %v", pins)
	}
}

func TestBuildPinSetEvalTargetPinsRun(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRef, outBytes := contentRef("output", []byte("run-a output"))
	writeRun(t, store, makeManifest("run-a", []runmanifest.Stage{makeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	target := artifactSource("run-a", "gen", outRef)
	writeEval(t, store, rubricEval("eval-1", target))

	pins, err := BuildPinSet(ctx, store)
	if err != nil {
		t.Fatalf("BuildPinSet: %v", err)
	}
	if _, ok := pins["run-a"]; !ok {
		t.Fatalf("expected run-a in pin set, got %v", pins)
	}
}

func TestBuildPinSetEvalContextPinsRun(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRefA, outBytesA := contentRef("output", []byte("run-a output"))
	outRefB, outBytesB := contentRef("output", []byte("run-b output"))
	writeRun(t, store, makeManifest("run-a", []runmanifest.Stage{makeStage("gen", outRefA, nil)}), map[string][]byte{outRefA.Path: outBytesA})
	writeRun(t, store, makeManifest("run-b", []runmanifest.Stage{makeStage("gen", outRefB, nil)}), map[string][]byte{outRefB.Path: outBytesB})

	// run-a = target, run-b = context
	target := artifactSource("run-a", "gen", outRefA)
	ctxSrc := artifactSource("run-b", "gen", outRefB)
	val := 8.0
	max := 10.0
	result := eval.EvalResult{
		EvalResultVersion: 1,
		EvalID:            "eval-ctx-1",
		Method:            "rubric",
		Score:             eval.Score{Kind: "rubric", Value: &val, Max: &max},
		Rubric:            &eval.RubricRef{Path: "rubric.md", Version: "v1"},
		Targets:           []eval.ArtifactSource{target},
		Context:           []eval.ArtifactSource{ctxSrc},
		Created:           time.Now().UTC(),
	}
	writeEval(t, store, result)

	pins, err := BuildPinSet(ctx, store)
	if err != nil {
		t.Fatalf("BuildPinSet: %v", err)
	}
	if _, ok := pins["run-a"]; !ok {
		t.Fatalf("expected run-a (target) in pin set, got %v", pins)
	}
	if _, ok := pins["run-b"]; !ok {
		t.Fatalf("expected run-b (context) in pin set, got %v", pins)
	}
}

func TestBuildPinSetReplayOfPinsSourceRun(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRefSrc, outBytesSrc := contentRef("output", []byte("source output"))
	writeRun(t, store, makeManifest("source-run", []runmanifest.Stage{makeStage("gen", outRefSrc, nil)}), map[string][]byte{outRefSrc.Path: outBytesSrc})

	srcCommit := strings.Repeat("c", 40)
	outRefReplay, outBytesReplay := contentRef("output", []byte("replay output"))
	replayStage := runmanifest.Stage{
		Name:       "gen",
		ProducedBy: "replay",
		GitSHA:     strings.Repeat("a", 40),
		Skill:      runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		Timestamp:  time.Now().UTC(),
		Inputs:     nil,
		Output:     outRefReplay,
		ReplayOf:   &runmanifest.ReplayLink{RunID: "source-run", Stage: "gen", Commit: srcCommit},
	}
	writeRun(t, store, makeManifest("replay-run", []runmanifest.Stage{replayStage}), map[string][]byte{outRefReplay.Path: outBytesReplay})

	pins, err := BuildPinSet(ctx, store)
	if err != nil {
		t.Fatalf("BuildPinSet: %v", err)
	}
	if _, ok := pins["source-run"]; !ok {
		t.Fatalf("expected source-run in pin set, got %v", pins)
	}
	if _, ok := pins["replay-run"]; ok {
		t.Fatalf("did not expect replay-run in pin set, got %v", pins)
	}
}

func TestBuildPinSetPinnedTwiceAppearsOnce(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRef, outBytes := contentRef("output", []byte("run-a output"))
	writeRun(t, store, makeManifest("run-a", []runmanifest.Stage{makeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	// Two evals both targeting run-a
	target := artifactSource("run-a", "gen", outRef)
	writeEval(t, store, rubricEval("eval-1", target))

	// Second eval with different output to get a different artifact
	outRef2, outBytes2 := contentRef("output", []byte("second output"))
	writeRun(t, store, makeManifest("run-b", []runmanifest.Stage{makeStage("gen", outRef2, nil)}), map[string][]byte{outRef2.Path: outBytes2})
	target2 := artifactSource("run-a", "gen", outRef) // still run-a
	ctxSrc2 := artifactSource("run-b", "gen", outRef2)
	val := 8.0
	max := 10.0
	result2 := eval.EvalResult{
		EvalResultVersion: 1,
		EvalID:            "eval-2",
		Method:            "rubric",
		Score:             eval.Score{Kind: "rubric", Value: &val, Max: &max},
		Rubric:            &eval.RubricRef{Path: "rubric.md", Version: "v1"},
		Targets:           []eval.ArtifactSource{target2},
		Context:           []eval.ArtifactSource{ctxSrc2},
		Created:           time.Now().UTC(),
	}
	writeEval(t, store, result2)

	pins, err := BuildPinSet(ctx, store)
	if err != nil {
		t.Fatalf("BuildPinSet: %v", err)
	}
	if _, ok := pins["run-a"]; !ok {
		t.Fatalf("expected run-a in pin set (pinned twice)")
	}
	// Verify it is a single map entry, not duplicated (maps can't have duplicates)
	count := 0
	for id := range pins {
		if id == "run-a" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("run-a appears %d times in pin set, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Collect / Report tests
// ---------------------------------------------------------------------------

func TestCollectEmptyStore(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	report, err := Collect(ctx, store, CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if report.RunCount != 0 || report.EvalCount != 0 || report.TotalBytes != 0 {
		t.Fatalf("expected empty report, got %+v", report)
	}
	if len(report.Oversized) != 0 || len(report.External) != 0 {
		t.Fatalf("expected no oversized/external entries, got %+v", report)
	}
}

func TestCollectTotalBytesAccurate(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	content1 := []byte("hello content one")  // 17 bytes
	content2 := []byte("hello content two!") // 18 bytes

	outRef1, outBytes1 := contentRef("output", content1)
	outRef2, outBytes2 := contentRef("output", content2)

	writeRun(t, store, makeManifest("run-1", []runmanifest.Stage{makeStage("gen", outRef1, nil)}), map[string][]byte{outRef1.Path: outBytes1})
	writeRun(t, store, makeManifest("run-2", []runmanifest.Stage{makeStage("gen", outRef2, nil)}), map[string][]byte{outRef2.Path: outBytes2})

	report, err := Collect(ctx, store, CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if report.RunCount != 2 {
		t.Fatalf("RunCount = %d, want 2", report.RunCount)
	}
	wantTotal := int64(len(content1) + len(content2))
	if report.TotalBytes != wantTotal {
		t.Fatalf("TotalBytes = %d, want %d", report.TotalBytes, wantTotal)
	}
}

func TestCollectOversizedBoundary(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	// run-big: 20 bytes (just over 15)
	bigContent := []byte("exactly twenty byt") // 18 bytes
	smallContent := []byte("fourteen bytes!")  // 15 bytes

	outBig, outBigBytes := contentRef("output", bigContent)
	outSmall, outSmallBytes := contentRef("output", smallContent)

	writeRun(t, store, makeManifest("run-big", []runmanifest.Stage{makeStage("gen", outBig, nil)}), map[string][]byte{outBig.Path: outBigBytes})
	writeRun(t, store, makeManifest("run-small", []runmanifest.Stage{makeStage("gen", outSmall, nil)}), map[string][]byte{outSmall.Path: outSmallBytes})

	maxSize := int64(len(smallContent)) // = 15; run-big (18) > 15, run-small (15) is NOT over

	report, err := Collect(ctx, store, CollectOptions{MaxSize: maxSize})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(report.Oversized) != 1 {
		t.Fatalf("Oversized count = %d, want 1; report: %+v", len(report.Oversized), report)
	}
	if report.Oversized[0].RunID != "run-big" {
		t.Fatalf("Oversized[0].RunID = %q, want run-big", report.Oversized[0].RunID)
	}
}

func TestCollectOversizedAbsentWithoutFlag(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRef, outBytes := contentRef("output", []byte("some content here"))
	writeRun(t, store, makeManifest("run-1", []runmanifest.Stage{makeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	report, err := Collect(ctx, store, CollectOptions{}) // MaxSize=0
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(report.Oversized) != 0 {
		t.Fatalf("Oversized should be empty when MaxSize=0, got %+v", report.Oversized)
	}
}

func TestCollectExternalListsPointerArtifacts(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	pRef, pBytes := pointerRef("output", "s3://bucket/object.bin")
	writeRun(t, store, makeManifest("run-ptr", []runmanifest.Stage{makeStage("gen", pRef, nil)}), map[string][]byte{pRef.Path: pBytes})

	report, err := Collect(ctx, store, CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(report.External) != 1 {
		t.Fatalf("External count = %d, want 1; report: %+v", len(report.External), report)
	}
	ext := report.External[0]
	if ext.RunID != "run-ptr" {
		t.Fatalf("External[0].RunID = %q, want run-ptr", ext.RunID)
	}
	if len(ext.Pointers) != 1 || ext.Pointers[0].URI != "s3://bucket/object.bin" {
		t.Fatalf("External[0].Pointers = %+v", ext.Pointers)
	}
}

func TestCollectEvalCountIncluded(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRef, outBytes := contentRef("output", []byte("run-a output"))
	writeRun(t, store, makeManifest("run-a", []runmanifest.Stage{makeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	target := artifactSource("run-a", "gen", outRef)
	writeEval(t, store, rubricEval("eval-1", target))

	report, err := Collect(ctx, store, CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if report.EvalCount != 1 {
		t.Fatalf("EvalCount = %d, want 1", report.EvalCount)
	}
}

// ---------------------------------------------------------------------------
// Prune tests
// ---------------------------------------------------------------------------

func TestPruneLeafRunDeleted(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRef1, outBytes1 := contentRef("output", []byte("run-1 content"))
	outRef2, outBytes2 := contentRef("output", []byte("run-2 content"))
	writeRun(t, store, makeManifest("run-1", []runmanifest.Stage{makeStage("gen", outRef1, nil)}), map[string][]byte{outRef1.Path: outBytes1})
	writeRun(t, store, makeManifest("run-2", []runmanifest.Stage{makeStage("gen", outRef2, nil)}), map[string][]byte{outRef2.Path: outBytes2})

	pruned, refused, err := Prune(ctx, store, []string{"run-1"})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(refused) != 0 {
		t.Fatalf("expected no refusals, got %v", refused)
	}
	if len(pruned) != 1 || pruned[0] != "run-1" {
		t.Fatalf("pruned = %v, want [run-1]", pruned)
	}

	// run-1 ref must be gone
	if _, err := store.Resolve(ctx, "refs/etude/runs/run-1"); !errors.Is(err, refstore.ErrNotFound) {
		t.Fatalf("Resolve run-1 after prune: %v (want ErrNotFound)", err)
	}
	// run-2 must survive
	if _, err := store.Resolve(ctx, "refs/etude/runs/run-2"); err != nil {
		t.Fatalf("run-2 should survive prune: %v", err)
	}
}

func TestPruneEvalPinnedRunRefused(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRef, outBytes := contentRef("output", []byte("pinned content"))
	writeRun(t, store, makeManifest("pinned-run", []runmanifest.Stage{makeStage("gen", outRef, nil)}), map[string][]byte{outRef.Path: outBytes})

	target := artifactSource("pinned-run", "gen", outRef)
	writeEval(t, store, rubricEval("eval-1", target))

	pruned, refused, err := Prune(ctx, store, []string{"pinned-run"})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("expected no pruned, got %v", pruned)
	}
	if len(refused) != 1 || refused[0].RunID != "pinned-run" {
		t.Fatalf("refused = %v, want [{pinned-run ...}]", refused)
	}
	if !strings.Contains(refused[0].Reason, "pinned") {
		t.Fatalf("refused reason = %q, want to contain 'pinned'", refused[0].Reason)
	}

	// ref must still exist
	if _, err := store.Resolve(ctx, "refs/etude/runs/pinned-run"); err != nil {
		t.Fatalf("pinned run must survive refusal: %v", err)
	}
}

func TestPruneReplaySourceRefused(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	outRefSrc, outBytesSrc := contentRef("output", []byte("source output"))
	writeRun(t, store, makeManifest("source-run", []runmanifest.Stage{makeStage("gen", outRefSrc, nil)}), map[string][]byte{outRefSrc.Path: outBytesSrc})

	srcCommit := strings.Repeat("c", 40)
	outRefReplay, outBytesReplay := contentRef("output", []byte("replay output"))
	replayStage := runmanifest.Stage{
		Name:       "gen",
		ProducedBy: "replay",
		GitSHA:     strings.Repeat("a", 40),
		Skill:      runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		Timestamp:  time.Now().UTC(),
		Output:     outRefReplay,
		ReplayOf:   &runmanifest.ReplayLink{RunID: "source-run", Stage: "gen", Commit: srcCommit},
	}
	writeRun(t, store, makeManifest("replay-run", []runmanifest.Stage{replayStage}), map[string][]byte{outRefReplay.Path: outBytesReplay})

	pruned, refused, err := Prune(ctx, store, []string{"source-run"})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("expected no pruned, got %v", pruned)
	}
	if len(refused) != 1 || refused[0].RunID != "source-run" {
		t.Fatalf("refused = %v, want [{source-run ...}]", refused)
	}
}

func TestPruneUnknownIDRefused(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	pruned, refused, err := Prune(ctx, store, []string{"no-such-run"})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("expected no pruned, got %v", pruned)
	}
	if len(refused) != 1 || refused[0].RunID != "no-such-run" || refused[0].Reason != "not found" {
		t.Fatalf("refused = %v, want [{no-such-run not found}]", refused)
	}
}

func TestPruneMixedBatchPrunesEligibleRefusesPinned(t *testing.T) {
	ctx := context.Background()
	store := refstore.New(initRepo(t))

	// leaf-run: prunable
	outRefLeaf, outBytesLeaf := contentRef("output", []byte("leaf content"))
	writeRun(t, store, makeManifest("leaf-run", []runmanifest.Stage{makeStage("gen", outRefLeaf, nil)}), map[string][]byte{outRefLeaf.Path: outBytesLeaf})

	// pinned-run: pinned by eval
	outRefPinned, outBytesPinned := contentRef("output", []byte("pinned content"))
	writeRun(t, store, makeManifest("pinned-run", []runmanifest.Stage{makeStage("gen", outRefPinned, nil)}), map[string][]byte{outRefPinned.Path: outBytesPinned})
	writeEval(t, store, rubricEval("eval-1", artifactSource("pinned-run", "gen", outRefPinned)))

	pruned, refused, err := Prune(ctx, store, []string{"leaf-run", "pinned-run", "ghost-run"})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "leaf-run" {
		t.Fatalf("pruned = %v, want [leaf-run]", pruned)
	}
	if len(refused) != 2 {
		t.Fatalf("refused count = %d, want 2; got %v", len(refused), refused)
	}

	refusedIDs := map[string]string{}
	for _, r := range refused {
		refusedIDs[r.RunID] = r.Reason
	}
	if _, ok := refusedIDs["pinned-run"]; !ok {
		t.Fatalf("expected pinned-run in refused: %v", refused)
	}
	if refusedIDs["ghost-run"] != "not found" {
		t.Fatalf("ghost-run reason = %q, want 'not found'", refusedIDs["ghost-run"])
	}

	// leaf-run deleted, pinned-run survives
	if _, err := store.Resolve(ctx, "refs/etude/runs/leaf-run"); !errors.Is(err, refstore.ErrNotFound) {
		t.Fatalf("leaf-run should be deleted")
	}
	if _, err := store.Resolve(ctx, "refs/etude/runs/pinned-run"); err != nil {
		t.Fatalf("pinned-run should survive: %v", err)
	}
}
