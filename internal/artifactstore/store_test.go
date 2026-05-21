package artifactstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAddContentAddressesBySHA256AndPath(t *testing.T) {
	store := New()

	artifact, err := store.AddContent("plan", "text/markdown", []byte("plan text"))
	if err != nil {
		t.Fatalf("AddContent returned error: %v", err)
	}

	wantHash := sha256Hex([]byte("plan text"))
	if artifact != (ManifestArtifact{
		Role:      "plan",
		MediaType: "text/markdown",
		Storage:   StorageContent,
		SHA256:    wantHash,
		Path:      "artifacts/sha256/" + wantHash[:2] + "/" + wantHash,
		Size:      int64(len("plan text")),
	}) {
		t.Fatalf("artifact = %#v", artifact)
	}
	if got := string(store.Files()[artifact.Path]); got != "plan text" {
		t.Fatalf("stored content = %q", got)
	}
}

func TestContentDedupePreservesOneManifestRecordPerCall(t *testing.T) {
	store := New()

	first, err := store.AddContent("plan", "text/markdown", []byte("same"))
	if err != nil {
		t.Fatalf("first AddContent returned error: %v", err)
	}
	second, err := store.AddContent("review", "text/plain", []byte("same"))
	if err != nil {
		t.Fatalf("second AddContent returned error: %v", err)
	}
	third, err := store.AddContent("plan", "text/markdown", []byte("same"))
	if err != nil {
		t.Fatalf("third AddContent returned error: %v", err)
	}

	if len(store.Files()) != 1 {
		t.Fatalf("files len = %d, want 1", len(store.Files()))
	}
	if first.Path != second.Path || first.Path != third.Path {
		t.Fatalf("deduped paths differ: %q %q %q", first.Path, second.Path, third.Path)
	}
	want := []ManifestArtifact{first, second, third}
	if got := store.ManifestArtifacts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest records = %#v, want %#v", got, want)
	}
}

func TestContentMetadataDoesNotAffectBlobHash(t *testing.T) {
	store := New()

	first, err := store.AddContent("plan", "text/markdown", []byte("same"))
	if err != nil {
		t.Fatalf("first AddContent returned error: %v", err)
	}
	second, err := store.AddContent("docs", "application/json", []byte("same"))
	if err != nil {
		t.Fatalf("second AddContent returned error: %v", err)
	}

	if first.SHA256 != second.SHA256 {
		t.Fatalf("hashes differ: %s vs %s", first.SHA256, second.SHA256)
	}
	if string(store.Files()[first.Path]) != "same" {
		t.Fatalf("stored content changed: %q", store.Files()[first.Path])
	}
}

func TestContentAllowsZeroByteArtifacts(t *testing.T) {
	store := New()

	artifact, err := store.AddContent("empty", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("AddContent returned error: %v", err)
	}

	wantHash := sha256Hex(nil)
	if artifact.SHA256 != wantHash {
		t.Fatalf("hash = %q, want %q", artifact.SHA256, wantHash)
	}
	if artifact.Size != 0 {
		t.Fatalf("size = %d, want 0", artifact.Size)
	}
	if got := store.Files()[artifact.Path]; len(got) != 0 {
		t.Fatalf("stored bytes len = %d, want 0", len(got))
	}
}

func TestContentBytesAreDefensivelyCopied(t *testing.T) {
	store := New()
	content := []byte("original")

	artifact, err := store.AddContent("plan", "text/markdown", content)
	if err != nil {
		t.Fatalf("AddContent returned error: %v", err)
	}
	content[0] = 'X'

	files := store.Files()
	files[artifact.Path][0] = 'Y'

	if got := string(store.Files()[artifact.Path]); got != "original" {
		t.Fatalf("stored content = %q, want original", got)
	}
}

func TestFilesReturnsDefensiveMap(t *testing.T) {
	store := New()

	artifact, err := store.AddContent("plan", "text/markdown", []byte("content"))
	if err != nil {
		t.Fatalf("AddContent returned error: %v", err)
	}
	files := store.Files()
	delete(files, artifact.Path)
	files["artifacts/sha256/00/fake"] = []byte("fake")

	got := store.Files()
	if len(got) != 1 {
		t.Fatalf("files len = %d, want 1", len(got))
	}
	if string(got[artifact.Path]) != "content" {
		t.Fatalf("stored content = %q, want content", got[artifact.Path])
	}
}

func TestPointerRecordCanonicalJSONAndPath(t *testing.T) {
	store := New()
	size := int64(0)
	externalHash := strings.Repeat("a", 64)

	artifact, err := store.AddPointer("screenshot", "image/png", Pointer{
		URI:    " https://example.test/image.png?x=<>&y=1 ",
		SHA256: externalHash,
		Size:   &size,
	})
	if err != nil {
		t.Fatalf("AddPointer returned error: %v", err)
	}

	wantJSON := []byte(`{"version":1,"uri":"https://example.test/image.png?x=<>&y=1","sha256":"` + externalHash + `","size":0}`)
	wantHash := sha256Hex(wantJSON)
	if artifact != (ManifestArtifact{
		Role:      "screenshot",
		MediaType: "image/png",
		Storage:   StoragePointer,
		SHA256:    wantHash,
		Path:      "artifacts/pointers/sha256/" + wantHash[:2] + "/" + wantHash + ".json",
		Size:      0,
	}) {
		t.Fatalf("artifact = %#v", artifact)
	}
	if got := string(store.Files()[artifact.Path]); got != string(wantJSON) {
		t.Fatalf("pointer JSON = %q, want %q", got, wantJSON)
	}
	if strings.Contains(string(wantJSON), "screenshot") || strings.Contains(string(wantJSON), "image/png") {
		t.Fatalf("test fixture includes manifest metadata in pointer JSON: %s", wantJSON)
	}
}

func TestPointerUnknownSizeOmitsSize(t *testing.T) {
	store := New()

	artifact, err := store.AddPointer("transcript", "text/plain", Pointer{URI: "s3://bucket/transcript.txt"})
	if err != nil {
		t.Fatalf("AddPointer returned error: %v", err)
	}

	wantJSON := []byte(`{"version":1,"uri":"s3://bucket/transcript.txt"}`)
	wantHash := sha256Hex(wantJSON)
	if artifact.SHA256 != wantHash {
		t.Fatalf("hash = %q, want %q", artifact.SHA256, wantHash)
	}
	if got := string(store.Files()[artifact.Path]); got != string(wantJSON) {
		t.Fatalf("pointer JSON = %q, want %q", got, wantJSON)
	}
	if artifact.Size != 0 {
		t.Fatalf("manifest size = %d, want 0 for unknown", artifact.Size)
	}
}

func TestPointerDedupePreservesOneManifestRecordPerCall(t *testing.T) {
	store := New()
	size := int64(42)
	pointer := Pointer{URI: "s3://bucket/object", Size: &size}

	first, err := store.AddPointer("image", "image/png", pointer)
	if err != nil {
		t.Fatalf("first AddPointer returned error: %v", err)
	}
	second, err := store.AddPointer("log", "text/plain", pointer)
	if err != nil {
		t.Fatalf("second AddPointer returned error: %v", err)
	}
	third, err := store.AddPointer("image", "image/png", pointer)
	if err != nil {
		t.Fatalf("third AddPointer returned error: %v", err)
	}

	if len(store.Files()) != 1 {
		t.Fatalf("files len = %d, want 1", len(store.Files()))
	}
	if first.Path != second.Path || first.Path != third.Path {
		t.Fatalf("deduped paths differ: %q %q %q", first.Path, second.Path, third.Path)
	}
	want := []ManifestArtifact{first, second, third}
	if got := store.ManifestArtifacts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest records = %#v, want %#v", got, want)
	}
}

func TestContentAndPointerPathsCannotCollide(t *testing.T) {
	store := New()

	content, err := store.AddContent("artifact", "application/octet-stream", []byte("payload"))
	if err != nil {
		t.Fatalf("AddContent returned error: %v", err)
	}
	pointer, err := store.AddPointer("artifact", "application/octet-stream", Pointer{URI: "s3://bucket/payload"})
	if err != nil {
		t.Fatalf("AddPointer returned error: %v", err)
	}

	if content.Path == pointer.Path {
		t.Fatalf("content and pointer paths collide at %q", content.Path)
	}
	if !strings.HasPrefix(content.Path, "artifacts/sha256/") {
		t.Fatalf("content path = %q", content.Path)
	}
	if !strings.HasPrefix(pointer.Path, "artifacts/pointers/sha256/") {
		t.Fatalf("pointer path = %q", pointer.Path)
	}
}

func TestTrimsRoleAndMediaTypeInManifestRecords(t *testing.T) {
	store := New()

	artifact, err := store.AddContent(" plan.role ", " text/markdown ", []byte("content"))
	if err != nil {
		t.Fatalf("AddContent returned error: %v", err)
	}

	if artifact.Role != "plan.role" || artifact.MediaType != "text/markdown" {
		t.Fatalf("role/media type = %q/%q", artifact.Role, artifact.MediaType)
	}
}

func TestManifestArtifactsReturnsDefensiveCopy(t *testing.T) {
	store := New()

	artifact, err := store.AddContent("plan", "text/markdown", []byte("content"))
	if err != nil {
		t.Fatalf("AddContent returned error: %v", err)
	}
	records := store.ManifestArtifacts()
	records[0].Role = "mutated"

	got := store.ManifestArtifacts()
	if !reflect.DeepEqual(got, []ManifestArtifact{artifact}) {
		t.Fatalf("records = %#v, want original artifact", got)
	}
}

func TestRejectsInvalidContentMetadata(t *testing.T) {
	store := New()

	for _, role := range []string{"", "has space", "bad/slash", "bad:colon"} {
		if _, err := store.AddContent(role, "text/markdown", []byte("content")); !errors.Is(err, ErrInvalidRole) {
			t.Fatalf("AddContent role %q error = %v, want ErrInvalidRole", role, err)
		}
	}
	for _, mediaType := range []string{"", "text/\nplain", "application/\x00json"} {
		if _, err := store.AddContent("plan", mediaType, []byte("content")); !errors.Is(err, ErrInvalidMediaType) {
			t.Fatalf("AddContent media type %q error = %v, want ErrInvalidMediaType", mediaType, err)
		}
	}
	if _, err := store.AddContent("plan", "application/json", []byte("content")); err != nil {
		t.Fatalf("AddContent with slash media type returned error: %v", err)
	}
}

func TestRejectsInvalidPointers(t *testing.T) {
	store := New()
	negative := int64(-1)

	tests := []struct {
		name    string
		pointer Pointer
		want    error
	}{
		{name: "missing URI", pointer: Pointer{}, want: ErrInvalidPointer},
		{name: "bad hash length", pointer: Pointer{URI: "s3://bucket/object", SHA256: "abc"}, want: ErrInvalidHash},
		{name: "uppercase hash", pointer: Pointer{URI: "s3://bucket/object", SHA256: strings.Repeat("A", 64)}, want: ErrInvalidHash},
		{name: "negative size", pointer: Pointer{URI: "s3://bucket/object", Size: &negative}, want: ErrInvalidPointer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.AddPointer("artifact", "application/octet-stream", tt.pointer)
			if !errors.Is(err, tt.want) {
				t.Fatalf("AddPointer error = %v, want %v", err, tt.want)
			}
		})
	}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
