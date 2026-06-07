package eval

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---- helpers ----

func ptr[T any](v T) *T { return &v }

const (
	validRunID   = "run-20260522"
	validStage   = "plan"
	validCommit  = "aabbccddeeff0011223344556677889900aabbcc"                         // 40 hex
	validSHA256s = "0000000000000000000000000000000000000000000000000000000000000001" // 64 hex
)

func validRubricResult() EvalResult {
	return EvalResult{
		EvalResultVersion: 1,
		EvalID:            "rubric-run-20260522-plan-20260522T120000Z",
		Method:            "rubric",
		Score: Score{
			Kind:  ScoreRubric,
			Value: ptr(7.5),
			Max:   ptr(10.0),
		},
		Findings: []Finding{
			{Severity: SeverityInfo, Message: "well structured", Pointer: ""},
		},
		Rubric: &RubricRef{
			Path:    "rubrics/code-review.md",
			Version: "v1.0",
		},
		Targets: []ArtifactSource{
			{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
		},
		Producer: runmanifest.Producer{
			Model: "claude-opus-4-7",
		},
		Created: time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	}
}

func validPairwiseResult() EvalResult {
	commit2 := strings.Repeat("b", 40)
	art2 := strings.Repeat("1", 64)
	ctxCommit := strings.Repeat("c", 40)
	ctxArt := strings.Repeat("2", 64)
	return EvalResult{
		EvalResultVersion: 1,
		EvalID:            "pairwise-run-20260522-plan-20260522T120000Z",
		Method:            "pairwise",
		Score: Score{
			Kind:   ScorePairwise,
			Winner: WinnerA,
		},
		Findings: []Finding{},
		Targets: []ArtifactSource{
			{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
			{RunID: "run-20260523", Stage: validStage, Commit: commit2, Artifact: art2},
		},
		Context: []ArtifactSource{
			{RunID: validRunID, Stage: "task", Commit: ctxCommit, Artifact: ctxArt},
		},
		Producer: runmanifest.Producer{
			Model: "claude-opus-4-7",
		},
		Created: time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	}
}

func validAssertionResult() EvalResult {
	return EvalResult{
		EvalResultVersion: 1,
		EvalID:            "assertion-run-20260522-plan-20260522T120000Z",
		Method:            "assertion",
		Score: Score{
			Kind:   ScoreAssertion,
			Passed: ptr(true),
		},
		Findings: []Finding{
			{Severity: SeverityWarning, Message: "test plan sparse", Pointer: "section:3"},
		},
		Assertion: &AssertionSpec{
			Checks: []AssertionCheck{
				{Kind: "test-plan-exists"},
				{Kind: "mentions-changed-files", Args: map[string]string{"files": "main.go"}},
			},
		},
		Targets: []ArtifactSource{
			{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
		},
		Producer: runmanifest.Producer{},
		Created:  time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	}
}

func validGateResult() EvalResult {
	ctxCommit := strings.Repeat("c", 40)
	ctxArt := strings.Repeat("2", 64)
	return EvalResult{
		EvalResultVersion: 1,
		EvalID:            "gate-run-20260522-plan-20260522T120000Z",
		Method:            "gate",
		Score: Score{
			Kind:   ScoreGate,
			Passed: ptr(false),
		},
		Findings: []Finding{
			{Severity: SeverityError, Message: "required change", Pointer: "plan"},
		},
		Targets: []ArtifactSource{
			{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
		},
		Context: []ArtifactSource{
			{RunID: validRunID, Stage: "gate-prompt", Commit: ctxCommit, Artifact: ctxArt},
		},
		Producer: runmanifest.Producer{
			Model: "gpt-5.5",
		},
		Created: time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	}
}

// ---- golden / deterministic JSON ----

func TestRubricJSONIsExactAndDeterministic(t *testing.T) {
	r := validRubricResult()
	got1, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	got2, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON second call returned error: %v", err)
	}
	if string(got1) != string(got2) {
		t.Fatal("JSON is not deterministic")
	}
	want := `{
  "eval_result_version": 1,
  "eval_id": "rubric-run-20260522-plan-20260522T120000Z",
  "method": "rubric",
  "score": {
    "kind": "rubric",
    "value": 7.5,
    "max": 10
  },
  "findings": [
    {
      "severity": "info",
      "message": "well structured"
    }
  ],
  "rubric": {
    "path": "rubrics/code-review.md",
    "version": "v1.0"
  },
  "targets": [
    {
      "run_id": "run-20260522",
      "stage": "plan",
      "commit": "aabbccddeeff0011223344556677889900aabbcc",
      "artifact": "0000000000000000000000000000000000000000000000000000000000000001"
    }
  ],
  "producer": {
    "model": "claude-opus-4-7"
  },
  "created": "2026-05-22T12:00:00Z"
}
`
	if string(got1) != want {
		t.Errorf("rubric JSON mismatch\ngot:\n%s\nwant:\n%s", got1, want)
	}
}

func TestPairwiseJSONIsExactAndDeterministic(t *testing.T) {
	r := validPairwiseResult()
	got1, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	got2, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON second call returned error: %v", err)
	}
	if string(got1) != string(got2) {
		t.Fatal("JSON is not deterministic")
	}
	want := `{
  "eval_result_version": 1,
  "eval_id": "pairwise-run-20260522-plan-20260522T120000Z",
  "method": "pairwise",
  "score": {
    "kind": "pairwise",
    "winner": "A"
  },
  "findings": [],
  "targets": [
    {
      "run_id": "run-20260522",
      "stage": "plan",
      "commit": "aabbccddeeff0011223344556677889900aabbcc",
      "artifact": "0000000000000000000000000000000000000000000000000000000000000001"
    },
    {
      "run_id": "run-20260523",
      "stage": "plan",
      "commit": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "artifact": "1111111111111111111111111111111111111111111111111111111111111111"
    }
  ],
  "context": [
    {
      "run_id": "run-20260522",
      "stage": "task",
      "commit": "cccccccccccccccccccccccccccccccccccccccc",
      "artifact": "2222222222222222222222222222222222222222222222222222222222222222"
    }
  ],
  "producer": {
    "model": "claude-opus-4-7"
  },
  "created": "2026-05-22T12:00:00Z"
}
`
	if string(got1) != want {
		t.Errorf("pairwise JSON mismatch\ngot:\n%s\nwant:\n%s", got1, want)
	}
}

func TestAssertionJSONIsExactAndDeterministic(t *testing.T) {
	r := validAssertionResult()
	got1, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	got2, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON second call returned error: %v", err)
	}
	if string(got1) != string(got2) {
		t.Fatal("JSON is not deterministic")
	}
	want := `{
  "eval_result_version": 1,
  "eval_id": "assertion-run-20260522-plan-20260522T120000Z",
  "method": "assertion",
  "score": {
    "kind": "assertion",
    "passed": true
  },
  "findings": [
    {
      "severity": "warning",
      "message": "test plan sparse",
      "pointer": "section:3"
    }
  ],
  "assertion": {
    "checks": [
      {
        "kind": "test-plan-exists"
      },
      {
        "kind": "mentions-changed-files",
        "args": {
          "files": "main.go"
        }
      }
    ]
  },
  "targets": [
    {
      "run_id": "run-20260522",
      "stage": "plan",
      "commit": "aabbccddeeff0011223344556677889900aabbcc",
      "artifact": "0000000000000000000000000000000000000000000000000000000000000001"
    }
  ],
  "producer": {},
  "created": "2026-05-22T12:00:00Z"
}
`
	if string(got1) != want {
		t.Errorf("assertion JSON mismatch\ngot:\n%s\nwant:\n%s", got1, want)
	}
}

func TestGateJSONRoundTrip(t *testing.T) {
	r := validGateResult()
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	raw2, err := parsed.JSON()
	if err != nil {
		t.Fatalf("JSON after round-trip: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Fatalf("gate JSON round-trip changed bytes\ngot:\n%s\nwant:\n%s", raw2, raw)
	}
	if parsed.Score.Kind != ScoreGate || parsed.Score.Passed == nil || *parsed.Score.Passed != false {
		t.Fatalf("parsed gate score = %+v, want gate passed=false", parsed.Score)
	}
}

// ---- round-trips ----

func TestRubricRoundTrip(t *testing.T) {
	r := validRubricResult()
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	raw2, err := parsed.JSON()
	if err != nil {
		t.Fatalf("JSON after round-trip: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Errorf("round-trip not equal\nbefore:\n%s\nafter:\n%s", raw, raw2)
	}
}

func TestPairwiseRoundTrip(t *testing.T) {
	r := validPairwiseResult()
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	raw2, err := parsed.JSON()
	if err != nil {
		t.Fatalf("JSON after round-trip: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Errorf("round-trip not equal\nbefore:\n%s\nafter:\n%s", raw, raw2)
	}
}

func TestAssertionRoundTrip(t *testing.T) {
	r := validAssertionResult()
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	raw2, err := parsed.JSON()
	if err != nil {
		t.Fatalf("JSON after round-trip: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Errorf("round-trip not equal\nbefore:\n%s\nafter:\n%s", raw, raw2)
	}
}

func TestPointerNullVsZeroPreserved(t *testing.T) {
	// value=0.0 is distinct from absent for rubric score.
	r := validRubricResult()
	r.Score.Value = ptr(0.0)
	r.Score.Max = ptr(10.0)
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if parsed.Score.Value == nil {
		t.Fatal("Score.Value should be non-nil (zero != absent)")
	}
	if *parsed.Score.Value != 0.0 {
		t.Errorf("Score.Value = %v, want 0.0", *parsed.Score.Value)
	}
}

func TestContextLaneRoundTrip(t *testing.T) {
	r := validPairwiseResult()
	if len(r.Context) == 0 {
		t.Fatal("test setup: expected non-empty context")
	}
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if len(parsed.Context) != len(r.Context) {
		t.Errorf("context len = %d, want %d", len(parsed.Context), len(r.Context))
	}
	if len(r.Context) > 0 && parsed.Context[0].Stage != r.Context[0].Stage {
		t.Errorf("context[0].stage = %q, want %q", parsed.Context[0].Stage, r.Context[0].Stage)
	}
}

// ---- ParseJSON guards ----

func TestParseJSONRejectsUnknownFields(t *testing.T) {
	raw, err := validRubricResult().JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	// Inject an unknown top-level field.
	injected := strings.Replace(string(raw), `"eval_result_version"`, `"unknown_field": "bad","eval_result_version"`, 1)
	_, err = ParseJSON([]byte(injected))
	if err == nil {
		t.Fatal("ParseJSON accepted unknown field, want error")
	}
}

func TestParseJSONRejectsTrailingData(t *testing.T) {
	raw, err := validRubricResult().JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	trailing := append(raw, []byte(`{"extra":true}`)...)
	_, err = ParseJSON(trailing)
	if err == nil {
		t.Fatal("ParseJSON accepted trailing data, want error")
	}
}

// ---- Validate guard matrix ----

func TestValidateGuardMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvalResult)
	}{
		// version
		{"version_0", func(r *EvalResult) { r.EvalResultVersion = 0 }},
		{"version_2", func(r *EvalResult) { r.EvalResultVersion = 2 }},
		// eval_id
		{"empty_eval_id", func(r *EvalResult) { r.EvalID = "" }},
		{"eval_id_with_colon", func(r *EvalResult) { r.EvalID = "rubric:bad" }},
		{"eval_id_leading_dot", func(r *EvalResult) { r.EvalID = ".bad" }},
		{"eval_id_dotdot", func(r *EvalResult) { r.EvalID = "a..b" }},
		{"eval_id_lock", func(r *EvalResult) { r.EvalID = "foo.lock" }},
		// method
		{"empty_method", func(r *EvalResult) { r.Method = "" }},
		{"bad_method", func(r *EvalResult) { r.Method = "unknown" }},
		// score.kind != method
		{"kind_mismatch", func(r *EvalResult) { r.Score.Kind = ScorePairwise }},
		// pairwise coherence
		{"pairwise_no_winner", func(r *EvalResult) {
			r.Method = "pairwise"
			r.Score.Kind = ScorePairwise
			r.Score.Winner = WinnerNone
			r.Score.Value = nil
			r.Score.Max = nil
			r.Score.Passed = nil
			r.Rubric = nil
			r.Assertion = nil
			r.Targets = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
				{RunID: "run-20260523", Stage: validStage, Commit: strings.Repeat("b", 40), Artifact: strings.Repeat("1", 64)},
			}
		}},
		{"pairwise_has_value", func(r *EvalResult) {
			r.Method = "pairwise"
			r.Score = Score{Kind: ScorePairwise, Winner: WinnerA, Value: ptr(1.0)}
			r.Rubric = nil
			r.Assertion = nil
			r.Targets = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
				{RunID: "run-20260523", Stage: validStage, Commit: strings.Repeat("b", 40), Artifact: strings.Repeat("1", 64)},
			}
		}},
		{"pairwise_has_passed", func(r *EvalResult) {
			r.Method = "pairwise"
			r.Score = Score{Kind: ScorePairwise, Winner: WinnerB, Passed: ptr(true)}
			r.Rubric = nil
			r.Assertion = nil
			r.Targets = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
				{RunID: "run-20260523", Stage: validStage, Commit: strings.Repeat("b", 40), Artifact: strings.Repeat("1", 64)},
			}
		}},
		// rubric coherence
		{"rubric_nil_value", func(r *EvalResult) { r.Score.Value = nil }},
		{"rubric_nil_max", func(r *EvalResult) { r.Score.Max = nil }},
		{"rubric_max_zero", func(r *EvalResult) { r.Score.Max = ptr(0.0) }},
		{"rubric_max_negative", func(r *EvalResult) { r.Score.Max = ptr(-1.0) }},
		{"rubric_value_negative", func(r *EvalResult) { r.Score.Value = ptr(-0.1) }},
		{"rubric_value_exceeds_max", func(r *EvalResult) { r.Score.Value = ptr(11.0) }},
		{"rubric_winner_set", func(r *EvalResult) { r.Score.Winner = WinnerA }},
		{"rubric_passed_set", func(r *EvalResult) { r.Score.Passed = ptr(true) }},
		{"rubric_confidence_set", func(r *EvalResult) { r.Score.Confidence = ptr(0.9) }},
		// assertion coherence
		{"assertion_nil_passed", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion}
			r.Rubric = nil
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "test-plan-exists"}}}
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		{"assertion_has_value", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion, Passed: ptr(true), Value: ptr(1.0)}
			r.Rubric = nil
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		{"assertion_has_winner", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion, Passed: ptr(true), Winner: WinnerA}
			r.Rubric = nil
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		{"assertion_has_confidence", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion, Passed: ptr(true), Confidence: ptr(0.9)}
			r.Rubric = nil
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		// gate coherence
		{"gate_nil_passed", func(r *EvalResult) {
			*r = validGateResult()
			r.Score.Passed = nil
		}},
		{"gate_has_value", func(r *EvalResult) {
			*r = validGateResult()
			r.Score.Value = ptr(1.0)
		}},
		{"gate_has_max", func(r *EvalResult) {
			*r = validGateResult()
			r.Score.Max = ptr(1.0)
		}},
		{"gate_has_winner", func(r *EvalResult) {
			*r = validGateResult()
			r.Score.Winner = WinnerA
		}},
		{"gate_has_confidence", func(r *EvalResult) {
			*r = validGateResult()
			r.Score.Confidence = ptr(0.9)
		}},
		// method<->config mismatches
		{"rubric_method_no_rubric_config", func(r *EvalResult) { r.Rubric = nil }},
		{"rubric_method_has_assertion_config", func(r *EvalResult) {
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
		}},
		{"rubric_config_path_empty", func(r *EvalResult) { r.Rubric = &RubricRef{Path: "", Version: "v1"} }},
		{"rubric_config_version_empty", func(r *EvalResult) { r.Rubric = &RubricRef{Path: "r.md", Version: ""} }},
		{"assertion_method_no_assertion_config", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion, Passed: ptr(true)}
			r.Rubric = nil
			r.Assertion = nil
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		{"assertion_method_empty_checks", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion, Passed: ptr(true)}
			r.Rubric = nil
			r.Assertion = &AssertionSpec{Checks: nil}
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		{"assertion_check_kind_empty", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion, Passed: ptr(true)}
			r.Rubric = nil
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: ""}}}
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		{"assertion_method_has_rubric_config", func(r *EvalResult) {
			r.Method = "assertion"
			r.Score = Score{Kind: ScoreAssertion, Passed: ptr(true)}
			r.Rubric = &RubricRef{Path: "r.md", Version: "v1"}
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
			r.Targets = []ArtifactSource{{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s}}
		}},
		{"pairwise_method_has_rubric_config", func(r *EvalResult) {
			r.Method = "pairwise"
			r.Score = Score{Kind: ScorePairwise, Winner: WinnerA}
			r.Rubric = &RubricRef{Path: "r.md", Version: "v1"}
			r.Assertion = nil
			r.Targets = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
				{RunID: "run-20260523", Stage: validStage, Commit: strings.Repeat("b", 40), Artifact: strings.Repeat("1", 64)},
			}
		}},
		{"pairwise_method_has_assertion_config", func(r *EvalResult) {
			r.Method = "pairwise"
			r.Score = Score{Kind: ScorePairwise, Winner: WinnerA}
			r.Rubric = nil
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
			r.Targets = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: strings.Repeat("1", 64)},
				{RunID: "run-20260523", Stage: validStage, Commit: strings.Repeat("b", 40), Artifact: strings.Repeat("2", 64)},
			}
		}},
		{"gate_method_has_rubric_config", func(r *EvalResult) {
			*r = validGateResult()
			r.Rubric = &RubricRef{Path: "r.md", Version: "v1"}
		}},
		{"gate_method_has_assertion_config", func(r *EvalResult) {
			*r = validGateResult()
			r.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
		}},
		// target count
		{"rubric_wrong_target_count_0", func(r *EvalResult) { r.Targets = nil }},
		{"rubric_wrong_target_count_2", func(r *EvalResult) {
			r.Targets = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
				{RunID: "run-20260523", Stage: validStage, Commit: strings.Repeat("b", 40), Artifact: strings.Repeat("1", 64)},
			}
		}},
		{"pairwise_wrong_target_count_1", func(r *EvalResult) {
			r.Method = "pairwise"
			r.Score = Score{Kind: ScorePairwise, Winner: WinnerA}
			r.Rubric = nil
			r.Assertion = nil
			r.Targets = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
			}
		}},
		{"gate_wrong_target_count_0", func(r *EvalResult) {
			*r = validGateResult()
			r.Targets = nil
		}},
		{"gate_wrong_target_count_2", func(r *EvalResult) {
			*r = validGateResult()
			r.Targets = append(r.Targets, ArtifactSource{RunID: "run-20260523", Stage: validStage, Commit: strings.Repeat("b", 40), Artifact: strings.Repeat("1", 64)})
		}},
		// bad target fields
		{"target_bad_run_id", func(r *EvalResult) {
			r.Targets[0].RunID = "bad:id"
		}},
		{"target_bad_stage", func(r *EvalResult) {
			r.Targets[0].Stage = "bad stage"
		}},
		{"target_bad_commit", func(r *EvalResult) {
			r.Targets[0].Commit = "notahex"
		}},
		{"target_commit_wrong_length_all_hex", func(r *EvalResult) {
			// 39 lowercase-hex chars: passes the char check but fails the
			// 40/64 length guard (pins isHexOID's length rule specifically).
			r.Targets[0].Commit = "abcdef0123456789abcdef0123456789abcdef0"
		}},
		{"target_bad_artifact", func(r *EvalResult) {
			r.Targets[0].Artifact = "tooshort"
		}},
		{"target_artifact_wrong_length_all_hex", func(r *EvalResult) {
			// 63 lowercase-hex chars: passes the char check but fails the
			// 64-char length guard (pins validSHA256's length rule).
			r.Targets[0].Artifact = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef012345678"
		}},
		// bad context fields
		{"context_bad_run_id", func(r *EvalResult) {
			r.Context = []ArtifactSource{
				{RunID: "bad:id", Stage: validStage, Commit: validCommit, Artifact: validSHA256s},
			}
		}},
		{"context_bad_commit", func(r *EvalResult) {
			r.Context = []ArtifactSource{
				{RunID: validRunID, Stage: validStage, Commit: "UPPERCASE40AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Artifact: validSHA256s},
			}
		}},
		// findings
		{"finding_bad_severity", func(r *EvalResult) {
			r.Findings = []Finding{{Severity: "critical", Message: "oops"}}
		}},
		{"finding_empty_message", func(r *EvalResult) {
			r.Findings = []Finding{{Severity: SeverityError, Message: ""}}
		}},
		{"finding_whitespace_message", func(r *EvalResult) {
			r.Findings = []Finding{{Severity: SeverityError, Message: "   "}}
		}},
		// zero created
		{"zero_created", func(r *EvalResult) { r.Created = time.Time{} }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := validRubricResult()
			tc.mutate(&r)
			if err := r.Validate(); err == nil {
				t.Fatalf("Validate succeeded, want error for %s", tc.name)
			}
		})
	}
}

// ---- IsValidEvalID table ----

func TestIsValidEvalID(t *testing.T) {
	valid := []string{
		"rubric-run-20260522-plan-20260522T120000Z",
		"pairwise-abc-def-20260101T000000Z",
		"assertion-run1-stage1-20260101T000000Z",
		"a",
		"abc-123",
		"abc.def",
		"ABC_123",
	}
	for _, s := range valid {
		if !IsValidEvalID(s) {
			t.Errorf("IsValidEvalID(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"bad:id",
		".leading-dot",
		"trailing-dot.",
		"a..b",
		"foo.lock",
		"has space",
		"has/slash",
	}
	for _, s := range invalid {
		if IsValidEvalID(s) {
			t.Errorf("IsValidEvalID(%q) = true, want false", s)
		}
	}
}

// ---- EvalIDBase ----

func TestEvalIDBase(t *testing.T) {
	ts := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	got := EvalIDBase("rubric", "run-20260522", "plan", ts)
	want := "rubric-run-20260522-plan-20260522T120000Z"
	if got != want {
		t.Errorf("EvalIDBase = %q, want %q", got, want)
	}
}

func TestEvalIDBaseFormatsCompactUTC(t *testing.T) {
	ts := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	base := EvalIDBase("pairwise", "run-1", "stage", ts)
	if !strings.Contains(base, "20260102T150405Z") {
		t.Errorf("EvalIDBase = %q, want compact UTC timestamp 20260102T150405Z", base)
	}
	if strings.Contains(base, ":") {
		t.Errorf("EvalIDBase = %q, must not contain colon", base)
	}
}

// ---- AllocateEvalID collision ----

func TestAllocateEvalIDCollisionSuffix(t *testing.T) {
	repo := initGitRepo(t)
	store := refstore.New(repo)
	ctx := context.Background()

	base := "rubric-run-20260522-plan-20260522T120000Z"

	// Seed base in the store.
	r := validRubricResult()
	r.EvalID = base
	if _, err := (Writer{Store: store}).Write(ctx, r, WriteOptions{}); err != nil {
		t.Fatalf("Write base: %v", err)
	}

	// AllocateEvalID must return base-2 (base is taken).
	got, err := AllocateEvalID(ctx, store, base)
	if err != nil {
		t.Fatalf("AllocateEvalID: %v", err)
	}
	want := base + "-2"
	if got != want {
		t.Errorf("AllocateEvalID = %q, want %q", got, want)
	}
}

// ---- Writer round-trip ----

func TestWriterRoundTrip(t *testing.T) {
	repo := initGitRepo(t)
	store := refstore.New(repo)
	ctx := context.Background()

	r := validRubricResult()
	commit, err := (Writer{Store: store}).Write(ctx, r, WriteOptions{Message: "test eval write"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if commit == "" {
		t.Fatal("Write returned empty commit")
	}

	// Verify the ref points to the commit.
	ref := evalsPrefix + r.EvalID
	resolved, err := store.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != commit {
		t.Errorf("resolved commit = %q, want %q", resolved, commit)
	}

	// Read back and parse.
	raw, err := store.ReadFile(ctx, ref, evalResultPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}

	// Verify key fields.
	if parsed.EvalID != r.EvalID {
		t.Errorf("EvalID = %q, want %q", parsed.EvalID, r.EvalID)
	}
	if parsed.Method != r.Method {
		t.Errorf("Method = %q, want %q", parsed.Method, r.Method)
	}
	if parsed.Score.Kind != r.Score.Kind {
		t.Errorf("Score.Kind = %q, want %q", parsed.Score.Kind, r.Score.Kind)
	}
}

func TestWriterCollisionReturnsErrRefExists(t *testing.T) {
	repo := initGitRepo(t)
	store := refstore.New(repo)
	ctx := context.Background()

	r := validRubricResult()
	if _, err := (Writer{Store: store}).Write(ctx, r, WriteOptions{}); err != nil {
		t.Fatalf("first Write: %v", err)
	}

	// Second write with same eval_id must fail with ErrRefExists.
	_, err := (Writer{Store: store}).Write(ctx, r, WriteOptions{})
	if !errors.Is(err, refstore.ErrRefExists) {
		t.Errorf("second Write err = %v, want ErrRefExists", err)
	}
}

func TestWriterPairwiseWithContext(t *testing.T) {
	repo := initGitRepo(t)
	store := refstore.New(repo)
	ctx := context.Background()

	r := validPairwiseResult()
	if _, err := (Writer{Store: store}).Write(ctx, r, WriteOptions{}); err != nil {
		t.Fatalf("Write pairwise: %v", err)
	}

	ref := evalsPrefix + r.EvalID
	raw, err := store.ReadFile(ctx, ref, evalResultPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if len(parsed.Targets) != 2 {
		t.Errorf("len(Targets) = %d, want 2", len(parsed.Targets))
	}
	if len(parsed.Context) != 1 {
		t.Errorf("len(Context) = %d, want 1", len(parsed.Context))
	}
}

// ---- initGitRepo (mirrors runmanifest/manifest_test.go) ----

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "--initial-branch=main")
	gitCmd(t, dir, "config", "user.name", "Test User")
	gitCmd(t, dir, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(dir+"/README.md", []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitCmd(t, dir, "add", "README.md")
	gitCmd(t, dir, "commit", "-m", "initial")
	return dir
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ---- JudgeID and Seed backward-compat round-trip tests ----

// TestJudgeIDAndSeedOmitempty verifies that an EvalResult with empty JudgeID and
// nil Seed serializes byte-identical to the pre-field JSON (no "judge_id" or
// "seed" keys appear), preserving backward compatibility with old doc readers.
func TestJudgeIDAndSeedOmitempty(t *testing.T) {
	r := validPairwiseResult()
	// Explicitly confirm the zero values.
	r.JudgeID = ""
	r.Seed = nil

	got, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Neither "judge_id" nor "seed" must appear in the output.
	if strings.Contains(string(got), "judge_id") {
		t.Errorf("JSON contains 'judge_id' despite empty JudgeID:\n%s", got)
	}
	if strings.Contains(string(got), `"seed"`) {
		t.Errorf("JSON contains 'seed' despite nil Seed:\n%s", got)
	}

	// Re-serialise a pre-field result (no JudgeID/Seed fields set) and compare.
	pre := validPairwiseResult()
	preSerialized, err := pre.JSON()
	if err != nil {
		t.Fatalf("pre-field JSON: %v", err)
	}
	if string(got) != string(preSerialized) {
		t.Errorf("byte mismatch: zero-value fields changed the serialisation\ngot:\n%s\npre:\n%s", got, preSerialized)
	}
}

// TestJudgeIDAndSeedRoundTrip verifies that an EvalResult with non-empty JudgeID
// and non-nil Seed round-trips correctly through JSON/ParseJSON.
func TestJudgeIDAndSeedRoundTrip(t *testing.T) {
	seed := int64(42)
	r := validPairwiseResult()
	r.JudgeID = strings.Repeat("a", 64) // valid hex-like string (not validated)
	r.Seed = &seed

	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Both fields must appear in the output.
	if !strings.Contains(string(raw), "judge_id") {
		t.Errorf("JSON missing 'judge_id':\n%s", raw)
	}
	if !strings.Contains(string(raw), `"seed"`) {
		t.Errorf("JSON missing 'seed':\n%s", raw)
	}

	// Round-trip.
	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if parsed.JudgeID != r.JudgeID {
		t.Errorf("JudgeID = %q, want %q", parsed.JudgeID, r.JudgeID)
	}
	if parsed.Seed == nil {
		t.Fatal("Seed is nil after round-trip")
	}
	if *parsed.Seed != seed {
		t.Errorf("Seed = %d, want %d", *parsed.Seed, seed)
	}

	// Re-serialise and verify byte-equality.
	raw2, err := parsed.JSON()
	if err != nil {
		t.Fatalf("JSON after round-trip: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Errorf("round-trip not byte-equal\nbefore:\n%s\nafter:\n%s", raw, raw2)
	}
}

// TestParseJSONOldDocWithoutJudgeIDOrSeed verifies that a persisted doc that
// predates the JudgeID/Seed fields (no "judge_id" or "seed" keys) is still
// parsed successfully with JudgeID="" and Seed==nil.
func TestParseJSONOldDocWithoutJudgeIDOrSeed(t *testing.T) {
	// Serialise a clean pairwise result (no JudgeID/Seed) and confirm the keys
	// are absent (established by TestJudgeIDAndSeedOmitempty), then parse it.
	r := validPairwiseResult()
	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Sanity check: the old-style doc must not contain judge_id/seed.
	if strings.Contains(string(raw), "judge_id") || strings.Contains(string(raw), `"seed"`) {
		t.Fatalf("test setup: pre-field doc unexpectedly contains judge_id or seed:\n%s", raw)
	}

	parsed, err := ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON of old doc: %v", err)
	}
	if parsed.JudgeID != "" {
		t.Errorf("JudgeID = %q, want empty for old doc", parsed.JudgeID)
	}
	if parsed.Seed != nil {
		t.Errorf("Seed = %v, want nil for old doc", *parsed.Seed)
	}
}

// TestValidateAcceptsEmptyJudgeIDAndNilSeed verifies that Validate does not
// reject a valid EvalResult simply because JudgeID is empty or Seed is nil.
func TestValidateAcceptsEmptyJudgeIDAndNilSeed(t *testing.T) {
	r := validPairwiseResult()
	r.JudgeID = ""
	r.Seed = nil
	if err := r.Validate(); err != nil {
		t.Errorf("Validate rejected empty JudgeID and nil Seed: %v", err)
	}
}

// TestValidateAcceptsNonEmptyJudgeIDAndSeed verifies that Validate accepts
// a populated JudgeID and Seed.
func TestValidateAcceptsNonEmptyJudgeIDAndSeed(t *testing.T) {
	seed := int64(99)
	r := validPairwiseResult()
	r.JudgeID = strings.Repeat("b", 64)
	r.Seed = &seed
	if err := r.Validate(); err != nil {
		t.Errorf("Validate rejected non-empty JudgeID and Seed: %v", err)
	}
}
