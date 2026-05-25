// Package index builds and queries a derived SQLite cache of etude run and
// eval refs. The cache lives at .git/etude-index.db and is a disposable,
// full-rebuild-only index (v1). It accelerates queries that would otherwise
// walk all refs via git for-each-ref.
//
// The index is purely additive: no existing command reads it yet. Consumer
// beads will rewire run list / bench / gc to query it.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	_ "modernc.org/sqlite"
)

const (
	SchemaVersion = 1

	runsPrefix  = "refs/etude/runs"
	evalsPrefix = "refs/etude/evals"
)

var ErrSchemaMismatch = errors.New("index schema version mismatch")

// DB wraps a read-only connection to an etude index database and exposes
// typed query helpers. Open validates the schema version on creation.
type DB struct {
	db *sql.DB
}

// ReindexResult holds the counts returned by Reindex.
type ReindexResult struct {
	Runs  int
	Evals int
}

// Open opens an existing index database at dbPath and validates the schema
// version. Returns ErrSchemaMismatch if the stored schema_version does not
// match SchemaVersion so callers can detect a stale index and reindex.
func Open(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open index %s: %w", dbPath, err)
	}
	var version int
	if err := db.QueryRow("SELECT schema_version FROM meta LIMIT 1").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if version != SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("%w: db has version %d, want %d (run etude reindex)", ErrSchemaMismatch, version, SchemaVersion)
	}
	return &DB{db: db}, nil
}

// Close releases the database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// RunRow holds one row from the runs table.
type RunRow struct {
	RunID           string
	Workflow        string
	WorkflowVersion string
	Created         time.Time
	Commit          string
}

// LastRuns returns up to n run rows ordered by created descending.
func (d *DB) LastRuns(n int) ([]RunRow, error) {
	rows, err := d.db.Query(
		`SELECT run_id, workflow, workflow_version, created, "commit"
		 FROM runs ORDER BY created DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("LastRuns: %w", err)
	}
	defer rows.Close()
	return scanRunRows(rows)
}

// RunsWithStage returns runs that have at least one stage with the given name.
func (d *DB) RunsWithStage(stageName string) ([]RunRow, error) {
	rows, err := d.db.Query(
		`SELECT r.run_id, r.workflow, r.workflow_version, r.created, r."commit"
		 FROM runs r
		 JOIN stages s ON s.run_id = r.run_id
		 WHERE s.name = ?
		 ORDER BY r.created DESC`, stageName)
	if err != nil {
		return nil, fmt.Errorf("RunsWithStage: %w", err)
	}
	defer rows.Close()
	return scanRunRows(rows)
}

func scanRunRows(rows *sql.Rows) ([]RunRow, error) {
	var result []RunRow
	for rows.Next() {
		var r RunRow
		var createdStr string
		if err := rows.Scan(&r.RunID, &r.Workflow, &r.WorkflowVersion, &createdStr, &r.Commit); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, createdStr)
		if err != nil {
			t, err = time.Parse(time.RFC3339, createdStr)
			if err != nil {
				return nil, fmt.Errorf("parse created %q: %w", createdStr, err)
			}
		}
		r.Created = t.UTC()
		result = append(result, r)
	}
	return result, rows.Err()
}

// Reindex builds a fresh SQLite index at dbPath by walking all run and eval
// refs in store. It creates a temp file in the same directory as dbPath, builds
// the full schema and data inside a single transaction, then atomically renames
// the temp file to dbPath. On any error, the temp file is removed and the
// existing dbPath (if any) is left untouched.
func Reindex(ctx context.Context, store refstore.Store, dbPath string) (ReindexResult, error) {
	tmpPath := filepath.Join(
		filepath.Dir(dbPath),
		fmt.Sprintf(".etude-index-tmp-%x.db", rand.Uint64()),
	)

	// Ensure temp file is removed on any failure path.
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return ReindexResult{}, fmt.Errorf("open temp db: %w", err)
	}
	defer db.Close()

	// Build schema, data, and meta in ONE transaction so the temp db is never
	// observed (or installed) in a partially-built state. SQLite supports
	// transactional DDL, so the CREATE statements roll back with the rest on any
	// error. The atomic install is still the final os.Rename.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ReindexResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := createSchema(ctx, tx); err != nil {
		return ReindexResult{}, err
	}

	result, err := populateIndex(ctx, store, tx)
	if err != nil {
		return ReindexResult{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta(schema_version, built_at) VALUES (?, ?)`,
		SchemaVersion, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return ReindexResult{}, fmt.Errorf("write meta: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ReindexResult{}, fmt.Errorf("commit: %w", err)
	}
	if err := db.Close(); err != nil {
		return ReindexResult{}, fmt.Errorf("close temp db: %w", err)
	}

	if err := os.Rename(tmpPath, dbPath); err != nil {
		return ReindexResult{}, fmt.Errorf("install index: %w", err)
	}

	cleanup = false
	return result, nil
}

func createSchema(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return nil
}

func populateIndex(ctx context.Context, store refstore.Store, tx *sql.Tx) (ReindexResult, error) {
	runRefs, err := store.List(ctx, runsPrefix)
	if err != nil {
		return ReindexResult{}, fmt.Errorf("list run refs: %w", err)
	}

	for _, ref := range runRefs {
		commit, err := store.Resolve(ctx, ref)
		if err != nil {
			return ReindexResult{}, fmt.Errorf("resolve commit for %s: %w", ref, err)
		}
		manifestBytes, err := store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return ReindexResult{}, fmt.Errorf("read manifest for %s: %w", ref, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return ReindexResult{}, fmt.Errorf("parse manifest for %s: %w", ref, err)
		}
		if err := insertRun(ctx, tx, manifest, commit); err != nil {
			return ReindexResult{}, fmt.Errorf("insert run from %s: %w", ref, err)
		}
	}

	evalRefs, err := store.List(ctx, evalsPrefix)
	if err != nil {
		return ReindexResult{}, fmt.Errorf("list eval refs: %w", err)
	}

	for _, ref := range evalRefs {
		evalBytes, err := store.ReadFile(ctx, ref, "eval_result.json")
		if err != nil {
			return ReindexResult{}, fmt.Errorf("read eval_result.json for %s: %w", ref, err)
		}
		result, err := eval.ParseJSON(evalBytes)
		if err != nil {
			return ReindexResult{}, fmt.Errorf("parse eval_result.json for %s: %w", ref, err)
		}
		if err := insertEval(ctx, tx, result); err != nil {
			return ReindexResult{}, fmt.Errorf("insert eval from %s: %w", ref, err)
		}
	}

	return ReindexResult{Runs: len(runRefs), Evals: len(evalRefs)}, nil
}

func insertRun(ctx context.Context, tx *sql.Tx, m runmanifest.Manifest, commit string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO runs(run_id, workflow, workflow_version, created, "commit")
		 VALUES (?, ?, ?, ?, ?)`,
		m.RunID,
		m.Workflow,
		m.WorkflowVersion,
		m.Created.UTC().Format(time.RFC3339Nano),
		commit,
	); err != nil {
		return fmt.Errorf("insert run %s: %w", m.RunID, err)
	}

	for _, stage := range m.Stages {
		var replayOfRunID *string
		if stage.ReplayOf != nil {
			replayOfRunID = &stage.ReplayOf.RunID
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stages(run_id, name, produced_by, git_sha,
			  skill_id, skill_repo, skill_version, model,
			  harness_name, harness_version, replay_of_run_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.RunID,
			stage.Name,
			stage.ProducedBy,
			stage.GitSHA,
			stage.Producer.Skill.ID,
			stage.Producer.Skill.Repo,
			stage.Producer.Skill.Version,
			stage.Producer.Model,
			stage.Producer.Harness.Name,
			stage.Producer.Harness.Version,
			replayOfRunID,
		); err != nil {
			return fmt.Errorf("insert stage %s/%s: %w", m.RunID, stage.Name, err)
		}

		// Insert output artifact.
		if err := insertArtifact(ctx, tx, m.RunID, stage.Name, stage.Output); err != nil {
			return err
		}
		// Insert input artifacts.
		for _, inp := range stage.Inputs {
			if err := insertArtifact(ctx, tx, m.RunID, stage.Name, inp); err != nil {
				return err
			}
		}
	}

	return nil
}

func insertArtifact(ctx context.Context, tx *sql.Tx, runID, stageName string, a runmanifest.ArtifactRef) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO artifacts(run_id, stage, role, sha256, size, storage)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		runID,
		stageName,
		a.Role,
		a.Artifact,
		a.Size,
		string(a.Storage),
	); err != nil {
		return fmt.Errorf("insert artifact %s/%s/%s: %w", runID, stageName, a.Role, err)
	}
	return nil
}

func insertEval(ctx context.Context, tx *sql.Tx, r eval.EvalResult) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO evals(eval_id, method, created)
		 VALUES (?, ?, ?)`,
		r.EvalID,
		r.Method,
		r.Created.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("insert eval %s: %w", r.EvalID, err)
	}

	for i, src := range r.Targets {
		if err := insertEvalSource(ctx, tx, r.EvalID, "target", i, src); err != nil {
			return err
		}
	}
	for i, src := range r.Context {
		if err := insertEvalSource(ctx, tx, r.EvalID, "context", i, src); err != nil {
			return err
		}
	}

	return nil
}

func insertEvalSource(ctx context.Context, tx *sql.Tx, evalID, kind string, idx int, src eval.ArtifactSource) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO eval_sources(eval_id, kind, idx, run_id, stage, "commit", artifact)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		evalID,
		kind,
		idx,
		src.RunID,
		src.Stage,
		src.Commit,
		src.Artifact,
	); err != nil {
		return fmt.Errorf("insert eval_source %s/%s[%d]: %w", evalID, kind, idx, err)
	}
	return nil
}

// schema is the DDL executed on every fresh index build.
const schema = `
CREATE TABLE runs (
	run_id           TEXT NOT NULL PRIMARY KEY,
	workflow         TEXT NOT NULL,
	workflow_version TEXT NOT NULL,
	created          TEXT NOT NULL,
	"commit"         TEXT NOT NULL
);

CREATE TABLE stages (
	run_id              TEXT NOT NULL,
	name                TEXT NOT NULL,
	produced_by         TEXT NOT NULL,
	git_sha             TEXT NOT NULL,
	skill_id            TEXT NOT NULL,
	skill_repo          TEXT NOT NULL,
	skill_version       TEXT NOT NULL,
	model               TEXT NOT NULL DEFAULT '',
	harness_name        TEXT NOT NULL DEFAULT '',
	harness_version     TEXT NOT NULL DEFAULT '',
	replay_of_run_id    TEXT,
	PRIMARY KEY (run_id, name)
);

CREATE TABLE artifacts (
	run_id  TEXT NOT NULL,
	stage   TEXT NOT NULL,
	role    TEXT NOT NULL,
	sha256  TEXT NOT NULL,
	size    INTEGER NOT NULL,
	storage TEXT NOT NULL
);

CREATE TABLE evals (
	eval_id TEXT NOT NULL PRIMARY KEY,
	method  TEXT NOT NULL,
	created TEXT NOT NULL
);

CREATE TABLE eval_sources (
	eval_id  TEXT NOT NULL,
	kind     TEXT NOT NULL,
	idx      INTEGER NOT NULL,
	run_id   TEXT NOT NULL,
	stage    TEXT NOT NULL,
	"commit" TEXT NOT NULL,
	artifact TEXT NOT NULL,
	PRIMARY KEY (eval_id, kind, idx)
);

CREATE TABLE meta (
	schema_version INTEGER NOT NULL,
	built_at       TEXT NOT NULL
);

CREATE INDEX idx_stages_name    ON stages(name);
CREATE INDEX idx_stages_skill   ON stages(skill_id);
CREATE INDEX idx_artifacts_sha  ON artifacts(sha256);
CREATE INDEX idx_eval_src_run   ON eval_sources(run_id);
`
