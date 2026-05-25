// Package bench provides cohort selection for the etude bench command.
// It enumerates stored runs, filters them to a set eligible for benchmarking
// against a named stage, and returns the N most-recent qualifying runs.
package bench

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// runsPrefix is the refstore namespace for run refs (no trailing slash, as
// required by store.List — mirrors internal/cli/run.go:18).
const runsPrefix = "refs/etude/runs/"

// ErrInvalidLast is returned by SelectCohort when last is <= 0.
var ErrInvalidLast = errors.New("last must be positive")

// SkipReason is a machine-readable label for why a run was excluded from the cohort.
type SkipReason string

const (
	// SkipStageMissing indicates the named stage does not appear in the manifest.
	SkipStageMissing SkipReason = "stage-missing"
	// SkipStageAmbiguous indicates the named stage appears more than once.
	SkipStageAmbiguous SkipReason = "stage-ambiguous"
	// SkipNoGitSHA indicates the stage's GitSHA field is empty.
	SkipNoGitSHA SkipReason = "no-git-sha"
	// SkipInvalidGitSHA indicates the stage's GitSHA is non-empty but not a valid OID.
	// Mirrors worktree.validateSHA — runs with a malformed SHA would fail at checkout.
	SkipInvalidGitSHA SkipReason = "invalid-git-sha"
	// SkipPointerInput indicates at least one input artifact uses pointer storage.
	// Mirrors replay.go:163 which rejects pointer inputs before running the agent.
	SkipPointerInput SkipReason = "pointer-input"
	// SkipPointerOutput indicates the output artifact uses pointer storage.
	// The bench pipeline (.2) must be able to read the output as an eval target.
	SkipPointerOutput SkipReason = "pointer-output"
)

// CohortResult is the output of SelectCohort.
type CohortResult struct {
	// Stage echoes the requested stage name.
	Stage string
	// Selected holds qualifying runs ordered by Created DESC (up to last entries).
	Selected []CohortRun
	// Skipped holds all non-qualifying runs in enumeration (ref) order.
	Skipped []SkippedRun
}

// CohortRun is a single qualifying run in the cohort.
type CohortRun struct {
	// RunID is the run identifier (the suffix of the run ref).
	RunID string
	// Commit is the resolved git commit OID for the run ref at selection time.
	// It pins the snapshot so the pipeline (.2) can read artifacts consistently.
	Commit string
	// Stage is the matched runmanifest.Stage (carries GitSHA, Inputs, Output, Producer).
	Stage runmanifest.Stage
	// Created is the manifest's Created timestamp, used for ordering and display.
	Created time.Time
}

// SkippedRun records a run that was excluded from the cohort.
type SkippedRun struct {
	RunID  string
	Reason SkipReason
	// Detail is a human-readable note (e.g. which input was a pointer artifact).
	Detail string
}

// SelectCohort enumerates all runs in the store, filters them to those eligible
// for benchmarking on the named stage, and returns the `last` most-recent
// qualifying runs ordered by Created DESC (tie-broken by RunID DESC).
//
// Eligibility mirrors the replayability preconditions that replay enforces:
//   - stage occurs exactly once in the manifest,
//   - stage.GitSHA is a syntactically valid git OID (40 or 64 lowercase hex chars),
//   - all input artifacts use content storage,
//   - the output artifact uses content storage.
//
// Zero runs or zero qualifying runs are not errors; the caller decides UX.
// A manifest that fails to parse is returned as an error (naming the run).
func SelectCohort(ctx context.Context, store refstore.Store, stage string, last int) (CohortResult, error) {
	if last <= 0 {
		return CohortResult{}, ErrInvalidLast
	}

	// Enumerate refs — mirrors run list (internal/cli/run.go:74).
	refs, err := store.List(ctx, strings.TrimSuffix(runsPrefix, "/"))
	if err != nil {
		return CohortResult{}, err
	}

	result := CohortResult{Stage: stage}

	for _, ref := range refs {
		id := strings.TrimPrefix(ref, runsPrefix)

		// Resolve the ref to a commit OID to pin the snapshot for .2.
		commit, err := store.Resolve(ctx, ref)
		if err != nil {
			return CohortResult{}, fmt.Errorf("run %q: %w", id, err)
		}

		// Read the manifest from the already-resolved commit (NOT ReadFile(ref),
		// which re-resolves the ref): this guarantees CohortRun.Commit and the
		// parsed manifest come from one consistent snapshot even if the ref moves.
		manifestBytes, err := store.ReadCommitFile(ctx, commit, "manifest.json")
		if err != nil {
			return CohortResult{}, fmt.Errorf("run %q: %w", id, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return CohortResult{}, fmt.Errorf("run %q: %w", id, err)
		}

		matchedStage, skipReason, detail, ok := classify(stage, manifest)
		if !ok {
			result.Skipped = append(result.Skipped, SkippedRun{
				RunID:  id,
				Reason: skipReason,
				Detail: detail,
			})
			continue
		}

		result.Selected = append(result.Selected, CohortRun{
			RunID:   id,
			Commit:  commit,
			Stage:   matchedStage,
			Created: manifest.Created,
		})
	}

	// Order selected runs by Created DESC, tie-break by RunID DESC for determinism.
	sort.Slice(result.Selected, func(i, j int) bool {
		a, b := result.Selected[i], result.Selected[j]
		if !a.Created.Equal(b.Created) {
			return a.Created.After(b.Created)
		}
		return a.RunID > b.RunID
	})

	// Truncate to the requested count.
	if len(result.Selected) > last {
		result.Selected = result.Selected[:last]
	}

	return result, nil
}

// classify checks whether the named stage in the manifest is eligible for
// benchmarking. It returns the matched stage, a skip reason, a detail string,
// and a boolean indicating eligibility.
//
// The predicate is a faithful subset of what replay can actually execute:
//   - stage count: mirrors resolve.go:102–128,
//   - sha format: mirrors worktree.validateSHA (worktree.go:148–158),
//   - pointer inputs: mirrors replay.go:163 ErrPointerNotMaterialized,
//   - pointer output: guards the eval target read in .2.
func classify(stage string, m runmanifest.Manifest) (runmanifest.Stage, SkipReason, string, bool) {
	var matches []runmanifest.Stage
	for _, s := range m.Stages {
		if s.Name == stage {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return runmanifest.Stage{}, SkipStageMissing, "", false
	default:
		return runmanifest.Stage{}, SkipStageAmbiguous,
			fmt.Sprintf("stage %q appears %d times", stage, len(matches)), false
	case 1:
		// Proceed to eligibility checks below.
	}

	s := matches[0]

	// Check git SHA: empty → no-git-sha; present but malformed → invalid-git-sha.
	// Mirrors worktree.go:148–158 validateSHA (the real format gate at checkout).
	if s.GitSHA == "" {
		return runmanifest.Stage{}, SkipNoGitSHA, "", false
	}
	if !isValidGitSHA(s.GitSHA) {
		return runmanifest.Stage{}, SkipInvalidGitSHA,
			fmt.Sprintf("git_sha %q is not a valid OID", s.GitSHA), false
	}

	// Check input storage — pointer inputs are rejected at replay.go:163.
	for _, input := range s.Inputs {
		if input.Storage != artifactstore.StorageContent {
			return runmanifest.Stage{}, SkipPointerInput,
				fmt.Sprintf("input %q is a pointer artifact", input.Role), false
		}
	}

	// Check output storage — bench pipeline (.2) must be able to read the output.
	if s.Output.Storage != artifactstore.StorageContent {
		return runmanifest.Stage{}, SkipPointerOutput, "output is a pointer artifact", false
	}

	return s, "", "", true
}

// isValidGitSHA reports whether s is a syntactically valid git OID:
// non-empty, exactly 40 (SHA-1) or 64 (SHA-256) lowercase hex characters.
//
// This reimplements runmanifest.isHexOID locally (which is unexported) following
// the same precedent as internal/eval/result.go:342. It mirrors worktree.validateSHA
// (worktree.go:148–158) — the real format gate hit at checkout — so SelectCohort
// never returns a CohortRun that replay would reject at the checkout step.
func isValidGitSHA(s string) bool {
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
