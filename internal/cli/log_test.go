package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// captureRunForLog seeds a single-stage run into repo and returns its run id.
func captureRunForLog(t *testing.T, repo, runID string) {
	t.Helper()
	writeFile(t, repo, "out-"+runID+".md", "content for "+runID)
	chdir(t, repo)
	_, stderr, err := execute("capture", "plan", "--run", runID, "--output", "output=out-"+runID+".md")
	if err != nil {
		t.Fatalf("captureRunForLog %q: %v\nstderr: %s", runID, err, stderr)
	}
}

// captureRetroForLog seeds a retro with the given subject run and returns the retro id.
func captureRetroForLog(t *testing.T, repo, subjectRun string) string {
	t.Helper()
	bodyFile := "retro-" + subjectRun + ".md"
	writeFile(t, repo, bodyFile, "# retro for "+subjectRun+"\n")
	chdir(t, repo)
	stdout, stderr, err := execute("retro", "capture", "run",
		"--file", bodyFile,
		"--subject-run", subjectRun,
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("captureRetroForLog %q: %v\nstderr: %s", subjectRun, err, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "ref refs/etude/retros/") {
			return strings.TrimPrefix(line, "ref refs/etude/retros/")
		}
	}
	t.Fatalf("could not extract retro id from stdout: %q", stdout)
	return ""
}

// seedRunWithTimestamp writes a run manifest with a specific Created timestamp
// directly into the repo via WriteManifestTree (content-addressed, validates).
func seedRunWithTimestamp(t *testing.T, repo, runID string, ts time.Time) {
	t.Helper()
	store := refstore.New(repo)
	ctx := context.Background()

	aStore := artifactstore.New()
	artifact, err := aStore.AddContent("output", "text/markdown; charset=utf-8", []byte("# "+runID))
	if err != nil {
		t.Fatalf("AddContent run %q: %v", runID, err)
	}
	outputRef := runmanifest.ArtifactFromManifestArtifact(artifact)
	files := aStore.Files()

	m := runmanifest.Manifest{
		RunID:           runID,
		Workflow:        "manual",
		WorkflowVersion: "manual-v1",
		Created:         ts,
		Refs:            map[string]string{},
		Stages: []runmanifest.Stage{
			{
				Name:       "plan",
				ProducedBy: "original",
				GitSHA:     "0000000000000000000000000000000000000000",
				Skill: runmanifest.Skill{
					ID:      "plan",
					Repo:    "manual",
					Version: "manual",
				},
				Inputs:    []runmanifest.ArtifactRef{},
				Output:    outputRef,
				Timestamp: ts,
			},
		},
	}
	if _, err := runmanifest.WriteManifestTree(ctx, store, runsPrefix, m, files, refstore.WriteOptions{}); err != nil {
		t.Fatalf("WriteManifestTree run %q: %v", runID, err)
	}
}

// seedRetroWithTimestamp writes a retro manifest with a specific Created timestamp.
func seedRetroWithTimestamp(t *testing.T, repo, retroID, subjectRunID string, ts time.Time) {
	t.Helper()
	store := refstore.New(repo)
	ctx := context.Background()

	aStore := artifactstore.New()
	artifact, err := aStore.AddContent("retro", "text/markdown; charset=utf-8", []byte("# retro "+retroID))
	if err != nil {
		t.Fatalf("AddContent retro %q: %v", retroID, err)
	}
	outputRef := runmanifest.ArtifactFromManifestArtifact(artifact)
	files := aStore.Files()

	m := runmanifest.Manifest{
		RunID:           retroID,
		Workflow:        "retro",
		WorkflowVersion: "retro-v1",
		Created:         ts,
		Refs: map[string]string{
			"scope":         "run",
			"trigger":       "manual",
			"subject_run.1": subjectRunID,
		},
		Stages: []runmanifest.Stage{
			{
				Name:       "retro",
				ProducedBy: "retro",
				GitSHA:     "0000000000000000000000000000000000000000",
				Skill: runmanifest.Skill{
					ID:      "retro",
					Repo:    "manual",
					Version: "manual",
				},
				Inputs:    []runmanifest.ArtifactRef{},
				Output:    outputRef,
				Timestamp: ts,
			},
		},
	}
	if _, err := runmanifest.WriteManifestTree(ctx, store, retrosPrefix, m, files, refstore.WriteOptions{}); err != nil {
		t.Fatalf("WriteManifestTree retro %q: %v", retroID, err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestLogIsRegisteredSubcommand(t *testing.T) {
	stdout, stderr, err := execute("log", "--help")
	if err != nil {
		t.Fatalf("log --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "log") {
		t.Fatalf("log --help output does not mention 'log':\n%s", stdout)
	}
}

func TestLogZeroEvents(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "no events found") {
		t.Fatalf("expected 'no events found', got: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestLogRunsAndRetrosUnioned(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Seed 2 runs.
	captureRunForLog(t, repo, "log-run-a")
	captureRunForLog(t, repo, "log-run-b")
	// Seed 1 retro.
	captureRetroForLog(t, repo, "log-run-a")

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// Header columns.
	for _, col := range []string{"TIMESTAMP", "KIND", "ID", "SUMMARY"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("expected column %q in header:\n%s", col, stdout)
		}
	}

	// All three events appear with correct kind labels.
	if !strings.Contains(stdout, "log-run-a") {
		t.Fatalf("log-run-a missing from output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "log-run-b") {
		t.Fatalf("log-run-b missing from output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "run") {
		t.Fatalf("kind 'run' missing from output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "retro") {
		t.Fatalf("kind 'retro' missing from output:\n%s", stdout)
	}

	// Count data rows (non-header, non-empty lines).
	var dataRows int
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "TIMESTAMP") {
			continue
		}
		dataRows++
	}
	if dataRows != 3 {
		t.Fatalf("expected 3 data rows, got %d:\n%s", dataRows, stdout)
	}
}

func TestLogChronologicalOrder(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)

	// Seed in reverse order to prove sort works.
	seedRunWithTimestamp(t, repo, "run-late", t3)
	seedRunWithTimestamp(t, repo, "run-mid", t2)
	seedRunWithTimestamp(t, repo, "run-early", t1)

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	idxEarly := strings.Index(stdout, "run-early")
	idxMid := strings.Index(stdout, "run-mid")
	idxLate := strings.Index(stdout, "run-late")
	if idxEarly < 0 || idxMid < 0 || idxLate < 0 {
		t.Fatalf("missing ids in output:\n%s", stdout)
	}
	if !(idxEarly < idxMid && idxMid < idxLate) {
		t.Fatalf("expected ascending order run-early < run-mid < run-late:\n%s", stdout)
	}
}

func TestLogChronologicalTiebreak(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Same timestamp: retro < run (kind tiebreak); within same kind: run-a < run-b (id tiebreak).
	ts := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	seedRunWithTimestamp(t, repo, "run-b", ts)
	seedRunWithTimestamp(t, repo, "run-a", ts)
	seedRetroWithTimestamp(t, repo, "retro-z", "run-a", ts)

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// retro before run (kind tiebreak).
	idxRetro := strings.Index(stdout, "retro-z")
	idxRunA := strings.Index(stdout, "run-a")
	idxRunB := strings.Index(stdout, "run-b")
	if idxRetro < 0 || idxRunA < 0 || idxRunB < 0 {
		t.Fatalf("missing ids in output:\n%s", stdout)
	}
	if !(idxRetro < idxRunA && idxRunA < idxRunB) {
		t.Fatalf("expected tiebreak order retro-z < run-a < run-b:\n%s", stdout)
	}
}

func TestLogKindFilterRun(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	captureRunForLog(t, repo, "kf-run")
	captureRetroForLog(t, repo, "kf-run")

	stdout, stderr, err := execute("log", "--kind", "run")
	if err != nil {
		t.Fatalf("log --kind run returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
	if !strings.Contains(stdout, "kf-run") {
		t.Fatalf("run row missing from --kind run output:\n%s", stdout)
	}
	// retro rows must not appear — check no row has "retro" in KIND column.
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "retro" {
			t.Fatalf("retro row present with --kind run:\n%s", stdout)
		}
	}
}

func TestLogKindFilterRetro(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	captureRunForLog(t, repo, "kr-run")
	captureRetroForLog(t, repo, "kr-run")

	stdout, stderr, err := execute("log", "--kind", "retro")
	if err != nil {
		t.Fatalf("log --kind retro returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
	// run rows must not appear.
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "run" {
			t.Fatalf("run row present with --kind retro:\n%s", stdout)
		}
	}
	// At least one retro row.
	if !strings.Contains(stdout, "retro") {
		t.Fatalf("no retro rows in --kind retro output:\n%s", stdout)
	}
}

func TestLogKindFilterInvalid(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("log", "--kind", "bogus")
	if err == nil {
		t.Fatal("log --kind bogus returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "bogus") {
		t.Fatalf("error does not name the invalid value 'bogus': %q", combined)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout on invalid --kind, got: %q", stdout)
	}
}

func TestLogSubjectFilter(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Seed two runs and a retro that covers run-sf-a.
	captureRunForLog(t, repo, "run-sf-a")
	captureRunForLog(t, repo, "run-sf-b")
	captureRetroForLog(t, repo, "run-sf-a")

	// --subject run-sf-a: should match the run with that id AND the retro whose subject_run.1 = run-sf-a.
	stdout, stderr, err := execute("log", "--subject", "run-sf-a")
	if err != nil {
		t.Fatalf("log --subject run-sf-a returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	if !strings.Contains(stdout, "run-sf-a") {
		t.Fatalf("run-sf-a missing from --subject run-sf-a output:\n%s", stdout)
	}
	if strings.Contains(stdout, "run-sf-b") {
		t.Fatalf("run-sf-b present in --subject run-sf-a output (should be filtered):\n%s", stdout)
	}

	// Count rows: should be 2 (the run and the retro).
	var dataRows int
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "TIMESTAMP") {
			continue
		}
		dataRows++
	}
	if dataRows != 2 {
		t.Fatalf("expected 2 data rows for --subject run-sf-a, got %d:\n%s", dataRows, stdout)
	}
}

func TestLogSubjectFilterUnmatched(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	captureRunForLog(t, repo, "run-sm")

	stdout, stderr, err := execute("log", "--subject", "no-such-id")
	if err != nil {
		t.Fatalf("log --subject no-such-id returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
	if !strings.Contains(stdout, "no events found") {
		t.Fatalf("expected 'no events found' for unmatched --subject, got: %q", stdout)
	}
}

// A retro must match --subject only by its subject_run.N/bead.N values, NOT by
// its own retro id. Passing the retro's own id as --subject yields no match.
func TestLogSubjectFilterRetroOwnIdNotMatched(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	retroID := captureRetroForLog(t, repo, "run-own")

	stdout, stderr, err := execute("log", "--subject", retroID)
	if err != nil {
		t.Fatalf("log --subject <retro-id> returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
	if !strings.Contains(stdout, "no events found") {
		t.Fatalf("a retro must not match its own id as --subject; got:\n%s", stdout)
	}
	// Sanity: the retro IS matched by its actual subject.
	stdout, _, err = execute("log", "--subject", "run-own")
	if err != nil {
		t.Fatalf("log --subject run-own returned error: %v", err)
	}
	if !strings.Contains(stdout, retroID) {
		t.Fatalf("retro should match its subject run-own; got:\n%s", stdout)
	}
}

func TestLogLimit(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	t1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	seedRunWithTimestamp(t, repo, "lim-early", t1)
	seedRunWithTimestamp(t, repo, "lim-mid", t2)
	seedRunWithTimestamp(t, repo, "lim-late", t3)

	// --limit 1: only the most-recent event.
	stdout, stderr, err := execute("log", "--limit", "1")
	if err != nil {
		t.Fatalf("log --limit 1 returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	var dataRows int
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "TIMESTAMP") {
			continue
		}
		dataRows++
	}
	if dataRows != 1 {
		t.Fatalf("expected 1 data row with --limit 1, got %d:\n%s", dataRows, stdout)
	}
	if !strings.Contains(stdout, "lim-late") {
		t.Fatalf("expected most-recent event 'lim-late' with --limit 1:\n%s", stdout)
	}
}

func TestLogLimitZeroIsUnlimited(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	t1 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	seedRunWithTimestamp(t, repo, "ulim-a", t1)
	seedRunWithTimestamp(t, repo, "ulim-b", t2)

	stdout, stderr, err := execute("log", "--limit", "0")
	if err != nil {
		t.Fatalf("log --limit 0 returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	var dataRows int
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "TIMESTAMP") {
			continue
		}
		dataRows++
	}
	if dataRows != 2 {
		t.Fatalf("expected 2 data rows with --limit 0, got %d:\n%s", dataRows, stdout)
	}
}

func TestLogLimitNegativeRejected(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("log", "--limit", "-1")
	if err == nil {
		t.Fatal("log --limit -1 returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "limit") {
		t.Fatalf("error does not mention 'limit': %q", combined)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout for negative --limit, got: %q", stdout)
	}
}

func TestLogMalformedManifest(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Seed a valid run so the list is non-empty.
	captureRunForLog(t, repo, "good-log-run")

	// Write a run with a corrupt manifest directly via refstore.
	store := refstore.New(repo)
	if _, err := store.WriteCommit(context.Background(), "refs/etude/runs/bad-log-run",
		map[string][]byte{"manifest.json": []byte(`not valid json`)},
		refstore.WriteOptions{}); err != nil {
		t.Fatalf("WriteCommit bad manifest: %v", err)
	}

	stdout, stderr, err := execute("log")
	if err == nil {
		t.Fatal("log with corrupt manifest returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "bad-log-run") {
		t.Fatalf("error does not name the offending run id 'bad-log-run': %q", combined)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout on corrupt-manifest failure, got: %q", stdout)
	}
}

func TestLogReadOnly(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	captureRunForLog(t, repo, "ro-run")
	captureRetroForLog(t, repo, "ro-run")

	// Snapshot refs before.
	store := refstore.New(repo)
	ctx := context.Background()
	runsBefore, err := store.List(ctx, "refs/etude/runs")
	if err != nil {
		t.Fatalf("List runs before: %v", err)
	}
	retrosBefore, err := store.List(ctx, "refs/etude/retros")
	if err != nil {
		t.Fatalf("List retros before: %v", err)
	}

	_, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}

	runsAfter, err := store.List(ctx, "refs/etude/runs")
	if err != nil {
		t.Fatalf("List runs after: %v", err)
	}
	retrosAfter, err := store.List(ctx, "refs/etude/retros")
	if err != nil {
		t.Fatalf("List retros after: %v", err)
	}

	if len(runsBefore) != len(runsAfter) {
		t.Fatalf("run ref count changed: before=%d after=%d", len(runsBefore), len(runsAfter))
	}
	if len(retrosBefore) != len(retrosAfter) {
		t.Fatalf("retro ref count changed: before=%d after=%d", len(retrosBefore), len(retrosAfter))
	}
}

func TestLogTimestampIsRFC3339UTC(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	captureRunForLog(t, repo, "ts-run")

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "TIMESTAMP") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		ts := fields[0]
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Fatalf("timestamp %q is not valid RFC3339: %v", ts, err)
		}
		if !strings.HasSuffix(ts, "Z") {
			t.Fatalf("timestamp %q is not UTC (does not end with Z)", ts)
		}
	}
}

func TestLogRunSummaryIncludesStageCount(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	captureRunForLog(t, repo, "summary-run")

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// The summary for a single-stage run should contain "(1 stages)".
	if !strings.Contains(stdout, "(1 stages)") {
		t.Fatalf("expected '(1 stages)' in run summary:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// OccurredAt / EVENT column tests (etude-8hq.2)
// ---------------------------------------------------------------------------

// seedRetroWithOccurredAt writes a retro manifest with both Created and OccurredAt set.
func seedRetroWithOccurredAt(t *testing.T, repo, retroID, subjectRunID string, created, occurred time.Time) {
	t.Helper()
	store := refstore.New(repo)
	ctx := context.Background()

	aStore := artifactstore.New()
	artifact, err := aStore.AddContent("retro", "text/markdown; charset=utf-8", []byte("# retro "+retroID))
	if err != nil {
		t.Fatalf("AddContent retro %q: %v", retroID, err)
	}
	outputRef := runmanifest.ArtifactFromManifestArtifact(artifact)
	files := aStore.Files()

	m := runmanifest.Manifest{
		RunID:           retroID,
		Workflow:        "retro",
		WorkflowVersion: "retro-v1",
		Created:         created,
		OccurredAt:      occurred,
		Refs: map[string]string{
			"scope":         "run",
			"trigger":       "manual",
			"subject_run.1": subjectRunID,
		},
		Stages: []runmanifest.Stage{
			{
				Name:       "retro",
				ProducedBy: "retro",
				GitSHA:     "0000000000000000000000000000000000000000",
				Skill: runmanifest.Skill{
					ID:      "retro",
					Repo:    "manual",
					Version: "manual",
				},
				Inputs:    []runmanifest.ArtifactRef{},
				Output:    outputRef,
				Timestamp: created,
			},
		},
	}
	if _, err := runmanifest.WriteManifestTree(ctx, store, retrosPrefix, m, files, refstore.WriteOptions{}); err != nil {
		t.Fatalf("WriteManifestTree retro %q: %v", retroID, err)
	}
}

// TestLogHeaderIncludesEventColumn verifies the new EVENT column is in the header.
func TestLogHeaderIncludesEventColumn(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	captureRunForLog(t, repo, "hdr-run")

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
	if !strings.Contains(stdout, "EVENT") {
		t.Fatalf("header missing EVENT column:\n%s", stdout)
	}
}

// TestLogEventColumnDashForNoOccurredAt verifies that rows without occurred_at
// show EVENT="-" and sorts by capture time (degrade guard / golden column test).
func TestLogEventColumnDashForNoOccurredAt(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	ts := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	seedRunWithTimestamp(t, repo, "ev-run-a", ts)
	seedRetroWithTimestamp(t, repo, "ev-retro-b", "ev-run-a", ts.Add(time.Hour))

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// Every data row (non-header) must have EVENT="-" since nothing has occurred_at.
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "TIMESTAMP") {
			continue
		}
		fields := strings.Fields(line)
		// Header order: TIMESTAMP KIND ID EVENT SUMMARY
		// EVENT is index 3 (0-based).
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "-" {
			t.Fatalf("expected EVENT='-' for row without occurred_at, got %q in line: %s", fields[3], line)
		}
	}
}

// TestLogEventTimeSortBackfilledRetro verifies that a retro with occurred_at
// set to a time BEFORE two runs sorts BEFORE those runs even though its capture
// time (Created) is AFTER them. The TIMESTAMP column must still show capture
// time and the EVENT column must show occurred time.
func TestLogEventTimeSortBackfilledRetro(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Two runs captured at t1 and t2.
	t1 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	seedRunWithTimestamp(t, repo, "bk-run-a", t1)
	seedRunWithTimestamp(t, repo, "bk-run-b", t2)

	// Retro captured AFTER both runs (t3) but with occurred_at BEFORE t1.
	t3 := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	occurred := time.Date(2026, 3, 25, 9, 0, 0, 0, time.UTC) // before both runs
	seedRetroWithOccurredAt(t, repo, "bk-retro-z", "bk-run-a", t3, occurred)

	stdout, stderr, err := execute("log")
	if err != nil {
		t.Fatalf("log returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// The retro should appear BEFORE both runs (sorted by effective time = occurred).
	idxRetro := strings.Index(stdout, "bk-retro-z")
	idxRunA := strings.Index(stdout, "bk-run-a")
	idxRunB := strings.Index(stdout, "bk-run-b")
	if idxRetro < 0 || idxRunA < 0 || idxRunB < 0 {
		t.Fatalf("missing ids in output:\n%s", stdout)
	}
	if !(idxRetro < idxRunA && idxRetro < idxRunB) {
		t.Fatalf("backfilled retro should sort before both runs by event time:\n%s", stdout)
	}

	// Find the retro row and verify TIMESTAMP shows capture time (t3) and EVENT shows occurred.
	captureStr := t3.UTC().Format(time.RFC3339)
	occurredStr := occurred.UTC().Format(time.RFC3339)
	var retroRow string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "bk-retro-z") {
			retroRow = line
			break
		}
	}
	if retroRow == "" {
		t.Fatalf("could not find retro row in output:\n%s", stdout)
	}
	if !strings.Contains(retroRow, captureStr) {
		t.Fatalf("TIMESTAMP column must show capture time %q:\n%s", captureStr, retroRow)
	}
	if !strings.Contains(retroRow, occurredStr) {
		t.Fatalf("EVENT column must show occurred time %q:\n%s", occurredStr, retroRow)
	}
}
