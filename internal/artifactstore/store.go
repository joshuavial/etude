package artifactstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	StorageContent = "content"
	StoragePointer = "pointer"

	pointerVersion = 1
)

var (
	ErrInvalidRole      = errors.New("invalid role")
	ErrInvalidMediaType = errors.New("invalid media type")
	ErrInvalidPointer   = errors.New("invalid pointer")
	ErrInvalidHash      = errors.New("invalid hash")
)

// Store is a single-goroutine builder for artifact files and manifest records.
type Store struct {
	files     map[string][]byte
	artifacts []ManifestArtifact
}

type Pointer struct {
	URI    string
	SHA256 string
	Size   *int64
}

type ManifestArtifact struct {
	Role      string
	MediaType string
	Storage   string
	SHA256    string
	Path      string
	// Size is the raw content length for content artifacts. For pointer
	// artifacts it is the external object size, or zero when unknown.
	Size int64
}

type pointerRecord struct {
	Version int    `json:"version"`
	URI     string `json:"uri"`
	SHA256  string `json:"sha256,omitempty"`
	Size    *int64 `json:"size,omitempty"`
}

func New() *Store {
	return &Store{files: make(map[string][]byte)}
}

func (s *Store) AddContent(role, mediaType string, content []byte) (ManifestArtifact, error) {
	role, mediaType, err := validateMetadata(role, mediaType)
	if err != nil {
		return ManifestArtifact{}, err
	}

	sum := hashBytes(content)
	path := contentPath(sum)
	if _, ok := s.files[path]; !ok {
		s.files[path] = cloneBytes(content)
	}

	artifact := ManifestArtifact{
		Role:      role,
		MediaType: mediaType,
		Storage:   StorageContent,
		SHA256:    sum,
		Path:      path,
		Size:      int64(len(content)),
	}
	s.artifacts = append(s.artifacts, artifact)
	return artifact, nil
}

func (s *Store) AddPointer(role, mediaType string, pointer Pointer) (ManifestArtifact, error) {
	role, mediaType, err := validateMetadata(role, mediaType)
	if err != nil {
		return ManifestArtifact{}, err
	}

	record, externalSize, err := normalizePointer(pointer)
	if err != nil {
		return ManifestArtifact{}, err
	}
	payload, err := canonicalPointerJSON(record)
	if err != nil {
		return ManifestArtifact{}, err
	}

	sum := hashBytes(payload)
	path := pointerPath(sum)
	if _, ok := s.files[path]; !ok {
		s.files[path] = cloneBytes(payload)
	}

	artifact := ManifestArtifact{
		Role:      role,
		MediaType: mediaType,
		Storage:   StoragePointer,
		SHA256:    sum,
		Path:      path,
		Size:      externalSize,
	}
	s.artifacts = append(s.artifacts, artifact)
	return artifact, nil
}

func (s *Store) Files() map[string][]byte {
	out := make(map[string][]byte, len(s.files))
	for path, content := range s.files {
		out[path] = cloneBytes(content)
	}
	return out
}

func (s *Store) ManifestArtifacts() []ManifestArtifact {
	return append([]ManifestArtifact(nil), s.artifacts...)
}

func validateMetadata(role, mediaType string) (string, string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return "", "", ErrInvalidRole
	}
	for _, r := range role {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return "", "", fmt.Errorf("%w: %q", ErrInvalidRole, role)
		}
	}

	mediaType = strings.TrimSpace(mediaType)
	if mediaType == "" {
		return "", "", ErrInvalidMediaType
	}
	for _, r := range mediaType {
		if unicode.IsControl(r) {
			return "", "", fmt.Errorf("%w: %q", ErrInvalidMediaType, mediaType)
		}
	}

	return role, mediaType, nil
}

func normalizePointer(pointer Pointer) (pointerRecord, int64, error) {
	uri := strings.TrimSpace(pointer.URI)
	if uri == "" {
		return pointerRecord{}, 0, ErrInvalidPointer
	}
	sha := strings.TrimSpace(pointer.SHA256)
	if sha != "" && !validSHA256(sha) {
		return pointerRecord{}, 0, fmt.Errorf("%w: %s", ErrInvalidHash, sha)
	}
	var size int64
	if pointer.Size != nil {
		if *pointer.Size < 0 {
			return pointerRecord{}, 0, fmt.Errorf("%w: negative size", ErrInvalidPointer)
		}
		size = *pointer.Size
	}
	return pointerRecord{
		Version: pointerVersion,
		URI:     uri,
		SHA256:  sha,
		Size:    pointer.Size,
	}, size, nil
}

func canonicalPointerJSON(record pointerRecord) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(record); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func contentPath(sum string) string {
	return "artifacts/sha256/" + sum[:2] + "/" + sum
}

func pointerPath(sum string) string {
	return "artifacts/pointers/sha256/" + sum[:2] + "/" + sum + ".json"
}

func validSHA256(sum string) bool {
	if len(sum) != sha256.Size*2 {
		return false
	}
	for _, r := range sum {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func cloneBytes(in []byte) []byte {
	return append([]byte(nil), in...)
}
