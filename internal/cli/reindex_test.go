package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/index"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Helpers (reindex-specific; avoid polluting the shared test namespace)
// ---------------------------------------------------------------------------

func riContentRef(role string, content []byte) (runmanifest.ArtifactRef, []byte) {
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

func riMakeStage(name string, output runmanifest.ArtifactRef) runmanifest.Stage {
	return runmanifest.Stage{
		Name:       name,
		ProducedBy: "test-agent",
		GitSHA:     strings.Repeat("a", 40),
		Skill:      runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
		Timestamp:  time.Now().UTC(),
		Output:     output,
	}
}

func riWriteRun(t *testing.T, store refstore.Store, runID, stageName string, content []byte) {
	t.Helper()
	outRef, outBytes := riContentRef("output", content)
	stage := riMakeStage(stageName, outRef)
	m := runmanifest.Manifest{
		RunID:           runID,
		Workflow:        "test-workflow",
		WorkflowVersion: "v1",
		Created:         time.Now().UTC(),
		Refs:            map[string]string{"pr": "1"},
		Stages:          []runmanifest.Stage{stage},
	}
	w := runmanifest.Writer{Store: store}
	if _, err := w.Write(context.Background(), m, map[string][]byte{outRef.Path: outBytes}, runmanifest.WriteOptions{}); err != nil {
		t.Fatalf("riWriteRun %s: %v", runID, err)
	}
}

// ---------------------------------------------------------------------------
// Registration and --help
// ---------------------------------------------------------------------------

func TestReindexCommandIsRegistered(t *testing.T) {
	stdout, stderr, err := execute("reindex", "--help")
	if err != nil {
		t.Fatalf("reindex --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "reindex") {
		t.Fatalf("reindex --help output does not mention 'reindex':\n%s", stdout)
	}
}

func TestReindexHelpDoesNotShowDbPath(t *testing.T) {
	stdout, _, err := execute("reindex", "--help")
	if err != nil {
		t.Fatalf("reindex --help: %v", err)
	}
	// --db-path is hidden and must not appear in help output.
	if strings.Contains(stdout, "--db-path") {
		t.Fatalf("hidden --db-path appeared in help:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: --db-path override
// ---------------------------------------------------------------------------

func TestReindexEndToEndWithDbPath(t *testing.T) {
	repo := initCaptureRepo(t)
	store := refstore.New(repo)
	chdir(t, repo)

	riWriteRun(t, store, "run-e2e", "gen", []byte("end-to-end content"))

	dbPath := filepath.Join(t.TempDir(), "idx.db")

	stdout, stderr, err := execute("reindex", "--db-path", dbPath)
	if err != nil {
		t.Fatalf("reindex --db-path: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "reindexed 1 runs") {
		t.Fatalf("expected 'reindexed 1 runs' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, dbPath) {
		t.Fatalf("expected db path in output:\n%s", stdout)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file should exist after reindex: %v", err)
	}

	db, err := index.Open(dbPath)
	if err != nil {
		t.Fatalf("Open after reindex: %v", err)
	}
	defer db.Close()

	runs, err := db.LastRuns(10)
	if err != nil {
		t.Fatalf("LastRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-e2e" {
		t.Fatalf("expected run-e2e in index; got %v", runs)
	}
}

func TestReindexPositionalArgError(t *testing.T) {
	// Running reindex with positional args should fail (NoArgs).
	repo := initCaptureRepo(t)
	chdir(t, repo)
	dbPath := filepath.Join(t.TempDir(), "idx.db")

	_, _, err := execute("reindex", "--db-path", dbPath, "unexpected-arg")
	if err == nil {
		t.Fatal("reindex with positional arg should return error")
	}
}

func TestReindexWritesToGitDir(t *testing.T) {
	// Verify the default (no --db-path) resolves to .git/etude-index.db.
	repo := initCaptureRepo(t)
	chdir(t, repo)

	store := refstore.New(repo)
	riWriteRun(t, store, "run-gitdir", "gen", []byte("git dir test"))

	stdout, stderr, err := execute("reindex")
	if err != nil {
		t.Fatalf("reindex (no --db-path): %v\nstderr: %s", err, stderr)
	}

	expectedDB := filepath.Join(repo, ".git", "etude-index.db")
	if _, statErr := os.Stat(expectedDB); statErr != nil {
		t.Fatalf(".git/etude-index.db should exist: %v", statErr)
	}
	if !strings.Contains(stdout, "etude-index.db") {
		t.Fatalf("expected 'etude-index.db' in output:\n%s", stdout)
	}
}

func TestReindexEmptyRepoViaDbPath(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	dbPath := filepath.Join(t.TempDir(), "empty.db")

	stdout, stderr, err := execute("reindex", "--db-path", dbPath)
	if err != nil {
		t.Fatalf("reindex empty: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "reindexed 0 runs, 0 evals") {
		t.Fatalf("expected zero counts; got:\n%s", stdout)
	}
}
