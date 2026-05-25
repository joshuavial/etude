package retro

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// ---- helpers ----

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	unsetGitEnv(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitCmd(t, dir, "init")
	return dir
}

func unsetGitEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	} {
		old, had := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		capturedOld := old
		capturedHad := had
		capturedKey := key
		t.Cleanup(func() {
			if capturedHad {
				os.Setenv(capturedKey, capturedOld)
			} else {
				os.Unsetenv(capturedKey)
			}
		})
	}
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=",
		"GIT_AUTHOR_EMAIL=",
		"GIT_AUTHOR_DATE=",
		"GIT_COMMITTER_NAME=",
		"GIT_COMMITTER_EMAIL=",
		"GIT_COMMITTER_DATE=",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

const fakeGitSHA = "aabbccddeeff0011223344556677889900aabbcc"

// buildValidManifest creates a minimal valid retro manifest with a single body artifact.
func buildValidManifest(t *testing.T, retroID string, bodyContent []byte) (runmanifest.Manifest, map[string][]byte) {
	t.Helper()

	store := artifactstore.New()
	artifact, err := store.AddContent("retro", "text/markdown; charset=utf-8", bodyContent)
	if err != nil {
		t.Fatalf("AddContent: %v", err)
	}
	ref := runmanifest.ArtifactFromManifestArtifact(artifact)

	manifest := runmanifest.Manifest{
		RunID:           retroID,
		Workflow:        retroWorkflow,
		WorkflowVersion: retroWorkflowVersion,
		Created:         time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
		Refs:            map[string]string{"scope": "cohort"},
		Stages: []runmanifest.Stage{
			{
				Name:       "retro",
				ProducedBy: "retro",
				GitSHA:     fakeGitSHA,
				Skill: runmanifest.Skill{
					ID:      "retro",
					Repo:    "manual",
					Version: "manual",
				},
				Producer: runmanifest.Producer{
					Skill: runmanifest.Skill{
						ID:      "retro",
						Repo:    "manual",
						Version: "manual",
					},
				},
				Inputs:    []runmanifest.ArtifactRef{},
				Output:    ref,
				Timestamp: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
			},
		},
	}
	files := store.Files()
	return manifest, files
}

// ---- RetroIDBase tests ----

func TestRetroIDBaseFormat(t *testing.T) {
	t.Run("compact UTC no colons", func(t *testing.T) {
		ts := time.Date(2026, 5, 26, 15, 4, 5, 0, time.UTC)
		got := RetroIDBase("cohort", "run-abc", ts)
		want := "retro-cohort-run-abc-20260526T150405Z"
		if got != want {
			t.Errorf("RetroIDBase = %q, want %q", got, want)
		}
	})

	t.Run("colon free", func(t *testing.T) {
		ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		got := RetroIDBase("phase", "bead-1", ts)
		if strings.Contains(got, ":") {
			t.Errorf("RetroIDBase contains colon: %q", got)
		}
	})

	t.Run("uses UTC not local", func(t *testing.T) {
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Skip("timezone not available")
		}
		// 20:00 Eastern = 00:00 UTC next day in May (EDT = UTC-4)
		ts := time.Date(2026, 5, 26, 20, 0, 0, 0, loc)
		got := RetroIDBase("run", "r1", ts)
		if !strings.HasSuffix(got, "20260527T000000Z") {
			t.Errorf("RetroIDBase did not convert to UTC: %q", got)
		}
	})
}

func TestRetroIDBaseIsValidID(t *testing.T) {
	ts := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	id := RetroIDBase("cohort", "r1", ts)
	if !IsValidRetroID(id) {
		t.Errorf("RetroIDBase %q is not a valid retro id", id)
	}
}

// ---- AllocateRetroId tests ----

func TestAllocateRetroIdReturnsBaseWhenFree(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)

	base := RetroIDBase("cohort", "r1", time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	id, err := AllocateRetroId(ctx, store, base)
	if err != nil {
		t.Fatalf("AllocateRetroId returned error: %v", err)
	}
	if id != base {
		t.Errorf("AllocateRetroId = %q, want %q", id, base)
	}
}

func TestAllocateRetroIdReturnsBase2OnCollision(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)

	base := RetroIDBase("cohort", "r1", time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))

	// Occupy the base ref.
	manifest, files := buildValidManifest(t, base, []byte("# retro body"))
	if _, err := (Writer{Store: store}).Write(ctx, manifest, files, WriteOptions{}); err != nil {
		t.Fatalf("Write (setup): %v", err)
	}

	id, err := AllocateRetroId(ctx, store, base)
	if err != nil {
		t.Fatalf("AllocateRetroId returned error: %v", err)
	}
	want := base + "-2"
	if id != want {
		t.Errorf("AllocateRetroId = %q, want %q", id, want)
	}
}

func TestAllocateRetroIdErrorsAfter10Attempts(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)

	base := RetroIDBase("cohort", "r1", time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC))
	bodyContent := []byte("# retro")

	// Occupy base and base-2 through base-10 (11 slots total).
	candidates := []string{base}
	for n := 2; n <= 10; n++ {
		candidates = append(candidates, fmt.Sprintf("%s-%d", base, n))
	}
	for _, candidate := range candidates {
		m, f := buildValidManifest(t, candidate, bodyContent)
		if _, err := (Writer{Store: store}).Write(ctx, m, f, WriteOptions{}); err != nil {
			t.Fatalf("Write %q: %v", candidate, err)
		}
	}

	_, err := AllocateRetroId(ctx, store, base)
	if err == nil {
		t.Fatal("AllocateRetroId should have returned an error after 10 attempts")
	}
	if !strings.Contains(err.Error(), "could not allocate unique retro id") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---- Writer tests ----

func TestWriterRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)

	retroID := "retro-cohort-r1-20260526T100000Z"
	bodyContent := []byte("# Retro\nThis is the body.\n")
	manifest, files := buildValidManifest(t, retroID, bodyContent)

	commit, err := (Writer{Store: store}).Write(ctx, manifest, files, WriteOptions{Message: "test retro"})
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if commit == "" {
		t.Fatal("Write returned empty commit")
	}

	ref := retrosPrefix + retroID

	// Verify ref resolves to the commit.
	resolved, err := store.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != commit {
		t.Errorf("Resolve = %q, want %q", resolved, commit)
	}

	// Read back manifest.json and verify it parses cleanly.
	manifestBytes, err := store.ReadFile(ctx, ref, "manifest.json")
	if err != nil {
		t.Fatalf("ReadFile manifest.json: %v", err)
	}
	parsed, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if parsed.RunID != retroID {
		t.Errorf("parsed.RunID = %q, want %q", parsed.RunID, retroID)
	}
	if parsed.Workflow != retroWorkflow {
		t.Errorf("parsed.Workflow = %q, want %q", parsed.Workflow, retroWorkflow)
	}
	if len(parsed.Stages) != 1 || parsed.Stages[0].Name != "retro" {
		t.Errorf("parsed.Stages = %#v", parsed.Stages)
	}

	// Verify body artifact is present and content-stable.
	stage := parsed.Stages[0]
	bodyBytes, err := store.ReadFile(ctx, ref, stage.Output.Path)
	if err != nil {
		t.Fatalf("ReadFile body artifact: %v", err)
	}
	if !bytes.Equal(bodyBytes, bodyContent) {
		t.Errorf("body artifact mismatch: got %q, want %q", bodyBytes, bodyContent)
	}
}

func TestWriterIsCreateOnly(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)

	retroID := "retro-cohort-r1-20260526T100000Z"
	bodyContent := []byte("# Retro")
	manifest, files := buildValidManifest(t, retroID, bodyContent)

	_, err := (Writer{Store: store}).Write(ctx, manifest, files, WriteOptions{})
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}

	manifest2, files2 := buildValidManifest(t, retroID, []byte("# Different"))
	_, err = (Writer{Store: store}).Write(ctx, manifest2, files2, WriteOptions{})
	if !errors.Is(err, refstore.ErrRefExists) {
		t.Errorf("second Write = %v, want ErrRefExists", err)
	}
}

func TestWriterRejectsManifestCollision(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)

	retroID := "retro-cohort-r1-20260526T100000Z"
	manifest, files := buildValidManifest(t, retroID, []byte("# retro body"))

	// Inject a manifest.json key into files to trigger the collision guard.
	files["manifest.json"] = []byte(`{"injected":true}`)

	_, err := (Writer{Store: store}).Write(ctx, manifest, files, WriteOptions{})
	if !errors.Is(err, runmanifest.ErrManifestCollision) {
		t.Errorf("Write with files[manifest.json] = %v, want ErrManifestCollision", err)
	}
}

func TestWriterRejectsManifestWithMissingSkill(t *testing.T) {
	badManifest := runmanifest.Manifest{
		RunID:           "retro-cohort-r1-20260526T100000Z",
		Workflow:        retroWorkflow,
		WorkflowVersion: retroWorkflowVersion,
		Created:         time.Now().UTC(),
		Refs:            map[string]string{},
		Stages: []runmanifest.Stage{
			{
				Name:       "retro",
				ProducedBy: "retro",
				GitSHA:     fakeGitSHA,
				// Deliberately missing Skill fields.
				Skill: runmanifest.Skill{
					ID:      "",
					Repo:    "",
					Version: "",
				},
				Producer:  runmanifest.Producer{},
				Inputs:    []runmanifest.ArtifactRef{},
				Output:    runmanifest.ArtifactRef{},
				Timestamp: time.Now().UTC(),
			},
		},
	}

	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)

	_, err := (Writer{Store: store}).Write(ctx, badManifest, map[string][]byte{}, WriteOptions{})
	if err == nil {
		t.Fatal("Write should have returned an error for invalid manifest")
	}
	if !errors.Is(err, runmanifest.ErrInvalidManifest) {
		t.Errorf("error = %v, want ErrInvalidManifest", err)
	}
}
