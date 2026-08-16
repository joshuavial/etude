package replay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactmedia"
	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/sessionevidence"
)

// RecordedRun is the result of RunRecorder.Record: the identity of the new
// linked replay run and a pin to its output artifact.
type RecordedRun struct {
	// RunID is the allocated replay run identifier.
	RunID string
	// Commit is the git commit OID written for the new run ref.
	Commit string
	// OutputArtifact is the SHA-256 hex of the replay output artifact.
	OutputArtifact string
	// OutputPath is the artifact path in the commit tree.
	OutputPath string
}

// RunRecorder persists a new replay run that links back to a source run/stage.
// It encapsulates the record-a-new-linked-replay-run logic that was previously
// unexported on cli.replayRunner, making it reusable by both cli and bench.
type RunRecorder struct {
	Store refstore.Store
	// Now returns the current time. Defaults to time.Now when nil; tests inject a
	// fixed clock to make replay run ids deterministic.
	Now func() time.Time
	// SessionScratchDir and SessionWorktreeDir are the containment roots from
	// the runner invocation. They remain live through Record and let a reported
	// transcript_path be imported without trusting an arbitrary filesystem path.
	SessionScratchDir  string
	SessionWorktreeDir string
}

// Record writes a new linked replay run and returns its identity.
//
// Flow:
//  1. Allocate a unique replay run id using AllocateRunID.
//  2. Add the replay output as a content artifact via artifactstore.
//  3. Copy each source input raw from resolved.Commit (handles pointer records).
//  4. Build the Stage with ProducedBy:"replay" and a ReplayOf link pinned to
//     resolved.Commit (the immutable source snapshot).
//  5. Write the manifest via runmanifest.Writer.
func (r RunRecorder) Record(
	ctx context.Context,
	sourceRunID, sourceStageName string,
	resolved ResolvedStage,
	res RunResult,
) (RecordedRun, error) {
	clockFn := r.Now
	if clockFn == nil {
		clockFn = time.Now
	}
	now := clockFn().UTC()

	// Allocate a unique run id derived from source id + timestamp.
	baseID := fmt.Sprintf("%s-replay-%s", sourceRunID, now.Format("20060102T150405Z"))
	replayRunID, err := AllocateRunID(ctx, r.Store, baseID)
	if err != nil {
		return RecordedRun{}, err
	}

	// Store the output bytes and seed the files map.
	as := artifactstore.New()
	outputArtifact, err := as.AddContent(resolved.Output.Role, res.MediaType, res.Output)
	if err != nil {
		return RecordedRun{}, fmt.Errorf("record: store output artifact: %w", err)
	}
	var logRef *runmanifest.ArtifactRef
	if len(res.Log) > 0 {
		if len(res.Log) > MaxStageLogBytes {
			return RecordedRun{}, fmt.Errorf("record: runner log exceeds %d bytes", MaxStageLogBytes)
		}
		logArtifact, err := as.AddContent(sourceStageName+"-log", "application/octet-stream", res.Log)
		if err != nil {
			return RecordedRun{}, fmt.Errorf("record: store log artifact: %w", err)
		}
		ref := runmanifest.ArtifactFromManifestArtifact(logArtifact)
		logRef = &ref
	}

	producer := res.Producer
	if res.Session != nil {
		if err := ValidateSessionInfo(res.Session); err != nil {
			return RecordedRun{}, fmt.Errorf("record: runner session: %w", err)
		}
		producer.Session = r.buildSessionEvidence(as, sourceStageName+"-transcript", res.Session)
	}
	files := as.Files()

	// Copy each source input raw from the source commit. Using ReadCommitFile
	// gives us the raw stored bytes (correct for both content blobs and
	// pointer-record JSON), bypassing ReadContent which would fail on pointer
	// artifacts.
	for _, inp := range resolved.ResolvedInputs {
		rawBytes, err := r.Store.ReadCommitFile(ctx, resolved.Commit, inp.ArtifactRef.Path)
		if err != nil {
			return RecordedRun{}, fmt.Errorf("record: read source input %q: %w", inp.ArtifactRef.Role, err)
		}
		files[inp.ArtifactRef.Path] = rawBytes
	}

	// Build the stage. Both Stage.Skill and Stage.Producer must be set
	// (mirrors capture.go's pattern; validateStage requires Skill fields).
	skill := producer.Skill
	stage := runmanifest.Stage{
		Name:       sourceStageName,
		ProducedBy: "replay",
		GitSHA:     resolved.RunGitSHA,
		Submodules: resolved.Submodules,
		Skill:      skill,
		Producer:   producer,
		Inputs:     sourceInputRefs(resolved),
		Output:     runmanifest.ArtifactFromManifestArtifact(outputArtifact),
		Log:        logRef,
		Timestamp:  now,
		ReplayOf: &runmanifest.ReplayLink{
			RunID:  sourceRunID,
			Stage:  sourceStageName,
			Commit: resolved.Commit,
		},
	}

	manifest := runmanifest.Manifest{
		RunID:           replayRunID,
		Workflow:        resolved.Workflow,
		WorkflowVersion: resolved.WorkflowVersion,
		Created:         now,
		Refs:            resolved.Refs,
		Stages:          []runmanifest.Stage{stage},
	}

	commit, err := (runmanifest.Writer{Store: r.Store}).Write(ctx, manifest, files, runmanifest.WriteOptions{
		Message: fmt.Sprintf("replay: record %s stage %s from %s", replayRunID, sourceStageName, sourceRunID),
	})
	if err != nil {
		return RecordedRun{}, fmt.Errorf("record: write replay run: %w", err)
	}

	return RecordedRun{
		RunID:          replayRunID,
		Commit:         commit,
		OutputArtifact: outputArtifact.SHA256,
		OutputPath:     outputArtifact.Path,
	}, nil
}

func (r RunRecorder) buildSessionEvidence(as *artifactstore.Store, role string, session *SessionInfo) *runmanifest.SessionEvidence {
	evidence := &runmanifest.SessionEvidence{
		SessionID:       session.SessionID,
		TranscriptURI:   session.TranscriptURI,
		RetrievalStatus: runmanifest.SessionEvidenceNotApplicable,
		RedactionStatus: runmanifest.SessionEvidenceNotApplicable,
	}
	if strings.TrimSpace(session.TranscriptPath) == "" {
		return evidence
	}

	path, root := resolveRecordedTranscriptPath(session.TranscriptPath, r.SessionScratchDir, r.SessionWorktreeDir)
	if root == "" {
		evidence.RetrievalStatus = runmanifest.SessionEvidenceFailed
		return evidence
	}
	content, err := sessionevidence.ReadRegularFileUnder(root, path)
	if err != nil {
		evidence.RetrievalStatus = runmanifest.SessionEvidenceFailed
		return evidence
	}
	evidence.RetrievalStatus = runmanifest.SessionEvidenceRetrievalImported
	if err := sessionevidence.ScanForSecrets(content); err != nil {
		evidence.RedactionStatus = runmanifest.SessionEvidenceFailed
		return evidence
	}
	evidence.RedactionStatus = runmanifest.SessionEvidenceRedactionPassed
	artifact, err := as.AddContent(role, artifactmedia.Infer(path), content)
	if err != nil {
		evidence.RetrievalStatus = runmanifest.SessionEvidenceFailed
		evidence.RedactionStatus = runmanifest.SessionEvidenceNotApplicable
		return evidence
	}
	ref := runmanifest.ArtifactFromManifestArtifact(artifact)
	evidence.TranscriptArtifact = &ref
	return evidence
}

func resolveRecordedTranscriptPath(pathValue, scratchDir, worktreeDir string) (string, string) {
	if filepath.IsAbs(pathValue) {
		for _, root := range []string{scratchDir, worktreeDir} {
			if root == "" {
				continue
			}
			rel, err := filepath.Rel(root, pathValue)
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return pathValue, root
			}
		}
		return "", ""
	}
	cleanPath := filepath.Clean(pathValue)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", ""
	}
	if scratchDir != "" {
		scratchPath := filepath.Join(scratchDir, cleanPath)
		if _, err := os.Lstat(scratchPath); err == nil {
			return scratchPath, scratchDir
		}
	}
	if worktreeDir != "" {
		return filepath.Join(worktreeDir, cleanPath), worktreeDir
	}
	return "", ""
}

// AllocateRunID probes for a free replay run id, trying base then
// base-2 through base-10. Returns an error if none are free.
// Mirrors allocateReplayRunID (now exported for use by bench).
func AllocateRunID(ctx context.Context, store refstore.Store, base string) (string, error) {
	if !runmanifest.IsValidRunID(base) {
		return "", fmt.Errorf("derived replay run id %q is not a valid run id", base)
	}

	candidates := make([]string, 0, 11)
	candidates = append(candidates, base)
	for n := 2; n <= 10; n++ {
		candidates = append(candidates, fmt.Sprintf("%s-%d", base, n))
	}

	for _, id := range candidates {
		ref := "refs/etude/runs/" + id
		_, err := store.Resolve(ctx, ref)
		if errors.Is(err, refstore.ErrNotFound) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("probe replay run id %q: %w", id, err)
		}
		// Already exists — try next suffix.
	}
	return "", fmt.Errorf("could not allocate unique replay run id after 10 attempts (base: %s)", base)
}

// sourceInputRefs extracts the ArtifactRefs from the resolved stage's inputs,
// preserving them verbatim so the replay run carries identical content-addressed
// refs.
func sourceInputRefs(resolved ResolvedStage) []runmanifest.ArtifactRef {
	refs := make([]runmanifest.ArtifactRef, len(resolved.ResolvedInputs))
	for i, inp := range resolved.ResolvedInputs {
		refs[i] = inp.ArtifactRef
	}
	return refs
}
