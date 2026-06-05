package nudge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// initRepo creates a minimal git repo in a temp dir and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.name", "Test")
	gitRun(t, dir, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "seed")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\nstderr: %s", args, err, stderr.String())
	}
}

// seedRun writes a synthetic run with Created = ts under refs/etude/runs/<id>.
func seedRun(t *testing.T, repo, id string, ts time.Time) {
	t.Helper()
	store := refstore.New(repo)
	a := artifactstore.New()
	art, err := a.AddContent("output", "text/markdown; charset=utf-8", []byte("# "+id))
	if err != nil {
		t.Fatalf("AddContent: %v", err)
	}
	m := runmanifest.Manifest{
		RunID:           id,
		Workflow:        "manual",
		WorkflowVersion: "manual-v1",
		Created:         ts,
		Refs:            map[string]string{},
		Stages: []runmanifest.Stage{{
			Name:       "plan",
			ProducedBy: "original",
			GitSHA:     "0000000000000000000000000000000000000000",
			Skill:      runmanifest.Skill{ID: "plan", Repo: "manual", Version: "manual"},
			Inputs:     []runmanifest.ArtifactRef{},
			Output:     runmanifest.ArtifactFromManifestArtifact(art),
			Timestamp:  ts,
		}},
	}
	if _, err := runmanifest.WriteManifestTree(context.Background(), store, runsPrefix, m, a.Files(), refstore.WriteOptions{}); err != nil {
		t.Fatalf("write run %q: %v", id, err)
	}
}

// seedRetro writes a synthetic retro with Created = ts under refs/etude/retros/<id>.
func seedRetro(t *testing.T, repo, id, subject string, ts time.Time) {
	t.Helper()
	store := refstore.New(repo)
	a := artifactstore.New()
	art, err := a.AddContent("retro", "text/markdown; charset=utf-8", []byte("# retro "+id))
	if err != nil {
		t.Fatalf("AddContent: %v", err)
	}
	m := runmanifest.Manifest{
		RunID:           id,
		Workflow:        "retro",
		WorkflowVersion: "retro-v1",
		Created:         ts,
		Refs: map[string]string{
			"scope":         "run",
			"trigger":       "manual",
			"subject_run.1": subject,
		},
		Stages: []runmanifest.Stage{{
			Name:       "retro",
			ProducedBy: "retro",
			GitSHA:     "0000000000000000000000000000000000000000",
			Skill:      runmanifest.Skill{ID: "retro", Repo: "manual", Version: "manual"},
			Inputs:     []runmanifest.ArtifactRef{},
			Output:     runmanifest.ArtifactFromManifestArtifact(art),
			Timestamp:  ts,
		}},
	}
	if _, err := runmanifest.WriteManifestTree(context.Background(), store, retrosPrefix, m, a.Files(), refstore.WriteOptions{}); err != nil {
		t.Fatalf("write retro %q: %v", id, err)
	}
}

func TestCountRunsSinceLastRetroEmptyRepo(t *testing.T) {
	repo := initRepo(t)
	count, lastID, err := CountRunsSinceLastRetro(context.Background(), refstore.New(repo))
	if err != nil {
		t.Fatalf("CountRunsSinceLastRetro empty repo: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if lastID != "" {
		t.Fatalf("lastRetroID = %q, want empty", lastID)
	}
}

func TestCountRunsSinceLastRetroNoRetros(t *testing.T) {
	repo := initRepo(t)
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedRun(t, repo, "r1", base)
	seedRun(t, repo, "r2", base.Add(time.Hour))
	seedRun(t, repo, "r3", base.Add(2*time.Hour))

	count, lastID, err := CountRunsSinceLastRetro(context.Background(), refstore.New(repo))
	if err != nil {
		t.Fatalf("CountRunsSinceLastRetro: %v", err)
	}
	// With zero retros every run counts.
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	if lastID != "" {
		t.Fatalf("lastRetroID = %q, want empty", lastID)
	}
}

func TestCountRunsSinceLastRetroOneRetroMidway(t *testing.T) {
	repo := initRepo(t)
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedRun(t, repo, "r1", base)
	seedRun(t, repo, "r2", base.Add(time.Hour))
	// Retro at T+90m
	seedRetro(t, repo, "retro-run-r2-20260501T112000Z", "r2", base.Add(90*time.Minute))
	seedRun(t, repo, "r3", base.Add(2*time.Hour))
	seedRun(t, repo, "r4", base.Add(3*time.Hour))

	count, lastID, err := CountRunsSinceLastRetro(context.Background(), refstore.New(repo))
	if err != nil {
		t.Fatalf("CountRunsSinceLastRetro: %v", err)
	}
	// r3 and r4 are after the retro.
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if lastID != "retro-run-r2-20260501T112000Z" {
		t.Fatalf("lastRetroID = %q", lastID)
	}
}

func TestCountRunsSinceLastRetroMultipleRetros(t *testing.T) {
	repo := initRepo(t)
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedRun(t, repo, "r1", base)
	seedRetro(t, repo, "retro-run-r1-20260501T103000Z", "r1", base.Add(30*time.Minute))
	seedRun(t, repo, "r2", base.Add(time.Hour))
	seedRetro(t, repo, "retro-run-r2-20260501T120000Z", "r2", base.Add(2*time.Hour))
	seedRun(t, repo, "r3", base.Add(3*time.Hour))

	count, lastID, err := CountRunsSinceLastRetro(context.Background(), refstore.New(repo))
	if err != nil {
		t.Fatalf("CountRunsSinceLastRetro: %v", err)
	}
	// Only r3 is after the LATEST retro (the second one).
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if lastID != "retro-run-r2-20260501T120000Z" {
		t.Fatalf("lastRetroID = %q", lastID)
	}
}

func TestSnoozeRoundTrip(t *testing.T) {
	repo := initRepo(t)
	got, present, err := ReadSnooze(repo)
	if err != nil {
		t.Fatalf("ReadSnooze empty: %v", err)
	}
	if present {
		t.Fatal("snooze unexpectedly present in fresh repo")
	}
	if got != (Snooze{}) {
		t.Fatalf("expected zero Snooze, got %+v", got)
	}

	want := Snooze{
		RunsAtSnooze:        4,
		SnoozeFor:           2,
		SnoozedAt:           time.Date(2026, 5, 30, 8, 15, 0, 0, time.UTC),
		LastRetroIDAtSnooze: "retro-run-x-20260530T080000Z",
	}
	if err := WriteSnooze(repo, want); err != nil {
		t.Fatalf("WriteSnooze: %v", err)
	}
	// File path is under .git/etude/.
	if _, err := os.Stat(SnoozePath(repo)); err != nil {
		t.Fatalf("snooze path %s: %v", SnoozePath(repo), err)
	}
	got, present, err = ReadSnooze(repo)
	if err != nil {
		t.Fatalf("ReadSnooze after write: %v", err)
	}
	if !present {
		t.Fatal("snooze missing after write")
	}
	if got.RunsAtSnooze != want.RunsAtSnooze ||
		got.SnoozeFor != want.SnoozeFor ||
		!got.SnoozedAt.Equal(want.SnoozedAt) ||
		got.LastRetroIDAtSnooze != want.LastRetroIDAtSnooze {
		t.Fatalf("round-trip differs:\n got %+v\nwant %+v", got, want)
	}
}

func TestSnoozeParseError(t *testing.T) {
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Dir(SnoozePath(repo)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(SnoozePath(repo), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, present, err := ReadSnooze(repo)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if present {
		t.Fatal("present should be false on parse error")
	}
}

func TestDecideDisabled(t *testing.T) {
	st := Decide(false, 3, 5, "retro-x", Snooze{}, false)
	if st.Overdue || st.WouldEmit {
		t.Fatalf("disabled should never be Overdue/WouldEmit: %+v", st)
	}
	if st.Enabled {
		t.Fatal("Enabled should reflect input")
	}
}

func TestDecideBelowThreshold(t *testing.T) {
	st := Decide(true, 3, 2, "retro-x", Snooze{}, false)
	if st.Overdue || st.WouldEmit {
		t.Fatalf("below threshold should not emit: %+v", st)
	}
	if !st.Enabled {
		t.Fatal("Enabled lost")
	}
}

func TestDecideAtThresholdEmits(t *testing.T) {
	st := Decide(true, 3, 3, "retro-x", Snooze{}, false)
	if !st.Overdue {
		t.Fatal("at threshold should be Overdue")
	}
	if !st.WouldEmit {
		t.Fatal("at threshold should WouldEmit")
	}
}

func TestDecideSnoozedSuppresses(t *testing.T) {
	sn := Snooze{
		RunsAtSnooze:        3,
		SnoozeFor:           2,
		SnoozedAt:           time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC),
		LastRetroIDAtSnooze: "retro-x",
	}
	// runsSinceLastRetro=4, snoozed 3..4 (inclusive of 3, exclusive of 5)
	st := Decide(true, 3, 4, "retro-x", sn, true)
	if !st.Overdue {
		t.Fatal("still Overdue while snoozed")
	}
	if st.WouldEmit {
		t.Fatalf("snoozed should not emit: %+v", st)
	}
	if st.SnoozedUntilRuns != 5 {
		t.Fatalf("SnoozedUntilRuns = %d, want 5", st.SnoozedUntilRuns)
	}
}

func TestDecideSnoozeExpired(t *testing.T) {
	sn := Snooze{
		RunsAtSnooze:        3,
		SnoozeFor:           1,
		SnoozedAt:           time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC),
		LastRetroIDAtSnooze: "retro-x",
	}
	// runsSinceLastRetro=4, snooze covers (3..4) i.e. only when count < 4
	st := Decide(true, 3, 4, "retro-x", sn, true)
	if !st.WouldEmit {
		t.Fatalf("expired snooze should emit: %+v", st)
	}
	if st.SnoozedUntilRuns != 0 {
		t.Fatalf("expired snooze should not set SnoozedUntilRuns: %d", st.SnoozedUntilRuns)
	}
}

func TestDecideSnoozeInvalidatedByNewRetro(t *testing.T) {
	// Snooze was recorded against retro-x; current retro is retro-y -> ignore.
	sn := Snooze{
		RunsAtSnooze:        0,
		SnoozeFor:           100,
		LastRetroIDAtSnooze: "retro-x",
	}
	st := Decide(true, 3, 3, "retro-y", sn, true)
	if !st.WouldEmit {
		t.Fatalf("stale snooze (different retro) should not suppress: %+v", st)
	}
	if st.SnoozedUntilRuns != 0 {
		t.Fatal("stale snooze should produce SnoozedUntilRuns=0")
	}
}

func TestStatusJSONHasExactlyContractKeys(t *testing.T) {
	// Marshal a Status and confirm the exact set of JSON keys promised by the
	// acceptance criteria: enabled, threshold, runs_since_last_retro,
	// last_retro_id, overdue, snoozed_until_runs, would_emit.  snoozed_at is
	// allowed when present (omitempty) but must NOT appear when zero.
	st := Decide(true, 3, 5, "retro-x", Snooze{}, false)
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal Status: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal Status: %v", err)
	}
	required := []string{
		"enabled", "threshold", "runs_since_last_retro", "last_retro_id",
		"overdue", "snoozed_until_runs", "would_emit",
	}
	for _, k := range required {
		if _, ok := got[k]; !ok {
			t.Fatalf("Status JSON missing key %q; full payload: %s", k, raw)
		}
	}
	if _, ok := got["snoozed_at"]; ok {
		t.Fatalf("snoozed_at should be omitted when zero; got: %s", raw)
	}
	// Reject unexpected keys (defensive — any extra key would be a contract drift).
	known := map[string]bool{}
	for _, k := range required {
		known[k] = true
	}
	known["snoozed_at"] = true
	for k := range got {
		if !known[k] {
			t.Fatalf("Status JSON has unexpected key %q: %s", k, raw)
		}
	}
}

func TestStatusJSONIncludesSnoozedAtWhenActive(t *testing.T) {
	sn := Snooze{
		RunsAtSnooze:        3,
		SnoozeFor:           2,
		SnoozedAt:           time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC),
		LastRetroIDAtSnooze: "retro-x",
	}
	st := Decide(true, 3, 4, "retro-x", sn, true)
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"snoozed_at"`) {
		t.Fatalf("expected snoozed_at in JSON when snooze active: %s", raw)
	}
}

func TestNudgeLineMatchesContract(t *testing.T) {
	line := NudgeLine(5, 3)
	wantPrefix := "etude: retro nudge: 5 bead(s) since last retro (threshold 3); "
	if !strings.HasPrefix(line, wantPrefix) {
		t.Fatalf("nudge line missing expected prefix:\n got: %s\nwant prefix: %s", line, wantPrefix)
	}
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("nudge line should end with newline; got %q", line)
	}
}
