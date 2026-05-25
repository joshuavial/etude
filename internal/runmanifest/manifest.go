package runmanifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
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
	// ManifestVersion versions the on-disk document format.
	// 0 = legacy/implicit v1 (no producer block); 2 = producer schema.
	// (No v1 is ever emitted; the transition goes directly 0→2.)
	ManifestVersion int
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
	// Skill is the per-stage skill identity. For new manifests (manifest_version 2)
	// it mirrors Producer.Skill; for legacy manifests it holds the lifted top-level
	// skill block. Kept so capture.go / run.go / replay compile unmodified.
	Skill     Skill
	Producer  Producer
	Inputs    []ArtifactRef
	Output    ArtifactRef
	Timestamp time.Time
}

type Skill struct {
	ID      string
	Repo    string
	Version string
}

// Harness identifies the agent runtime that executed a stage (e.g. claude-code).
type Harness struct {
	Name    string
	Version string
}

// Producer records who produced a stage: the agent runtime (Harness), the LLM
// (Model), and the skill (Skill). These are three of the four provenance axes;
// the fourth — Workflow — is recorded at the top level of the manifest:
//   - Harness: the agent runtime, e.g. "claude-code"
//   - Model:   the LLM, e.g. "claude-opus-4-7"
//   - Skill:   the per-stage skill identity {id, repo, version}
//
// Harness and Model are populated by the capture adapter (bead 1.2).
// Empty Harness/Model values are valid for this schema bead (1.1).
type Producer struct {
	Harness Harness
	Model   string
	Skill   Skill
}

type ArtifactRef struct {
	Role      string
	Artifact  string
	Path      string
	MediaType string
	Storage   artifactstore.StorageType
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

func ParseJSON(content []byte) (Manifest, error) {
	var payload manifestJSON
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode: %v", ErrInvalidManifest, err)
	}
	if err := ensureEOF(dec); err != nil {
		return Manifest{}, err
	}
	manifest, err := payload.toManifest()
	if err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ArtifactPaths(manifest Manifest) []string {
	seen := make(map[string]struct{})
	for _, stage := range manifest.Stages {
		for _, input := range stage.Inputs {
			seen[input.Path] = struct{}{}
		}
		seen[stage.Output.Path] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
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

func expectedArtifactPath(storage artifactstore.StorageType, sum string) string {
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

// IsValidIdentifier reports whether value is a non-empty string using only
// the [A-Za-z0-9_.-] character set.
func IsValidIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// IsValidRunID reports whether value is a valid run identifier: it must
// satisfy IsValidIdentifier and additionally must not have a leading dot,
// trailing dot, contain "..", consist entirely of dots, or end with ".lock".
func IsValidRunID(value string) bool {
	if !IsValidIdentifier(value) {
		return false
	}
	return !strings.HasPrefix(value, ".") &&
		!strings.HasSuffix(value, ".") &&
		!strings.Contains(value, "..") &&
		strings.Trim(value, ".") != "" &&
		!strings.HasSuffix(value, ".lock")
}

func validateRunID(runID string) error {
	if err := validateIdentifier("run id", runID); err != nil {
		return err
	}
	if !IsValidRunID(runID) {
		return fmt.Errorf("%w: invalid run id %q", ErrInvalidManifest, runID)
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s required", ErrInvalidManifest, name)
	}
	if !IsValidIdentifier(value) {
		return fmt.Errorf("%w: invalid %s %q", ErrInvalidManifest, name, value)
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
	if strings.ContainsAny(filePath, "\\:,") {
		return fmt.Errorf("%w: path %q", ErrInvalidArtifact, filePath)
	}
	// NUL and every other control character are rejected by the IsControl scan.
	for _, r := range filePath {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: path %q", ErrInvalidArtifact, filePath)
		}
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
	// ManifestVersion versions the on-disk document format.
	// 0 = legacy/implicit v1 (no producer block); 2 = producer schema.
	// (No v1 is ever emitted; the transition goes directly 0→2.)
	ManifestVersion int               `json:"manifest_version,omitempty"`
	RunID           string            `json:"run_id"`
	Workflow        string            `json:"workflow"`
	WorkflowVersion string            `json:"workflow_version"`
	Created         string            `json:"created"`
	Refs            map[string]string `json:"refs"`
	Stages          []stageJSON       `json:"stages"`
}

type stageJSON struct {
	Stage      string `json:"stage"`
	ProducedBy string `json:"produced_by"`
	GitSHA     string `json:"git_sha"`
	// Skill is present only in legacy manifests (no producer block).
	// New manifests omit it; the skill travels inside producer.
	Skill     *skillJSON     `json:"skill,omitempty"`
	Producer  *producerJSON  `json:"producer,omitempty"`
	Inputs    []artifactJSON `json:"inputs"`
	Output    artifactJSON   `json:"output"`
	Timestamp string         `json:"timestamp"`
}

type skillJSON struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Version string `json:"version"`
}

type harnessJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type producerJSON struct {
	Harness *harnessJSON `json:"harness,omitempty"`
	Model   string       `json:"model,omitempty"`
	Skill   *skillJSON   `json:"skill,omitempty"`
}

type artifactJSON struct {
	Role      string                    `json:"role"`
	Artifact  string                    `json:"artifact"`
	Path      string                    `json:"path"`
	MediaType string                    `json:"media_type"`
	Storage   artifactstore.StorageType `json:"storage"`
	Size      int64                     `json:"size"`
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

		// Derive the canonical skill for the producer block: Producer.Skill is
		// authoritative; fall back to Stage.Skill when Producer.Skill is empty
		// so the two fields stay coherent.
		skillForProducer := stage.Producer.Skill
		if skillForProducer.ID == "" {
			skillForProducer = stage.Skill
		}

		// Encode the harness block only when at least Name is set (avoids emitting
		// an empty harness object for legacy/partially-populated stages).
		var harnessBlock *harnessJSON
		if stage.Producer.Harness.Name != "" {
			harnessBlock = &harnessJSON{
				Name:    stage.Producer.Harness.Name,
				Version: stage.Producer.Harness.Version,
			}
		}

		producerBlock := &producerJSON{
			Harness: harnessBlock,
			Model:   stage.Producer.Model,
			Skill: &skillJSON{
				ID:      skillForProducer.ID,
				Repo:    skillForProducer.Repo,
				Version: skillForProducer.Version,
			},
		}

		stages = append(stages, stageJSON{
			Stage:      stage.Name,
			ProducedBy: stage.ProducedBy,
			GitSHA:     stage.GitSHA,
			// Do NOT emit top-level skill — it lives inside producer only.
			Producer:  producerBlock,
			Inputs:    inputs,
			Output:    stage.Output.toJSON(),
			Timestamp: formatTime(stage.Timestamp),
		})
	}
	return manifestJSON{
		ManifestVersion: 2,
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

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidManifest, err)
	}
	return fmt.Errorf("%w: trailing data", ErrInvalidManifest)
}

func (m manifestJSON) toManifest() (Manifest, error) {
	created, err := parseTime("created", m.Created)
	if err != nil {
		return Manifest{}, err
	}
	stages := make([]Stage, 0, len(m.Stages))
	for i, stage := range m.Stages {
		converted, err := stage.toStage(i)
		if err != nil {
			return Manifest{}, err
		}
		stages = append(stages, converted)
	}
	refs := make(map[string]string, len(m.Refs))
	for key, value := range m.Refs {
		refs[key] = value
	}
	return Manifest{
		ManifestVersion: m.ManifestVersion,
		RunID:           m.RunID,
		Workflow:        m.Workflow,
		WorkflowVersion: m.WorkflowVersion,
		Created:         created,
		Refs:            refs,
		Stages:          stages,
	}, nil
}

func (s stageJSON) toStage(index int) (Stage, error) {
	timestamp, err := parseTime(fmt.Sprintf("stage[%d].timestamp", index), s.Timestamp)
	if err != nil {
		return Stage{}, err
	}
	inputs := make([]ArtifactRef, 0, len(s.Inputs))
	for _, input := range s.Inputs {
		inputs = append(inputs, input.toArtifactRef())
	}

	var skill Skill
	var producer Producer

	if s.Producer != nil {
		// New manifest (manifest_version 2): producer is authoritative.
		// Mirror producer.skill into Stage.Skill so run.go / replay keep working.
		if s.Producer.Skill != nil {
			skill = Skill{
				ID:      s.Producer.Skill.ID,
				Repo:    s.Producer.Skill.Repo,
				Version: s.Producer.Skill.Version,
			}
		}
		var harness Harness
		if s.Producer.Harness != nil {
			harness = Harness{
				Name:    s.Producer.Harness.Name,
				Version: s.Producer.Harness.Version,
			}
		}
		producer = Producer{
			Harness: harness,
			Model:   s.Producer.Model,
			Skill:   skill,
		}
	} else if s.Skill != nil {
		// Legacy manifest: top-level skill block only, no producer.
		// Lift it into BOTH Stage.Skill AND Stage.Producer.Skill; Harness/Model empty.
		skill = Skill{
			ID:      s.Skill.ID,
			Repo:    s.Skill.Repo,
			Version: s.Skill.Version,
		}
		producer = Producer{
			Skill: skill,
		}
	}

	return Stage{
		Name:       s.Stage,
		ProducedBy: s.ProducedBy,
		GitSHA:     s.GitSHA,
		Skill:      skill,
		Producer:   producer,
		Inputs:     inputs,
		Output:     s.Output.toArtifactRef(),
		Timestamp:  timestamp,
	}, nil
}

func (a artifactJSON) toArtifactRef() ArtifactRef {
	return ArtifactRef{
		Role:      a.Role,
		Artifact:  a.Artifact,
		Path:      a.Path,
		MediaType: a.MediaType,
		Storage:   a.Storage,
		Size:      a.Size,
	}
}

func parseTime(field, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s: %v", ErrInvalidManifest, field, err)
	}
	return t.UTC(), nil
}
