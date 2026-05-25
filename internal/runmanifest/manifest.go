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
	// 0 = legacy/implicit v1 (no producer block); 2 = producer schema;
	// 3 = schema with gates.
	// (No v1 is ever emitted; the transition goes directly 0→2, then 2→3.)
	ManifestVersion int
	RunID           string
	Workflow        string
	WorkflowVersion string
	Created         time.Time
	Refs            map[string]string
	Stages          []Stage
	Gates           []GateAttempt
}

// GateAttempt records one full panel re-examination of one phase gate.
// Every rerun (BLOCK -> incorporate -> rerun) is a new GateAttempt with an
// incremented Round; prior rounds are retained as the queryable history.
type GateAttempt struct {
	GateID         string
	Phase          string
	Round          int
	Tier           int
	Status         GateStatus
	ReviewedStages []ReviewedRef
	Seats          []SeatResult
	Decision       GateDecision
	Timestamp      time.Time
}

// ReviewedRef ties a gate attempt to the exact stage/artifact it reviewed.
// Artifact is optional (a gate may reference a stage by name only); when set it
// is the same content-addressed sha the stage's Output/Input carries.
type ReviewedRef struct {
	Stage    string
	Artifact string
	Role     string
}

// SeatResult is one reviewer seat's outcome within one gate attempt.
type SeatResult struct {
	Seat        string
	Harness     Harness
	Provider    Provider
	Skill       Skill
	Verdict     SeatVerdict
	Required    []string
	Optional    []string
	RawOutput   *ArtifactRef
	FailureNote string
	Timestamp   time.Time
}

// Provider names the model provider and model for a reviewer seat.
type Provider struct {
	Name  string
	Model string
}

// GateDecision holds the aggregate decision detail of one gate attempt.
type GateDecision struct {
	EscalationReason string
	DegradedReason   string
	DeferredBeads    []string
}

// GateStatus is the aggregate status of one gate attempt.
type GateStatus string

// SeatVerdict is the outcome of one reviewer seat.
type SeatVerdict string

const (
	GateStatusPass      GateStatus = "pass"
	GateStatusRerun     GateStatus = "rerun"
	GateStatusEscalated GateStatus = "escalated"

	SeatVerdictGo          SeatVerdict = "go"
	SeatVerdictBlock       SeatVerdict = "block"
	SeatVerdictFailed      SeatVerdict = "failed"
	SeatVerdictEmpty       SeatVerdict = "empty"
	SeatVerdictMalfunction SeatVerdict = "malfunction"
	SeatVerdictDisregarded SeatVerdict = "disregarded"
)

// ReplayLink records the durable source identity for a produced_by:"replay" stage.
// RunID and Stage name the source run and stage; Commit pins the immutable source
// commit so the link remains resolvable even after future appends to the source run.
type ReplayLink struct {
	RunID  string
	Stage  string
	Commit string
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
	// ReplayOf, when non-nil, identifies the source run/stage this stage was
	// replayed from. Required when ProducedBy=="replay"; forbidden otherwise.
	ReplayOf *ReplayLink
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

	// Build a stage index for referential integrity checks in gate validation.
	stageIndex := make(map[string]Stage, len(m.Stages))
	for _, stage := range m.Stages {
		stageIndex[stage.Name] = stage
	}

	seenGateIDs := make(map[string]struct{}, len(m.Gates))
	seenPhaseRound := make(map[string]struct{}, len(m.Gates))
	for i, gate := range m.Gates {
		if _, dup := seenGateIDs[gate.GateID]; dup {
			return fmt.Errorf("%w: duplicate gate_id %q", ErrInvalidManifest, gate.GateID)
		}
		seenGateIDs[gate.GateID] = struct{}{}

		phaseRoundKey := gate.Phase + "\x00" + fmt.Sprintf("%d", gate.Round)
		if _, dup := seenPhaseRound[phaseRoundKey]; dup {
			return fmt.Errorf("%w: duplicate (phase, round) (%q, %d)", ErrInvalidManifest, gate.Phase, gate.Round)
		}
		seenPhaseRound[phaseRoundKey] = struct{}{}

		if err := validateGate(i, gate, stageIndex); err != nil {
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
	for _, gate := range manifest.Gates {
		for _, seat := range gate.Seats {
			if seat.RawOutput != nil {
				seen[seat.RawOutput.Path] = struct{}{}
			}
		}
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
	// Bidirectional replay_of / produced_by constraint.
	if stage.ProducedBy == "replay" && stage.ReplayOf == nil {
		return fmt.Errorf("%w: %s produced_by \"replay\" requires replay_of", ErrInvalidManifest, prefix)
	}
	if stage.ReplayOf != nil && stage.ProducedBy != "replay" {
		return fmt.Errorf("%w: %s replay_of only allowed when produced_by is \"replay\"", ErrInvalidManifest, prefix)
	}
	if stage.ReplayOf != nil {
		if !IsValidRunID(stage.ReplayOf.RunID) {
			return fmt.Errorf("%w: %s replay_of.run_id invalid", ErrInvalidManifest, prefix)
		}
		if err := validateIdentifier(prefix+".replay_of.stage", stage.ReplayOf.Stage); err != nil {
			return err
		}
		if !isHexOID(stage.ReplayOf.Commit) {
			return fmt.Errorf("%w: %s replay_of.commit must be a 40- or 64-char lowercase hex git oid", ErrInvalidManifest, prefix)
		}
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

func validateGate(index int, gate GateAttempt, stageIndex map[string]Stage) error {
	prefix := fmt.Sprintf("gate[%d]", index)

	if err := validateIdentifier(prefix+".gate_id", gate.GateID); err != nil {
		return err
	}
	if err := validateIdentifier(prefix+".phase", gate.Phase); err != nil {
		return err
	}
	if gate.Round < 1 {
		return fmt.Errorf("%w: %s round must be >= 1", ErrInvalidManifest, prefix)
	}
	if gate.Tier < 0 || gate.Tier > 3 {
		return fmt.Errorf("%w: %s tier must be in {0, 1, 2, 3}", ErrInvalidManifest, prefix)
	}
	if !isGateStatus(gate.Status) {
		return fmt.Errorf("%w: %s status %q is not one of {pass, rerun, escalated}", ErrInvalidManifest, prefix, gate.Status)
	}
	if gate.Status == GateStatusEscalated && strings.TrimSpace(gate.Decision.EscalationReason) == "" {
		return fmt.Errorf("%w: %s escalation_reason required when status is escalated", ErrInvalidManifest, prefix)
	}
	if len(gate.ReviewedStages) == 0 {
		return fmt.Errorf("%w: %s at least one reviewed_stage required", ErrInvalidManifest, prefix)
	}
	for i, ref := range gate.ReviewedStages {
		refPrefix := fmt.Sprintf("%s.reviewed_stages[%d]", prefix, i)
		if err := validateIdentifier(refPrefix+".stage", ref.Stage); err != nil {
			return err
		}
		stage, ok := stageIndex[ref.Stage]
		if !ok {
			return fmt.Errorf("%w: %s stage %q not found in manifest", ErrInvalidManifest, refPrefix, ref.Stage)
		}
		if ref.Role != "" {
			if err := validateIdentifier(refPrefix+".role", ref.Role); err != nil {
				return err
			}
		}
		if ref.Artifact != "" {
			if !validSHA256(ref.Artifact) {
				return fmt.Errorf("%w: %s artifact must be a lowercase sha256", ErrInvalidManifest, refPrefix)
			}
			// Artifact must match the stage's output or one of its inputs.
			if !stageHasArtifact(stage, ref.Artifact) {
				return fmt.Errorf("%w: %s artifact %q not found on stage %q output or inputs", ErrInvalidManifest, refPrefix, ref.Artifact, ref.Stage)
			}
		}
	}
	if len(gate.Seats) == 0 {
		return fmt.Errorf("%w: %s at least one seat required", ErrInvalidManifest, prefix)
	}
	for i, seat := range gate.Seats {
		if err := validateSeat(prefix, i, seat); err != nil {
			return err
		}
	}
	if gate.Timestamp.IsZero() {
		return fmt.Errorf("%w: %s timestamp required", ErrInvalidManifest, prefix)
	}
	return nil
}

func stageHasArtifact(stage Stage, artifact string) bool {
	if stage.Output.Artifact == artifact {
		return true
	}
	for _, input := range stage.Inputs {
		if input.Artifact == artifact {
			return true
		}
	}
	return false
}

func validateSeat(gatePrefix string, index int, seat SeatResult) error {
	prefix := fmt.Sprintf("%s.seat[%d]", gatePrefix, index)

	if err := validateIdentifier(prefix+".seat", seat.Seat); err != nil {
		return err
	}
	if strings.TrimSpace(seat.Harness.Name) == "" {
		return fmt.Errorf("%w: %s harness.name required", ErrInvalidManifest, prefix)
	}
	if err := validateProviderField(prefix, seat.Provider); err != nil {
		return err
	}
	if !isSeatVerdict(seat.Verdict) {
		return fmt.Errorf("%w: %s verdict %q is not one of {go, block, failed, empty, malfunction, disregarded}", ErrInvalidManifest, prefix, seat.Verdict)
	}
	// failure_note required iff verdict in {failed, empty, malfunction, disregarded}
	// failure_note forbidden iff verdict in {go, block}
	switch seat.Verdict {
	case SeatVerdictFailed, SeatVerdictEmpty, SeatVerdictMalfunction, SeatVerdictDisregarded:
		if strings.TrimSpace(seat.FailureNote) == "" {
			return fmt.Errorf("%w: %s failure_note required for verdict %q", ErrInvalidManifest, prefix, seat.Verdict)
		}
	case SeatVerdictGo, SeatVerdictBlock:
		if seat.FailureNote != "" {
			return fmt.Errorf("%w: %s failure_note must be empty for verdict %q", ErrInvalidManifest, prefix, seat.Verdict)
		}
	}
	if seat.RawOutput != nil {
		if err := validateArtifactRef(*seat.RawOutput); err != nil {
			return fmt.Errorf("%w: %s raw_output: %v", ErrInvalidManifest, prefix, err)
		}
	}
	if seat.Timestamp.IsZero() {
		return fmt.Errorf("%w: %s timestamp required", ErrInvalidManifest, prefix)
	}
	return nil
}

func validateProviderField(prefix string, p Provider) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: %s provider.name required", ErrInvalidManifest, prefix)
	}
	if strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("%w: %s provider.model required", ErrInvalidManifest, prefix)
	}
	for _, r := range p.Name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s provider.name contains control character", ErrInvalidManifest, prefix)
		}
	}
	for _, r := range p.Model {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s provider.model contains control character", ErrInvalidManifest, prefix)
		}
	}
	return nil
}

func isGateStatus(s GateStatus) bool {
	return s == GateStatusPass || s == GateStatusRerun || s == GateStatusEscalated
}

func isSeatVerdict(v SeatVerdict) bool {
	switch v {
	case SeatVerdictGo, SeatVerdictBlock, SeatVerdictFailed, SeatVerdictEmpty, SeatVerdictMalfunction, SeatVerdictDisregarded:
		return true
	}
	return false
}

// isHexOID reports whether s is a valid git object id: non-empty, all lowercase
// hex characters, and exactly 40 (SHA-1) or 64 (SHA-256) characters long.
func isHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
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
	for _, gate := range manifest.Gates {
		for _, seat := range gate.Seats {
			if seat.RawOutput != nil {
				paths[seat.RawOutput.Path] = struct{}{}
			}
		}
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
	// 0 = legacy/implicit v1 (no producer block); 2 = producer schema;
	// 3 = schema with gates.
	// (No v1 is ever emitted; the transition goes directly 0→2, then 2→3.)
	ManifestVersion int               `json:"manifest_version,omitempty"`
	RunID           string            `json:"run_id"`
	Workflow        string            `json:"workflow"`
	WorkflowVersion string            `json:"workflow_version"`
	Created         string            `json:"created"`
	Refs            map[string]string `json:"refs"`
	Stages          []stageJSON       `json:"stages"`
	Gates           []gateJSON        `json:"gates,omitempty"`
}

type gateJSON struct {
	GateID         string            `json:"gate_id"`
	Phase          string            `json:"phase"`
	Round          int               `json:"round"`
	Tier           int               `json:"tier"`
	Status         string            `json:"status"`
	ReviewedStages []reviewedRefJSON `json:"reviewed_stages"`
	Seats          []seatResultJSON  `json:"seats"`
	Decision       gateDecisionJSON  `json:"decision,omitempty"`
	Timestamp      string            `json:"timestamp"`
}

type reviewedRefJSON struct {
	Stage    string `json:"stage"`
	Role     string `json:"role,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

type seatResultJSON struct {
	Seat        string        `json:"seat"`
	Harness     harnessJSON   `json:"harness"`
	Provider    providerJSON  `json:"provider"`
	Skill       *skillJSON    `json:"skill,omitempty"`
	Verdict     string        `json:"verdict"`
	Required    []string      `json:"required,omitempty"`
	Optional    []string      `json:"optional,omitempty"`
	RawOutput   *artifactJSON `json:"raw_output,omitempty"`
	FailureNote string        `json:"failure_note,omitempty"`
	Timestamp   string        `json:"timestamp"`
}

type providerJSON struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

type gateDecisionJSON struct {
	EscalationReason string   `json:"escalation_reason,omitempty"`
	DegradedReason   string   `json:"degraded_reason,omitempty"`
	DeferredBeads    []string `json:"deferred_beads,omitempty"`
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
	ReplayOf  *replayOfJSON  `json:"replay_of,omitempty"`
}

// replayOfJSON is the wire representation of ReplayLink in the manifest JSON.
// All three fields are required when the object is present; only the object
// itself is omitempty so that absent replay_of is omitted from the document.
type replayOfJSON struct {
	RunID  string `json:"run_id"`
	Stage  string `json:"stage"`
	Commit string `json:"commit"`
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

		var replayOfBlock *replayOfJSON
		if stage.ReplayOf != nil {
			replayOfBlock = &replayOfJSON{
				RunID:  stage.ReplayOf.RunID,
				Stage:  stage.ReplayOf.Stage,
				Commit: stage.ReplayOf.Commit,
			}
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
			ReplayOf:  replayOfBlock,
		})
	}
	version := 2
	var gatesOut []gateJSON
	if len(m.Gates) > 0 {
		version = 3
		gatesOut = make([]gateJSON, 0, len(m.Gates))
		for _, gate := range m.Gates {
			gatesOut = append(gatesOut, gate.toJSON())
		}
	}

	return manifestJSON{
		ManifestVersion: version,
		RunID:           m.RunID,
		Workflow:        m.Workflow,
		WorkflowVersion: m.WorkflowVersion,
		Created:         formatTime(m.Created),
		Refs:            refs,
		Stages:          stages,
		Gates:           gatesOut,
	}
}

func (g GateAttempt) toJSON() gateJSON {
	refs := make([]reviewedRefJSON, 0, len(g.ReviewedStages))
	for _, r := range g.ReviewedStages {
		refs = append(refs, reviewedRefJSON{
			Stage:    r.Stage,
			Role:     r.Role,
			Artifact: r.Artifact,
		})
	}
	seats := make([]seatResultJSON, 0, len(g.Seats))
	for _, seat := range g.Seats {
		seats = append(seats, seat.toJSON())
	}
	return gateJSON{
		GateID:         g.GateID,
		Phase:          g.Phase,
		Round:          g.Round,
		Tier:           g.Tier,
		Status:         string(g.Status),
		ReviewedStages: refs,
		Seats:          seats,
		Decision:       g.Decision.toJSON(),
		Timestamp:      formatTime(g.Timestamp),
	}
}

func (s SeatResult) toJSON() seatResultJSON {
	sj := seatResultJSON{
		Seat: s.Seat,
		Harness: harnessJSON{
			Name:    s.Harness.Name,
			Version: s.Harness.Version,
		},
		Provider: providerJSON{
			Name:  s.Provider.Name,
			Model: s.Provider.Model,
		},
		Verdict:     string(s.Verdict),
		Required:    s.Required,
		Optional:    s.Optional,
		FailureNote: s.FailureNote,
		Timestamp:   formatTime(s.Timestamp),
	}
	if s.Skill.ID != "" {
		sj.Skill = &skillJSON{
			ID:      s.Skill.ID,
			Repo:    s.Skill.Repo,
			Version: s.Skill.Version,
		}
	}
	if s.RawOutput != nil {
		aj := s.RawOutput.toJSON()
		sj.RawOutput = &aj
	}
	return sj
}

func (d GateDecision) toJSON() gateDecisionJSON {
	return gateDecisionJSON{
		EscalationReason: d.EscalationReason,
		DegradedReason:   d.DegradedReason,
		DeferredBeads:    d.DeferredBeads,
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
	// Version allowlist: accept 0 (legacy), 2 (producer schema), 3 (with gates).
	// Reject 1 (never emitted) and any future version this binary cannot model.
	switch m.ManifestVersion {
	case 0, 2, 3:
		// accepted
	default:
		return Manifest{}, fmt.Errorf("%w: unsupported manifest_version %d (accepted: 0, 2, 3)", ErrInvalidManifest, m.ManifestVersion)
	}

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
	gates := make([]GateAttempt, 0, len(m.Gates))
	for i, g := range m.Gates {
		gate, err := g.toGate(i)
		if err != nil {
			return Manifest{}, err
		}
		gates = append(gates, gate)
	}
	return Manifest{
		ManifestVersion: m.ManifestVersion,
		RunID:           m.RunID,
		Workflow:        m.Workflow,
		WorkflowVersion: m.WorkflowVersion,
		Created:         created,
		Refs:            refs,
		Stages:          stages,
		Gates:           gates,
	}, nil
}

func (g gateJSON) toGate(index int) (GateAttempt, error) {
	ts, err := parseTime(fmt.Sprintf("gate[%d].timestamp", index), g.Timestamp)
	if err != nil {
		return GateAttempt{}, err
	}
	refs := make([]ReviewedRef, 0, len(g.ReviewedStages))
	for _, r := range g.ReviewedStages {
		refs = append(refs, ReviewedRef{
			Stage:    r.Stage,
			Artifact: r.Artifact,
			Role:     r.Role,
		})
	}
	seats := make([]SeatResult, 0, len(g.Seats))
	for i, s := range g.Seats {
		seat, err := s.toSeatResult(index, i)
		if err != nil {
			return GateAttempt{}, err
		}
		seats = append(seats, seat)
	}
	return GateAttempt{
		GateID:         g.GateID,
		Phase:          g.Phase,
		Round:          g.Round,
		Tier:           g.Tier,
		Status:         GateStatus(g.Status),
		ReviewedStages: refs,
		Seats:          seats,
		Decision: GateDecision{
			EscalationReason: g.Decision.EscalationReason,
			DegradedReason:   g.Decision.DegradedReason,
			DeferredBeads:    g.Decision.DeferredBeads,
		},
		Timestamp: ts,
	}, nil
}

func (s seatResultJSON) toSeatResult(gateIndex, seatIndex int) (SeatResult, error) {
	ts, err := parseTime(fmt.Sprintf("gate[%d].seat[%d].timestamp", gateIndex, seatIndex), s.Timestamp)
	if err != nil {
		return SeatResult{}, err
	}
	var skill Skill
	if s.Skill != nil {
		skill = Skill{ID: s.Skill.ID, Repo: s.Skill.Repo, Version: s.Skill.Version}
	}
	var rawOutput *ArtifactRef
	if s.RawOutput != nil {
		a := s.RawOutput.toArtifactRef()
		rawOutput = &a
	}
	return SeatResult{
		Seat: s.Seat,
		Harness: Harness{
			Name:    s.Harness.Name,
			Version: s.Harness.Version,
		},
		Provider: Provider{
			Name:  s.Provider.Name,
			Model: s.Provider.Model,
		},
		Skill:       skill,
		Verdict:     SeatVerdict(s.Verdict),
		Required:    s.Required,
		Optional:    s.Optional,
		RawOutput:   rawOutput,
		FailureNote: s.FailureNote,
		Timestamp:   ts,
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

	var replayOf *ReplayLink
	if s.ReplayOf != nil {
		replayOf = &ReplayLink{
			RunID:  s.ReplayOf.RunID,
			Stage:  s.ReplayOf.Stage,
			Commit: s.ReplayOf.Commit,
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
		ReplayOf:   replayOf,
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
