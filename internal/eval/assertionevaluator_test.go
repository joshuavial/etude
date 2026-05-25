package eval

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// ---- helpers ----

func assertionTarget(role string, content []byte) EvalInput {
	return EvalInput{Role: role, Content: content}
}

func assertionContext(role string, content []byte) EvalInput {
	return EvalInput{Role: role, Content: content}
}

func assertionSpec(checks ...AssertionCheck) *AssertionSpec {
	return &AssertionSpec{Checks: checks}
}

func assertionCheck(kind string, args map[string]string) AssertionCheck {
	return AssertionCheck{Kind: kind, Args: args}
}

func newAssertionReq(target EvalInput, spec *AssertionSpec, ctx ...EvalInput) EvalRequest {
	return EvalRequest{
		Method:    "assertion",
		Targets:   []EvalInput{target},
		Context:   ctx,
		Assertion: spec,
	}
}

// ---- head-validation tests ----

func TestAssertionEvaluator_WrongMethod(t *testing.T) {
	e := NewAssertionEvaluator()
	_, err := e.Evaluate(context.Background(), EvalRequest{
		Method:    "rubric",
		Targets:   []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "out"})),
	})
	if err == nil {
		t.Fatal("want error for non-assertion method, got nil")
	}
	if errors.Is(err, ErrAssertionConfig) {
		t.Errorf("method mismatch should not wrap ErrAssertionConfig, got %v", err)
	}
}

func TestAssertionEvaluator_NonNilRubric(t *testing.T) {
	e := NewAssertionEvaluator()
	_, err := e.Evaluate(context.Background(), EvalRequest{
		Method:    "assertion",
		Targets:   []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "out"})),
		Rubric:    &RubricRef{Path: "rubric.md", Version: "abc"},
	})
	if !errors.Is(err, ErrAssertionConfig) {
		t.Fatalf("want ErrAssertionConfig for non-nil Rubric, got %v", err)
	}
}

func TestAssertionEvaluator_NilAssertion(t *testing.T) {
	e := NewAssertionEvaluator()
	_, err := e.Evaluate(context.Background(), EvalRequest{
		Method:    "assertion",
		Targets:   []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: nil,
	})
	if !errors.Is(err, ErrAssertionConfig) {
		t.Fatalf("want ErrAssertionConfig for nil Assertion, got %v", err)
	}
}

func TestAssertionEvaluator_EmptyChecks(t *testing.T) {
	e := NewAssertionEvaluator()
	_, err := e.Evaluate(context.Background(), EvalRequest{
		Method:    "assertion",
		Targets:   []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: &AssertionSpec{Checks: []AssertionCheck{}},
	})
	if !errors.Is(err, ErrAssertionConfig) {
		t.Fatalf("want ErrAssertionConfig for empty Checks, got %v", err)
	}
}

func TestAssertionEvaluator_BlankCheckKind(t *testing.T) {
	e := NewAssertionEvaluator()
	_, err := e.Evaluate(context.Background(), EvalRequest{
		Method:  "assertion",
		Targets: []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(AssertionCheck{
			Kind: "   ", // blank after trim
			Args: nil,
		}),
	})
	if !errors.Is(err, ErrAssertionConfig) {
		t.Fatalf("want ErrAssertionConfig for blank Kind, got %v", err)
	}
}

func TestAssertionEvaluator_WrongTargetCount(t *testing.T) {
	e := NewAssertionEvaluator()
	spec := assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "out"}))

	// Zero targets.
	_, err := e.Evaluate(context.Background(), EvalRequest{
		Method:    "assertion",
		Targets:   []EvalInput{},
		Assertion: spec,
	})
	if !errors.Is(err, ErrAssertionConfig) {
		t.Errorf("zero targets: want ErrAssertionConfig, got %v", err)
	}

	// Two targets.
	src := validArtifactSource()
	_, err = e.Evaluate(context.Background(), EvalRequest{
		Method: "assertion",
		Targets: []EvalInput{
			{Role: "a", Content: []byte("x"), Source: src},
			{Role: "b", Content: []byte("y"), Source: src},
		},
		Assertion: spec,
	})
	if !errors.Is(err, ErrAssertionConfig) {
		t.Errorf("two targets: want ErrAssertionConfig, got %v", err)
	}
}

func TestAssertionEvaluator_UnknownCheckKind(t *testing.T) {
	e := NewAssertionEvaluator()
	_, err := e.Evaluate(context.Background(), EvalRequest{
		Method:    "assertion",
		Targets:   []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(assertionCheck("no-such-check", nil)),
	})
	if !errors.Is(err, ErrUnknownCheck) {
		t.Fatalf("want ErrUnknownCheck, got %v", err)
	}
}

// ---- required-artifact-presence tests ----

func TestRequiredArtifactPresence_Pass(t *testing.T) {
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("some content")),
		assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "output"})),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || !*eval_.Score.Passed {
		t.Errorf("want Passed=true, got %v", eval_.Score.Passed)
	}
	if len(eval_.Findings) != 0 {
		t.Errorf("want no findings on pass, got %v", eval_.Findings)
	}
}

func TestRequiredArtifactPresence_FailRoleAbsent(t *testing.T) {
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("some content")),
		assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "missing-role"})),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("want Passed=false, got %v", eval_.Score.Passed)
	}
	if len(eval_.Findings) != 1 {
		t.Errorf("want 1 finding, got %d", len(eval_.Findings))
	}
}

func TestRequiredArtifactPresence_FailEmptyContent(t *testing.T) {
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte{}), // empty content
		assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "output"})),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("want Passed=false for empty Content, got %v", eval_.Score.Passed)
	}
}

func TestRequiredArtifactPresence_MissingRoleArg(t *testing.T) {
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("content")),
		assertionSpec(assertionCheck("required-artifact-presence", map[string]string{})),
	)
	_, err := e.Evaluate(context.Background(), req)
	if !errors.Is(err, ErrAssertionConfig) {
		t.Fatalf("want ErrAssertionConfig for missing role arg, got %v", err)
	}
}

func TestRequiredArtifactPresence_InSelectorTargets(t *testing.T) {
	e := NewAssertionEvaluator()
	// Role "plan" is in Context, not Targets. in=targets should fail.
	req := newAssertionReq(
		assertionTarget("output", []byte("content")),
		assertionSpec(assertionCheck("required-artifact-presence", map[string]string{
			"role": "plan",
			"in":   "targets",
		})),
		assertionContext("plan", []byte("plan text")), // in Context only
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("in=targets: want Passed=false (plan is in context not targets), got true")
	}
}

func TestRequiredArtifactPresence_InSelectorContext(t *testing.T) {
	e := NewAssertionEvaluator()
	// Role "plan" is in Context. in=context should pass.
	req := newAssertionReq(
		assertionTarget("output", []byte("content")),
		assertionSpec(assertionCheck("required-artifact-presence", map[string]string{
			"role": "plan",
			"in":   "context",
		})),
		assertionContext("plan", []byte("plan text")),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || !*eval_.Score.Passed {
		t.Errorf("in=context: want Passed=true, got false")
	}
}

// ---- test-plan-mentions-every-changed-file tests ----

const sampleDiff = `diff --git a/internal/foo/bar.go b/internal/foo/bar.go
index abc1234..def5678 100644
--- a/internal/foo/bar.go
+++ b/internal/foo/bar.go
@@ -1,3 +1,4 @@
 package foo
+
+// changed
diff --git a/cmd/main.go b/cmd/main.go
index 111..222 100644
--- a/cmd/main.go
+++ b/cmd/main.go
@@ -1 +1,2 @@
 package main
+// change
`

func TestTestPlanMentions_Pass_Path(t *testing.T) {
	planText := "The test plan covers internal/foo/bar.go and cmd/main.go changes."
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("artifact content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", map[string]string{
			"match": "path",
		})),
		assertionContext("plan", []byte(planText)),
		assertionContext("diff", []byte(sampleDiff)),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || !*eval_.Score.Passed {
		t.Errorf("match=path: want Passed=true, got false; findings: %v", eval_.Findings)
	}
}

func TestTestPlanMentions_Pass_Basename(t *testing.T) {
	// Plan only mentions basenames.
	planText := "Test plan mentions bar.go and main.go."
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("artifact content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", map[string]string{
			"match": "basename",
		})),
		assertionContext("plan", []byte(planText)),
		assertionContext("diff", []byte(sampleDiff)),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || !*eval_.Score.Passed {
		t.Errorf("match=basename: want Passed=true, got false; findings: %v", eval_.Findings)
	}
}

func TestTestPlanMentions_FailWithFindingPerFile(t *testing.T) {
	// Plan mentions neither file.
	planText := "This plan says nothing about the changed files."
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("artifact content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", nil)),
		assertionContext("plan", []byte(planText)),
		assertionContext("diff", []byte(sampleDiff)),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("want Passed=false, got true")
	}
	// Two changed files → two findings.
	if len(eval_.Findings) != 2 {
		t.Errorf("want 2 findings (one per missing file), got %d: %v", len(eval_.Findings), eval_.Findings)
	}
	for _, f := range eval_.Findings {
		if f.Severity != SeverityError {
			t.Errorf("finding severity = %q, want error", f.Severity)
		}
	}
}

func TestTestPlanMentions_CaseInsensitiveDefault(t *testing.T) {
	// Plan mentions paths in UPPERCASE; default case_sensitive=false should still pass.
	planText := "Covers INTERNAL/FOO/BAR.GO and CMD/MAIN.GO."
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("artifact content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", nil)),
		assertionContext("plan", []byte(planText)),
		assertionContext("diff", []byte(sampleDiff)),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || !*eval_.Score.Passed {
		t.Errorf("case_sensitive=false default: want Passed=true, got false; findings: %v", eval_.Findings)
	}
}

func TestTestPlanMentions_CaseSensitiveTrue(t *testing.T) {
	// Plan mentions UPPERCASE paths, but case_sensitive=true → should fail.
	planText := "Covers INTERNAL/FOO/BAR.GO and CMD/MAIN.GO."
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("artifact content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", map[string]string{
			"case_sensitive": "true",
		})),
		assertionContext("plan", []byte(planText)),
		assertionContext("diff", []byte(sampleDiff)),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("case_sensitive=true: want Passed=false (uppercase plan ≠ lowercase paths), got true")
	}
}

func TestTestPlanMentions_PlanNotFound(t *testing.T) {
	e := NewAssertionEvaluator()
	// No input with role "plan".
	req := newAssertionReq(
		assertionTarget("output", []byte("artifact content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", nil)),
		assertionContext("diff", []byte(sampleDiff)),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("want Passed=false when plan role absent, got true")
	}
	if len(eval_.Findings) != 1 {
		t.Errorf("want 1 finding for missing plan, got %d: %v", len(eval_.Findings), eval_.Findings)
	}
}

func TestTestPlanMentions_DiffNotFound(t *testing.T) {
	e := NewAssertionEvaluator()
	// No input with role "diff".
	req := newAssertionReq(
		assertionTarget("output", []byte("artifact content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", nil)),
		assertionContext("plan", []byte("some plan text")),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("want Passed=false when diff role absent, got true")
	}
	if len(eval_.Findings) != 1 {
		t.Errorf("want 1 finding for missing diff, got %d: %v", len(eval_.Findings), eval_.Findings)
	}
}

// TestTestPlanMentions_BasenameCollision verifies that match=path does NOT
// count an unrelated text "data.go" as mentioning the changed file "a.go".
func TestTestPlanMentions_BasenameCollision(t *testing.T) {
	diff := `diff --git a/internal/a.go b/internal/a.go
index 123..456 100644
--- a/internal/a.go
+++ b/internal/a.go
@@ -1 +1,2 @@
 package internal
+// changed
`
	// Plan mentions "data.go" and "some/other/a.go" but NOT "internal/a.go".
	planText := "Plan mentions data.go and some/other/a.go but not the real path."

	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("content")),
		assertionSpec(assertionCheck("test-plan-mentions-every-changed-file", map[string]string{
			"match": "path",
		})),
		assertionContext("plan", []byte(planText)),
		assertionContext("diff", []byte(diff)),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// match=path: "internal/a.go" is not in the plan text (only "data.go" and "some/other/a.go" are).
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("match=path: want Passed=false (basename collision should not count), got true")
	}
}

// ---- parseChangedFiles edge-case tests ----

func TestParseChangedFiles_Add(t *testing.T) {
	diff := `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ b/new.go
@@ -0,0 +1 @@
+package main
`
	files := parseChangedFiles([]byte(diff))
	if len(files) != 1 || files[0] != "new.go" {
		t.Errorf("add: want [new.go], got %v", files)
	}
}

func TestParseChangedFiles_Modify(t *testing.T) {
	diff := `diff --git a/foo/bar.go b/foo/bar.go
index abc..def 100644
--- a/foo/bar.go
+++ b/foo/bar.go
@@ -1 +1,2 @@
 package foo
+// changed
`
	files := parseChangedFiles([]byte(diff))
	if len(files) != 1 || files[0] != "foo/bar.go" {
		t.Errorf("modify: want [foo/bar.go], got %v", files)
	}
}

func TestParseChangedFiles_PhantomAddedPlusLine(t *testing.T) {
	// A hunk BODY that adds a line whose content begins "++ " renders as
	// "+++ note". It must NOT be misread as a post-image header (no phantom).
	diff := `diff --git a/real.go b/real.go
index abc..def 100644
--- a/real.go
+++ b/real.go
@@ -1,2 +1,3 @@
 package main
+++ note
`
	files := parseChangedFiles([]byte(diff))
	if len(files) != 1 || files[0] != "real.go" {
		t.Errorf("phantom +++ body line: want [real.go], got %v", files)
	}
}

func TestParseChangedFiles_PhantomBodyHeaderPair(t *testing.T) {
	// A hunk body with a removed "-- foo" line and an added "++ bar" line
	// renders as "--- foo"/"+++ bar". Neither may be read as a header.
	diff := `diff --git a/real.go b/real.go
index abc..def 100644
--- a/real.go
+++ b/real.go
@@ -1,3 +1,3 @@
 package main
--- foo
+++ bar
`
	files := parseChangedFiles([]byte(diff))
	if len(files) != 1 || files[0] != "real.go" {
		t.Errorf("phantom body header pair: want [real.go], got %v", files)
	}
}

func TestParseChangedFiles_QuotedPathAbsent(t *testing.T) {
	// A quoted/core.quotePath-escaped path is unsupported: it must be ABSENT
	// from the result (not appended as the raw escaped string), and other files
	// in the same diff must still parse. Covers both "+++ " and "rename to ".
	diff := `diff --git a/plain.go b/plain.go
index abc..def 100644
--- a/plain.go
+++ b/plain.go
@@ -1 +1,2 @@
 package main
+// changed
diff --git "a/uni\303\251.go" "b/uni\303\251.go"
index abc..def 100644
--- "a/uni\303\251.go"
+++ "b/uni\303\251.go"
@@ -1 +1,2 @@
 package main
+// changed
diff --git a/old.go b/renamed.go
similarity index 100%
rename from old.go
rename to "renamed\303\251.go"
`
	files := parseChangedFiles([]byte(diff))
	if len(files) != 1 || files[0] != "plain.go" {
		t.Errorf("quoted paths must be absent: want [plain.go], got %v", files)
	}
}

func TestParseChangedFiles_Delete(t *testing.T) {
	// Pure delete: +++ /dev/null — should be skipped.
	diff := `diff --git a/gone.go b/gone.go
deleted file mode 100644
index abc1234..0000000
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-package gone
`
	files := parseChangedFiles([]byte(diff))
	if len(files) != 0 {
		t.Errorf("delete: want [], got %v", files)
	}
}

func TestParseChangedFiles_Rename(t *testing.T) {
	// Rename: the new path comes from "rename to <path>".
	diff := `diff --git a/old/name.go b/new/name.go
similarity index 95%
rename from old/name.go
rename to new/name.go
index abc..def 100644
--- a/old/name.go
+++ b/new/name.go
@@ -1,2 +1,2 @@
 package foo
-// old comment
+// new comment
`
	files := parseChangedFiles([]byte(diff))
	// Both "rename to" and "+++ b/new/name.go" should resolve to "new/name.go".
	// After dedup there should be exactly one entry.
	if len(files) != 1 || files[0] != "new/name.go" {
		t.Errorf("rename: want [new/name.go], got %v", files)
	}
}

func TestParseChangedFiles_MultipleFilesDedup(t *testing.T) {
	// Two distinct files; ensure stable (first-seen) order.
	diff := `diff --git a/alpha.go b/alpha.go
--- a/alpha.go
+++ b/alpha.go
@@ -1 +1 @@
 package main
diff --git a/beta.go b/beta.go
--- a/beta.go
+++ b/beta.go
@@ -1 +1 @@
 package main
diff --git a/alpha.go b/alpha.go
--- a/alpha.go
+++ b/alpha.go
@@ -2 +2 @@
 extra
`
	files := parseChangedFiles([]byte(diff))
	// alpha.go appears twice but should deduplicate; beta.go once.
	if len(files) != 2 {
		t.Errorf("dedup: want 2 files, got %d: %v", len(files), files)
	}
	if files[0] != "alpha.go" || files[1] != "beta.go" {
		t.Errorf("order: want [alpha.go, beta.go], got %v", files)
	}
}

func TestParseChangedFiles_PrefixStrip(t *testing.T) {
	// Verify both "b/" and "a/" prefixes are stripped.
	diff := "+++ b/src/main.go\n"
	files := parseChangedFiles([]byte(diff))
	if len(files) != 1 || files[0] != "src/main.go" {
		t.Errorf("b/ strip: want [src/main.go], got %v", files)
	}

	diff2 := "+++ a/src/main.go\n"
	files2 := parseChangedFiles([]byte(diff2))
	if len(files2) != 1 || files2[0] != "src/main.go" {
		t.Errorf("a/ strip: want [src/main.go], got %v", files2)
	}
}

func TestParseChangedFiles_TimestampStrip(t *testing.T) {
	// Tabs introduce a timestamp suffix in some diff formats.
	diff := "+++ b/foo/bar.go\t2024-01-01 12:00:00 +0000\n"
	files := parseChangedFiles([]byte(diff))
	if len(files) != 1 || files[0] != "foo/bar.go" {
		t.Errorf("timestamp strip: want [foo/bar.go], got %v", files)
	}
}

// ---- aggregation tests ----

func TestAssertionEvaluator_AggregationBothPass(t *testing.T) {
	passCheck := func(_ EvalRequest, _ map[string]string) (bool, []Finding, error) {
		return true, nil, nil
	}
	e := &AssertionEvaluator{Checks: map[string]checkFunc{
		"pass1": passCheck,
		"pass2": passCheck,
	}}
	req := EvalRequest{
		Method:  "assertion",
		Targets: []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(
			assertionCheck("pass1", nil),
			assertionCheck("pass2", nil),
		),
	}
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || !*eval_.Score.Passed {
		t.Errorf("both pass: want Passed=true, got %v", eval_.Score.Passed)
	}
	if len(eval_.Findings) != 0 {
		t.Errorf("both pass: want no findings, got %v", eval_.Findings)
	}
}

func TestAssertionEvaluator_AggregationOneFails(t *testing.T) {
	failFinding := Finding{Severity: SeverityError, Message: "check-a failed"}
	e := &AssertionEvaluator{Checks: map[string]checkFunc{
		"pass": func(_ EvalRequest, _ map[string]string) (bool, []Finding, error) {
			return true, nil, nil
		},
		"fail": func(_ EvalRequest, _ map[string]string) (bool, []Finding, error) {
			return false, []Finding{failFinding}, nil
		},
	}}
	req := EvalRequest{
		Method:  "assertion",
		Targets: []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(
			assertionCheck("pass", nil),
			assertionCheck("fail", nil),
		),
	}
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("one fails: want Passed=false, got true")
	}
	if len(eval_.Findings) != 1 || eval_.Findings[0].Message != failFinding.Message {
		t.Errorf("one fails: want 1 finding %q, got %v", failFinding.Message, eval_.Findings)
	}
}

func TestAssertionEvaluator_AggregationBothFail(t *testing.T) {
	// Both checks fail; findings should accumulate.
	e := &AssertionEvaluator{Checks: map[string]checkFunc{
		"fail1": func(_ EvalRequest, _ map[string]string) (bool, []Finding, error) {
			return false, []Finding{{Severity: SeverityError, Message: "f1"}}, nil
		},
		"fail2": func(_ EvalRequest, _ map[string]string) (bool, []Finding, error) {
			return false, []Finding{{Severity: SeverityError, Message: "f2"}}, nil
		},
	}}
	req := EvalRequest{
		Method:  "assertion",
		Targets: []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(
			assertionCheck("fail1", nil),
			assertionCheck("fail2", nil),
		),
	}
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Passed == nil || *eval_.Score.Passed {
		t.Errorf("both fail: want Passed=false, got true")
	}
	if len(eval_.Findings) != 2 {
		t.Errorf("both fail: want 2 findings, got %d: %v", len(eval_.Findings), eval_.Findings)
	}
}

func TestAssertionEvaluator_CheckErrorPropagates(t *testing.T) {
	// A check that returns an error should cause Evaluate to return that error,
	// NOT set Passed=false.
	sentinel := fmt.Errorf("internal check error")
	e := &AssertionEvaluator{Checks: map[string]checkFunc{
		"err-check": func(_ EvalRequest, _ map[string]string) (bool, []Finding, error) {
			return false, nil, sentinel
		},
	}}
	req := EvalRequest{
		Method:    "assertion",
		Targets:   []EvalInput{assertionTarget("out", []byte("x"))},
		Assertion: assertionSpec(assertionCheck("err-check", nil)),
	}
	_, err := e.Evaluate(context.Background(), req)
	if err == nil {
		t.Fatal("want error propagated from check, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error, got %v", err)
	}
}

func TestAssertionEvaluator_AllPassEmitsNoFindings(t *testing.T) {
	e := NewAssertionEvaluator()
	req := newAssertionReq(
		assertionTarget("output", []byte("content")),
		assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "output"})),
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Findings != nil {
		t.Errorf("all-pass: want nil Findings, got %v", eval_.Findings)
	}
}

// ---- Score coherence tests (EvalResult.Validate) ----

// buildValidEvalResult wraps an Evaluation into a valid EvalResult.
func buildValidEvalResult(eval_ Evaluation, spec *AssertionSpec) EvalResult {
	src := validArtifactSource()
	base := EvalIDBase("assertion", "assertion-test-run", "output", time.Now())
	return EvalResult{
		EvalResultVersion: 1,
		EvalID:            base,
		Method:            "assertion",
		Score:             eval_.Score,
		Findings:          eval_.Findings,
		Assertion:         spec,
		Targets:           []ArtifactSource{src},
		Producer:          testProducer(),
		Created:           time.Now().UTC(),
	}
}

func TestAssertionEvaluator_ScoreCoherence_Pass(t *testing.T) {
	e := NewAssertionEvaluator()
	spec := assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "output"}))
	req := newAssertionReq(
		assertionTarget("output", []byte("content")),
		spec,
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	result := buildValidEvalResult(eval_, spec)
	if err := result.Validate(); err != nil {
		t.Fatalf("EvalResult.Validate() failed for pass Score: %v", err)
	}
}

func TestAssertionEvaluator_ScoreCoherence_Fail(t *testing.T) {
	e := NewAssertionEvaluator()
	spec := assertionSpec(assertionCheck("required-artifact-presence", map[string]string{"role": "missing"}))
	req := newAssertionReq(
		assertionTarget("output", []byte("content")),
		spec,
	)
	eval_, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	result := buildValidEvalResult(eval_, spec)
	if err := result.Validate(); err != nil {
		t.Fatalf("EvalResult.Validate() failed for fail Score: %v", err)
	}
}
