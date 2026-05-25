package replay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

var (
	ErrInvalidRunID           = errors.New("invalid run id")
	ErrRunNotFound            = errors.New("run not found")
	ErrStageNotFound          = errors.New("stage not found")
	ErrAmbiguousStage         = errors.New("ambiguous stage")
	ErrPointerNotMaterialized = errors.New("pointer artifact not materialized")
)

// ResolvedStage embeds runmanifest.Stage (providing Name, ProducedBy, GitSHA,
// Skill, Inputs []ArtifactRef, Output ArtifactRef, Timestamp) and adds
// resolution metadata.
type ResolvedStage struct {
	runmanifest.Stage
	// Refs is the manifest-level Refs map (e.g. PR number, branch).
	Refs map[string]string
	// Commit is the resolved git commit OID at which the manifest was read.
	Commit string
	// ResolvedInputs holds the resolved inputs for this stage.
	// Named ResolvedInputs (not Inputs) to avoid shadowing Stage.Inputs []ArtifactRef.
	ResolvedInputs []ResolvedInput
}

// ResolvedInput pairs an artifact reference with a lazy content reader.
type ResolvedInput struct {
	Role        string
	ArtifactRef runmanifest.ArtifactRef
	// ReadContent reads the artifact bytes from the resolved commit (consistent
	// snapshot, immune to TOCTOU ref advances). For pointer artifacts it returns
	// ErrPointerNotMaterialized instead of the pointer-record JSON.
	ReadContent func(ctx context.Context) ([]byte, error)
}

// ResolveInputs resolves the inputs for a named stage of a run.
//
// Steps:
//  1. Validate runID via runmanifest.IsValidRunID — returns ErrInvalidRunID
//     before any git call so invalid IDs fail fast even outside a repo.
//  2. Resolve refs/etude/runs/<runID> to a commit; maps ErrNotFound to ErrRunNotFound.
//  3. Read manifest.json from that exact commit (never re-resolves the ref).
//  4. Locate stageName; returns ErrStageNotFound (listing available names) or
//     ErrAmbiguousStage (including per-duplicate ProducedBy+Timestamp) as appropriate.
//  5. Returns a ResolvedStage whose ReadContent closures are bound to the resolved
//     commit OID, not the ref — providing a consistent snapshot across all reads.
func ResolveInputs(ctx context.Context, store refstore.Store, runID, stageName string) (ResolvedStage, error) {
	// Step 1: validate runID before any git call.
	if !runmanifest.IsValidRunID(runID) {
		return ResolvedStage{}, fmt.Errorf("%w: %s", ErrInvalidRunID, runID)
	}

	// Step 2: resolve ref to commit once.
	ref := "refs/etude/runs/" + runID
	commit, err := store.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, refstore.ErrNotFound) {
			return ResolvedStage{}, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
		}
		return ResolvedStage{}, err
	}

	// Step 3: read manifest from the resolved commit (not the ref).
	raw, err := store.ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		return ResolvedStage{}, err
	}
	manifest, err := runmanifest.ParseJSON(raw)
	if err != nil {
		return ResolvedStage{}, err
	}

	// Step 4: defense-in-depth — run id must match.
	if manifest.RunID != runID {
		return ResolvedStage{}, fmt.Errorf("manifest run id mismatch: got %q, want %q", manifest.RunID, runID)
	}

	// Step 5: locate stage by name.
	var matches []runmanifest.Stage
	for _, s := range manifest.Stages {
		if s.Name == stageName {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		// Build sorted list of available names.
		names := make([]string, 0, len(manifest.Stages))
		seen := make(map[string]struct{}, len(manifest.Stages))
		for _, s := range manifest.Stages {
			if _, ok := seen[s.Name]; !ok {
				names = append(names, s.Name)
				seen[s.Name] = struct{}{}
			}
		}
		sort.Strings(names)
		return ResolvedStage{}, fmt.Errorf("%w: %q not found in run %q; available: %s",
			ErrStageNotFound, stageName, runID, strings.Join(names, ", "))

	case 1:
		// Single match — proceed below.

	default:
		// Duplicate stage names — surface detail for each duplicate.
		parts := make([]string, 0, len(matches))
		for _, m := range matches {
			parts = append(parts, fmt.Sprintf("{produced_by=%s timestamp=%s}", m.ProducedBy, m.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00")))
		}
		return ResolvedStage{}, fmt.Errorf("%w: %q appears %d times in run %q: %s",
			ErrAmbiguousStage, stageName, len(matches), runID, strings.Join(parts, ", "))
	}

	stage := matches[0]

	// Step 6: build ResolvedInputs. Each ReadContent closure captures the
	// resolved commit OID (not the ref) for snapshot consistency.
	resolvedInputs := make([]ResolvedInput, len(stage.Inputs))
	for i, input := range stage.Inputs {
		// Capture by value — create a local copy to avoid closure aliasing.
		inp := input
		resolvedInputs[i] = ResolvedInput{
			Role:        inp.Role,
			ArtifactRef: inp,
			ReadContent: func(ctx context.Context) ([]byte, error) {
				if inp.Storage == artifactstore.StoragePointer {
					return nil, fmt.Errorf("%w: %s", ErrPointerNotMaterialized, inp.Path)
				}
				data, err := store.ReadCommitFile(ctx, commit, inp.Path)
				if err != nil {
					return nil, err
				}
				return data, nil
			},
		}
	}

	// Step 7: return resolved stage.
	return ResolvedStage{
		Stage:          stage,
		Refs:           manifest.Refs,
		Commit:         commit,
		ResolvedInputs: resolvedInputs,
	}, nil
}
