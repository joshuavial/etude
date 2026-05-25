package eval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Sentinel errors for AssertionEvaluator.
var (
	// ErrUnknownCheck is returned when a check Kind is not present in the registry.
	ErrUnknownCheck = errors.New("unknown assertion check")
	// ErrAssertionConfig is returned for invalid or missing assertion configuration
	// (nil Assertion, empty Checks, rubric set on assertion request, wrong target
	// count, or blank check Kind).
	ErrAssertionConfig = errors.New("invalid assertion config")
)

// checkFunc is the signature for a built-in deterministic check.
// passed=true means the check passed. findings accumulate per-item observations
// (failures only; passes produce no findings). err signals a configuration or
// internal error — the caller propagates it rather than treating it as Passed=false.
type checkFunc func(req EvalRequest, args map[string]string) (passed bool, findings []Finding, err error)

// AssertionEvaluator implements Evaluator by running a set of deterministic
// checks against EvalRequest bytes. No Judge, no LLM, no exec, no IO.
//
// Checks is the injectable check registry. If nil, defaultChecks() is used,
// which includes the two built-in checks. Tests may inject a custom registry.
type AssertionEvaluator struct {
	Checks map[string]checkFunc
}

// compile-time interface satisfaction assertion.
var _ Evaluator = (*AssertionEvaluator)(nil)

// NewAssertionEvaluator returns an AssertionEvaluator seeded with defaultChecks().
func NewAssertionEvaluator() *AssertionEvaluator {
	return &AssertionEvaluator{Checks: defaultChecks()}
}

// defaultChecks returns the built-in check registry.
func defaultChecks() map[string]checkFunc {
	return map[string]checkFunc{
		"required-artifact-presence":            checkRequiredArtifactPresence,
		"test-plan-mentions-every-changed-file": checkTestPlanMentionsEveryChangedFile,
	}
}

// checks returns the evaluator's registry, falling back to defaultChecks() if nil.
func (e *AssertionEvaluator) checks() map[string]checkFunc {
	if e.Checks != nil {
		return e.Checks
	}
	return defaultChecks()
}

// Evaluate runs all configured assertion checks against req and returns an
// assertion-coherent Evaluation.
//
// Head-validation order:
//  1. req.Method must be "assertion"
//  2. req.Rubric must be nil (assertion forbids rubric config)
//  3. req.Assertion must be non-nil
//  4. len(req.Assertion.Checks) must be >= 1
//  5. each check Kind must be non-blank
//  6. len(req.Targets) must be exactly 1
//
// Each check is resolved in the registry; an unknown Kind aborts Evaluate with
// ErrUnknownCheck (config error, not a content failure). Check-internal errors
// are propagated directly; they do NOT produce Passed=false.
//
// Passed is the logical AND of all check results. Failures emit Findings;
// passes emit none.
func (e *AssertionEvaluator) Evaluate(ctx context.Context, req EvalRequest) (Evaluation, error) {
	if req.Method != "assertion" {
		return Evaluation{}, fmt.Errorf("AssertionEvaluator requires method \"assertion\", got %q", req.Method)
	}
	if req.Rubric != nil {
		return Evaluation{}, fmt.Errorf("%w: assertion method must not have rubric config", ErrAssertionConfig)
	}
	if req.Assertion == nil {
		return Evaluation{}, fmt.Errorf("%w: assertion method requires assertion config", ErrAssertionConfig)
	}
	if len(req.Assertion.Checks) == 0 {
		return Evaluation{}, fmt.Errorf("%w: assertion.checks must have at least one check", ErrAssertionConfig)
	}
	for i, c := range req.Assertion.Checks {
		if strings.TrimSpace(c.Kind) == "" {
			return Evaluation{}, fmt.Errorf("%w: assertion.checks[%d].kind required", ErrAssertionConfig, i)
		}
	}
	if len(req.Targets) != 1 {
		return Evaluation{}, fmt.Errorf("%w: assertion requires exactly 1 target, got %d", ErrAssertionConfig, len(req.Targets))
	}

	registry := e.checks()
	passed := true
	var findings []Finding

	for _, c := range req.Assertion.Checks {
		fn, ok := registry[c.Kind]
		if !ok {
			return Evaluation{}, fmt.Errorf("%w: %q", ErrUnknownCheck, c.Kind)
		}
		ok, fs, err := fn(req, c.Args)
		if err != nil {
			return Evaluation{}, err
		}
		if !ok {
			passed = false
		}
		findings = append(findings, fs...)
	}

	// Keep Findings nil when empty (mirrors pairwise combineDouble convention).
	if len(findings) == 0 {
		findings = nil
	}

	return Evaluation{
		Score: Score{
			Kind:   ScoreAssertion,
			Passed: &passed,
		},
		Findings: findings,
	}, nil
}

// ---- Built-in check #1: required-artifact-presence ----

// checkRequiredArtifactPresence passes iff an EvalInput with the given role
// exists and has non-empty Content in the selected input lane(s).
//
// Args:
//   - role (required): the Role to look for.
//   - in (optional): "targets", "context", or "both" (default "both").
func checkRequiredArtifactPresence(req EvalRequest, args map[string]string) (bool, []Finding, error) {
	role, ok := args["role"]
	if !ok || strings.TrimSpace(role) == "" {
		return false, nil, fmt.Errorf("%w: required-artifact-presence requires \"role\" arg", ErrAssertionConfig)
	}

	lane := "both"
	if v, ok := args["in"]; ok && strings.TrimSpace(v) != "" {
		lane = strings.TrimSpace(v)
	}

	var inputs []EvalInput
	switch lane {
	case "targets":
		inputs = req.Targets
	case "context":
		inputs = req.Context
	default: // "both"
		inputs = append(req.Targets, req.Context...)
	}

	for _, inp := range inputs {
		if inp.Role == role && len(inp.Content) > 0 {
			return true, nil, nil
		}
	}

	return false, []Finding{{
		Severity: SeverityError,
		Message:  fmt.Sprintf("required artifact with role %q is absent or empty", role),
		Pointer:  role,
	}}, nil
}

// ---- Built-in check #2: test-plan-mentions-every-changed-file ----

// checkTestPlanMentionsEveryChangedFile passes iff every file path reported by
// the unified diff is mentioned in the test plan text.
//
// Args:
//   - plan_role (optional, default "plan"): Role of the plan input. Resolved
//     from Targets first, then Context.
//   - diff_role (optional, default "diff"): Role of the diff input. Resolved
//     from Context.
//   - match (optional, default "either"): "path", "basename", or "either".
//     Determines whether the full path, the basename, or either must appear in
//     the plan text as a substring.
//   - case_sensitive (optional, default "false"): "true" or "false".
func checkTestPlanMentionsEveryChangedFile(req EvalRequest, args map[string]string) (bool, []Finding, error) {
	planRole := argOrDefault(args, "plan_role", "plan")
	diffRole := argOrDefault(args, "diff_role", "diff")
	matchMode := argOrDefault(args, "match", "either")
	caseSensitive := argOrDefault(args, "case_sensitive", "false") == "true"

	// Resolve plan content: Targets first, then Context.
	planContent := findInputContent(req.Targets, planRole)
	if planContent == nil {
		planContent = findInputContent(req.Context, planRole)
	}
	if planContent == nil {
		return false, []Finding{{
			Severity: SeverityError,
			Message:  fmt.Sprintf("test plan artifact (role %q) not found", planRole),
			Pointer:  planRole,
		}}, nil
	}

	// Resolve diff content from Context.
	diffContent := findInputContent(req.Context, diffRole)
	if diffContent == nil {
		return false, []Finding{{
			Severity: SeverityError,
			Message:  fmt.Sprintf("diff artifact (role %q) not found", diffRole),
			Pointer:  diffRole,
		}}, nil
	}

	changedFiles := parseChangedFiles(diffContent)

	planText := string(planContent)
	if !caseSensitive {
		planText = strings.ToLower(planText)
	}

	var findings []Finding
	for _, file := range changedFiles {
		if !fileMentioned(planText, file, matchMode, caseSensitive) {
			findings = append(findings, Finding{
				Severity: SeverityError,
				Message:  fmt.Sprintf("changed file %q not mentioned in test plan", file),
				Pointer:  file,
			})
		}
	}

	return len(findings) == 0, findings, nil
}

// fileMentioned reports whether file is mentioned in planText per the match mode.
// planText must already be lowercased when caseSensitive=false.
func fileMentioned(planText, file, matchMode string, caseSensitive bool) bool {
	candidate := file
	if !caseSensitive {
		candidate = strings.ToLower(file)
	}

	base := filepath.Base(candidate)

	switch matchMode {
	case "path":
		return strings.Contains(planText, candidate)
	case "basename":
		return strings.Contains(planText, base)
	default: // "either"
		return strings.Contains(planText, candidate) || strings.Contains(planText, base)
	}
}

// parseChangedFiles extracts the list of changed (added/modified/renamed) file
// paths from a unified diff. It returns a deduplicated, stable (first-seen)
// ordered slice of paths.
//
// The parser is a small state machine that only reads file-header lines OUTSIDE
// hunk bodies, so added/removed content lines inside a hunk can never be
// mistaken for headers. An "@@ " hunk header switches into hunk-body mode; a
// "diff --git " line switches back to header mode for the next file section.
// Without this guard, a hunk-body line that ADDS content beginning "++ "
// renders as "+++ <content>" (and a removed "-- " line as "--- <content>") and
// would be misread as a file header, yielding a phantom changed-file.
//
// Supported diff subset:
//   - Git-format unified diffs whose file sections are delimited by
//     "diff --git " lines (what `git diff` and the bench produce).
//   - Per file: the post-image "+++ " header (leading "b/"/"a/" prefix stripped,
//     tab timestamp suffix discarded), and "rename to <path>" lines (the new
//     path after a rename, which avoids the ambiguous "diff --git a/X b/Y" split
//     on paths that contain spaces).
//   - Pure deletes ("+++ /dev/null") are skipped — a deleted file is not a
//     "changed file the plan must mention".
//
// Explicitly unsupported (and not silently wrong — the affected paths are
// simply absent from the output):
//   - Plain "diff -u" output WITHOUT "diff --git " delimiters: only the first
//     file's header (before its first hunk) is read; later files are skipped.
//   - Paths with embedded spaces on "diff --git a/X b/Y" header lines (the
//     a/b prefix stripping used by git is ambiguous when paths contain spaces).
//   - Quoted or core.quotePath-escaped paths (paths containing non-ASCII or
//     special characters that git escapes with backslash sequences).
func parseChangedFiles(diff []byte) []string {
	seen := make(map[string]struct{})
	var order []string
	inHunk := false

	scanner := bufio.NewScanner(bytes.NewReader(diff))
	for scanner.Scan() {
		line := scanner.Text()

		// Track hunk state so body content is never read as a header.
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false // new file section; headers follow until the first hunk
			continue
		case strings.HasPrefix(line, "@@ "):
			inHunk = true // hunk header; body content follows
			continue
		}
		if inHunk {
			continue
		}

		// Primary source: post-image "+++ " header.
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimPrefix(line, "+++ ")
			// Strip timestamp suffix after a tab (e.g. "b/foo.go\t2024-01-01...").
			if i := strings.IndexByte(path, '\t'); i >= 0 {
				path = path[:i]
			}
			// Skip pure deletes.
			if path == "/dev/null" {
				continue
			}
			// Skip quoted/core.quotePath-escaped paths (e.g. "b/foo\303\251.go").
			// These are documented as unsupported: leave them absent rather than
			// append the raw escaped string as a bogus path.
			if strings.HasPrefix(path, "\"") {
				continue
			}
			// Strip leading "b/" or "a/" prefix added by git.
			path = stripDiffPrefix(path)
			if path == "" {
				continue
			}
			if _, dup := seen[path]; !dup {
				seen[path] = struct{}{}
				order = append(order, path)
			}
			continue
		}

		// Rename new-path: "rename to <path>" (git extended header line).
		if strings.HasPrefix(line, "rename to ") {
			path := strings.TrimPrefix(line, "rename to ")
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			// Skip quoted/escaped paths (unsupported — absent, not bogus).
			if strings.HasPrefix(path, "\"") {
				continue
			}
			if _, dup := seen[path]; !dup {
				seen[path] = struct{}{}
				order = append(order, path)
			}
		}
	}

	return order
}

// stripDiffPrefix strips the leading "b/" or "a/" prefix that git prepends to
// diff header paths.
func stripDiffPrefix(path string) string {
	if strings.HasPrefix(path, "b/") {
		return path[2:]
	}
	if strings.HasPrefix(path, "a/") {
		return path[2:]
	}
	return path
}

// ---- helpers ----

// findInputContent returns the Content of the first EvalInput in inputs whose
// Role matches role, or nil if none is found.
func findInputContent(inputs []EvalInput, role string) []byte {
	for _, inp := range inputs {
		if inp.Role == role {
			return inp.Content
		}
	}
	return nil
}

// argOrDefault returns args[key] if present and non-empty, otherwise def.
func argOrDefault(args map[string]string, key, def string) string {
	if v, ok := args[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}
