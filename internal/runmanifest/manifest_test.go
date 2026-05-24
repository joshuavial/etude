package runmanifest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
)

func TestManifestJSONIsDeterministicAndExact(t *testing.T) {
	store := artifactstore.New()
	input, err := store.AddContent("input", "text/plain", []byte("input <>&"))
	if err != nil {
		t.Fatalf("AddContent input returned error: %v", err)
	}
	output, err := store.AddContent("output", "text/markdown", []byte("# output\n"))
	if err != nil {
		t.Fatalf("AddContent output returned error: %v", err)
	}
	manifest := Manifest{
		RunID:           "20260522-run.1",
		Workflow:        "review",
		WorkflowVersion: "workflow-sha",
		Created:         time.Date(2026, 5, 22, 1, 2, 3, 4, time.UTC),
		Refs: map[string]string{
			"pr":     "469",
			"bead":   "etude-run-manifest",
			"branch": "feature/<>&",
		},
		Stages: []Stage{{
			Name:       "plan",
			ProducedBy: "original",
			GitSHA:     "abc123",
			Skill: Skill{
				ID:      "dev-workflow",
				Repo:    "github.com/example/skills",
				Version: "v1.2.3",
			},
			Inputs:    []ArtifactRef{ArtifactFromManifestArtifact(input)},
			Output:    ArtifactFromManifestArtifact(output),
			Timestamp: time.Date(2026, 5, 22, 1, 3, 4, 5, time.UTC),
		}},
	}

	got, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	want := fmt.Sprintf(`{
  "run_id": "20260522-run.1",
  "workflow": "review",
  "workflow_version": "workflow-sha",
  "created": "2026-05-22T01:02:03.000000004Z",
  "refs": {
    "bead": "etude-run-manifest",
    "branch": "feature/<>&",
    "pr": "469"
  },
  "stages": [
    {
      "stage": "plan",
      "produced_by": "original",
      "git_sha": "abc123",
      "skill": {
        "id": "dev-workflow",
        "repo": "github.com/example/skills",
        "version": "v1.2.3"
      },
      "inputs": [
        {
          "role": "input",
          "artifact": "%s",
          "path": "%s",
          "media_type": "text/plain",
          "storage": "content",
          "size": 9
        }
      ],
      "output": {
        "role": "output",
        "artifact": "%s",
        "path": "%s",
        "media_type": "text/markdown",
        "storage": "content",
        "size": 9
      },
      "timestamp": "2026-05-22T01:03:04.000000005Z"
    }
  ]
}
`, input.SHA256, input.Path, output.SHA256, output.Path)
	if string(got) != want {
		t.Fatalf("JSON mismatch\n got:\n%s\nwant:\n%s", got, want)
	}

	second, err := manifest.JSON()
	if err != nil {
		t.Fatalf("second JSON returned error: %v", err)
	}
	if string(second) != string(got) {
		t.Fatalf("JSON bytes changed between calls\nfirst:\n%s\nsecond:\n%s", got, second)
	}
}

func TestJSONCanonicalizesEmptyRefsAndInputs(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	manifest := validManifest(output)
	manifest.Refs = nil
	manifest.Stages[0].Inputs = nil

	got, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	text := string(got)
	if !strings.Contains(text, "  \"refs\": {},\n") {
		t.Fatalf("refs did not serialize as empty object:\n%s", text)
	}
	if !strings.Contains(text, "      \"inputs\": [],\n") {
		t.Fatalf("inputs did not serialize as empty array:\n%s", text)
	}
}

func TestJSONNormalizesTimesToUTC(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	manifest := validManifest(output)
	offset := time.FixedZone("test", 10*60*60)
	manifest.Created = time.Date(2026, 5, 22, 11, 0, 0, 0, offset)
	manifest.Stages[0].Timestamp = time.Date(2026, 5, 22, 12, 0, 0, 0, offset)

	got, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		`"created": "2026-05-22T01:00:00Z"`,
		`"timestamp": "2026-05-22T02:00:00Z"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("JSON missing %s:\n%s", want, text)
		}
	}
}

func TestParseJSONRoundTripsManifestAndArtifactPaths(t *testing.T) {
	store := artifactstore.New()
	input, err := store.AddContent("input", "text/plain", []byte("input"))
	if err != nil {
		t.Fatalf("AddContent input returned error: %v", err)
	}
	output, err := store.AddContent("output", "text/markdown", []byte("output"))
	if err != nil {
		t.Fatalf("AddContent output returned error: %v", err)
	}
	manifest := validManifest(ArtifactFromManifestArtifact(output))
	manifest.Stages[0].Inputs = []ArtifactRef{ArtifactFromManifestArtifact(input)}
	content, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}

	got, err := ParseJSON(content)
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	if got.RunID != manifest.RunID || got.Workflow != manifest.Workflow || len(got.Stages) != 1 {
		t.Fatalf("parsed manifest = %#v", got)
	}
	if !got.Created.Equal(manifest.Created) || !got.Stages[0].Timestamp.Equal(manifest.Stages[0].Timestamp) {
		t.Fatalf("timestamps changed: %#v", got)
	}
	paths := ArtifactPaths(got)
	wantPaths := []string{input.Path, output.Path}
	if strings.Join(paths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("ArtifactPaths = %#v, want %#v", paths, wantPaths)
	}
}

func TestParseJSONRejectsMalformedUnknownAndInvalidManifest(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	manifest := validManifest(output)
	content, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}

	cases := map[string]string{
		"malformed":      `{`,
		"top unknown":    strings.Replace(string(content), `"run_id"`, `"extra": true, "run_id"`, 1),
		"stage unknown":  strings.Replace(string(content), `"stage"`, `"extra": true, "stage"`, 1),
		"skill unknown":  strings.Replace(string(content), `"id"`, `"extra": true, "id"`, 1),
		"artifact extra": strings.Replace(string(content), `"role"`, `"extra": true, "role"`, 1),
		"object refs":    strings.Replace(string(content), `"refs": {`, `"refs": {"pr": {"number": 1},`, 1),
		"invalid value":  strings.Replace(string(content), `"run_id": "run-1"`, `"run_id": ".bad"`, 1),
		"trailing data":  string(content) + `{}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseJSON([]byte(payload)); err == nil {
				t.Fatal("ParseJSON returned nil error")
			}
		})
	}
}

func TestValidateRejectsInvalidManifests(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	cases := []struct {
		name string
		edit func(*Manifest)
	}{
		{"missing run id", func(m *Manifest) { m.RunID = "" }},
		{"bad run id leading dot", func(m *Manifest) { m.RunID = ".run" }},
		{"bad run id lock", func(m *Manifest) { m.RunID = "run.lock" }},
		{"bad run id dots", func(m *Manifest) { m.RunID = "run..1" }},
		{"missing workflow", func(m *Manifest) { m.Workflow = "" }},
		{"missing workflow version", func(m *Manifest) { m.WorkflowVersion = "" }},
		{"missing created", func(m *Manifest) { m.Created = time.Time{} }},
		{"empty ref value", func(m *Manifest) { m.Refs["pr"] = " " }},
		{"missing stages", func(m *Manifest) { m.Stages = nil }},
		{"missing stage name", func(m *Manifest) { m.Stages[0].Name = "" }},
		{"missing produced by", func(m *Manifest) { m.Stages[0].ProducedBy = "" }},
		{"missing git sha", func(m *Manifest) { m.Stages[0].GitSHA = "" }},
		{"missing skill id", func(m *Manifest) { m.Stages[0].Skill.ID = "" }},
		{"missing skill repo", func(m *Manifest) { m.Stages[0].Skill.Repo = "" }},
		{"missing skill version", func(m *Manifest) { m.Stages[0].Skill.Version = "" }},
		{"missing timestamp", func(m *Manifest) { m.Stages[0].Timestamp = time.Time{} }},
		{"bad output role", func(m *Manifest) { m.Stages[0].Output.Role = "bad role" }},
		{"bad artifact hash", func(m *Manifest) { m.Stages[0].Output.Artifact = strings.Repeat("A", 64) }},
		{"bad artifact path", func(m *Manifest) { m.Stages[0].Output.Path = "../artifact" }},
		{"bad media type", func(m *Manifest) { m.Stages[0].Output.MediaType = "text/plain\n" }},
		{"bad storage", func(m *Manifest) { m.Stages[0].Output.Storage = "remote" }},
		{"negative size", func(m *Manifest) { m.Stages[0].Output.Size = -1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validManifest(output)
			tc.edit(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("Validate returned nil error")
			}
		})
	}
}

func TestParseJSONRejectsUnknownStorageValue(t *testing.T) {
	// Build a valid manifest, serialise it, then replace the storage value with
	// an unknown string. ParseJSON must return an error wrapping ErrInvalidManifest
	// (validateStage wraps ErrInvalidManifest with %w; the inner ErrInvalidArtifact
	// is included via %v so errors.Is reaches ErrInvalidManifest but not the inner
	// sentinel — this locks that named-string-type fields do NOT loosen decode-time
	// validation: the typed field accepts any string during unmarshal, but Validate
	// rejects it).
	output := contentArtifact("output", "text/plain", []byte("out"))
	manifest := validManifest(output)
	content, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	payload := strings.Replace(string(content), `"storage": "content"`, `"storage": "remote"`, 1)
	_, err = ParseJSON([]byte(payload))
	if err == nil {
		t.Fatal("ParseJSON returned nil error for unknown storage value")
	}
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ParseJSON error = %v, want error wrapping ErrInvalidManifest", err)
	}
}

func TestValidateAllowsZeroInputStage(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	manifest := validManifest(output)
	manifest.Stages[0].Inputs = nil

	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestArtifactFromManifestArtifactPreservesMetadata(t *testing.T) {
	source := artifactstore.ManifestArtifact{
		Role:      "plan",
		MediaType: "text/markdown",
		Storage:   artifactstore.StoragePointer,
		SHA256:    strings.Repeat("a", 64),
		Path:      "artifacts/pointers/sha256/aa/" + strings.Repeat("a", 64) + ".json",
		Size:      123,
	}

	got := ArtifactFromManifestArtifact(source)
	if got != (ArtifactRef{
		Role:      source.Role,
		Artifact:  source.SHA256,
		Path:      source.Path,
		MediaType: source.MediaType,
		Storage:   source.Storage,
		Size:      source.Size,
	}) {
		t.Fatalf("ArtifactFromManifestArtifact = %#v", got)
	}
}

func TestWriterWritesManifestAndArtifacts(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	artifactStore := artifactstore.New()
	input, err := artifactStore.AddContent("input", "text/plain", []byte("input"))
	if err != nil {
		t.Fatalf("AddContent input returned error: %v", err)
	}
	output, err := artifactStore.AddContent("output", "text/plain", []byte("output"))
	if err != nil {
		t.Fatalf("AddContent output returned error: %v", err)
	}
	manifest := validManifest(ArtifactFromManifestArtifact(output))
	manifest.Stages[0].Inputs = []ArtifactRef{ArtifactFromManifestArtifact(input)}

	commit, err := (Writer{Store: refstore.New(repo)}).Write(ctx, manifest, artifactStore.Files(), WriteOptions{Message: "capture run"})
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	resolved, err := refstore.New(repo).Resolve(ctx, "refs/etude/runs/"+manifest.RunID)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != commit {
		t.Fatalf("resolved commit = %q, want %q", resolved, commit)
	}
	gotManifest, err := refstore.New(repo).ReadFile(ctx, "refs/etude/runs/"+manifest.RunID, manifestPath)
	if err != nil {
		t.Fatalf("ReadFile manifest returned error: %v", err)
	}
	wantManifest, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	if string(gotManifest) != string(wantManifest) {
		t.Fatalf("manifest bytes mismatch\n got:\n%s\nwant:\n%s", gotManifest, wantManifest)
	}
	gotOutput, err := refstore.New(repo).ReadFile(ctx, "refs/etude/runs/"+manifest.RunID, output.Path)
	if err != nil {
		t.Fatalf("ReadFile output returned error: %v", err)
	}
	if string(gotOutput) != "output" {
		t.Fatalf("output bytes = %q", gotOutput)
	}
}

func TestWriterAcceptsRealArtifactstorePointer(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	artifactStore := artifactstore.New()
	pointer, err := artifactStore.AddPointer("output", "image/png", artifactstore.Pointer{
		URI:    "s3://bucket/screenshot.png",
		SHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("AddPointer returned error: %v", err)
	}
	manifest := validManifest(ArtifactFromManifestArtifact(pointer))
	manifest.Stages[0].Inputs = nil

	if _, err := (Writer{Store: refstore.New(repo)}).Write(ctx, manifest, artifactStore.Files(), WriteOptions{}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	got, err := refstore.New(repo).ReadFile(ctx, "refs/etude/runs/"+manifest.RunID, pointer.Path)
	if err != nil {
		t.Fatalf("ReadFile pointer returned error: %v", err)
	}
	if string(got) != string(artifactStore.Files()[pointer.Path]) {
		t.Fatalf("pointer bytes = %q, want %q", got, artifactStore.Files()[pointer.Path])
	}
}

func TestWriterAcceptsMultiStageManifestWithMultipleInputsAndReplay(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	artifactStore := artifactstore.New()
	task, err := artifactStore.AddContent("task", "text/plain", []byte("task"))
	if err != nil {
		t.Fatalf("AddContent task returned error: %v", err)
	}
	plan, err := artifactStore.AddContent("plan", "text/markdown", []byte("plan"))
	if err != nil {
		t.Fatalf("AddContent plan returned error: %v", err)
	}
	rubric, err := artifactStore.AddContent("rubric", "text/plain", []byte("rubric"))
	if err != nil {
		t.Fatalf("AddContent rubric returned error: %v", err)
	}
	replay, err := artifactStore.AddContent("replay", "text/markdown", []byte("replay"))
	if err != nil {
		t.Fatalf("AddContent replay returned error: %v", err)
	}
	manifest := validManifest(ArtifactFromManifestArtifact(plan))
	manifest.Stages[0].Inputs = []ArtifactRef{ArtifactFromManifestArtifact(task)}
	manifest.Stages = append(manifest.Stages, Stage{
		Name:       "plan",
		ProducedBy: "replay",
		GitSHA:     "def456",
		Skill: Skill{
			ID:      "dev-workflow",
			Repo:    "github.com/example/skills",
			Version: "v2",
		},
		Inputs: []ArtifactRef{
			ArtifactFromManifestArtifact(plan),
			ArtifactFromManifestArtifact(rubric),
		},
		Output:    ArtifactFromManifestArtifact(replay),
		Timestamp: time.Date(2026, 5, 22, 1, 2, 0, 0, time.UTC),
	})

	if _, err := (Writer{Store: refstore.New(repo)}).Write(ctx, manifest, artifactStore.Files(), WriteOptions{}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	got, err := refstore.New(repo).ReadFile(ctx, "refs/etude/runs/"+manifest.RunID, replay.Path)
	if err != nil {
		t.Fatalf("ReadFile replay returned error: %v", err)
	}
	if string(got) != "replay" {
		t.Fatalf("replay artifact bytes = %q", got)
	}
}

func TestWriterRejectsManifestCollisionMissingAndUnreferencedArtifacts(t *testing.T) {
	ctx := context.Background()
	output := contentArtifact("output", "text/plain", []byte("out"))
	manifest := validManifest(output)

	cases := []struct {
		name  string
		files map[string][]byte
		want  error
	}{
		{"manifest collision", map[string][]byte{manifestPath: []byte("{}"), output.Path: []byte("out")}, ErrManifestCollision},
		{"missing artifact", map[string][]byte{}, ErrMissingArtifact},
		{"unreferenced artifact", map[string][]byte{output.Path: []byte("out"), "artifacts/sha256/aa/" + strings.Repeat("a", 64): []byte("extra")}, ErrUnreferencedArtifact},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (Writer{Store: refstore.New(initGitRepo(t))}).Write(ctx, manifest, tc.files, WriteOptions{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Write error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestWriterRejectsArtifactContractMismatches(t *testing.T) {
	ctx := context.Background()
	output := contentArtifact("output", "text/plain", []byte("out"))
	pointer := pointerArtifact("output", "image/png", []byte(`{"version":1,"uri":"s3://bucket/object"}`))

	cases := []struct {
		name     string
		artifact ArtifactRef
		files    map[string][]byte
	}{
		{"content hash mismatch", output, map[string][]byte{output.Path: []byte("wrong")}},
		{"content path mismatch", func() ArtifactRef {
			a := output
			a.Path = "artifacts/sha256/ff/" + a.Artifact
			return a
		}(), map[string][]byte{"artifacts/sha256/ff/" + output.Artifact: []byte("out")}},
		{"pointer path mismatch", func() ArtifactRef {
			a := pointer
			a.Path = "artifacts/sha256/" + a.Artifact[:2] + "/" + a.Artifact
			return a
		}(), map[string][]byte{"artifacts/sha256/" + pointer.Artifact[:2] + "/" + pointer.Artifact: []byte(`{"version":1,"uri":"s3://bucket/object"}`)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validManifest(tc.artifact)
			_, err := (Writer{Store: refstore.New(initGitRepo(t))}).Write(ctx, manifest, tc.files, WriteOptions{})
			if !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("Write error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

func TestWriterForwardsCAS(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)
	store := refstore.New(repo)
	artifact := contentArtifact("output", "text/plain", []byte("out"))
	files := map[string][]byte{artifact.Path: []byte("out")}
	manifest := validManifest(artifact)

	first, err := (Writer{Store: store}).Write(ctx, manifest, files, WriteOptions{})
	if err != nil {
		t.Fatalf("first Write returned error: %v", err)
	}
	manifest.Created = manifest.Created.Add(time.Second)
	second, err := (Writer{Store: store}).Write(ctx, manifest, files, WriteOptions{ExpectedOld: first})
	if err != nil {
		t.Fatalf("second Write returned error: %v", err)
	}
	if second == first {
		t.Fatal("CAS Write did not create new commit")
	}
	parent := strings.TrimSpace(git(t, repo, "rev-parse", second+"^"))
	if parent != first {
		t.Fatalf("parent = %q, want %q", parent, first)
	}
	_, err = (Writer{Store: store}).Write(ctx, manifest, files, WriteOptions{ExpectedOld: first})
	if !errors.Is(err, refstore.ErrStaleRef) {
		t.Fatalf("stale CAS error = %v, want ErrStaleRef", err)
	}
}

func validManifest(output ArtifactRef) Manifest {
	return Manifest{
		RunID:           "run-1",
		Workflow:        "review",
		WorkflowVersion: "workflow-version",
		Created:         time.Date(2026, 5, 22, 1, 0, 0, 0, time.UTC),
		Refs: map[string]string{
			"bead": "etude-run-manifest",
			"pr":   "469",
		},
		Stages: []Stage{{
			Name:       "plan",
			ProducedBy: "original",
			GitSHA:     "abc123",
			Skill: Skill{
				ID:      "dev-workflow",
				Repo:    "github.com/example/skills",
				Version: "v1",
			},
			Output:    output,
			Timestamp: time.Date(2026, 5, 22, 1, 1, 0, 0, time.UTC),
		}},
	}
}

func contentArtifact(role, mediaType string, content []byte) ArtifactRef {
	sum := sha256Hex(content)
	return ArtifactRef{
		Role:      role,
		Artifact:  sum,
		Path:      "artifacts/sha256/" + sum[:2] + "/" + sum,
		MediaType: mediaType,
		Storage:   artifactstore.StorageContent,
		Size:      int64(len(content)),
	}
}

func pointerArtifact(role, mediaType string, content []byte) ArtifactRef {
	sum := sha256Hex(content)
	return ArtifactRef{
		Role:      role,
		Artifact:  sum,
		Path:      "artifacts/pointers/sha256/" + sum[:2] + "/" + sum + ".json",
		MediaType: mediaType,
		Storage:   artifactstore.StoragePointer,
		Size:      0,
	}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--initial-branch=main")
	git(t, dir, "config", "user.name", "Test User")
	git(t, dir, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(dir+"/README.md", []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	git(t, dir, "add", "README.md")
	git(t, dir, "commit", "-m", "initial")
	return dir
}

func TestValidateFilePathRejectsControlCharsInRunmanifest(t *testing.T) {
	// Build a minimal valid artifact ref and mutate its path to contain a
	// control character; Validate must reject it via ErrInvalidArtifact.
	base := contentArtifact("output", "text/plain", []byte("out"))

	controlPaths := []struct {
		name string
		path string
	}{
		{"newline", "artifacts/sha256/ab/bad\npath"},
		{"tab", "artifacts/sha256/ab/bad\tpath"},
		{"carriage return", "artifacts/sha256/ab/bad\rpath"},
		{"SOH", "artifacts/sha256/ab/bad\x01path"},
		{"NUL", "artifacts/sha256/ab/bad\x00path"},
		{"DEL", "artifacts/sha256/ab/bad\x7fpath"},
		{"C1 U+0085", "artifacts/sha256/ab/badpath"},
	}
	for _, tc := range controlPaths {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			a.Path = tc.path
			manifest := validManifest(a)
			if err := manifest.Validate(); err == nil {
				t.Fatalf("Validate(%q) = nil, want error", tc.path)
			}
		})
	}

	// Valid ASCII paths must still pass validateFilePath.
	validPaths := []string{
		"manifest.json",
		"artifacts/sha256/ab/cd",
		base.Path,
	}
	for _, p := range validPaths {
		if err := validateFilePath(p); err != nil {
			t.Fatalf("validateFilePath(%q) = %v, want nil", p, err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
