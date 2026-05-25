package eval

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sentinel errors for RubricEvaluator.
var (
	// ErrRubricLoad is returned when the rubric file cannot be loaded (path escape,
	// missing file, or read error).
	ErrRubricLoad = errors.New("rubric load failed")
	// ErrRubricVersionMismatch is returned when the rubric bytes on disk do not
	// match the pinned SHA-256 version in the RubricRef.
	ErrRubricVersionMismatch = errors.New("rubric version mismatch")
)

// RubricEvaluator implements Evaluator by loading a versioned rubric from disk,
// invoking a Judge, and mapping the response to a rubric-coherent Evaluation.
//
// Root is the rubric resolution root (e.g. the .etude dir or repo root).
// Rubric paths in EvalRequests are resolved relative to Root; path-escape
// is rejected.
type RubricEvaluator struct {
	Judge Judge
	Root  string
}

// compile-time interface satisfaction assertion.
var _ Evaluator = (*RubricEvaluator)(nil)

// PinRubric loads the rubric at path under root and returns a RubricRef with
// the SHA-256 content hash as the Version. The same loadRubric used by
// Evaluate ensures pin and verify can never skew.
func PinRubric(root, path string) (RubricRef, error) {
	data, err := loadRubric(root, path)
	if err != nil {
		return RubricRef{}, err
	}
	return RubricRef{
		Path:    path,
		Version: sha256hex(data),
	}, nil
}

// Evaluate scores a single target against a versioned rubric.
//
// Requirements:
//   - req.Method must be "rubric"
//   - req.Rubric must be non-nil with Path and Version set
//   - req.Targets must have exactly one entry
//
// The rubric file is loaded from disk and its SHA-256 hash verified against
// req.Rubric.Version before the judge is called. A mismatch returns
// ErrRubricVersionMismatch. Tamper-evident comparability: a rubric edit
// silently changing scoring semantics is caught before any LLM call.
func (r *RubricEvaluator) Evaluate(ctx context.Context, req EvalRequest) (Evaluation, error) {
	if req.Method != "rubric" {
		return Evaluation{}, fmt.Errorf("RubricEvaluator requires method \"rubric\", got %q", req.Method)
	}
	if req.Rubric == nil {
		return Evaluation{}, fmt.Errorf("RubricEvaluator requires non-nil Rubric")
	}
	if strings.TrimSpace(req.Rubric.Path) == "" {
		return Evaluation{}, fmt.Errorf("RubricEvaluator requires non-empty Rubric.Path")
	}
	if strings.TrimSpace(req.Rubric.Version) == "" {
		return Evaluation{}, fmt.Errorf("RubricEvaluator requires non-empty Rubric.Version")
	}
	if len(req.Targets) != 1 {
		return Evaluation{}, fmt.Errorf("RubricEvaluator requires exactly 1 target, got %d", len(req.Targets))
	}
	if r.Judge == nil {
		return Evaluation{}, ErrJudgeNotConfigured
	}

	data, err := loadRubric(r.Root, req.Rubric.Path)
	if err != nil {
		return Evaluation{}, err
	}

	if sha256hex(data) != req.Rubric.Version {
		return Evaluation{}, fmt.Errorf("%w: rubric %q on-disk hash does not match pinned version", ErrRubricVersionMismatch, req.Rubric.Path)
	}

	// Build the JudgeRequest from the EvalRequest.
	targets := make([]JudgeInput, 0, len(req.Targets))
	for _, t := range req.Targets {
		targets = append(targets, JudgeInput{
			Role:      t.Role,
			MediaType: t.MediaType,
			Content:   t.Content,
			Source:    t.Source,
		})
	}
	context_ := make([]JudgeInput, 0, len(req.Context))
	for _, c := range req.Context {
		context_ = append(context_, JudgeInput{
			Role:      c.Role,
			MediaType: c.MediaType,
			Content:   c.Content,
			Source:    c.Source,
		})
	}

	jr := JudgeRequest{
		Method:   "rubric",
		Targets:  targets,
		Context:  context_,
		Rubric:   data,
		Producer: req.Producer,
	}

	resp, err := r.Judge.Judge(ctx, jr)
	if err != nil {
		return Evaluation{}, err
	}

	// Validate rubric-method coherence on the response.
	if resp.Value == nil {
		return Evaluation{}, fmt.Errorf("%w: rubric response missing value", ErrJudgeOutputInvalid)
	}
	if resp.Max == nil {
		return Evaluation{}, fmt.Errorf("%w: rubric response missing max", ErrJudgeOutputInvalid)
	}
	if *resp.Max <= 0 {
		return Evaluation{}, fmt.Errorf("%w: rubric response max must be > 0", ErrJudgeOutputInvalid)
	}
	if *resp.Value < 0 || *resp.Value > *resp.Max {
		return Evaluation{}, fmt.Errorf("%w: rubric response value must be in [0, max]", ErrJudgeOutputInvalid)
	}
	if resp.Winner != WinnerNone {
		return Evaluation{}, fmt.Errorf("%w: rubric response must not set winner", ErrJudgeOutputInvalid)
	}
	if resp.Confidence != nil {
		return Evaluation{}, fmt.Errorf("%w: rubric response must not set confidence", ErrJudgeOutputInvalid)
	}

	// Validate findings severity (cheap; mirrors validateFinding in result.go).
	for i, f := range resp.Findings {
		if err := validateFinding(i, f); err != nil {
			return Evaluation{}, fmt.Errorf("%w: %v", ErrJudgeOutputInvalid, err)
		}
	}

	return Evaluation{
		Score: Score{
			Kind:  ScoreRubric,
			Value: resp.Value,
			Max:   resp.Max,
		},
		Findings: resp.Findings,
	}, nil
}

// loadRubric loads the rubric file at path resolved under root.
// It rejects absolute paths outright and relative paths that escape root, both
// lexically (via "..") and after resolving symlink components.
func loadRubric(root, path string) ([]byte, error) {
	if filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: rubric path %q must be relative to root", ErrRubricLoad, path)
	}

	// Join cleans the path — no need for a redundant filepath.Clean(path).
	joined := filepath.Join(root, path)

	// Lexical containment check: cheap, and rejects ".." escapes even when the
	// target does not exist.
	if rel, err := filepath.Rel(root, joined); err != nil {
		return nil, fmt.Errorf("%w: resolve path %q: %v", ErrRubricLoad, path, err)
	} else if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: path %q escapes root", ErrRubricLoad, path)
	}

	// Symlink-resolved containment check: a path component may be a symlink
	// pointing outside root, which the lexical check cannot see. Resolve both
	// sides (root itself may legitimately contain symlinks, e.g. /tmp on macOS)
	// and re-verify containment before reading.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %v", ErrRubricLoad, err)
	}
	realPath, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve %q: %v", ErrRubricLoad, path, err)
	}
	if rel, err := filepath.Rel(realRoot, realPath); err != nil {
		return nil, fmt.Errorf("%w: resolve path %q: %v", ErrRubricLoad, path, err)
	} else if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: path %q escapes root via symlink", ErrRubricLoad, path)
	}

	data, err := os.ReadFile(realPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read %q: %v", ErrRubricLoad, path, err)
	}
	return data, nil
}

// sha256hex returns the lowercase hex SHA-256 hash of data.
func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}
