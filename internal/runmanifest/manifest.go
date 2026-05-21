package runmanifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
)

const manifestPath = "manifest.json"

var (
	ErrInvalidManifest      = errors.New("invalid manifest")
	ErrInvalidArtifact      = errors.New("invalid artifact")
	ErrMissingArtifact      = errors.New("missing artifact")
	ErrManifestCollision    = errors.New("manifest collision")
	ErrUnreferencedArtifact = errors.New("unreferenced artifact")
)

type Manifest struct {
	RunID           string
	Workflow        string
	WorkflowVersion string
	Created         time.Time
	Refs            map[string]string
	Stages          []Stage
}

type Stage struct {
	Name       string
	ProducedBy string
	GitSHA     string
	Skill      Skill
	Inputs     []ArtifactRef
	Output     ArtifactRef
	Timestamp  time.Time
}

type Skill struct {
	ID      string
	Repo    string
	Version string
}

type ArtifactRef struct {
	Role      string
	Artifact  string
	Path      string
	MediaType string
	Storage   string
	Size      int64
}

type Writer struct {
	Store refstore.Store
}

type WriteOptions struct {
	ExpectedOld string
	Message     string
}

func ArtifactFromManifestArtifact(a artifactstore.ManifestArtifact) ArtifactRef {
	return ArtifactRef{
		Role:      a.Role,
		Artifact:  a.SHA256,
		Path:      a.Path,
		MediaType: a.MediaType,
		Storage:   a.Storage,
		Size:      a.Size,
	}
}

func (m Manifest) Validate() error {
	if err := validateRunID(m.RunID); err != nil {
		return err
	}
	if err := validateIdentifier("workflow", m.Workflow); err != nil {
		return err
	}
	if strings.TrimSpace(m.WorkflowVersion) == "" {
		return fmt.Errorf("%w: workflow version required", ErrInvalidManifest)
	}
	if m.Created.IsZero() {
		return fmt.Errorf("%w: created required", ErrInvalidManifest)
	}
	for key, value := range m.Refs {
		if err := validateIdentifier("ref key", key); err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: ref %q value required", ErrInvalidManifest, key)
		}
	}
	if len(m.Stages) == 0 {
		return fmt.Errorf("%w: at least one stage required", ErrInvalidManifest)
	}
	for i, stage := range m.Stages {
		if err := validateStage(i, stage); err != nil {
			return err
		}
	}
	return nil
}

func (m Manifest) JSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m.toJSON()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (w Writer) Write(ctx context.Context, manifest Manifest, files map[string][]byte, opts WriteOptions) (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	if _, ok := files[manifestPath]; ok {
		return "", ErrManifestCollision
	}
	manifestBytes, err := manifest.JSON()
	if err != nil {
		return "", err
	}

	referenced := referencedArtifactPaths(manifest)
	hashes := make(map[string]string, len(referenced))
	for _, stage := range manifest.Stages {
		for _, input := range stage.Inputs {
			if err := verifyArtifactFile(input, files, hashes); err != nil {
				return "", err
			}
		}
		if err := verifyArtifactFile(stage.Output, files, hashes); err != nil {
			return "", err
		}
	}

	out := make(map[string][]byte, len(files)+1)
	for filePath, content := range files {
		if _, ok := referenced[filePath]; !ok {
			return "", fmt.Errorf("%w: %s", ErrUnreferencedArtifact, filePath)
		}
		out[filePath] = cloneBytes(content)
	}
	out[manifestPath] = manifestBytes

	return w.Store.WriteCommit(ctx, "refs/etude/runs/"+manifest.RunID, out, refstore.WriteOptions{
		ExpectedOld: opts.ExpectedOld,
		Message:     opts.Message,
	})
}

func validateStage(index int, stage Stage) error {
	prefix := fmt.Sprintf("stage[%d]", index)
	if err := validateIdentifier(prefix+".stage", stage.Name); err != nil {
		return err
	}
	if err := validateIdentifier(prefix+".produced_by", stage.ProducedBy); err != nil {
		return err
	}
	if strings.TrimSpace(stage.GitSHA) == "" {
		return fmt.Errorf("%w: %s git sha required", ErrInvalidManifest, prefix)
	}
	if strings.TrimSpace(stage.Skill.ID) == "" {
		return fmt.Errorf("%w: %s skill id required", ErrInvalidManifest, prefix)
	}
	if strings.TrimSpace(stage.Skill.Repo) == "" {
		return fmt.Errorf("%w: %s skill repo required", ErrInvalidManifest, prefix)
	}
	if strings.TrimSpace(stage.Skill.Version) == "" {
		return fmt.Errorf("%w: %s skill version required", ErrInvalidManifest, prefix)
	}
	if stage.Timestamp.IsZero() {
		return fmt.Errorf("%w: %s timestamp required", ErrInvalidManifest, prefix)
	}
	for i, input := range stage.Inputs {
		if err := validateArtifactRef(input); err != nil {
			return fmt.Errorf("%w: %s input[%d]: %v", ErrInvalidManifest, prefix, i, err)
		}
	}
	if err := validateArtifactRef(stage.Output); err != nil {
		return fmt.Errorf("%w: %s output: %v", ErrInvalidManifest, prefix, err)
	}
	return nil
}

func validateArtifactRef(artifact ArtifactRef) error {
	if err := validateIdentifier("artifact role", artifact.Role); err != nil {
		return err
	}
	if !validSHA256(artifact.Artifact) {
		return fmt.Errorf("%w: artifact address must be lowercase sha256", ErrInvalidArtifact)
	}
	if err := validateFilePath(artifact.Path); err != nil {
		return err
	}
	if err := validateMediaType(artifact.MediaType); err != nil {
		return err
	}
	if artifact.Storage != artifactstore.StorageContent && artifact.Storage != artifactstore.StoragePointer {
		return fmt.Errorf("%w: storage %q", ErrInvalidArtifact, artifact.Storage)
	}
	if artifact.Size < 0 {
		return fmt.Errorf("%w: negative size", ErrInvalidArtifact)
	}
	return nil
}

func verifyArtifactFile(artifact ArtifactRef, files map[string][]byte, hashes map[string]string) error {
	content, ok := files[artifact.Path]
	if !ok {
		return fmt.Errorf("%w: %s", ErrMissingArtifact, artifact.Path)
	}
	expectedPath := expectedArtifactPath(artifact.Storage, artifact.Artifact)
	if artifact.Path != expectedPath {
		return fmt.Errorf("%w: path %q does not match %s", ErrInvalidArtifact, artifact.Path, artifact.Artifact)
	}
	actual := hashes[artifact.Path]
	if actual == "" {
		sum := sha256.Sum256(content)
		actual = hex.EncodeToString(sum[:])
		hashes[artifact.Path] = actual
	}
	if actual != artifact.Artifact {
		return fmt.Errorf("%w: hash mismatch for %s", ErrInvalidArtifact, artifact.Path)
	}
	// Size is provenance metadata from artifactstore. For pointer artifacts it is
	// the external object size, not the pointer-record byte length.
	return nil
}

func expectedArtifactPath(storage, sum string) string {
	switch storage {
	case artifactstore.StorageContent:
		return "artifacts/sha256/" + sum[:2] + "/" + sum
	case artifactstore.StoragePointer:
		return "artifacts/pointers/sha256/" + sum[:2] + "/" + sum + ".json"
	default:
		return ""
	}
}

func referencedArtifactPaths(manifest Manifest) map[string]struct{} {
	paths := make(map[string]struct{})
	for _, stage := range manifest.Stages {
		for _, input := range stage.Inputs {
			paths[input.Path] = struct{}{}
		}
		paths[stage.Output.Path] = struct{}{}
	}
	return paths
}

func validateRunID(runID string) error {
	if err := validateIdentifier("run id", runID); err != nil {
		return err
	}
	if strings.HasPrefix(runID, ".") || strings.HasSuffix(runID, ".") ||
		strings.Contains(runID, "..") || strings.Trim(runID, ".") == "" ||
		strings.HasSuffix(runID, ".lock") {
		return fmt.Errorf("%w: invalid run id %q", ErrInvalidManifest, runID)
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s required", ErrInvalidManifest, name)
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return fmt.Errorf("%w: invalid %s %q", ErrInvalidManifest, name, value)
		}
	}
	return nil
}

func validateMediaType(mediaType string) error {
	if strings.TrimSpace(mediaType) == "" {
		return fmt.Errorf("%w: media type required", ErrInvalidArtifact)
	}
	for _, r := range mediaType {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: invalid media type %q", ErrInvalidArtifact, mediaType)
		}
	}
	return nil
}

func validateFilePath(filePath string) error {
	if filePath == "" || !utf8.ValidString(filePath) {
		return fmt.Errorf("%w: path %q", ErrInvalidArtifact, filePath)
	}
	if path.IsAbs(filePath) || path.Clean(filePath) != filePath || strings.HasPrefix(filePath, "../") || strings.Contains(filePath, "/../") {
		return fmt.Errorf("%w: path %q", ErrInvalidArtifact, filePath)
	}
	if filePath == "." || filePath == ".." || filePath == ".git" || strings.HasPrefix(filePath, ".git/") {
		return fmt.Errorf("%w: path %q", ErrInvalidArtifact, filePath)
	}
	if strings.ContainsAny(filePath, "\\:\x00,") {
		return fmt.Errorf("%w: path %q", ErrInvalidArtifact, filePath)
	}
	for _, segment := range strings.Split(filePath, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" {
			return fmt.Errorf("%w: path %q", ErrInvalidArtifact, filePath)
		}
	}
	return nil
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

type manifestJSON struct {
	RunID           string            `json:"run_id"`
	Workflow        string            `json:"workflow"`
	WorkflowVersion string            `json:"workflow_version"`
	Created         string            `json:"created"`
	Refs            map[string]string `json:"refs"`
	Stages          []stageJSON       `json:"stages"`
}

type stageJSON struct {
	Stage      string         `json:"stage"`
	ProducedBy string         `json:"produced_by"`
	GitSHA     string         `json:"git_sha"`
	Skill      skillJSON      `json:"skill"`
	Inputs     []artifactJSON `json:"inputs"`
	Output     artifactJSON   `json:"output"`
	Timestamp  string         `json:"timestamp"`
}

type skillJSON struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Version string `json:"version"`
}

type artifactJSON struct {
	Role      string `json:"role"`
	Artifact  string `json:"artifact"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Storage   string `json:"storage"`
	Size      int64  `json:"size"`
}

func (m Manifest) toJSON() manifestJSON {
	refs := m.Refs
	if refs == nil {
		refs = map[string]string{}
	}
	stages := make([]stageJSON, 0, len(m.Stages))
	for _, stage := range m.Stages {
		inputs := make([]artifactJSON, 0, len(stage.Inputs))
		for _, input := range stage.Inputs {
			inputs = append(inputs, input.toJSON())
		}
		stages = append(stages, stageJSON{
			Stage:      stage.Name,
			ProducedBy: stage.ProducedBy,
			GitSHA:     stage.GitSHA,
			Skill: skillJSON{
				ID:      stage.Skill.ID,
				Repo:    stage.Skill.Repo,
				Version: stage.Skill.Version,
			},
			Inputs:    inputs,
			Output:    stage.Output.toJSON(),
			Timestamp: formatTime(stage.Timestamp),
		})
	}
	return manifestJSON{
		RunID:           m.RunID,
		Workflow:        m.Workflow,
		WorkflowVersion: m.WorkflowVersion,
		Created:         formatTime(m.Created),
		Refs:            refs,
		Stages:          stages,
	}
}

func (a ArtifactRef) toJSON() artifactJSON {
	return artifactJSON{
		Role:      a.Role,
		Artifact:  a.Artifact,
		Path:      a.Path,
		MediaType: a.MediaType,
		Storage:   a.Storage,
		Size:      a.Size,
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
