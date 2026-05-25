package bench

import (
	"context"
	"fmt"

	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
)

// cacheKey identifies a specific pairwise evaluation for cache lookup purposes.
// All fields must match exactly for a cache hit.
//
// Matching uses the Artifact SHA-256 hashes as the content fingerprints.
// The Artifact hashes are the cryptographic identity of the content: equal
// Artifacts guarantee byte-identical inputs regardless of which git commit
// tree they were recorded into. Commits are provenance metadata but are NOT
// part of the match predicate.
//
// The JudgeID field is always non-empty when this struct is used for a lookup;
// the caller (BenchRun) must enforce the non-empty gate before calling
// lookupCachedEval. An unidentified judge (empty JudgeID) is never cached.
type cacheKey struct {
	Method    string
	ArtifactA string // SHA-256 of original output (content identity for target A)
	ArtifactB string // SHA-256 of replay output (content identity for target B)
	JudgeID   string // always non-empty at call sites
	Seed      int64
}

// lookupCachedEval scans all refs under refs/etude/evals/ and returns the first
// EvalResult whose (Method, Targets[0].Artifact, Targets[1].Artifact, JudgeID,
// Seed) all match key exactly.
//
// Content identity is determined by the Artifact SHA-256 hashes, not by the
// git Commit OIDs. Equal artifact hashes guarantee byte-identical inputs
// regardless of which commit tree they were recorded into.
//
// The JudgeID match is strict: key.JudgeID (which is always non-empty) must
// equal the candidate doc's JudgeID exactly. A doc with an empty or different
// JudgeID is never a hit. A doc with Seed==nil is never a hit for a
// concrete-seed query.
//
// Malformed or unparseable eval docs are silently skipped so that one corrupt
// ref does not abort the entire bench run.
//
// The scan is O(N) over all persisted eval refs. A secondary index is deferred
// to a future bead once the corpus grows large enough to warrant it.
func lookupCachedEval(ctx context.Context, store refstore.Store, key cacheKey) (eval.EvalResult, bool, error) {
	refs, err := store.List(ctx, "refs/etude/evals")
	if err != nil {
		return eval.EvalResult{}, false, fmt.Errorf("cache lookup: list evals: %w", err)
	}

	for _, ref := range refs {
		raw, err := store.ReadFile(ctx, ref, "eval_result.json")
		if err != nil {
			// Skip refs that cannot be read (e.g. partially written or corrupt).
			continue
		}
		doc, err := eval.ParseJSON(raw)
		if err != nil {
			// Skip unparseable docs; one bad ref must not fail the whole bench.
			continue
		}
		if matchesCacheKey(doc, key) {
			return doc, true, nil
		}
	}
	return eval.EvalResult{}, false, nil
}

// matchesCacheKey reports whether doc satisfies key. All fields must match exactly.
// Artifact SHA-256 hashes are used as the content fingerprints for targets;
// git Commit OIDs are provenance metadata and are NOT part of the match.
func matchesCacheKey(doc eval.EvalResult, key cacheKey) bool {
	if doc.Method != key.Method {
		return false
	}
	if len(doc.Targets) < 2 {
		return false
	}
	// Content identity: match on Artifact SHA-256 only (not Commit).
	if doc.Targets[0].Artifact != key.ArtifactA {
		return false
	}
	if doc.Targets[1].Artifact != key.ArtifactB {
		return false
	}
	// JudgeID must match exactly (non-empty gate enforced by caller).
	if doc.JudgeID != key.JudgeID {
		return false
	}
	// Seed must be present in the doc and equal to the query seed.
	// A doc with nil Seed (legacy) does not match any concrete-seed query.
	if doc.Seed == nil {
		return false
	}
	return *doc.Seed == key.Seed
}
