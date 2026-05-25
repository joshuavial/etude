package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/worktree"
)

// ProducerOverrides carries optional overrides for the replay producer identity.
// These mirror the per-flag changed booleans in cli.replayProducerFlags so that
// the bench pipeline can apply the same "only override when explicitly set"
// logic. (.4 maps CLI flags onto this struct.)
type ProducerOverrides struct {
	SkillIDChanged        bool
	SkillRepoChanged      bool
	SkillVersionChanged   bool
	ModelChanged          bool
	HarnessChanged        bool
	HarnessVersionChanged bool

	SkillID        string
	SkillRepo      string
	SkillVersion   string
	Model          string
	Harness        string
	HarnessVersion string
}

// BenchOutcome is the result of a single BenchRun.
type BenchOutcome struct {
	// SourceRunID is the source cohort run ID (cr.RunID).
	SourceRunID string
	// Stage is the stage name that was benchmarked.
	Stage string
	// ReplayRunID is the allocated replay run ID created during this bench run.
	ReplayRunID string
	// ReplayCommit is the git commit OID of the recorded replay run.
	ReplayCommit string
	// EvalID is the identifier of the persisted EvalResult.
	EvalID string
	// Winner is the pairwise judge verdict (A=original, B=replay).
	Winner eval.Winner
	// Confidence is the optional judge confidence score (nil when absent).
	Confidence *float64
	// Findings are the structured observations from the judge.
	Findings []eval.Finding
	// Result is the full persisted EvalResult document (for .3 cache + .4 report).
	Result eval.EvalResult
	// Reused is true when the outcome was served from the eval-result cache
	// (no judge call was made and no new eval ref was written).
	Reused bool
}

// Pipeline runs the per-run replay -> record -> pairwise-eval pipeline.
// All fields except Now are required; Now defaults to time.Now when nil.
type Pipeline struct {
	Store     refstore.Store
	Runner    replay.Runner
	Judge     eval.Judge
	Recorder  replay.RunRecorder
	Seed      int64 // -> PairwiseEvaluator.Seed
	Overrides ProducerOverrides
	Now       func() time.Time
	// Cache enables eval-result caching. When true and JudgeID is non-empty,
	// BenchRun will look up a prior matching eval before calling the judge.
	Cache bool
	// JudgeID is the stable fingerprint of the active judge (from eval.JudgeIdentity).
	// An empty JudgeID disables caching for this pipeline regardless of Cache.
	JudgeID string
}

// BenchRun executes a single bench pipeline step for cr:
//  1. ResolveInputs for the stage.
//  2. Materialize inputs via ReadContent (pointer inputs -> error).
//  3. Read original output bytes from the source commit.
//  4. Checkout the recorded git_sha into a throwaway worktree.
//  5. Run the injected replay.Runner.
//  6. Record via RunRecorder (mints ReplayRunID + Commit + OutputArtifact sha).
//  7. Build and run a pairwise eval (A=original, B=replay).
//  8. Build and persist the EvalResult.
//  9. Return BenchOutcome.
//
// Any single-step failure causes BenchRun to return an error wrapped with
// run-id and stage context. Abort-vs-skip policy is the caller's responsibility.
func (p Pipeline) BenchRun(ctx context.Context, root string, cr CohortRun) (BenchOutcome, error) {
	stageName := cr.Stage.Name
	wrap := func(msg string, err error) error {
		return fmt.Errorf("bench run %s stage %s: %s: %w", cr.RunID, stageName, msg, err)
	}

	now := p.Now
	if now == nil {
		now = time.Now
	}

	// Step 1: resolve inputs.
	resolved, err := replay.ResolveInputs(ctx, p.Store, cr.RunID, stageName)
	if err != nil {
		return BenchOutcome{}, wrap("resolve inputs", err)
	}

	// Step 2: materialize inputs (pointer inputs -> error, as in replay.go).
	inputs := make([]replay.RunInput, 0, len(resolved.ResolvedInputs))
	for _, inp := range resolved.ResolvedInputs {
		content, err := inp.ReadContent(ctx)
		if err != nil {
			if errors.Is(err, replay.ErrPointerNotMaterialized) {
				return BenchOutcome{}, wrap(
					fmt.Sprintf("input %q is a pointer artifact", inp.Role), err)
			}
			return BenchOutcome{}, wrap(fmt.Sprintf("read input %q", inp.Role), err)
		}
		inputs = append(inputs, replay.RunInput{
			Role:      inp.ArtifactRef.Role,
			MediaType: inp.ArtifactRef.MediaType,
			Content:   content,
		})
	}

	// Step 3: read original output bytes from the source commit.
	origBytes, err := p.Store.ReadCommitFile(ctx, resolved.Commit, resolved.Output.Path)
	if err != nil {
		return BenchOutcome{}, wrap("read original output", err)
	}

	// Step 4: checkout the recorded git SHA.
	wt, err := worktree.Checkout(ctx, root, resolved.GitSHA)
	if err != nil {
		return BenchOutcome{}, wrap("checkout git sha", err)
	}
	defer wt.Close()

	// Step 5: create a scratch directory and run the replay.
	scratch, err := os.MkdirTemp("", "etude-bench-scratch-*")
	if err != nil {
		return BenchOutcome{}, wrap("create scratch dir", err)
	}
	defer os.RemoveAll(scratch)

	src := resolved.Producer
	producer := runmanifest.Producer{
		Harness: runmanifest.Harness{
			Name:    mergeString(p.Overrides.HarnessChanged, p.Overrides.Harness, src.Harness.Name),
			Version: mergeString(p.Overrides.HarnessVersionChanged, p.Overrides.HarnessVersion, src.Harness.Version),
		},
		Model: mergeString(p.Overrides.ModelChanged, p.Overrides.Model, src.Model),
		Skill: runmanifest.Skill{
			ID:      mergeString(p.Overrides.SkillIDChanged, p.Overrides.SkillID, src.Skill.ID),
			Repo:    mergeString(p.Overrides.SkillRepoChanged, p.Overrides.SkillRepo, src.Skill.Repo),
			Version: mergeString(p.Overrides.SkillVersionChanged, p.Overrides.SkillVersion, src.Skill.Version),
		},
	}

	req := replay.RunRequest{
		WorktreeDir:     wt.Dir,
		ScratchDir:      scratch,
		Inputs:          inputs,
		OutputRole:      resolved.Output.Role,
		OutputMediaType: resolved.Output.MediaType,
		Producer:        producer,
	}
	res, err := p.Runner.Run(ctx, req)
	if err != nil {
		return BenchOutcome{}, wrap("runner", err)
	}
	if len(res.Output) == 0 {
		return BenchOutcome{}, wrap("runner", fmt.Errorf("runner produced no output"))
	}

	// Step 6: record the replay run.
	recorded, err := p.Recorder.Record(ctx, cr.RunID, stageName, resolved, res)
	if err != nil {
		return BenchOutcome{}, wrap("record replay run", err)
	}

	// Build the two target ArtifactSources (needed for both cache lookup and eval).
	targetA := eval.ArtifactSource{
		RunID:    cr.RunID,
		Stage:    stageName,
		Commit:   resolved.Commit,
		Artifact: resolved.Output.Artifact,
	}
	targetB := eval.ArtifactSource{
		RunID:    recorded.RunID,
		Stage:    stageName,
		Commit:   recorded.Commit,
		Artifact: recorded.OutputArtifact,
	}

	// Cache lookup: performed ONLY when Cache is enabled and JudgeID is non-empty.
	// An empty JudgeID means an unidentified judge — never consult the cache.
	if p.Cache && p.JudgeID != "" {
		key := cacheKey{
			Method:    "pairwise",
			ArtifactA: targetA.Artifact,
			ArtifactB: targetB.Artifact,
			JudgeID:   p.JudgeID,
			Seed:      p.Seed,
		}
		cached, hit, err := lookupCachedEval(ctx, p.Store, key)
		if err != nil {
			return BenchOutcome{}, wrap("cache lookup", err)
		}
		if hit {
			return BenchOutcome{
				SourceRunID:  cr.RunID,
				Stage:        stageName,
				ReplayRunID:  recorded.RunID,
				ReplayCommit: recorded.Commit,
				EvalID:       cached.EvalID,
				Winner:       cached.Score.Winner,
				Confidence:   cached.Score.Confidence,
				Findings:     cached.Findings,
				Result:       cached,
				Reused:       true,
			}, nil
		}
	}

	// Step 7: build the pairwise eval request.
	// A = original (source run); B = replay (recorded run).
	evalReq := eval.EvalRequest{
		Method: "pairwise",
		Targets: []eval.EvalInput{
			{
				Role:      "original",
				MediaType: resolved.Output.MediaType,
				Content:   origBytes,
				Source:    targetA,
			},
			{
				Role:      "replay",
				MediaType: res.MediaType,
				Content:   res.Output,
				Source:    targetB,
			},
		},
		Producer: res.Producer,
	}

	evaluator := &eval.PairwiseEvaluator{Judge: p.Judge, Seed: p.Seed}
	evaluation, err := evaluator.Evaluate(ctx, evalReq)
	if err != nil {
		return BenchOutcome{}, wrap("evaluate", err)
	}

	// Step 8: build and persist the EvalResult (miss path).
	ts := now().UTC()
	evalIDBase := eval.EvalIDBase("pairwise", cr.RunID, stageName, ts)
	evalID, err := eval.AllocateEvalID(ctx, p.Store, evalIDBase)
	if err != nil {
		return BenchOutcome{}, wrap("allocate eval id", err)
	}

	seed := p.Seed
	result := eval.EvalResult{
		EvalResultVersion: 1,
		EvalID:            evalID,
		Method:            "pairwise",
		Score:             evaluation.Score,
		Findings:          evaluation.Findings,
		Targets: []eval.ArtifactSource{
			evalReq.Targets[0].Source,
			evalReq.Targets[1].Source,
		},
		Producer: res.Producer,
		Created:  ts,
		JudgeID:  p.JudgeID,
		Seed:     &seed,
	}

	if err := result.Validate(); err != nil {
		return BenchOutcome{}, wrap("validate eval result", err)
	}

	_, err = eval.Writer{Store: p.Store}.Write(ctx, result, eval.WriteOptions{
		Message: fmt.Sprintf("bench: eval %s run %s stage %s", evalID, cr.RunID, stageName),
	})
	if err != nil {
		return BenchOutcome{}, wrap("write eval result", err)
	}

	return BenchOutcome{
		SourceRunID:  cr.RunID,
		Stage:        stageName,
		ReplayRunID:  recorded.RunID,
		ReplayCommit: recorded.Commit,
		EvalID:       evalID,
		Winner:       evaluation.Score.Winner,
		Confidence:   evaluation.Score.Confidence,
		Findings:     evaluation.Findings,
		Result:       result,
		Reused:       false,
	}, nil
}

// mergeString returns override if changed is true, otherwise fallback.
// Mirrors the function in internal/cli/replay.go.
func mergeString(changed bool, override, fallback string) string {
	if changed {
		return override
	}
	return fallback
}
