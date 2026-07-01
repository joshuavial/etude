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
	// Intentional format change: skill block is now nested inside producer;
	// manifest_version:2 is emitted. Top-level skill key is no longer present.
	// This golden is deliberately updated to lock the new v2 shape.
	want := fmt.Sprintf(`{
  "manifest_version": 2,
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
      "producer": {
        "skill": {
          "id": "dev-workflow",
          "repo": "github.com/example/skills",
          "version": "v1.2.3"
        }
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
		ReplayOf: &ReplayLink{
			RunID:  manifest.RunID,
			Stage:  "plan",
			Commit: strings.Repeat("a", 64),
		},
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

// TestWriteManifestTreeParameterization locks the three seams that
// WriteManifestTree exposes to callers:
//
//  1. The ref prefix is honoured: writing with the retros prefix stores the ref
//     under refs/etude/retros/ (not refs/etude/runs/), and the runs ref stays absent.
//
//  2. Create-only (empty ExpectedOld) rejects a second write with ErrRefExists.
//
//  3. CAS append (non-empty ExpectedOld) succeeds with a new commit whose
//     parent is the one supplied — invoked directly on WriteManifestTree so the
//     seam is independently locked from TestWriterForwardsCAS.
func TestWriteManifestTreeParameterization(t *testing.T) {
	ctx := context.Background()
	artifact := contentArtifact("output", "text/plain", []byte("tree-param"))
	files := map[string][]byte{artifact.Path: []byte("tree-param")}
	manifest := validManifest(artifact)

	// --- seam 1 + 2: retros prefix, create-only ---
	t.Run("retros prefix and create-only", func(t *testing.T) {
		repo := initGitRepo(t)
		store := refstore.New(repo)
		const retrosPrefix = "refs/etude/retros/"

		commit, err := WriteManifestTree(ctx, store, retrosPrefix, manifest, files, refstore.WriteOptions{
			Message: "retros prefix write",
		})
		if err != nil {
			t.Fatalf("WriteManifestTree returned error: %v", err)
		}

		// Ref must resolve under retros/, not under runs/.
		resolved, err := store.Resolve(ctx, retrosPrefix+manifest.RunID)
		if err != nil {
			t.Fatalf("Resolve retros ref: %v", err)
		}
		if resolved != commit {
			t.Fatalf("resolved commit = %q, want %q", resolved, commit)
		}

		// The runs prefix must NOT exist (prefix isolation).
		_, err = store.Resolve(ctx, "refs/etude/runs/"+manifest.RunID)
		if !errors.Is(err, refstore.ErrNotFound) {
			t.Fatalf("runs ref exists unexpectedly (err=%v); prefix isolation broken", err)
		}

		// Second write to same ref with no ExpectedOld must yield ErrRefExists.
		_, err = WriteManifestTree(ctx, store, retrosPrefix, manifest, files, refstore.WriteOptions{
			Message: "duplicate write",
		})
		if !errors.Is(err, refstore.ErrRefExists) {
			t.Fatalf("second create-only write error = %v, want ErrRefExists", err)
		}
	})

	// --- seam 3: CAS append via ExpectedOld ---
	t.Run("CAS append via ExpectedOld", func(t *testing.T) {
		repo := initGitRepo(t)
		store := refstore.New(repo)

		first, err := WriteManifestTree(ctx, store, "refs/etude/runs/", manifest, files, refstore.WriteOptions{
			Message: "first",
		})
		if err != nil {
			t.Fatalf("first WriteManifestTree: %v", err)
		}

		manifest2 := manifest
		manifest2.Created = manifest.Created.Add(time.Second)
		second, err := WriteManifestTree(ctx, store, "refs/etude/runs/", manifest2, files, refstore.WriteOptions{
			ExpectedOld: first,
			Message:     "second",
		})
		if err != nil {
			t.Fatalf("CAS WriteManifestTree: %v", err)
		}
		if second == first {
			t.Fatal("CAS write did not produce a new commit")
		}
		parent := strings.TrimSpace(git(t, repo, "rev-parse", second+"^"))
		if parent != first {
			t.Fatalf("parent = %q, want first commit %q", parent, first)
		}

		// Stale CAS must return ErrStaleRef.
		_, err = WriteManifestTree(ctx, store, "refs/etude/runs/", manifest2, files, refstore.WriteOptions{
			ExpectedOld: first,
			Message:     "stale",
		})
		if !errors.Is(err, refstore.ErrStaleRef) {
			t.Fatalf("stale CAS error = %v, want ErrStaleRef", err)
		}
	})
}

// TestLegacyBackCompatFrozenBytes is a FROZEN regression guard: it embeds a
// real legacy-shaped manifest byte string (top-level per-stage skill{}, no
// producer, no manifest_version) modelled on refs/etude/runs/etude-88o and
// asserts that ParseJSON succeeds, ManifestVersion == 0, and that Stage.Skill
// AND Stage.Producer.Skill are populated while Harness and Model remain empty.
// This test guards the 8 existing captured runs against future regressions
// without relying on live git refs.
func TestLegacyBackCompatFrozenBytes(t *testing.T) {
	// Frozen bytes: a single-stage legacy manifest with a top-level skill block,
	// no producer field, no manifest_version. Derived from etude-88o shape.
	const legacy = `{
  "run_id": "etude-88o",
  "workflow": "default",
  "workflow_version": "v1",
  "created": "2026-05-24T14:25:42.373947Z",
  "refs": {
    "bead": "etude-88o"
  },
  "stages": [
    {
      "stage": "plan",
      "produced_by": "original",
      "git_sha": "7bf2c65fffb00a1e07f0548b15570d3c6f8dcc07",
      "skill": {
        "id": "dev-planner",
        "repo": "manual",
        "version": "manual"
      },
      "inputs": [
        {
          "role": "task",
          "artifact": "73ca31930913c76c28892e2ec6c9946f70d59ae5b146fbd8652f28ef1567d7c7",
          "path": "artifacts/sha256/73/73ca31930913c76c28892e2ec6c9946f70d59ae5b146fbd8652f28ef1567d7c7",
          "media_type": "text/markdown; charset=utf-8",
          "storage": "content",
          "size": 4352
        }
      ],
      "output": {
        "role": "plan",
        "artifact": "7b211635fa52fae93311db2c1a6c4f904988726e928b34d80ab845014c502531",
        "path": "artifacts/sha256/7b/7b211635fa52fae93311db2c1a6c4f904988726e928b34d80ab845014c502531",
        "media_type": "text/markdown; charset=utf-8",
        "storage": "content",
        "size": 3753
      },
      "timestamp": "2026-05-24T14:25:42.373947Z"
    }
  ]
}`
	got, err := ParseJSON([]byte(legacy))
	if err != nil {
		t.Fatalf("ParseJSON(legacy) returned error: %v", err)
	}
	if got.ManifestVersion != 0 {
		t.Fatalf("ManifestVersion = %d, want 0 (legacy)", got.ManifestVersion)
	}
	if len(got.Stages) != 1 {
		t.Fatalf("len(Stages) = %d, want 1", len(got.Stages))
	}
	s := got.Stages[0]
	wantSkill := Skill{ID: "dev-planner", Repo: "manual", Version: "manual"}
	if s.Skill != wantSkill {
		t.Fatalf("Stage.Skill = %+v, want %+v", s.Skill, wantSkill)
	}
	if s.Producer.Skill != wantSkill {
		t.Fatalf("Stage.Producer.Skill = %+v, want %+v", s.Producer.Skill, wantSkill)
	}
	if s.Producer.Harness != (Harness{}) {
		t.Fatalf("Stage.Producer.Harness = %+v, want empty", s.Producer.Harness)
	}
	if s.Producer.Model != "" {
		t.Fatalf("Stage.Producer.Model = %q, want empty", s.Producer.Model)
	}
}

// TestProducerWinsConflict asserts that when both a top-level skill and a
// producer.skill are present and differ, producer is authoritative and
// Stage.Skill mirrors producer.skill (not the legacy top-level value).
func TestProducerWinsConflict(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	// Build a v2 manifest, serialise it, then inject a conflicting legacy top-level
	// skill block to simulate a document with both fields.
	s := Skill{ID: "new-skill", Repo: "github.com/example/skills", Version: "v2"}
	manifest := validManifest(output)
	manifest.Stages[0].Producer = Producer{
		Skill: s,
	}
	manifest.Stages[0].Skill = s // keep Skill consistent so Validate passes
	jsonBytes, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}

	// Inject a conflicting legacy top-level skill into the stage JSON.
	conflicting := strings.Replace(
		string(jsonBytes),
		`"producer":`,
		`"skill": {"id": "legacy-skill", "repo": "legacy-repo", "version": "v0"}, "producer":`,
		1,
	)

	got, err := ParseJSON([]byte(conflicting))
	if err != nil {
		t.Fatalf("ParseJSON(conflicting) returned error: %v", err)
	}
	if got.Stages[0].Skill != s {
		t.Fatalf("Stage.Skill = %+v, want producer's %+v (producer must win)", got.Stages[0].Skill, s)
	}
	if got.Stages[0].Producer.Skill != s {
		t.Fatalf("Stage.Producer.Skill = %+v, want %+v", got.Stages[0].Producer.Skill, s)
	}
}

// TestProducerRoundTrip verifies that a Manifest with a fully-populated Producer
// (Harness name+version, Model, Skill) round-trips through JSON() -> ParseJSON
// with all fields intact.
func TestProducerRoundTrip(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	skill := Skill{ID: "dev-planner", Repo: "github.com/example/skills", Version: "v3"}
	manifest := validManifest(output)
	manifest.Stages[0].Skill = skill
	manifest.Stages[0].Producer = Producer{
		Harness: Harness{Name: "claude-code", Version: "1.0"},
		Model:   "claude-opus-4-7",
		Skill:   skill,
	}

	jsonBytes, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}

	got, err := ParseJSON(jsonBytes)
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	s := got.Stages[0]
	if s.Skill != skill {
		t.Fatalf("Stage.Skill = %+v, want %+v", s.Skill, skill)
	}
	if s.Producer.Skill != skill {
		t.Fatalf("Stage.Producer.Skill = %+v, want %+v", s.Producer.Skill, skill)
	}
	if s.Producer.Harness != (Harness{Name: "claude-code", Version: "1.0"}) {
		t.Fatalf("Stage.Producer.Harness = %+v, want {claude-code 1.0}", s.Producer.Harness)
	}
	if s.Producer.Model != "claude-opus-4-7" {
		t.Fatalf("Stage.Producer.Model = %q, want claude-opus-4-7", s.Producer.Model)
	}
	if got.ManifestVersion != 2 {
		t.Fatalf("ManifestVersion = %d, want 2", got.ManifestVersion)
	}
}

// TestReplayOfJSONRoundTrip verifies that a stage with ReplayOf set round-trips
// through JSON() -> ParseJSON with all three fields (RunID, Stage, Commit) intact.
func TestReplayOfJSONRoundTrip(t *testing.T) {
	store := artifactstore.New()
	output, err := store.AddContent("output", "text/plain", []byte("replay-output"))
	if err != nil {
		t.Fatalf("AddContent: %v", err)
	}
	skill := Skill{ID: "dev-workflow", Repo: "github.com/example/skills", Version: "v1"}
	commit := strings.Repeat("b", 64)
	manifest := Manifest{
		RunID:           "replay-roundtrip-run",
		Workflow:        "test",
		WorkflowVersion: "v1",
		Created:         time.Date(2026, 5, 22, 1, 0, 0, 0, time.UTC),
		Refs:            map[string]string{"bead": "test"},
		Stages: []Stage{{
			Name:       "plan",
			ProducedBy: "replay",
			GitSHA:     strings.Repeat("c", 40),
			Skill:      skill,
			Producer:   Producer{Skill: skill},
			Output:     ArtifactFromManifestArtifact(output),
			Timestamp:  time.Date(2026, 5, 22, 1, 1, 0, 0, time.UTC),
			ReplayOf: &ReplayLink{
				RunID:  "source-run-1",
				Stage:  "plan",
				Commit: commit,
			},
		}},
	}

	jsonBytes, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Verify the commit field is present in the raw JSON.
	if !strings.Contains(string(jsonBytes), `"commit"`) {
		t.Fatalf("JSON does not contain commit field:\n%s", jsonBytes)
	}

	// Verify manifest_version is 2.
	got, err := ParseJSON(jsonBytes)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if got.ManifestVersion != 2 {
		t.Fatalf("ManifestVersion = %d, want 2", got.ManifestVersion)
	}
	if len(got.Stages) != 1 {
		t.Fatalf("len(Stages) = %d, want 1", len(got.Stages))
	}
	s := got.Stages[0]
	if s.ReplayOf == nil {
		t.Fatal("ReplayOf is nil, want non-nil")
	}
	if s.ReplayOf.RunID != "source-run-1" {
		t.Fatalf("ReplayOf.RunID = %q, want %q", s.ReplayOf.RunID, "source-run-1")
	}
	if s.ReplayOf.Stage != "plan" {
		t.Fatalf("ReplayOf.Stage = %q, want %q", s.ReplayOf.Stage, "plan")
	}
	if s.ReplayOf.Commit != commit {
		t.Fatalf("ReplayOf.Commit = %q, want %q", s.ReplayOf.Commit, commit)
	}
}

// TestV2WithReplayOfFrozenBytes is a frozen-bytes regression test for a v2
// manifest containing a stage with a replay_of field (including all three sub-fields).
func TestV2WithReplayOfFrozenBytes(t *testing.T) {
	// SHA-256 content hash for "replay-artifact-data".
	const contentHex = "9e3d07d6e1e91f1e7b37c88f76edbd42fe5fccb4f1ff17d2c52c98a49f0b5cc1"

	commit := strings.Repeat("d", 40)
	frozen := fmt.Sprintf(`{
  "manifest_version": 2,
  "run_id": "frozen-replay-run",
  "workflow": "bench",
  "workflow_version": "v1",
  "created": "2026-05-22T01:00:00Z",
  "refs": {},
  "stages": [
    {
      "stage": "eval",
      "produced_by": "replay",
      "git_sha": "%s",
      "producer": {
        "skill": {
          "id": "bench-skill",
          "repo": "github.com/example/skills",
          "version": "v2"
        }
      },
      "inputs": [],
      "output": {
        "role": "result",
        "artifact": "%s",
        "path": "artifacts/sha256/%s/%s",
        "media_type": "text/plain",
        "storage": "content",
        "size": 20
      },
      "timestamp": "2026-05-22T01:01:00Z",
      "replay_of": {
        "run_id": "source-run-frozen",
        "stage": "eval",
        "commit": "%s"
      }
    }
  ]
}
`, strings.Repeat("e", 40), contentHex, contentHex[:2], contentHex, commit)

	got, err := ParseJSON([]byte(frozen))
	if err != nil {
		t.Fatalf("ParseJSON(frozen v2 with replay_of): %v", err)
	}
	if got.ManifestVersion != 2 {
		t.Fatalf("ManifestVersion = %d, want 2", got.ManifestVersion)
	}
	if len(got.Stages) != 1 {
		t.Fatalf("len(Stages) = %d, want 1", len(got.Stages))
	}
	s := got.Stages[0]
	if s.ReplayOf == nil {
		t.Fatal("ReplayOf is nil after parsing frozen v2-with-replay_of")
	}
	if s.ReplayOf.RunID != "source-run-frozen" {
		t.Fatalf("ReplayOf.RunID = %q, want source-run-frozen", s.ReplayOf.RunID)
	}
	if s.ReplayOf.Stage != "eval" {
		t.Fatalf("ReplayOf.Stage = %q, want eval", s.ReplayOf.Stage)
	}
	if s.ReplayOf.Commit != commit {
		t.Fatalf("ReplayOf.Commit = %q, want %q", s.ReplayOf.Commit, commit)
	}
}

// TestV2WithoutReplayOfHasNilReplayOf verifies that a v2 manifest without a
// replay_of field parses with Stage.ReplayOf == nil (back-compat assertion).
func TestV2WithoutReplayOfHasNilReplayOf(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	manifest := validManifest(output)
	jsonBytes, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got, err := ParseJSON(jsonBytes)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if got.Stages[0].ReplayOf != nil {
		t.Fatalf("ReplayOf = %+v, want nil for stage without replay_of", got.Stages[0].ReplayOf)
	}
}

// TestValidateReplayOfGuards exercises all bidirectional Validate rules for replay_of.
func TestValidateReplayOfGuards(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	validCommit := strings.Repeat("a", 40)
	validLink := &ReplayLink{RunID: "source-run", Stage: "plan", Commit: validCommit}

	cases := []struct {
		name string
		edit func(*Manifest)
	}{
		// produced_by:replay without replay_of
		{"replay without replay_of", func(m *Manifest) {
			m.Stages[0].ProducedBy = "replay"
			m.Stages[0].ReplayOf = nil
		}},
		// replay_of present on produced_by:original
		{"replay_of on original", func(m *Manifest) {
			m.Stages[0].ProducedBy = "original"
			m.Stages[0].ReplayOf = validLink
		}},
		// invalid run_id in replay_of
		{"invalid replay_of run_id", func(m *Manifest) {
			m.Stages[0].ProducedBy = "replay"
			m.Stages[0].ReplayOf = &ReplayLink{RunID: "..", Stage: "plan", Commit: validCommit}
		}},
		// empty stage in replay_of
		{"empty replay_of stage", func(m *Manifest) {
			m.Stages[0].ProducedBy = "replay"
			m.Stages[0].ReplayOf = &ReplayLink{RunID: "source-run", Stage: "", Commit: validCommit}
		}},
		// invalid stage (spaces) in replay_of
		{"invalid replay_of stage", func(m *Manifest) {
			m.Stages[0].ProducedBy = "replay"
			m.Stages[0].ReplayOf = &ReplayLink{RunID: "source-run", Stage: "bad stage", Commit: validCommit}
		}},
		// empty commit in replay_of
		{"empty replay_of commit", func(m *Manifest) {
			m.Stages[0].ProducedBy = "replay"
			m.Stages[0].ReplayOf = &ReplayLink{RunID: "source-run", Stage: "plan", Commit: ""}
		}},
		// non-hex commit in replay_of
		{"non-hex replay_of commit", func(m *Manifest) {
			m.Stages[0].ProducedBy = "replay"
			m.Stages[0].ReplayOf = &ReplayLink{RunID: "source-run", Stage: "plan", Commit: "xyz" + strings.Repeat("0", 37)}
		}},
		// wrong-length commit (39 chars)
		{"wrong-length replay_of commit", func(m *Manifest) {
			m.Stages[0].ProducedBy = "replay"
			m.Stages[0].ReplayOf = &ReplayLink{RunID: "source-run", Stage: "plan", Commit: strings.Repeat("a", 39)}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest(output)
			// validManifest uses produced_by:original with no replay_of; we need
			// the Skill set on the stage so only the replay_of rule triggers.
			tc.edit(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("Validate returned nil error, want error")
			}
		})
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

// TestGateRoundTrip verifies that a manifest with the spec §5 two-round four-seat
// example round-trips through JSON() -> ParseJSON intact, and ManifestVersion == 3.
func TestGateRoundTrip(t *testing.T) {
	m := gateManifest(t)

	jsonBytes, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	got, err := ParseJSON(jsonBytes)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if got.ManifestVersion != 3 {
		t.Fatalf("ManifestVersion = %d, want 3", got.ManifestVersion)
	}
	if len(got.Gates) != 2 {
		t.Fatalf("len(Gates) = %d, want 2", len(got.Gates))
	}
	g0 := got.Gates[0]
	if g0.GateID != "plan.r1" || g0.Phase != "plan" || g0.Round != 1 || g0.Tier != 1 {
		t.Fatalf("gate[0] = %+v", g0)
	}
	if g0.Status != GateStatusRerun {
		t.Fatalf("gate[0].Status = %q, want rerun", g0.Status)
	}
	if len(g0.Seats) != 4 {
		t.Fatalf("gate[0] seat count = %d, want 4", len(g0.Seats))
	}
	// Verify provider with slash in model round-trips (pilms acceptance criterion).
	pilms := g0.Seats[3]
	if pilms.Provider.Name != "lmstudio" || pilms.Provider.Model != "qwen/qwen3.6-35b-a3b" {
		t.Fatalf("pilms provider = %+v", pilms.Provider)
	}
	if pilms.Verdict != SeatVerdictMalfunction {
		t.Fatalf("pilms verdict = %q, want malfunction", pilms.Verdict)
	}

	g1 := got.Gates[1]
	if g1.GateID != "plan.r2" || g1.Status != GateStatusPass {
		t.Fatalf("gate[1] = %+v", g1)
	}
	disregarded := g1.Seats[3]
	if disregarded.Verdict != SeatVerdictDisregarded {
		t.Fatalf("disregarded seat verdict = %q, want disregarded", disregarded.Verdict)
	}
	if g1.Decision.DegradedReason == "" {
		t.Fatalf("gate[1].Decision.DegradedReason should be set")
	}
}

// TestGatelessManifestStaysV2ByteIdentical verifies that a stage-only manifest
// (no gates) serializes with manifest_version: 2 and no gates key — byte-identical
// to a manifest serialized by the previous code path.
func TestGatelessManifestStaysV2ByteIdentical(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	m := validManifest(output)

	jsonBytes, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	text := string(jsonBytes)

	if !strings.Contains(text, `"manifest_version": 2`) {
		t.Fatalf("expected manifest_version 2 in gate-less manifest:\n%s", text)
	}
	if strings.Contains(text, "gates") {
		t.Fatalf("gate-less manifest must not contain 'gates' key:\n%s", text)
	}

	// Must parse back to ManifestVersion == 2.
	got, err := ParseJSON(jsonBytes)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if got.ManifestVersion != 2 {
		t.Fatalf("ManifestVersion = %d, want 2", got.ManifestVersion)
	}
}

// TestValidateGateRejects is a table test covering all validation rules for gates.
func TestValidateGateRejects(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	otherOutput := contentArtifact("other", "text/plain", []byte("other"))
	now := time.Date(2026, 5, 25, 3, 10, 0, 0, time.UTC)

	validSeat := func() SeatResult {
		return SeatResult{
			Seat:      "gemini",
			Harness:   Harness{Name: "gemini-cli", Version: "3.1"},
			Provider:  Provider{Name: "google", Model: "gemini-3.1-pro-preview"},
			Verdict:   SeatVerdictGo,
			Timestamp: now,
		}
	}

	validRef := func() ReviewedRef {
		return ReviewedRef{Stage: "plan"}
	}

	validGate := func() GateAttempt {
		return GateAttempt{
			GateID:         "plan.r1",
			Phase:          "plan",
			Round:          1,
			Tier:           1,
			Status:         GateStatusPass,
			ReviewedStages: []ReviewedRef{validRef()},
			Seats:          []SeatResult{validSeat()},
			Timestamp:      now,
		}
	}

	baseManifest := func(edits ...func(*Manifest)) Manifest {
		m := validManifest(output)
		for _, e := range edits {
			e(&m)
		}
		return m
	}

	cases := []struct {
		name string
		edit func(*Manifest)
	}{
		// gate_id
		{"missing gate_id", func(m *Manifest) {
			g := validGate()
			g.GateID = ""
			m.Gates = []GateAttempt{g}
		}},
		{"invalid gate_id chars", func(m *Manifest) {
			g := validGate()
			g.GateID = "bad/id"
			m.Gates = []GateAttempt{g}
		}},
		{"duplicate gate_id", func(m *Manifest) {
			g1 := validGate()
			g2 := validGate()
			g2.Round = 2
			m.Gates = []GateAttempt{g1, g2}
		}},
		// phase
		{"missing phase", func(m *Manifest) {
			g := validGate()
			g.Phase = ""
			m.Gates = []GateAttempt{g}
		}},
		// round
		{"round zero", func(m *Manifest) {
			g := validGate()
			g.Round = 0
			m.Gates = []GateAttempt{g}
		}},
		{"round negative", func(m *Manifest) {
			g := validGate()
			g.Round = -1
			m.Gates = []GateAttempt{g}
		}},
		// tier
		{"tier negative", func(m *Manifest) {
			g := validGate()
			g.Tier = -1
			m.Gates = []GateAttempt{g}
		}},
		{"tier 4", func(m *Manifest) {
			g := validGate()
			g.Tier = 4
			m.Gates = []GateAttempt{g}
		}},
		// status
		{"unknown status", func(m *Manifest) {
			g := validGate()
			g.Status = "unknown"
			m.Gates = []GateAttempt{g}
		}},
		// escalation_reason
		{"escalated without reason", func(m *Manifest) {
			g := validGate()
			g.Status = GateStatusEscalated
			m.Gates = []GateAttempt{g}
		}},
		// reviewed_stages
		{"no reviewed_stages", func(m *Manifest) {
			g := validGate()
			g.ReviewedStages = nil
			m.Gates = []GateAttempt{g}
		}},
		{"reviewed_stage references unknown stage", func(m *Manifest) {
			g := validGate()
			g.ReviewedStages = []ReviewedRef{{Stage: "nonexistent"}}
			m.Gates = []GateAttempt{g}
		}},
		{"reviewed_stage dangling artifact", func(m *Manifest) {
			g := validGate()
			g.ReviewedStages = []ReviewedRef{{Stage: "plan", Artifact: strings.Repeat("b", 64)}}
			m.Gates = []GateAttempt{g}
		}},
		{"reviewed_stage bad artifact format", func(m *Manifest) {
			g := validGate()
			g.ReviewedStages = []ReviewedRef{{Stage: "plan", Artifact: "not-sha256"}}
			m.Gates = []GateAttempt{g}
		}},
		{"reviewed_stage bad role chars", func(m *Manifest) {
			g := validGate()
			g.ReviewedStages = []ReviewedRef{{Stage: "plan", Role: "bad role"}}
			m.Gates = []GateAttempt{g}
		}},
		// duplicate (phase, round)
		{"duplicate phase round", func(m *Manifest) {
			g1 := validGate()
			g2 := validGate()
			g2.GateID = "plan.r1b"
			m.Gates = []GateAttempt{g1, g2}
		}},
		// seats
		{"no seats", func(m *Manifest) {
			g := validGate()
			g.Seats = nil
			m.Gates = []GateAttempt{g}
		}},
		{"missing seat name", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Seat = ""
			m.Gates = []GateAttempt{g}
		}},
		{"invalid seat name", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Seat = "bad seat"
			m.Gates = []GateAttempt{g}
		}},
		{"missing harness name", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Harness.Name = ""
			m.Gates = []GateAttempt{g}
		}},
		// provider
		{"missing provider name", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Provider.Name = ""
			m.Gates = []GateAttempt{g}
		}},
		{"missing provider model", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Provider.Model = ""
			m.Gates = []GateAttempt{g}
		}},
		{"provider name with control char", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Provider.Name = "bad\x01name"
			m.Gates = []GateAttempt{g}
		}},
		{"provider model with control char", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Provider.Model = "bad\x00model"
			m.Gates = []GateAttempt{g}
		}},
		// verdict
		{"unknown verdict", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Verdict = "unknown"
			m.Gates = []GateAttempt{g}
		}},
		// failure_note required for failed/empty/malfunction/disregarded
		{"failed without failure_note", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Verdict = SeatVerdictFailed
			g.Seats[0].FailureNote = ""
			m.Gates = []GateAttempt{g}
		}},
		{"empty without failure_note", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Verdict = SeatVerdictEmpty
			g.Seats[0].FailureNote = ""
			m.Gates = []GateAttempt{g}
		}},
		{"malfunction without failure_note", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Verdict = SeatVerdictMalfunction
			g.Seats[0].FailureNote = ""
			m.Gates = []GateAttempt{g}
		}},
		{"disregarded without failure_note", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Verdict = SeatVerdictDisregarded
			g.Seats[0].FailureNote = ""
			m.Gates = []GateAttempt{g}
		}},
		// failure_note forbidden for go/block
		{"go with failure_note", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Verdict = SeatVerdictGo
			g.Seats[0].FailureNote = "oops"
			m.Gates = []GateAttempt{g}
		}},
		{"block with failure_note", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Verdict = SeatVerdictBlock
			g.Seats[0].FailureNote = "oops"
			m.Gates = []GateAttempt{g}
		}},
		// raw_output validation
		{"bad raw_output ref", func(m *Manifest) {
			bad := ArtifactRef{Role: "t", Artifact: "badhash", Path: "artifacts/sha256/ab/badhash", MediaType: "text/plain", Storage: "content", Size: 0}
			g := validGate()
			g.Seats[0].RawOutput = &bad
			m.Gates = []GateAttempt{g}
		}},
		// gate timestamp
		{"missing gate timestamp", func(m *Manifest) {
			g := validGate()
			g.Timestamp = time.Time{}
			m.Gates = []GateAttempt{g}
		}},
		// seat timestamp
		{"missing seat timestamp", func(m *Manifest) {
			g := validGate()
			g.Seats[0].Timestamp = time.Time{}
			m.Gates = []GateAttempt{g}
		}},
		// reviewed_stage with artifact matching other stage's artifact (dangling)
		{"artifact belongs to different stage output", func(m *Manifest) {
			m.Stages = append(m.Stages, Stage{
				Name: "review", ProducedBy: "original", GitSHA: "def",
				Skill: m.Stages[0].Skill, Output: otherOutput,
				Timestamp: now,
			})
			g := validGate()
			g.ReviewedStages = []ReviewedRef{{Stage: "plan", Artifact: otherOutput.Artifact}}
			m.Gates = []GateAttempt{g}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseManifest(tc.edit)
			if err := m.Validate(); err == nil {
				t.Fatal("Validate returned nil error, want error")
			}
		})
	}

	// Acceptance: all six verdicts succeed when combined correctly.
	t.Run("all six verdicts accepted", func(t *testing.T) {
		m := baseManifest()
		g := validGate()
		g.Seats = []SeatResult{
			{Seat: "s1", Harness: Harness{Name: "h"}, Provider: Provider{Name: "p", Model: "m"}, Verdict: SeatVerdictGo, Timestamp: now},
			{Seat: "s2", Harness: Harness{Name: "h"}, Provider: Provider{Name: "p", Model: "m"}, Verdict: SeatVerdictBlock, Timestamp: now},
			{Seat: "s3", Harness: Harness{Name: "h"}, Provider: Provider{Name: "p", Model: "m"}, Verdict: SeatVerdictFailed, FailureNote: "auth failed", Timestamp: now},
			{Seat: "s4", Harness: Harness{Name: "h"}, Provider: Provider{Name: "p", Model: "m"}, Verdict: SeatVerdictEmpty, FailureNote: "no output", Timestamp: now},
			{Seat: "s5", Harness: Harness{Name: "h"}, Provider: Provider{Name: "p", Model: "m"}, Verdict: SeatVerdictMalfunction, FailureNote: "hang", Timestamp: now},
			{Seat: "s6", Harness: Harness{Name: "h"}, Provider: Provider{Name: "p", Model: "m"}, Verdict: SeatVerdictDisregarded, FailureNote: "skipped", Timestamp: now},
		}
		m.Gates = []GateAttempt{g}
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate returned error for valid six-verdict gate: %v", err)
		}
	})

	// Acceptance: reviewed_stage with artifact "" (name-only) is valid.
	t.Run("name-only reviewed_stage is valid", func(t *testing.T) {
		m := baseManifest()
		g := validGate()
		g.ReviewedStages = []ReviewedRef{{Stage: "plan", Artifact: ""}}
		m.Gates = []GateAttempt{g}
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate returned error for name-only reviewed_stage: %v", err)
		}
	})

	// Acceptance: reviewed_stage with artifact matching the stage's output is valid.
	t.Run("artifact matches stage output", func(t *testing.T) {
		m := baseManifest()
		g := validGate()
		g.ReviewedStages = []ReviewedRef{{Stage: "plan", Artifact: output.Artifact}}
		m.Gates = []GateAttempt{g}
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate returned error for valid artifact ref: %v", err)
		}
	})

	// Acceptance: escalated with reason is valid.
	t.Run("escalated with reason is valid", func(t *testing.T) {
		m := baseManifest()
		g := validGate()
		g.Status = GateStatusEscalated
		g.Decision.EscalationReason = "human needed"
		m.Gates = []GateAttempt{g}
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate returned error for escalated gate with reason: %v", err)
		}
	})

	// Acceptance: provider with slash in model (pilms) is valid.
	t.Run("provider model with slash is valid", func(t *testing.T) {
		m := baseManifest()
		g := validGate()
		g.Seats[0].Provider = Provider{Name: "lmstudio", Model: "qwen/qwen3.6-35b-a3b"}
		m.Gates = []GateAttempt{g}
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate returned error for pilms provider: %v", err)
		}
	})
}

// TestVersionAllowlist verifies that ParseJSON accepts manifest_version 0, 2, 3
// and rejects 1 and 4 with ErrInvalidManifest.
func TestVersionAllowlist(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	m := validManifest(output)
	baseJSON, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Versions that should be accepted.
	for _, v := range []int{0, 2} {
		v := v
		t.Run(fmt.Sprintf("accept version %d", v), func(t *testing.T) {
			raw := strings.Replace(string(baseJSON), `"manifest_version": 2`, fmt.Sprintf(`"manifest_version": %d`, v), 1)
			if _, err := ParseJSON([]byte(raw)); err != nil {
				t.Fatalf("ParseJSON rejected version %d: %v", v, err)
			}
		})
	}

	// Version 3: need a manifest with gates to emit v3.
	t.Run("accept version 3", func(t *testing.T) {
		mg := gateManifest(t)
		j, err := mg.JSON()
		if err != nil {
			t.Fatalf("JSON: %v", err)
		}
		if _, err := ParseJSON(j); err != nil {
			t.Fatalf("ParseJSON rejected version 3: %v", err)
		}
	})

	// Versions that should be rejected.
	for _, v := range []int{1, 4, 5, 100} {
		v := v
		t.Run(fmt.Sprintf("reject version %d", v), func(t *testing.T) {
			raw := strings.Replace(string(baseJSON), `"manifest_version": 2`, fmt.Sprintf(`"manifest_version": %d`, v), 1)
			_, err := ParseJSON([]byte(raw))
			if err == nil {
				t.Fatalf("ParseJSON accepted invalid version %d", v)
			}
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ParseJSON version %d error = %v, want ErrInvalidManifest", v, err)
			}
		})
	}
}

// TestArtifactPathsWalksGateRawOutput verifies that ArtifactPaths and
// referencedArtifactPaths include a seat's raw_output path.
func TestArtifactPathsWalksGateRawOutput(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)

	stageOutput := contentArtifact("output", "text/plain", []byte("stage content"))
	rawTranscript := contentArtifact("codex-transcript", "text/markdown", []byte("codex review"))
	sessionTranscript := contentArtifact("codex-session-transcript", "text/plain", []byte("full codex transcript"))

	m := validManifest(stageOutput)
	now := time.Date(2026, 5, 25, 3, 0, 0, 0, time.UTC)
	m.Gates = []GateAttempt{{
		GateID:         "plan.r1",
		Phase:          "plan",
		Round:          1,
		Tier:           1,
		Status:         GateStatusPass,
		ReviewedStages: []ReviewedRef{{Stage: "plan"}},
		Seats: []SeatResult{{
			Seat:      "codex",
			Harness:   Harness{Name: "codex", Version: "1.0"},
			Provider:  Provider{Name: "openai", Model: "gpt-5.5"},
			Verdict:   SeatVerdictGo,
			RawOutput: &rawTranscript,
			Session: &SessionEvidence{
				SessionID:          "codex-session-123",
				TranscriptArtifact: &sessionTranscript,
				RetrievalStatus:    SessionEvidenceRetrievalImported,
				RedactionStatus:    SessionEvidenceRedactionPassed,
			},
			Timestamp: now,
		}},
		Timestamp: now,
	}}

	// ArtifactPaths must include stage output, raw output, and transcript evidence.
	paths := ArtifactPaths(m)
	wantPaths := []string{rawTranscript.Path, sessionTranscript.Path, stageOutput.Path}
	// Sort both for stable comparison.
	if len(paths) != 3 {
		t.Fatalf("ArtifactPaths count = %d, want 3; paths = %v", len(paths), paths)
	}
	foundTranscript := false
	foundSessionTranscript := false
	for _, p := range paths {
		if p == rawTranscript.Path {
			foundTranscript = true
		}
		if p == sessionTranscript.Path {
			foundSessionTranscript = true
		}
	}
	if !foundTranscript {
		t.Fatalf("ArtifactPaths did not include raw_output path %q; got %v", rawTranscript.Path, paths)
	}
	if !foundSessionTranscript {
		t.Fatalf("ArtifactPaths did not include session transcript path %q; got %v", sessionTranscript.Path, paths)
	}
	_ = wantPaths

	// Write to the git ref — Writer.Write uses referencedArtifactPaths to allow
	// files, so if it doesn't walk gates the write will fail with ErrUnreferencedArtifact.
	files := map[string][]byte{
		stageOutput.Path:       []byte("stage content"),
		rawTranscript.Path:     []byte("codex review"),
		sessionTranscript.Path: []byte("full codex transcript"),
	}
	if _, err := (Writer{Store: refstore.New(repo)}).Write(ctx, m, files, WriteOptions{}); err != nil {
		t.Fatalf("Write with gate raw_output returned error: %v", err)
	}

	// Now verify that on append the raw_output blob is carried forward.
	commit, err := refstore.New(repo).Resolve(ctx, "refs/etude/runs/"+m.RunID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Read the manifest back and verify paths include the gate transcript.
	manifestBytes, err := refstore.New(repo).ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		t.Fatalf("ReadCommitFile: %v", err)
	}
	parsed, err := ParseJSON(manifestBytes)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	paths2 := ArtifactPaths(parsed)
	foundTranscript2 := false
	foundSessionTranscript2 := false
	for _, p := range paths2 {
		if p == rawTranscript.Path {
			foundTranscript2 = true
		}
		if p == sessionTranscript.Path {
			foundSessionTranscript2 = true
		}
	}
	if !foundTranscript2 {
		t.Fatalf("ArtifactPaths after round-trip did not include raw_output path %q; got %v", rawTranscript.Path, paths2)
	}
	if !foundSessionTranscript2 {
		t.Fatalf("ArtifactPaths after round-trip did not include session transcript path %q; got %v", sessionTranscript.Path, paths2)
	}

	// An extra unreferenced blob still trips ErrUnreferencedArtifact.
	extraBlob := contentArtifact("extra", "text/plain", []byte("extra blob"))
	files2 := map[string][]byte{
		stageOutput.Path:       []byte("stage content"),
		rawTranscript.Path:     []byte("codex review"),
		sessionTranscript.Path: []byte("full codex transcript"),
		extraBlob.Path:         []byte("extra blob"),
	}
	_, err = (Writer{Store: refstore.New(repo)}).Write(ctx, m, files2, WriteOptions{})
	if !errors.Is(err, ErrUnreferencedArtifact) {
		t.Fatalf("extra blob error = %v, want ErrUnreferencedArtifact", err)
	}
}

// gateManifest builds a manifest with the spec §5 two-round four-seat example.
func gateManifest(t *testing.T) Manifest {
	t.Helper()
	planOutput := contentArtifact("plan", "text/markdown", []byte("plan content"))
	now := func(h, m int) time.Time {
		return time.Date(2026, 5, 25, h, m, 0, 0, time.UTC)
	}
	base := validManifest(planOutput)
	base.Gates = []GateAttempt{
		{
			GateID:         "plan.r1",
			Phase:          "plan",
			Round:          1,
			Tier:           1,
			Status:         GateStatusRerun,
			ReviewedStages: []ReviewedRef{{Stage: "plan", Role: "plan"}},
			Seats: []SeatResult{
				{
					Seat:      "gemini",
					Harness:   Harness{Name: "gemini-cli", Version: "3.1"},
					Provider:  Provider{Name: "google", Model: "gemini-3.1-pro-preview"},
					Verdict:   SeatVerdictGo,
					Optional:  []string{"clarify version-gate wording"},
					Timestamp: now(3, 10),
				},
				{
					Seat:      "opus",
					Harness:   Harness{Name: "claude-code", Version: "opus-4-7"},
					Provider:  Provider{Name: "anthropic", Model: "claude-opus-4-7"},
					Verdict:   SeatVerdictGo,
					Timestamp: now(3, 11),
				},
				{
					Seat:      "codex",
					Harness:   Harness{Name: "codex", Version: "gpt-5.5-xhigh"},
					Provider:  Provider{Name: "openai", Model: "gpt-5.5"},
					Verdict:   SeatVerdictBlock,
					Required:  []string{"specify parser accepts {0,2,3} explicitly; reject v1/v4"},
					Timestamp: now(3, 12),
				},
				{
					Seat:        "pilms",
					Harness:     Harness{Name: "pi", Version: "x"},
					Provider:    Provider{Name: "lmstudio", Model: "qwen/qwen3.6-35b-a3b"},
					Verdict:     SeatVerdictMalfunction,
					FailureNote: "0-CPU client hang, backend healthy (curl 200); known artifact",
					Timestamp:   now(3, 25),
				},
			},
			Decision:  GateDecision{},
			Timestamp: now(3, 26),
		},
		{
			GateID:         "plan.r2",
			Phase:          "plan",
			Round:          2,
			Tier:           1,
			Status:         GateStatusPass,
			ReviewedStages: []ReviewedRef{{Stage: "plan", Role: "plan"}},
			Seats: []SeatResult{
				{
					Seat:      "gemini",
					Harness:   Harness{Name: "gemini-cli", Version: "3.1"},
					Provider:  Provider{Name: "google", Model: "gemini-3.1-pro-preview"},
					Verdict:   SeatVerdictGo,
					Timestamp: now(3, 40),
				},
				{
					Seat:      "opus",
					Harness:   Harness{Name: "claude-code", Version: "opus-4-7"},
					Provider:  Provider{Name: "anthropic", Model: "claude-opus-4-7"},
					Verdict:   SeatVerdictGo,
					Timestamp: now(3, 41),
				},
				{
					Seat:      "codex",
					Harness:   Harness{Name: "codex", Version: "gpt-5.5-xhigh"},
					Provider:  Provider{Name: "openai", Model: "gpt-5.5"},
					Verdict:   SeatVerdictGo,
					Timestamp: now(3, 42),
				},
				{
					Seat:        "pilms",
					Harness:     Harness{Name: "pi", Version: "x"},
					Provider:    Provider{Name: "lmstudio", Model: "qwen/qwen3.6-35b-a3b"},
					Verdict:     SeatVerdictDisregarded,
					FailureNote: "0-CPU client hang reproduced after 1 reroll; known artifact",
					Timestamp:   now(3, 55),
				},
			},
			Decision: GateDecision{
				DegradedReason: "pilms reproducible 0-CPU hang (2 rounds); other 3 seats unanimous GO; autonomous-loop fallback",
			},
			Timestamp: now(3, 56),
		},
	}
	return base
}

// ---------------------------------------------------------------------------
// OccurredAt tests (etude-8hq.2)
// ---------------------------------------------------------------------------

// TestOccurredAtRoundTrip verifies that a manifest with OccurredAt set
// serializes to JSON with "occurred_at" present and parses back equal.
func TestOccurredAtRoundTrip(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	m := validManifest(output)
	eventTime := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	m.OccurredAt = eventTime

	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, `"occurred_at"`) {
		t.Fatalf("JSON missing occurred_at key:\n%s", text)
	}
	if !strings.Contains(text, "2026-01-15T09:30:00Z") {
		t.Fatalf("JSON missing expected occurred_at value:\n%s", text)
	}

	parsed, err := ParseJSON(b)
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	if !parsed.OccurredAt.Equal(eventTime) {
		t.Fatalf("parsed OccurredAt = %v, want %v", parsed.OccurredAt, eventTime)
	}
}

// TestOccurredAtZeroOmitted verifies that a zero OccurredAt produces NO
// "occurred_at" key in the JSON and that round-tripping such a manifest leaves
// OccurredAt zero (byte-stability: the key must never appear for zero time).
func TestOccurredAtZeroOmitted(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	m := validManifest(output)
	// OccurredAt is zero by default.

	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	text := string(b)
	if strings.Contains(text, "occurred_at") {
		t.Fatalf("JSON must NOT contain occurred_at when OccurredAt is zero:\n%s", text)
	}

	// Round-trip: parse and re-serialize; bytes must be identical.
	parsed, err := ParseJSON(b)
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	if !parsed.OccurredAt.IsZero() {
		t.Fatalf("parsed OccurredAt should be zero, got %v", parsed.OccurredAt)
	}
	b2, err := parsed.JSON()
	if err != nil {
		t.Fatalf("second JSON returned error: %v", err)
	}
	if string(b) != string(b2) {
		t.Fatalf("round-trip changed bytes:\nbefore:\n%s\nafter:\n%s", b, b2)
	}
}

// TestOccurredAtMalformedRejected verifies that a JSON document with a
// malformed occurred_at value is rejected by ParseJSON.
func TestOccurredAtMalformedRejected(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	m := validManifest(output)
	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	// Inject a malformed occurred_at after "created".
	bad := strings.Replace(string(b),
		`"created":`,
		`"occurred_at": "not-a-timestamp", "created":`,
		1,
	)
	if _, err := ParseJSON([]byte(bad)); err == nil {
		t.Fatal("ParseJSON accepted malformed occurred_at; want error")
	}
}

// ---------------------------------------------------------------------------
// EnvAllowlist tests
// ---------------------------------------------------------------------------

// TestManifestEnvAllowlist_AbsentOmitted verifies no env_allowlist key in JSON
// when EnvAllowlist is nil.
func TestManifestEnvAllowlist_AbsentOmitted(t *testing.T) {
	m := validManifest(contentArtifact("output", "text/plain", []byte("out")))
	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(b), "env_allowlist") {
		t.Errorf("env_allowlist key present when nil:\n%s", b)
	}
}

// TestManifestEnvAllowlist_EmptyOmitted verifies no env_allowlist key when the
// field is an empty slice (omitempty semantics).
func TestManifestEnvAllowlist_EmptyOmitted(t *testing.T) {
	m := validManifest(contentArtifact("output", "text/plain", []byte("out")))
	m.EnvAllowlist = []string{}
	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(b), "env_allowlist") {
		t.Errorf("env_allowlist key present when empty slice:\n%s", b)
	}
}

// TestManifestEnvAllowlist_Present verifies env_allowlist round-trips and contains
// only NAMES (never values).
func TestManifestEnvAllowlist_Present(t *testing.T) {
	m := validManifest(contentArtifact("output", "text/plain", []byte("out")))
	m.EnvAllowlist = []string{"FOO", "BAR"}
	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(string(b), `"env_allowlist"`) {
		t.Fatalf("env_allowlist key missing:\n%s", b)
	}
	// Paranoia: no secret values should appear in the manifest bytes.
	if strings.Contains(string(b), "secretval") {
		t.Errorf("value leaked into manifest bytes")
	}
	got, err := ParseJSON(b)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if len(got.EnvAllowlist) != 2 || got.EnvAllowlist[0] != "FOO" || got.EnvAllowlist[1] != "BAR" {
		t.Errorf("round-trip EnvAllowlist = %v, want [FOO BAR]", got.EnvAllowlist)
	}
}

// TestManifestEnvAllowlist_NoVersionBump verifies manifest_version stays 2
// when env_allowlist is present (additive omitempty field, no version bump).
func TestManifestEnvAllowlist_NoVersionBump(t *testing.T) {
	m := validManifest(contentArtifact("output", "text/plain", []byte("out")))
	m.EnvAllowlist = []string{"FOO"}
	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(string(b), `"manifest_version": 2`) {
		t.Errorf("manifest_version != 2 with env_allowlist:\n%s", b)
	}
}

// TestManifestEnvAllowlist_ByteStable verifies two JSON() calls on the same
// manifest with env_allowlist produce identical bytes.
func TestManifestEnvAllowlist_ByteStable(t *testing.T) {
	m := validManifest(contentArtifact("output", "text/plain", []byte("out")))
	m.EnvAllowlist = []string{"FOO", "BAR"}
	b1, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON first: %v", err)
	}
	b2, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON second: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("JSON not byte-stable:\nfirst:\n%s\nsecond:\n%s", b1, b2)
	}
}

// TestOccurredAtExistingManifestNoKey verifies that a manifest document that
// predates the occurred_at field (no key present) still decodes and validates
// successfully, with OccurredAt left as the zero time.
func TestOccurredAtExistingManifestNoKey(t *testing.T) {
	output := contentArtifact("output", "text/plain", []byte("out"))
	m := validManifest(output)
	b, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	// The zero-OccurredAt manifest must not contain occurred_at.
	if strings.Contains(string(b), "occurred_at") {
		t.Fatalf("pre-condition: base manifest already contains occurred_at:\n%s", b)
	}
	parsed, err := ParseJSON(b)
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	if err := parsed.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if !parsed.OccurredAt.IsZero() {
		t.Fatalf("OccurredAt should be zero for legacy manifest, got %v", parsed.OccurredAt)
	}
}
