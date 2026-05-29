package index_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/index"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func initRepo(t *testing.T) (string, refstore.Store) {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.name", "Test User")
	gitRun(t, dir, "config", "user.email", "test@example.invalid")
	writeFile(t, dir, "README.md", "test\n")
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "initial")
	return dir, refstore.New(dir)
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func contentRef(t *testing.T, role string, content []byte) (runmanifest.ArtifactRef, []byte) {
	t.Helper()
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

func makeStage(t *testing.T, name string, output runmanifest.ArtifactRef, inputs []runmanifest.ArtifactRef) runmanifest.Stage {
	t.Helper()
	return runmanifest.Stage{
		Name:       name,
		ProducedBy: "test-agent",
		GitSHA:     strings.Repeat("a", 40),
		Skill:      runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		Producer: runmanifest.Producer{
			Skill:   runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
			Model:   "test-model",
			Harness: runmanifest.Harness{Name: "test-harness", Version: "v0"},
		},
		Timestamp: time.Now().UTC(),
		Inputs:    inputs,
		Output:    output,
	}
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

func writeRun(t *testing.T, store refstore.Store, manifest runmanifest.Manifest, files map[string][]byte) {
	t.Helper()
	w := runmanifest.Writer{Store: store}
	if _, err := w.Write(context.Background(), manifest, files, runmanifest.WriteOptions{}); err != nil {
		t.Fatalf("writeRun %s: %v", manifest.RunID, err)
	}
}

func writeEval(t *testing.T, store refstore.Store, result eval.EvalResult) {
	t.Helper()
	w := eval.Writer{Store: store}
	if _, err := w.Write(context.Background(), result, eval.WriteOptions{}); err != nil {
		t.Fatalf("writeEval %s: %v", result.EvalID, err)
	}
}

func rubricEval(evalID, targetRunID, targetStage string, targetArtifact runmanifest.ArtifactRef, contextRunID string, contextArtifact runmanifest.ArtifactRef) eval.EvalResult {
	val := 8.0
	max := 10.0
	var ctxSources []eval.ArtifactSource
	if contextRunID != "" {
		ctxSources = []eval.ArtifactSource{{
			RunID:    contextRunID,
			Stage:    "gen",
			Commit:   strings.Repeat("c", 40),
			Artifact: contextArtifact.Artifact,
		}}
	}
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
		Context: ctxSources,
		Created: time.Now().UTC(),
	}
}

// tamperSchemaVersion directly updates the meta table to simulate a stale db.
func tamperSchemaVersion(t *testing.T, dbPath string, newVersion int) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("tamper open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE meta SET schema_version = ?", newVersion); err != nil {
		t.Fatalf("tamper update: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReindexEmptyStore(t *testing.T) {
	_, store := initRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	result, err := index.Reindex(context.Background(), store, dbPath)
	if err != nil {
		t.Fatalf("Reindex empty store: %v", err)
	}
	if result.Runs != 0 || result.Evals != 0 {
		t.Fatalf("want 0 runs, 0 evals; got %d runs, %d evals", result.Runs, result.Evals)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file should exist: %v", err)
	}

	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open after empty reindex: %v", err)
	}
	defer db.Close()

	rows, err := db.LastRuns(10)
	if err != nil {
		t.Fatalf("LastRuns: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows; got %d", len(rows))
	}
}

func TestReindexRowCounts(t *testing.T) {
	_, store := initRepo(t)

	outRef1, outBytes1 := contentRef(t, "output", []byte("run1 output"))
	inRef1, inBytes1 := contentRef(t, "input", []byte("run1 input"))
	stage1 := makeStage(t, "gen", outRef1, []runmanifest.ArtifactRef{inRef1})
	writeRun(t, store, makeManifest("run-1", []runmanifest.Stage{stage1}),
		map[string][]byte{outRef1.Path: outBytes1, inRef1.Path: inBytes1})

	outRef2, outBytes2 := contentRef(t, "output", []byte("run2 stage1 output"))
	stage2a := makeStage(t, "gen", outRef2, nil)
	outRef2b, outBytes2b := contentRef(t, "output", []byte("run2 stage2 output"))
	stage2b := makeStage(t, "review", outRef2b, []runmanifest.ArtifactRef{outRef2})
	writeRun(t, store, makeManifest("run-2", []runmanifest.Stage{stage2a, stage2b}),
		map[string][]byte{outRef2.Path: outBytes2, outRef2b.Path: outBytes2b})

	writeEval(t, store, rubricEval("eval-1", "run-1", "gen", outRef1, "", runmanifest.ArtifactRef{}))
	writeEval(t, store, rubricEval("eval-2", "run-2", "review", outRef2b, "run-1", outRef1))

	dbPath := filepath.Join(t.TempDir(), "test.db")
	result, err := index.Reindex(context.Background(), store, dbPath)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	if result.Runs != 2 {
		t.Fatalf("want 2 runs; got %d", result.Runs)
	}
	if result.Evals != 2 {
		t.Fatalf("want 2 evals; got %d", result.Evals)
	}

	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	runs, err := db.LastRuns(10)
	if err != nil {
		t.Fatalf("LastRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 run rows; got %d", len(runs))
	}

	genRuns, err := db.RunsWithStage("gen")
	if err != nil {
		t.Fatalf("RunsWithStage gen: %v", err)
	}
	if len(genRuns) != 2 {
		t.Fatalf("want 2 runs with stage 'gen'; got %d", len(genRuns))
	}

	reviewRuns, err := db.RunsWithStage("review")
	if err != nil {
		t.Fatalf("RunsWithStage review: %v", err)
	}
	if len(reviewRuns) != 1 {
		t.Fatalf("want 1 run with stage 'review'; got %d", len(reviewRuns))
	}
	if reviewRuns[0].RunID != "run-2" {
		t.Fatalf("want run-2 with stage review; got %s", reviewRuns[0].RunID)
	}
}

func TestReindexRunCommitMatchesResolve(t *testing.T) {
	_, store := initRepo(t)

	outRef, outBytes := contentRef(t, "output", []byte("content for commit check"))
	stage := makeStage(t, "gen", outRef, nil)
	writeRun(t, store, makeManifest("run-commit", []runmanifest.Stage{stage}),
		map[string][]byte{outRef.Path: outBytes})

	expectedCommit, err := store.Resolve(context.Background(), "refs/etude/runs/run-commit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, err := index.Reindex(context.Background(), store, dbPath); err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	runs, err := db.LastRuns(1)
	if err != nil {
		t.Fatalf("LastRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run; got %d", len(runs))
	}
	if runs[0].Commit != expectedCommit {
		t.Fatalf("run commit = %q, want %q", runs[0].Commit, expectedCommit)
	}
}

func TestReindexIdempotent(t *testing.T) {
	_, store := initRepo(t)

	outRef, outBytes := contentRef(t, "output", []byte("idempotent content"))
	stage := makeStage(t, "gen", outRef, nil)
	writeRun(t, store, makeManifest("run-idem", []runmanifest.Stage{stage}),
		map[string][]byte{outRef.Path: outBytes})

	writeEval(t, store, rubricEval("eval-idem", "run-idem", "gen", outRef, "", runmanifest.ArtifactRef{}))

	dbPath := filepath.Join(t.TempDir(), "test.db")

	if _, err := index.Reindex(context.Background(), store, dbPath); err != nil {
		t.Fatalf("first Reindex: %v", err)
	}

	db1, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open after first reindex: %v", err)
	}
	runs1, err := db1.LastRuns(10)
	db1.Close()
	if err != nil {
		t.Fatalf("LastRuns first: %v", err)
	}

	if _, err := index.Reindex(context.Background(), store, dbPath); err != nil {
		t.Fatalf("second Reindex: %v", err)
	}

	db2, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open after second reindex: %v", err)
	}
	defer db2.Close()

	runs2, err := db2.LastRuns(10)
	if err != nil {
		t.Fatalf("LastRuns second: %v", err)
	}

	if len(runs1) != len(runs2) {
		t.Fatalf("run count changed between reindexes: %d vs %d", len(runs1), len(runs2))
	}
	for i := range runs1 {
		if runs1[i].RunID != runs2[i].RunID {
			t.Fatalf("run[%d] run_id: %q vs %q", i, runs1[i].RunID, runs2[i].RunID)
		}
		if runs1[i].Commit != runs2[i].Commit {
			t.Fatalf("run[%d] commit: %q vs %q", i, runs1[i].Commit, runs2[i].Commit)
		}
	}
}

func TestReindexMalformedManifestLeavesOldDbIntact(t *testing.T) {
	_, store := initRepo(t)

	// Write a good run first and build the initial index.
	outRef, outBytes := contentRef(t, "output", []byte("good content"))
	stage := makeStage(t, "gen", outRef, nil)
	writeRun(t, store, makeManifest("run-good", []runmanifest.Stage{stage}),
		map[string][]byte{outRef.Path: outBytes})

	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, err := index.Reindex(context.Background(), store, dbPath); err != nil {
		t.Fatalf("first Reindex: %v", err)
	}

	dbStat, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat good db: %v", err)
	}
	goodModTime := dbStat.ModTime()

	// Inject a malformed manifest directly into the store.
	bad := []byte(`{not valid json at all`)
	if _, err := store.WriteCommit(context.Background(),
		"refs/etude/runs/bad-run",
		map[string][]byte{"manifest.json": bad},
		refstore.WriteOptions{},
	); err != nil {
		t.Fatalf("WriteCommit bad manifest: %v", err)
	}

	// Reindex should fail and name the bad ref.
	_, reindexErr := index.Reindex(context.Background(), store, dbPath)
	if reindexErr == nil {
		t.Fatal("Reindex with malformed manifest should fail")
	}
	if !strings.Contains(reindexErr.Error(), "bad-run") {
		t.Fatalf("error should name bad ref, got: %v", reindexErr)
	}

	// The old db must be untouched (same mod time, still readable).
	newStat, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db after failed reindex: %v", err)
	}
	if !newStat.ModTime().Equal(goodModTime) {
		t.Fatalf("db was modified by failed reindex: old=%v new=%v", goodModTime, newStat.ModTime())
	}

	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open old db: %v", err)
	}
	defer db.Close()

	runs, err := db.LastRuns(10)
	if err != nil {
		t.Fatalf("LastRuns old db: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-good" {
		t.Fatalf("old db should still have run-good, got %v", runs)
	}
}

func TestReindexEvalSources(t *testing.T) {
	_, store := initRepo(t)

	outRef1, outBytes1 := contentRef(t, "output", []byte("eval target content"))
	outRef2, outBytes2 := contentRef(t, "output", []byte("eval context content"))
	stage1 := makeStage(t, "gen", outRef1, nil)
	stage2 := makeStage(t, "gen", outRef2, nil)
	writeRun(t, store, makeManifest("run-target", []runmanifest.Stage{stage1}),
		map[string][]byte{outRef1.Path: outBytes1})
	writeRun(t, store, makeManifest("run-context", []runmanifest.Stage{stage2}),
		map[string][]byte{outRef2.Path: outBytes2})

	writeEval(t, store, rubricEval("eval-sources", "run-target", "gen", outRef1, "run-context", outRef2))

	dbPath := filepath.Join(t.TempDir(), "test.db")
	result, err := index.Reindex(context.Background(), store, dbPath)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if result.Evals != 1 {
		t.Fatalf("want 1 eval; got %d", result.Evals)
	}

	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	runs, err := db.LastRuns(10)
	if err != nil {
		t.Fatalf("LastRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 runs; got %d", len(runs))
	}
}

func TestOpenSchemaMismatch(t *testing.T) {
	_, store := initRepo(t)
	outRef, outBytes := contentRef(t, "output", []byte("schema check"))
	stage := makeStage(t, "gen", outRef, nil)
	writeRun(t, store, makeManifest("run-schema", []runmanifest.Stage{stage}),
		map[string][]byte{outRef.Path: outBytes})

	dbPath := filepath.Join(t.TempDir(), "stale.db")
	if _, err := index.Reindex(context.Background(), store, dbPath); err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	tamperSchemaVersion(t, dbPath, 999)

	_, err := index.Open(dbPath)
	if err == nil {
		t.Fatal("Open with wrong schema version should fail")
	}
	if !errors.Is(err, index.ErrSchemaMismatch) {
		t.Fatalf("want ErrSchemaMismatch; got %v", err)
	}
}

func TestLastRunsLimit(t *testing.T) {
	_, store := initRepo(t)

	for i := 0; i < 5; i++ {
		id := "run-limit-" + string(rune('a'+i))
		outRef, outBytes := contentRef(t, "output", []byte("content "+id))
		stage := makeStage(t, "gen", outRef, nil)
		// Stagger created times so ordering is deterministic.
		m := makeManifest(id, []runmanifest.Stage{stage})
		m.Created = time.Now().UTC().Add(time.Duration(i) * time.Second)
		writeRun(t, store, m, map[string][]byte{outRef.Path: outBytes})
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, err := index.Reindex(context.Background(), store, dbPath); err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	runs, err := db.LastRuns(3)
	if err != nil {
		t.Fatalf("LastRuns(3): %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("want 3 rows from LastRuns(3); got %d", len(runs))
	}
}

func TestReindexIndexesGatesAndLatestGate(t *testing.T) {
	_, store := initRepo(t)

	out, outBytes := contentRef(t, "output", []byte("plan output"))
	stage := makeStage(t, "plan", out, nil)
	m := makeManifest("gate-run", []runmanifest.Stage{stage})

	now := time.Date(2026, 5, 25, 3, 10, 0, 0, time.UTC)
	mkGate := func(id string, round int, status runmanifest.GateStatus) runmanifest.GateAttempt {
		decision := runmanifest.GateDecision{}
		if status == runmanifest.GateStatusEscalated {
			// Validate requires escalation_reason when status is escalated.
			decision.EscalationReason = "rework ceiling reached"
		}
		return runmanifest.GateAttempt{
			GateID:         id,
			Phase:          "plan",
			Round:          round,
			Tier:           1,
			Status:         status,
			ReviewedStages: []runmanifest.ReviewedRef{{Stage: "plan", Role: "plan"}},
			Seats: []runmanifest.SeatResult{{
				Seat:      "gemini",
				Harness:   runmanifest.Harness{Name: "gemini-cli", Version: "3.1"},
				Provider:  runmanifest.Provider{Name: "google", Model: "gemini-3.1-pro-preview"},
				Verdict:   runmanifest.SeatVerdictGo,
				Timestamp: now,
			}},
			Decision:  decision,
			Timestamp: now,
		}
	}
	// Two rounds for the same phase: the latest (highest round) must win.
	m.Gates = []runmanifest.GateAttempt{
		mkGate("plan.r1", 1, runmanifest.GateStatusRerun),
		mkGate("plan.r2", 2, runmanifest.GateStatusPass),
	}
	writeRun(t, store, m, map[string][]byte{out.Path: outBytes})

	// A second run with a same-phase, higher-round gate must not leak into the
	// first run's LatestGate lookup (run_id scoping).
	out2, out2Bytes := contentRef(t, "output", []byte("other plan output"))
	stage2 := makeStage(t, "plan", out2, nil)
	m2 := makeManifest("other-run", []runmanifest.Stage{stage2})
	m2.Gates = []runmanifest.GateAttempt{mkGate("plan.r9", 9, runmanifest.GateStatusEscalated)}
	writeRun(t, store, m2, map[string][]byte{out2.Path: out2Bytes})

	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, err := index.Reindex(context.Background(), store, dbPath); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	g, ok, err := db.LatestGate("gate-run", "plan")
	if err != nil {
		t.Fatalf("LatestGate: %v", err)
	}
	if !ok {
		t.Fatal("LatestGate: expected a gate, got none")
	}
	if g.Round != 2 || g.GateID != "plan.r2" || g.Status != "pass" {
		t.Fatalf("LatestGate returned the wrong attempt: %+v", g)
	}

	// A phase with no gate must report not-found, not error.
	if _, ok, err := db.LatestGate("gate-run", "verify"); err != nil || ok {
		t.Fatalf("LatestGate(unknown phase): ok=%v err=%v; want ok=false, nil", ok, err)
	}
}
