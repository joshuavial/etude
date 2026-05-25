package gc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

const (
	runsPrefix  = "refs/etude/runs"
	evalsPrefix = "refs/etude/evals"
	runsNS      = "refs/etude/runs/"
)

// CollectOptions configures a Collect call.
type CollectOptions struct {
	// MaxSize, when positive, enables the OVERSIZED section: runs whose total
	// content-artifact bytes exceed MaxSize are reported.
	MaxSize int64
}

// RunSummary holds per-run totals and pointer-artifact info.
type RunSummary struct {
	RunID       string
	ContentSize int64
	Pointers    []PointerInfo
}

// PointerInfo records one pointer artifact for a run.
type PointerInfo struct {
	Stage    string
	Role     string
	Artifact string
	URI      string
}

// Report is the output of Collect: totals, optional oversized list, and
// pointer-artifact list for the EXTERNAL section.
type Report struct {
	RunCount   int
	EvalCount  int
	TotalBytes int64
	Oversized  []RunSummary // only populated when CollectOptions.MaxSize > 0
	External   []RunSummary // runs with at least one pointer artifact
}

// RefusedRun records a single refusal from Prune.
type RefusedRun struct {
	RunID  string
	Reason string
}

// BuildPinSet walks all run and eval refs and returns the set of run IDs that
// must not be deleted: every run ID referenced by an eval ArtifactSource (Targets
// or Context) and every run ID in a stage's ReplayOf link. A parse error on any
// document is returned immediately (naming the ref) so gc is never under-reporting.
func BuildPinSet(ctx context.Context, store refstore.Store) (map[string]struct{}, error) {
	pins := make(map[string]struct{})

	runRefs, err := store.List(ctx, runsPrefix)
	if err != nil {
		return nil, fmt.Errorf("list run refs: %w", err)
	}
	for _, ref := range runRefs {
		manifestBytes, err := store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return nil, fmt.Errorf("read manifest for %s: %w", ref, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return nil, fmt.Errorf("parse manifest for %s: %w", ref, err)
		}
		for _, stage := range manifest.Stages {
			if stage.ReplayOf != nil {
				pins[stage.ReplayOf.RunID] = struct{}{}
			}
		}
	}

	evalRefs, err := store.List(ctx, evalsPrefix)
	if err != nil {
		return nil, fmt.Errorf("list eval refs: %w", err)
	}
	for _, ref := range evalRefs {
		evalBytes, err := store.ReadFile(ctx, ref, "eval_result.json")
		if err != nil {
			return nil, fmt.Errorf("read eval_result.json for %s: %w", ref, err)
		}
		result, err := eval.ParseJSON(evalBytes)
		if err != nil {
			return nil, fmt.Errorf("parse eval_result.json for %s: %w", ref, err)
		}
		for _, src := range result.Targets {
			pins[src.RunID] = struct{}{}
		}
		for _, src := range result.Context {
			pins[src.RunID] = struct{}{}
		}
	}

	return pins, nil
}

// Collect builds a Report by walking all run and eval refs. It sums content-
// artifact bytes, counts refs, identifies oversized runs (when opts.MaxSize > 0),
// and lists pointer artifacts (EXTERNAL section).
func Collect(ctx context.Context, store refstore.Store, opts CollectOptions) (Report, error) {
	runRefs, err := store.List(ctx, runsPrefix)
	if err != nil {
		return Report{}, fmt.Errorf("list run refs: %w", err)
	}
	evalRefs, err := store.List(ctx, evalsPrefix)
	if err != nil {
		return Report{}, fmt.Errorf("list eval refs: %w", err)
	}

	report := Report{
		RunCount:  len(runRefs),
		EvalCount: len(evalRefs),
	}

	for _, ref := range runRefs {
		manifestBytes, err := store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return Report{}, fmt.Errorf("read manifest for %s: %w", ref, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return Report{}, fmt.Errorf("parse manifest for %s: %w", ref, err)
		}

		summary := runSummaryFromManifest(ctx, store, ref, manifest)
		report.TotalBytes += summary.ContentSize

		if opts.MaxSize > 0 && summary.ContentSize > opts.MaxSize {
			report.Oversized = append(report.Oversized, summary)
		}
		if len(summary.Pointers) > 0 {
			report.External = append(report.External, summary)
		}
	}

	return report, nil
}

// runSummaryFromManifest builds a RunSummary for a single run ref. It reads
// pointer artifact files to extract URIs for the EXTERNAL section. Any pointer
// file read error is tolerated: the URI is reported as empty rather than aborting.
func runSummaryFromManifest(ctx context.Context, store refstore.Store, ref string, manifest runmanifest.Manifest) RunSummary {
	summary := RunSummary{RunID: manifest.RunID}
	seen := make(map[string]bool) // deduplicate by artifact SHA so shared blobs are counted once

	for _, stage := range manifest.Stages {
		collectArtifact(ctx, store, ref, stage.Name, stage.Output, &summary, seen)
		for _, inp := range stage.Inputs {
			collectArtifact(ctx, store, ref, stage.Name, inp, &summary, seen)
		}
	}

	return summary
}

func collectArtifact(ctx context.Context, store refstore.Store, ref, stageName string, a runmanifest.ArtifactRef, summary *RunSummary, seen map[string]bool) {
	if seen[a.Artifact] {
		return
	}
	seen[a.Artifact] = true

	switch a.Storage {
	case artifactstore.StorageContent:
		summary.ContentSize += a.Size
	case artifactstore.StoragePointer:
		uri := readPointerURI(ctx, store, ref, a.Path)
		summary.Pointers = append(summary.Pointers, PointerInfo{
			Stage:    stageName,
			Role:     a.Role,
			Artifact: a.Artifact,
			URI:      uri,
		})
	}
}

// readPointerURI reads the pointer JSON file from the store and returns the URI.
// Returns empty string on any error rather than failing the whole report.
func readPointerURI(ctx context.Context, store refstore.Store, ref, path string) string {
	data, err := store.ReadFile(ctx, ref, path)
	if err != nil {
		return ""
	}
	var rec struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return ""
	}
	return rec.URI
}

// Prune deletes the named run refs that are not pinned by the current pin-set.
// It recomputes the pin-set immediately before deletion to guard against races.
// For each named run:
//   - If the ref does not exist: refused with reason "not found".
//   - If the run ID is pinned:   refused with a descriptive reason.
//   - Otherwise: the ref is deleted and the ID is added to pruned.
//
// Prune returns all three lists. Eligible runs are deleted even when some are
// refused; callers should exit non-zero when any refusals or "not found" occur.
func Prune(ctx context.Context, store refstore.Store, runIDs []string) (pruned []string, refused []RefusedRun, err error) {
	pins, err := BuildPinSet(ctx, store)
	if err != nil {
		return nil, nil, fmt.Errorf("build pin set: %w", err)
	}

	for _, id := range runIDs {
		ref := runsNS + id
		if _, resolveErr := store.Resolve(ctx, ref); resolveErr != nil {
			refused = append(refused, RefusedRun{RunID: id, Reason: "not found"})
			continue
		}
		if _, pinned := pins[id]; pinned {
			refused = append(refused, RefusedRun{RunID: id, Reason: pinnedReason(ctx, store, id)})
			continue
		}
		if deleteErr := store.DeleteRef(ctx, ref); deleteErr != nil {
			return pruned, refused, fmt.Errorf("delete ref %s: %w", ref, deleteErr)
		}
		pruned = append(pruned, id)
	}

	return pruned, refused, nil
}

// pinnedReason returns a human-readable string explaining why a run is pinned,
// inspecting the live eval and run refs. Falls back to a generic message.
func pinnedReason(ctx context.Context, store refstore.Store, runID string) string {
	evalRefs, err := store.List(ctx, evalsPrefix)
	if err == nil {
		for _, ref := range evalRefs {
			evalBytes, err := store.ReadFile(ctx, ref, "eval_result.json")
			if err != nil {
				continue
			}
			result, err := eval.ParseJSON(evalBytes)
			if err != nil {
				continue
			}
			for _, src := range result.Targets {
				if src.RunID == runID {
					evalID := strings.TrimPrefix(ref, "refs/etude/evals/")
					return fmt.Sprintf("pinned by eval %s (target)", evalID)
				}
			}
			for _, src := range result.Context {
				if src.RunID == runID {
					evalID := strings.TrimPrefix(ref, "refs/etude/evals/")
					return fmt.Sprintf("pinned by eval %s (context)", evalID)
				}
			}
		}
	}

	runRefs, err := store.List(ctx, runsPrefix)
	if err == nil {
		for _, ref := range runRefs {
			manifestBytes, err := store.ReadFile(ctx, ref, "manifest.json")
			if err != nil {
				continue
			}
			manifest, err := runmanifest.ParseJSON(manifestBytes)
			if err != nil {
				continue
			}
			for _, stage := range manifest.Stages {
				if stage.ReplayOf != nil && stage.ReplayOf.RunID == runID {
					return fmt.Sprintf("pinned by replay in run %s (stage %s)", manifest.RunID, stage.Name)
				}
			}
		}
	}

	return "pinned by eval or replay"
}
