package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

const (
	evalResultPath    = "eval_result.json"
	evalsPrefix       = "refs/etude/evals/"
	evalResultVersion = 1
)

var ErrInvalidEvalResult = errors.New("invalid eval result")

// EvalResult is the durable eval document persisted at refs/etude/evals/<eval-id>.
// Method is explicit at top level (like Manifest.Workflow). Targets and Context
// are recorded as separate ArtifactSource lists.
type EvalResult struct {
	EvalResultVersion int
	EvalID            string
	Method            string
	Score             Score
	Findings          []Finding
	Rubric            *RubricRef       // present iff Method=="rubric"
	Assertion         *AssertionSpec   // present iff Method=="assertion"
	Targets           []ArtifactSource // len 1 rubric/assertion, len 2 pairwise (A,B)
	Context           []ArtifactSource // optional unscored inputs
	Producer          runmanifest.Producer
	Created           time.Time // RFC3339Nano UTC
	// JudgeID is the stable fingerprint of the judge that produced this result.
	// Empty for unidentified judges (e.g. StubJudge). Omitted from JSON when empty.
	JudgeID string
	// Seed is the PairwiseEvaluator seed used during evaluation. Nil when absent
	// (legacy docs or non-pairwise methods). Omitted from JSON when nil.
	Seed *int64
}

// Validate enforces the full EvalResult invariants.
func (r EvalResult) Validate() error {
	if r.EvalResultVersion != evalResultVersion {
		return fmt.Errorf("%w: eval_result_version must be 1, got %d", ErrInvalidEvalResult, r.EvalResultVersion)
	}
	if !IsValidEvalID(r.EvalID) {
		return fmt.Errorf("%w: invalid eval_id %q", ErrInvalidEvalResult, r.EvalID)
	}
	if err := validateEvalIdentifier("method", r.Method); err != nil {
		return err
	}
	validMethods := map[string]bool{"rubric": true, "pairwise": true, "assertion": true}
	if !validMethods[r.Method] {
		return fmt.Errorf("%w: method %q must be one of rubric, pairwise, assertion", ErrInvalidEvalResult, r.Method)
	}
	if string(r.Score.Kind) != r.Method {
		return fmt.Errorf("%w: score.kind %q must match method %q", ErrInvalidEvalResult, r.Score.Kind, r.Method)
	}
	if err := validateScoreCoherence(r.Method, r.Score); err != nil {
		return err
	}
	if err := validateMethodConfig(r.Method, r.Rubric, r.Assertion); err != nil {
		return err
	}
	if err := validateTargets(r.Method, r.Targets); err != nil {
		return err
	}
	for i, ctx := range r.Context {
		if err := validateArtifactSource(fmt.Sprintf("context[%d]", i), ctx); err != nil {
			return err
		}
	}
	for i, f := range r.Findings {
		if err := validateFinding(i, f); err != nil {
			return err
		}
	}
	if r.Created.IsZero() {
		return fmt.Errorf("%w: created required", ErrInvalidEvalResult)
	}
	return nil
}

func validateScoreCoherence(method string, s Score) error {
	switch method {
	case "pairwise":
		if s.Winner != WinnerA && s.Winner != WinnerB && s.Winner != WinnerTie {
			return fmt.Errorf("%w: pairwise score.winner must be A, B, or tie; got %q", ErrInvalidEvalResult, s.Winner)
		}
		if s.Value != nil {
			return fmt.Errorf("%w: pairwise score.value must be absent", ErrInvalidEvalResult)
		}
		if s.Max != nil {
			return fmt.Errorf("%w: pairwise score.max must be absent", ErrInvalidEvalResult)
		}
		if s.Passed != nil {
			return fmt.Errorf("%w: pairwise score.passed must be absent", ErrInvalidEvalResult)
		}
	case "rubric":
		if s.Value == nil {
			return fmt.Errorf("%w: rubric score.value required", ErrInvalidEvalResult)
		}
		if s.Max == nil {
			return fmt.Errorf("%w: rubric score.max required", ErrInvalidEvalResult)
		}
		if *s.Max <= 0 {
			return fmt.Errorf("%w: rubric score.max must be > 0", ErrInvalidEvalResult)
		}
		if *s.Value < 0 || *s.Value > *s.Max {
			return fmt.Errorf("%w: rubric score.value must be in [0, max]", ErrInvalidEvalResult)
		}
		if s.Winner != WinnerNone {
			return fmt.Errorf("%w: rubric score.winner must be absent", ErrInvalidEvalResult)
		}
		if s.Passed != nil {
			return fmt.Errorf("%w: rubric score.passed must be absent", ErrInvalidEvalResult)
		}
		if s.Confidence != nil {
			return fmt.Errorf("%w: rubric score.confidence not allowed for rubric method", ErrInvalidEvalResult)
		}
	case "assertion":
		if s.Passed == nil {
			return fmt.Errorf("%w: assertion score.passed required", ErrInvalidEvalResult)
		}
		if s.Value != nil {
			return fmt.Errorf("%w: assertion score.value must be absent", ErrInvalidEvalResult)
		}
		if s.Max != nil {
			return fmt.Errorf("%w: assertion score.max must be absent", ErrInvalidEvalResult)
		}
		if s.Winner != WinnerNone {
			return fmt.Errorf("%w: assertion score.winner must be absent", ErrInvalidEvalResult)
		}
		if s.Confidence != nil {
			return fmt.Errorf("%w: assertion score.confidence must be absent", ErrInvalidEvalResult)
		}
	}
	return nil
}

func validateMethodConfig(method string, rubric *RubricRef, assertion *AssertionSpec) error {
	switch method {
	case "rubric":
		if rubric == nil {
			return fmt.Errorf("%w: rubric method requires rubric config", ErrInvalidEvalResult)
		}
		if strings.TrimSpace(rubric.Path) == "" {
			return fmt.Errorf("%w: rubric.path required", ErrInvalidEvalResult)
		}
		if strings.TrimSpace(rubric.Version) == "" {
			return fmt.Errorf("%w: rubric.version required", ErrInvalidEvalResult)
		}
		if assertion != nil {
			return fmt.Errorf("%w: rubric method must not have assertion config", ErrInvalidEvalResult)
		}
	case "assertion":
		if assertion == nil {
			return fmt.Errorf("%w: assertion method requires assertion config", ErrInvalidEvalResult)
		}
		if len(assertion.Checks) == 0 {
			return fmt.Errorf("%w: assertion.checks must have at least one check", ErrInvalidEvalResult)
		}
		for i, c := range assertion.Checks {
			if strings.TrimSpace(c.Kind) == "" {
				return fmt.Errorf("%w: assertion.checks[%d].kind required", ErrInvalidEvalResult, i)
			}
		}
		if rubric != nil {
			return fmt.Errorf("%w: assertion method must not have rubric config", ErrInvalidEvalResult)
		}
	case "pairwise":
		if rubric != nil {
			return fmt.Errorf("%w: pairwise method must not have rubric config", ErrInvalidEvalResult)
		}
		if assertion != nil {
			return fmt.Errorf("%w: pairwise method must not have assertion config", ErrInvalidEvalResult)
		}
	}
	return nil
}

func validateTargets(method string, targets []ArtifactSource) error {
	want := 1
	if method == "pairwise" {
		want = 2
	}
	if len(targets) != want {
		return fmt.Errorf("%w: method %q requires %d target(s), got %d", ErrInvalidEvalResult, method, want, len(targets))
	}
	for i, t := range targets {
		if err := validateArtifactSource(fmt.Sprintf("targets[%d]", i), t); err != nil {
			return err
		}
	}
	return nil
}

func validateArtifactSource(label string, s ArtifactSource) error {
	if !runmanifest.IsValidRunID(s.RunID) {
		return fmt.Errorf("%w: %s.run_id %q invalid", ErrInvalidEvalResult, label, s.RunID)
	}
	if !runmanifest.IsValidIdentifier(s.Stage) {
		return fmt.Errorf("%w: %s.stage %q invalid", ErrInvalidEvalResult, label, s.Stage)
	}
	if !isHexOID(s.Commit) {
		return fmt.Errorf("%w: %s.commit must be a 40- or 64-char lowercase hex git oid", ErrInvalidEvalResult, label)
	}
	if !validSHA256(s.Artifact) {
		return fmt.Errorf("%w: %s.artifact must be a 64-char lowercase hex sha256", ErrInvalidEvalResult, label)
	}
	return nil
}

func validateFinding(index int, f Finding) error {
	validSeverities := map[Severity]bool{SeverityInfo: true, SeverityWarning: true, SeverityError: true}
	if !validSeverities[f.Severity] {
		return fmt.Errorf("%w: findings[%d].severity %q must be info, warning, or error", ErrInvalidEvalResult, index, f.Severity)
	}
	if strings.TrimSpace(f.Message) == "" {
		return fmt.Errorf("%w: findings[%d].message required", ErrInvalidEvalResult, index)
	}
	return nil
}

// JSON serialises the EvalResult deterministically. Validate is called first.
func (r EvalResult) JSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.toJSON()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseJSON decodes an eval_result.json document. Rejects unknown fields,
// trailing data, and invalid content.
func ParseJSON(content []byte) (EvalResult, error) {
	var payload evalResultJSON
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return EvalResult{}, fmt.Errorf("%w: decode: %v", ErrInvalidEvalResult, err)
	}
	if err := ensureEOF(dec); err != nil {
		return EvalResult{}, err
	}
	result, err := payload.toEvalResult()
	if err != nil {
		return EvalResult{}, err
	}
	if err := result.Validate(); err != nil {
		return EvalResult{}, err
	}
	return result, nil
}

// IsValidEvalID reports whether s is a valid eval identifier. It applies the
// same rules as runmanifest.IsValidRunID (letters/digits/_-. charset, no
// leading/trailing dot, no "..", no ".lock") so refstore.validateRef accepts
// refs/etude/evals/<s>.
func IsValidEvalID(s string) bool {
	return runmanifest.IsValidRunID(s)
}

// EvalIDBase formats the base eval id from method, run id, stage, and time.
// Format: <method>-<runID>-<stage>-<UTC yyyymmddThhmmssZ>
// The timestamp uses compact UTC layout with no colons (colons are rejected by
// refstore validateFilePath and the identifier charset).
func EvalIDBase(method, runID, stage string, t time.Time) string {
	return fmt.Sprintf("%s-%s-%s-%s", method, runID, stage, t.UTC().Format("20060102T150405Z"))
}

// AllocateEvalID probes for a free eval id, trying base then base-2..base-10.
// Returns an error if none are free. Mirrors allocateReplayRunID.
func AllocateEvalID(ctx context.Context, store refstore.Store, base string) (string, error) {
	if !IsValidEvalID(base) {
		return "", fmt.Errorf("derived eval id %q is not a valid eval id", base)
	}

	candidates := make([]string, 0, 11)
	candidates = append(candidates, base)
	for n := 2; n <= 10; n++ {
		candidates = append(candidates, fmt.Sprintf("%s-%d", base, n))
	}

	for _, id := range candidates {
		ref := evalsPrefix + id
		_, err := store.Resolve(ctx, ref)
		if errors.Is(err, refstore.ErrNotFound) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("probe eval id %q: %w", id, err)
		}
	}
	return "", fmt.Errorf("could not allocate unique eval id after 10 attempts (base: %s)", base)
}

// Writer persists EvalResult documents under refs/etude/evals/.
type Writer struct {
	Store refstore.Store
}

// WriteOptions configures a Write call. Evals are create-only; there is no
// ExpectedOld (collision is a hard error returning ErrRefExists).
type WriteOptions struct {
	Message string
}

// Write validates and persists result to refs/etude/evals/<result.EvalID>.
// The tree holds only eval_result.json. Returns ErrRefExists on collision.
func (w Writer) Write(ctx context.Context, result EvalResult, opts WriteOptions) (string, error) {
	if err := result.Validate(); err != nil {
		return "", err
	}
	resultBytes, err := result.JSON()
	if err != nil {
		return "", err
	}
	files := map[string][]byte{
		evalResultPath: resultBytes,
	}
	msg := opts.Message
	if strings.TrimSpace(msg) == "" {
		msg = fmt.Sprintf("eval: %s %s", result.Method, result.EvalID)
	}
	return w.Store.WriteCommit(ctx, evalsPrefix+result.EvalID, files, refstore.WriteOptions{
		Message: msg,
	})
}

// isHexOID reports whether s is a valid git object id: exactly 40 (SHA-1) or
// 64 (SHA-256) lowercase hex characters. Mirrors runmanifest.isHexOID and
// refstore.validateOID; eval package keeps its own copy to avoid relying on
// unexported symbols from those packages.
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

// validSHA256 reports whether s is a valid 64-char lowercase hex SHA-256 sum.
func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// validateEvalIdentifier wraps runmanifest.IsValidIdentifier with an eval sentinel error.
func validateEvalIdentifier(name, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s required", ErrInvalidEvalResult, name)
	}
	if !runmanifest.IsValidIdentifier(value) {
		return fmt.Errorf("%w: invalid %s %q", ErrInvalidEvalResult, name, value)
	}
	return nil
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidEvalResult, err)
	}
	return fmt.Errorf("%w: trailing data", ErrInvalidEvalResult)
}

// ---- JSON wire types ----

type evalResultJSON struct {
	EvalResultVersion int            `json:"eval_result_version"`
	EvalID            string         `json:"eval_id"`
	Method            string         `json:"method"`
	Score             scoreJSON      `json:"score"`
	Findings          []findingJSON  `json:"findings"`
	Rubric            *rubricJSON    `json:"rubric,omitempty"`
	Assertion         *assertionJSON `json:"assertion,omitempty"`
	Targets           []sourceJSON   `json:"targets"`
	Context           []sourceJSON   `json:"context,omitempty"`
	Producer          producerJSON   `json:"producer"`
	Created           string         `json:"created"`
	JudgeID           string         `json:"judge_id,omitempty"`
	Seed              *int64         `json:"seed,omitempty"`
}

type scoreJSON struct {
	Kind       string   `json:"kind"`
	Value      *float64 `json:"value,omitempty"`
	Max        *float64 `json:"max,omitempty"`
	Winner     string   `json:"winner,omitempty"`
	Passed     *bool    `json:"passed,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type findingJSON struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Pointer  string `json:"pointer,omitempty"`
}

type rubricJSON struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type assertionCheckJSON struct {
	Kind string            `json:"kind"`
	Args map[string]string `json:"args,omitempty"`
}

type assertionJSON struct {
	Checks []assertionCheckJSON `json:"checks"`
}

type sourceJSON struct {
	RunID    string `json:"run_id"`
	Stage    string `json:"stage"`
	Commit   string `json:"commit"`
	Artifact string `json:"artifact"`
}

type harnessJSON struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type skillJSON struct {
	ID      string `json:"id,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Version string `json:"version,omitempty"`
}

type producerJSON struct {
	Harness *harnessJSON `json:"harness,omitempty"`
	Model   string       `json:"model,omitempty"`
	Skill   *skillJSON   `json:"skill,omitempty"`
}

func (r EvalResult) toJSON() evalResultJSON {
	findings := make([]findingJSON, 0, len(r.Findings))
	for _, f := range r.Findings {
		findings = append(findings, findingJSON{
			Severity: string(f.Severity),
			Message:  f.Message,
			Pointer:  f.Pointer,
		})
	}

	targets := make([]sourceJSON, 0, len(r.Targets))
	for _, t := range r.Targets {
		targets = append(targets, sourceJSON{
			RunID:    t.RunID,
			Stage:    t.Stage,
			Commit:   t.Commit,
			Artifact: t.Artifact,
		})
	}

	var contextSources []sourceJSON
	if len(r.Context) > 0 {
		contextSources = make([]sourceJSON, 0, len(r.Context))
		for _, c := range r.Context {
			contextSources = append(contextSources, sourceJSON{
				RunID:    c.RunID,
				Stage:    c.Stage,
				Commit:   c.Commit,
				Artifact: c.Artifact,
			})
		}
	}

	var rubric *rubricJSON
	if r.Rubric != nil {
		rubric = &rubricJSON{Path: r.Rubric.Path, Version: r.Rubric.Version}
	}

	var assertion *assertionJSON
	if r.Assertion != nil {
		checks := make([]assertionCheckJSON, 0, len(r.Assertion.Checks))
		for _, c := range r.Assertion.Checks {
			checks = append(checks, assertionCheckJSON{Kind: c.Kind, Args: c.Args})
		}
		assertion = &assertionJSON{Checks: checks}
	}

	producer := producerToJSON(r.Producer)

	return evalResultJSON{
		EvalResultVersion: evalResultVersion,
		EvalID:            r.EvalID,
		Method:            r.Method,
		Score: scoreJSON{
			Kind:       string(r.Score.Kind),
			Value:      r.Score.Value,
			Max:        r.Score.Max,
			Winner:     string(r.Score.Winner),
			Passed:     r.Score.Passed,
			Confidence: r.Score.Confidence,
		},
		Findings:  findings,
		Rubric:    rubric,
		Assertion: assertion,
		Targets:   targets,
		Context:   contextSources,
		Producer:  producer,
		Created:   r.Created.UTC().Format(time.RFC3339Nano),
		JudgeID:   r.JudgeID,
		Seed:      r.Seed,
	}
}

func producerToJSON(p runmanifest.Producer) producerJSON {
	pj := producerJSON{
		Model: p.Model,
	}
	if p.Harness.Name != "" {
		pj.Harness = &harnessJSON{Name: p.Harness.Name, Version: p.Harness.Version}
	}
	if p.Skill.ID != "" || p.Skill.Repo != "" || p.Skill.Version != "" {
		pj.Skill = &skillJSON{ID: p.Skill.ID, Repo: p.Skill.Repo, Version: p.Skill.Version}
	}
	return pj
}

func (j evalResultJSON) toEvalResult() (EvalResult, error) {
	created, err := time.Parse(time.RFC3339Nano, j.Created)
	if err != nil {
		return EvalResult{}, fmt.Errorf("%w: created: %v", ErrInvalidEvalResult, err)
	}

	findings := make([]Finding, 0, len(j.Findings))
	for _, f := range j.Findings {
		findings = append(findings, Finding{
			Severity: Severity(f.Severity),
			Message:  f.Message,
			Pointer:  f.Pointer,
		})
	}

	targets := make([]ArtifactSource, 0, len(j.Targets))
	for _, t := range j.Targets {
		targets = append(targets, ArtifactSource{
			RunID:    t.RunID,
			Stage:    t.Stage,
			Commit:   t.Commit,
			Artifact: t.Artifact,
		})
	}

	contextSources := make([]ArtifactSource, 0, len(j.Context))
	for _, c := range j.Context {
		contextSources = append(contextSources, ArtifactSource{
			RunID:    c.RunID,
			Stage:    c.Stage,
			Commit:   c.Commit,
			Artifact: c.Artifact,
		})
	}

	var rubric *RubricRef
	if j.Rubric != nil {
		rubric = &RubricRef{Path: j.Rubric.Path, Version: j.Rubric.Version}
	}

	var assertion *AssertionSpec
	if j.Assertion != nil {
		checks := make([]AssertionCheck, 0, len(j.Assertion.Checks))
		for _, c := range j.Assertion.Checks {
			checks = append(checks, AssertionCheck{Kind: c.Kind, Args: c.Args})
		}
		assertion = &AssertionSpec{Checks: checks}
	}

	producer := producerFromJSON(j.Producer)

	return EvalResult{
		EvalResultVersion: j.EvalResultVersion,
		EvalID:            j.EvalID,
		Method:            j.Method,
		Score: Score{
			Kind:       ScoreKind(j.Score.Kind),
			Value:      j.Score.Value,
			Max:        j.Score.Max,
			Winner:     Winner(j.Score.Winner),
			Passed:     j.Score.Passed,
			Confidence: j.Score.Confidence,
		},
		Findings:  findings,
		Rubric:    rubric,
		Assertion: assertion,
		Targets:   targets,
		Context:   contextSources,
		Producer:  producer,
		Created:   created.UTC(),
		JudgeID:   j.JudgeID,
		Seed:      j.Seed,
	}, nil
}

func producerFromJSON(j producerJSON) runmanifest.Producer {
	var harness runmanifest.Harness
	if j.Harness != nil {
		harness = runmanifest.Harness{Name: j.Harness.Name, Version: j.Harness.Version}
	}
	var skill runmanifest.Skill
	if j.Skill != nil {
		skill = runmanifest.Skill{ID: j.Skill.ID, Repo: j.Skill.Repo, Version: j.Skill.Version}
	}
	return runmanifest.Producer{
		Harness: harness,
		Model:   j.Model,
		Skill:   skill,
	}
}
