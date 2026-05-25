package bench

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Judge test doubles
// ---------------------------------------------------------------------------

// countingJudge counts how many times Judge is called and delegates to inner.
type countingJudge struct {
	inner eval.Judge
	calls int
}

func (c *countingJudge) Judge(ctx context.Context, req eval.JudgeRequest) (eval.JudgeResponse, error) {
	c.calls++
	return c.inner.Judge(ctx, req)
}

// errorIfCalledJudge errors if Judge is ever called — used to assert cache hits.
type errorIfCalledJudge struct{}

func (e *errorIfCalledJudge) Judge(_ context.Context, _ eval.JudgeRequest) (eval.JudgeResponse, error) {
	return eval.JudgeResponse{}, errors.New("errorIfCalledJudge: should not have been called")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	knownJudgeID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 64-char hex
	otherJudgeID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // 64-char hex
)

// buildCachePipeline builds a Pipeline with cache control fields set.
func buildCachePipeline(repoDir string, runner replay.Runner, judge eval.Judge, fixedTime time.Time, cache bool, judgeID string, seed int64) Pipeline {
	store := newStore(repoDir)
	rec := replay.RunRecorder{Store: store, Now: func() time.Time { return fixedTime }}
	return Pipeline{
		Store:    store,
		Runner:   runner,
		Judge:    judge,
		Recorder: rec,
		Now:      func() time.Time { return fixedTime },
		Seed:     seed,
		Cache:    cache,
		JudgeID:  judgeID,
	}
}

// seedEvalResult directly writes an EvalResult into the store for cache seeding.
func seedEvalResult(t *testing.T, repoDir string, result eval.EvalResult) {
	t.Helper()
	store := newStore(repoDir)
	_, err := eval.Writer{Store: store}.Write(context.Background(), result, eval.WriteOptions{})
	if err != nil {
		t.Fatalf("seedEvalResult Write: %v", err)
	}
}

// buildCachedEvalResult constructs a valid EvalResult that matches the two target
// ArtifactSources for use as a cache seed.
func buildCachedEvalResult(commitA, artifactA, commitB, artifactB, judgeID string, seed int64, id string, created time.Time) eval.EvalResult {
	seedVal := seed
	return eval.EvalResult{
		EvalResultVersion: 1,
		EvalID:            id,
		Method:            "pairwise",
		Score: eval.Score{
			Kind:   eval.ScorePairwise,
			Winner: eval.WinnerB,
		},
		Findings: []eval.Finding{},
		Targets: []eval.ArtifactSource{
			{RunID: "src-run", Stage: "plan", Commit: commitA, Artifact: artifactA},
			{RunID: "rep-run", Stage: "plan", Commit: commitB, Artifact: artifactB},
		},
		Producer: runmanifest.Producer{},
		Created:  created,
		JudgeID:  judgeID,
		Seed:     &seedVal,
	}
}

// ---------------------------------------------------------------------------
// Cache MISS tests
// ---------------------------------------------------------------------------

// TestCacheMissEmptyStore verifies that on an empty eval store the judge IS
// called, the result is written with JudgeID+Seed, and Reused is false.
func TestCacheMissEmptyStore(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerTie}}
	counting := &countingJudge{inner: inner}

	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	fixedTime := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	p := buildCachePipeline(repoDir, stub, counting, fixedTime, true, knownJudgeID, 7)

	outcome, err := p.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("BenchRun: %v", err)
	}

	// Judge must have been called exactly once.
	if counting.calls != 1 {
		t.Errorf("judge calls = %d, want 1", counting.calls)
	}
	if outcome.Reused {
		t.Error("Reused = true, want false on miss")
	}

	// Persisted doc must carry JudgeID and Seed.
	refsAfter, err := store.List(context.Background(), "refs/etude/evals")
	if err != nil {
		t.Fatalf("List evals: %v", err)
	}
	if len(refsAfter) != 1 {
		t.Fatalf("eval ref count = %d, want 1", len(refsAfter))
	}
	raw, err := store.ReadFile(context.Background(), refsAfter[0], "eval_result.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	doc, err := eval.ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if doc.JudgeID != knownJudgeID {
		t.Errorf("doc.JudgeID = %q, want %q", doc.JudgeID, knownJudgeID)
	}
	if doc.Seed == nil {
		t.Fatal("doc.Seed is nil, want non-nil")
	}
	if *doc.Seed != 7 {
		t.Errorf("doc.Seed = %d, want 7", *doc.Seed)
	}
}

// ---------------------------------------------------------------------------
// Cache HIT tests
// ---------------------------------------------------------------------------

// TestCacheHitIdentifiedJudge verifies that when a matching cached doc exists
// (same targets, same non-empty JudgeID, same seed), the judge is NOT called,
// Reused is true, the cached EvalID is returned, and no new eval ref is written.
func TestCacheHitIdentifiedJudge(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	// First run: perform a real bench run to obtain the actual target artifact
	// shas (they are content-addressed, so we need to run once to know them).
	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output cache test"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}
	fixedTime1 := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}
	p1 := buildCachePipeline(repoDir, stub, inner, fixedTime1, true, knownJudgeID, 5)
	outcome1, err := p1.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("first BenchRun: %v", err)
	}
	if outcome1.Reused {
		t.Fatal("first run unexpectedly reused a cached result")
	}

	refsAfterFirst, err := store.List(context.Background(), "refs/etude/evals")
	if err != nil {
		t.Fatalf("List evals after first run: %v", err)
	}
	firstEvalCount := len(refsAfterFirst)

	// Second run: use errorIfCalledJudge to assert judge is not called.
	errorJudge := &errorIfCalledJudge{}
	fixedTime2 := time.Date(2026, 5, 11, 1, 0, 0, 0, time.UTC)
	p2 := buildCachePipeline(repoDir, stub, errorJudge, fixedTime2, true, knownJudgeID, 5)
	outcome2, err := p2.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("second BenchRun (cache hit): %v", err)
	}

	// Must be a cache hit.
	if !outcome2.Reused {
		t.Error("Reused = false, want true on hit")
	}
	// EvalID must be the cached one (no new ref written).
	if outcome2.EvalID != outcome1.EvalID {
		t.Errorf("EvalID = %q, want cached %q", outcome2.EvalID, outcome1.EvalID)
	}
	// No new eval ref must have been written.
	refsAfterSecond, err := store.List(context.Background(), "refs/etude/evals")
	if err != nil {
		t.Fatalf("List evals after second run: %v", err)
	}
	if len(refsAfterSecond) != firstEvalCount {
		t.Errorf("eval ref count = %d, want %d (no new refs on hit)", len(refsAfterSecond), firstEvalCount)
	}
	// Cached verdict must match the first run's verdict (canonical winner is stable).
	if outcome2.Winner != outcome1.Winner {
		t.Errorf("Winner = %q, want %q (from cache, same as first run)", outcome2.Winner, outcome1.Winner)
	}
}

// ---------------------------------------------------------------------------
// Empty JudgeID tests (FIX 3 — no reuse for unidentified judges)
// ---------------------------------------------------------------------------

// TestEmptyJudgeIDNeverReuses verifies that when Pipeline.JudgeID is empty,
// the cache is NOT consulted and the judge IS called even when an identical
// prior eval doc exists. This closes the empty==empty hole.
func TestEmptyJudgeIDNeverReuses(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output empty judge"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	// First run with empty JudgeID.
	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerTie}}
	counting1 := &countingJudge{inner: inner}
	fixedTime1 := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	p1 := buildCachePipeline(repoDir, stub, counting1, fixedTime1, true, "", 0)
	outcome1, err := p1.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("first BenchRun: %v", err)
	}
	if counting1.calls != 1 {
		t.Errorf("first run: judge calls = %d, want 1", counting1.calls)
	}
	if outcome1.Reused {
		t.Error("first run: Reused = true, want false")
	}

	// Second run with empty JudgeID: judge must still be called.
	counting2 := &countingJudge{inner: inner}
	fixedTime2 := time.Date(2026, 5, 12, 1, 0, 0, 0, time.UTC)
	p2 := buildCachePipeline(repoDir, stub, counting2, fixedTime2, true, "", 0)
	outcome2, err := p2.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("second BenchRun: %v", err)
	}
	if counting2.calls != 1 {
		t.Errorf("second run: judge calls = %d, want 1 (no reuse for empty JudgeID)", counting2.calls)
	}
	if outcome2.Reused {
		t.Error("second run: Reused = true, want false (empty JudgeID must never reuse)")
	}
}

// ---------------------------------------------------------------------------
// Cache miss scenarios
// ---------------------------------------------------------------------------

// TestCacheMissDifferentJudgeID verifies that a cached doc with one non-empty
// judgeID does not match a query with a different non-empty judgeID.
func TestCacheMissDifferentJudgeID(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output diff judge"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	// Seed a cached doc with knownJudgeID.
	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}
	fixedTime1 := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	p1 := buildCachePipeline(repoDir, stub, inner, fixedTime1, true, knownJudgeID, 3)
	_, err := p1.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("first BenchRun: %v", err)
	}

	// Query with otherJudgeID: must miss.
	counting := &countingJudge{inner: inner}
	fixedTime2 := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	p2 := buildCachePipeline(repoDir, stub, counting, fixedTime2, true, otherJudgeID, 3)
	outcome2, err := p2.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("second BenchRun: %v", err)
	}
	if outcome2.Reused {
		t.Error("Reused = true, want false (different JudgeID must miss)")
	}
	if counting.calls != 1 {
		t.Errorf("judge calls = %d, want 1", counting.calls)
	}
}

// TestCacheMissDifferentSeed verifies that a cached doc with seed=X does not
// match a query with seed=Y.
func TestCacheMissDifferentSeed(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output diff seed"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	// Seed a cached doc with seed=10.
	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}
	fixedTime1 := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	p1 := buildCachePipeline(repoDir, stub, inner, fixedTime1, true, knownJudgeID, 10)
	_, err := p1.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("first BenchRun (seed=10): %v", err)
	}

	// Query with seed=99: must miss.
	counting := &countingJudge{inner: inner}
	fixedTime2 := time.Date(2026, 5, 14, 1, 0, 0, 0, time.UTC)
	p2 := buildCachePipeline(repoDir, stub, counting, fixedTime2, true, knownJudgeID, 99)
	outcome2, err := p2.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("second BenchRun (seed=99): %v", err)
	}
	if outcome2.Reused {
		t.Error("Reused = true, want false (different seed must miss)")
	}
	if counting.calls != 1 {
		t.Errorf("judge calls = %d, want 1", counting.calls)
	}
}

// TestCacheSameSeedHit verifies that identical (targets, judgeID, seed) gives a hit.
func TestCacheSameSeedHit(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output same seed"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerB}}
	fixedTime1 := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	p1 := buildCachePipeline(repoDir, stub, inner, fixedTime1, true, knownJudgeID, 55)
	_, err := p1.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("first BenchRun: %v", err)
	}

	// Second run with same seed — must hit.
	errorJudge := &errorIfCalledJudge{}
	fixedTime2 := time.Date(2026, 5, 15, 1, 0, 0, 0, time.UTC)
	p2 := buildCachePipeline(repoDir, stub, errorJudge, fixedTime2, true, knownJudgeID, 55)
	outcome2, err := p2.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("second BenchRun (same seed): %v", err)
	}
	if !outcome2.Reused {
		t.Error("Reused = false, want true (same seed, same judge)")
	}
}

// TestCacheDocWithNilSeedMissesConcreteQuery verifies that a legacy cached doc
// (Seed==nil) does NOT match a query with a concrete seed.
func TestCacheDocWithNilSeedMissesConcreteQuery(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output nil seed"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	// Seed a doc by doing a real run first (this writes a doc with a seed).
	// Then we'll inject a nil-seed doc manually by reading the targets from it.
	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}
	fixedTime1 := time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)
	p1 := buildCachePipeline(repoDir, stub, inner, fixedTime1, true, knownJudgeID, 7)
	outcome1, err := p1.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// Manually write a "legacy" doc with Seed==nil using the same targets.
	legacyDoc := buildCachedEvalResult(
		outcome1.Result.Targets[0].Commit, outcome1.Result.Targets[0].Artifact,
		outcome1.Result.Targets[1].Commit, outcome1.Result.Targets[1].Artifact,
		knownJudgeID, 0, "pairwise-legacy-nil-seed-plan-20260516T020000Z",
		time.Date(2026, 5, 16, 2, 0, 0, 0, time.UTC),
	)
	legacyDoc.Seed = nil // simulate pre-seed-field doc
	seedEvalResult(t, repoDir, legacyDoc)

	// Query with seed=7: the legacy doc (nil seed) must not be a hit;
	// only the real doc from p1 (with seed=7) may match.
	// Since the real doc from p1 already exists, this tests nil-seed exclusion
	// by using a different known seed that won't match the seeded p1 doc.
	// Use seed=99 to get a clean miss from both.
	counting := &countingJudge{inner: inner}
	fixedTime2 := time.Date(2026, 5, 16, 3, 0, 0, 0, time.UTC)
	p2 := buildCachePipeline(repoDir, stub, counting, fixedTime2, true, knownJudgeID, 99)
	outcome2, err := p2.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("nil-seed miss query: %v", err)
	}
	if outcome2.Reused {
		t.Error("Reused = true despite nil-seed doc; want miss")
	}
	if counting.calls != 1 {
		t.Errorf("judge calls = %d, want 1", counting.calls)
	}
}

// ---------------------------------------------------------------------------
// --no-cache / Pipeline.Cache=false
// ---------------------------------------------------------------------------

// TestNoCacheDisablesReuse verifies that when Cache=false the judge is called
// even when a perfectly matching cached doc exists.
func TestNoCacheDisablesReuse(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("replay output no cache"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	inner := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerB}}

	// Seed a cacheable doc.
	fixedTime1 := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	p1 := buildCachePipeline(repoDir, stub, inner, fixedTime1, true, knownJudgeID, 1)
	_, err := p1.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}

	refsAfterSeed, err := store.List(context.Background(), "refs/etude/evals")
	if err != nil {
		t.Fatalf("List evals: %v", err)
	}

	// Second run with Cache=false: judge must be called, new ref written.
	counting := &countingJudge{inner: inner}
	fixedTime2 := time.Date(2026, 5, 17, 1, 0, 0, 0, time.UTC)
	p2 := buildCachePipeline(repoDir, stub, counting, fixedTime2, false, knownJudgeID, 1)
	outcome2, err := p2.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("no-cache run: %v", err)
	}
	if outcome2.Reused {
		t.Error("Reused = true despite Cache=false, want false")
	}
	if counting.calls != 1 {
		t.Errorf("judge calls = %d, want 1", counting.calls)
	}
	// New ref must have been written.
	refsAfterNoCache, err := store.List(context.Background(), "refs/etude/evals")
	if err != nil {
		t.Fatalf("List evals after no-cache: %v", err)
	}
	if len(refsAfterNoCache) <= len(refsAfterSeed) {
		t.Errorf("eval ref count = %d, want > %d (new ref written on miss)", len(refsAfterNoCache), len(refsAfterSeed))
	}
}

// ---------------------------------------------------------------------------
// lookupCachedEval unit tests
// ---------------------------------------------------------------------------

// TestLookupCachedEvalMatchesCacheKey exercises the lookup function directly
// with a seeded eval doc.
func TestLookupCachedEvalMatchesCacheKey(t *testing.T) {
	repoDir, _ := initRepoWithCommit(t)
	store := newStore(repoDir)

	commitA := strings.Repeat("a", 40)
	artA := strings.Repeat("0", 64)
	commitB := strings.Repeat("b", 40)
	artB := strings.Repeat("1", 64)

	created := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	seedDoc := buildCachedEvalResult(commitA, artA, commitB, artB, knownJudgeID, 42, "pairwise-lookup-test-plan-20260518T000000Z", created)
	seedEvalResult(t, repoDir, seedDoc)

	key := cacheKey{
		Method:    "pairwise",
		ArtifactA: artA,
		ArtifactB: artB,
		JudgeID:   knownJudgeID,
		Seed:      42,
	}

	found, hit, err := lookupCachedEval(context.Background(), store, key)
	if err != nil {
		t.Fatalf("lookupCachedEval: %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit, got miss")
	}
	if found.EvalID != seedDoc.EvalID {
		t.Errorf("EvalID = %q, want %q", found.EvalID, seedDoc.EvalID)
	}
}

// TestLookupCachedEvalMissOnEmptyStore verifies that an empty store returns a miss.
func TestLookupCachedEvalMissOnEmptyStore(t *testing.T) {
	repoDir, _ := initRepoWithCommit(t)
	store := newStore(repoDir)

	key := cacheKey{
		Method:    "pairwise",
		ArtifactA: strings.Repeat("0", 64),
		ArtifactB: strings.Repeat("1", 64),
		JudgeID:   knownJudgeID,
		Seed:      1,
	}

	_, hit, err := lookupCachedEval(context.Background(), store, key)
	if err != nil {
		t.Fatalf("lookupCachedEval: %v", err)
	}
	if hit {
		t.Fatal("expected miss on empty store, got hit")
	}
}

// TestLookupCachedEvalSkipsMalformedDoc verifies that a corrupt doc does not
// abort the scan; the lookup continues and returns miss (or hits another doc).
func TestLookupCachedEvalSkipsMalformedDoc(t *testing.T) {
	repoDir, _ := initRepoWithCommit(t)
	store := newStore(repoDir)

	// Write a corrupt eval doc directly.
	_, err := store.WriteCommit(context.Background(), "refs/etude/evals/corrupt-doc", map[string][]byte{
		"eval_result.json": []byte("not valid json {{{"),
	}, refstore.WriteOptions{Message: "corrupt doc for test"})
	// The content is malformed JSON; the git object write succeeds but parsing will fail.
	_ = err

	key := cacheKey{
		Method:    "pairwise",
		ArtifactA: strings.Repeat("0", 64),
		ArtifactB: strings.Repeat("1", 64),
		JudgeID:   knownJudgeID,
		Seed:      1,
	}

	// Should return miss (not error) despite the corrupt doc.
	_, hit, lookupErr := lookupCachedEval(context.Background(), store, key)
	if lookupErr != nil {
		t.Fatalf("lookupCachedEval returned error on corrupt doc: %v", lookupErr)
	}
	if hit {
		t.Fatal("expected miss despite corrupt doc")
	}
}
