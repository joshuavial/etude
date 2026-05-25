package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeJudgeScript writes a POSIX sh script to path and makes it executable.
func writeJudgeScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+content+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestExecJudge_EmptyCommand(t *testing.T) {
	j := &ExecJudge{}
	_, err := j.Judge(context.Background(), JudgeRequest{Method: "rubric"})
	if !errors.Is(err, ErrJudgeNotConfigured) {
		t.Fatalf("want ErrJudgeNotConfigured, got %v", err)
	}
}

func TestExecJudge_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":7.5,"max":10.0}' > "$ETUDE_OUTPUT_FILE"`)

	content := []byte("artifact content")
	j := &ExecJudge{Command: []string{script}}
	resp, err := j.Judge(context.Background(), JudgeRequest{
		Method: "rubric",
		Targets: []JudgeInput{
			{Role: "output", Content: content},
		},
		Rubric: []byte("score this"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Value == nil || *resp.Value != 7.5 {
		t.Errorf("Value = %v, want 7.5", resp.Value)
	}
	if resp.Max == nil || *resp.Max != 10.0 {
		t.Errorf("Max = %v, want 10.0", resp.Max)
	}
}

func TestExecJudge_MultiTargetOrdering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	// Script verifies that 00-target-A and 01-target-B are both present,
	// then writes a valid rubric response.
	writeJudgeScript(t, script, `
[ -f "$ETUDE_INPUTS_DIR/00-target-A" ] || { echo "missing 00-target-A" >&2; exit 1; }
[ -f "$ETUDE_INPUTS_DIR/01-target-B" ] || { echo "missing 01-target-B" >&2; exit 1; }
printf '{"value":5.0,"max":10.0}' > "$ETUDE_OUTPUT_FILE"
`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method: "rubric",
		Targets: []JudgeInput{
			{Role: "A", Content: []byte("first")},
			{Role: "B", Content: []byte("second")},
		},
		Rubric: []byte("rubric bytes"),
	})
	if err != nil {
		t.Fatalf("multi-target ordering: %v", err)
	}
}

func TestExecJudge_ContextOrdering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `
[ -f "$ETUDE_INPUTS_DIR/00-context-task" ]  || { echo "missing 00-context-task" >&2; exit 1; }
[ -f "$ETUDE_INPUTS_DIR/01-context-plan" ]  || { echo "missing 01-context-plan" >&2; exit 1; }
printf '{"value":3.0,"max":5.0}' > "$ETUDE_OUTPUT_FILE"
`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method: "rubric",
		Targets: []JudgeInput{
			{Role: "output", Content: []byte("artifact")},
		},
		Context: []JudgeInput{
			{Role: "task", Content: []byte("the task")},
			{Role: "plan", Content: []byte("the plan")},
		},
		Rubric: []byte("rubric bytes"),
	})
	if err != nil {
		t.Fatalf("context ordering: %v", err)
	}
}

func TestExecJudge_EnvIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	// Set a parent env var that must NOT be visible to the judge.
	t.Setenv("ETUDE_TEST_PARENT_SECRET", "should-not-leak")

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `
# Fail if parent secret leaked into judge env.
if [ -n "$ETUDE_TEST_PARENT_SECRET" ]; then
  echo "parent env leaked: ETUDE_TEST_PARENT_SECRET=$ETUDE_TEST_PARENT_SECRET" >&2
  exit 1
fi
printf '{"value":1.0,"max":1.0}' > "$ETUDE_OUTPUT_FILE"
`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
		Rubric:  []byte("r"),
	})
	if err != nil {
		t.Fatalf("env isolation: %v", err)
	}
}

func TestExecJudge_ETUDEMODELAlwaysSet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	// Test that ETUDE_MODEL is set even when Model is empty string.
	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `
# ETUDE_MODEL must be defined (even if empty).
if ! env | grep -q '^ETUDE_MODEL='; then
  echo "ETUDE_MODEL not set" >&2
  exit 1
fi
printf '{"value":1.0,"max":1.0}' > "$ETUDE_OUTPUT_FILE"
`)

	// First test with empty Model.
	j := &ExecJudge{Command: []string{script}, Model: ""}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
		Rubric:  []byte("r"),
	})
	if err != nil {
		t.Fatalf("ETUDE_MODEL empty: %v", err)
	}

	// Also test with a non-empty Model value.
	dir2 := t.TempDir()
	script2 := filepath.Join(dir2, "judge.sh")
	writeJudgeScript(t, script2, `
model_val=$(printenv ETUDE_MODEL)
if [ "$model_val" != "claude-opus-4-7" ]; then
  echo "ETUDE_MODEL wrong: $model_val" >&2
  exit 1
fi
printf '{"value":1.0,"max":1.0}' > "$ETUDE_OUTPUT_FILE"
`)

	j2 := &ExecJudge{Command: []string{script2}, Model: "claude-opus-4-7"}
	_, err = j2.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
		Rubric:  []byte("r"),
	})
	if err != nil {
		t.Fatalf("ETUDE_MODEL set: %v", err)
	}
}

func TestExecJudge_RubricFileSetWhenRubricPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	rubricContent := "my rubric text"
	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, fmt.Sprintf(`
rubric_content=$(cat "$ETUDE_RUBRIC_FILE")
if [ "$rubric_content" != "%s" ]; then
  echo "rubric content wrong: $rubric_content" >&2
  exit 1
fi
printf '{"value":2.0,"max":5.0}' > "$ETUDE_OUTPUT_FILE"
`, rubricContent))

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
		Rubric:  []byte(rubricContent),
	})
	if err != nil {
		t.Fatalf("ETUDE_RUBRIC_FILE: %v", err)
	}
}

func TestExecJudge_RubricFileAbsentWhenRubricNil(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `
# ETUDE_RUBRIC_FILE must NOT be set when Rubric is nil.
if [ -n "$ETUDE_RUBRIC_FILE" ]; then
  echo "ETUDE_RUBRIC_FILE should be absent, got: $ETUDE_RUBRIC_FILE" >&2
  exit 1
fi
# For this test we use a non-rubric method — just write valid output.
printf '{"value":1.0,"max":1.0}' > "$ETUDE_OUTPUT_FILE"
`)

	j := &ExecJudge{Command: []string{script}}
	// Use rubric method but with Rubric nil — note: this bypasses RubricEvaluator's
	// validation, testing ExecJudge directly (ExecJudge itself does not enforce method).
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
		Rubric:  nil,
	})
	if err != nil {
		t.Fatalf("ETUDE_RUBRIC_FILE absent: %v", err)
	}
}

func TestExecJudge_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `echo "judge crash" >&2; exit 3`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeFailed) {
		t.Fatalf("want ErrJudgeFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "judge crash") {
		t.Errorf("want stderr in error, got: %v", err)
	}
}

func TestExecJudge_OutputMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `exit 0`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputMissing) {
		t.Fatalf("want ErrJudgeOutputMissing, got %v", err)
	}
}

func TestExecJudge_OutputIsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink tests skipped on Windows")
	}

	dir := t.TempDir()
	outsideFile := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte(`{"value":1,"max":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, fmt.Sprintf(`ln -s %s "$ETUDE_OUTPUT_FILE"`, outsideFile))

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputNotRegular) {
		t.Fatalf("want ErrJudgeOutputNotRegular, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_NonJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf 'not json' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for non-JSON, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_UnknownField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":1.0,"max":1.0,"unknown_field":"x"}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for unknown field, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_TrailingData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":1.0,"max":1.0}extra' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for trailing data, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_MissingValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"max":10.0}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for missing value, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_MissingMax(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":5.0}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for missing max, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_MaxZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":0.0,"max":0.0}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for max=0, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_ValueExceedsMax(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":11.0,"max":10.0}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for value>max, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_WinnerSetForRubric(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":5.0,"max":10.0,"winner":"A"}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for winner set in rubric, got %v", err)
	}
}

func TestExecJudge_MalformedOutput_BadSeverity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":5.0,"max":10.0,"findings":[{"severity":"critical","message":"bad"}]}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for bad severity, got %v", err)
	}
}

func TestExecJudge_CancelledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `sleep 10`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(ctx, JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

func TestExecJudge_CancelledContextBeforeRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":1.0,"max":1.0}' > "$ETUDE_OUTPUT_FILE"`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Judge

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(ctx, JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
	})
	if err == nil {
		t.Fatal("want error for pre-cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.Canceled or DeadlineExceeded, got %v", err)
	}
}

func TestValidateJudgeOutput_UnsupportedMethod(t *testing.T) {
	v := 7.5
	m := 10.0
	validRubricOutput := judgeOutputJSON{Value: &v, Max: &m}

	cases := []struct {
		name   string
		method string
	}{
		{"pairwise method", "pairwise"},
		{"empty method", ""},
		{"unknown method", "foobar"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateJudgeOutput(tc.method, validRubricOutput)
			if !errors.Is(err, ErrJudgeOutputInvalid) {
				t.Errorf("method %q: want ErrJudgeOutputInvalid, got %v", tc.method, err)
			}
		})
	}
}

func TestExecJudge_RoleWithPathSeparator(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	// Role "a/b" must materialize as "00-target-b" (base only), not try to create a subdir.
	writeJudgeScript(t, script, `
[ -f "$ETUDE_INPUTS_DIR/00-target-b" ] || { echo "missing 00-target-b" >&2; exit 1; }
printf '{"value":1.0,"max":1.0}' > "$ETUDE_OUTPUT_FILE"
`)

	j := &ExecJudge{Command: []string{script}}
	_, err := j.Judge(context.Background(), JudgeRequest{
		Method: "rubric",
		Targets: []JudgeInput{
			{Role: "a/b", Content: []byte("content")},
		},
		Rubric: []byte("rubric"),
	})
	if err != nil {
		t.Fatalf("role with path separator: %v", err)
	}
}

func TestExecJudge_WithFindings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	writeJudgeScript(t, script, `printf '{"value":6.0,"max":10.0,"findings":[{"severity":"warning","message":"minor issue","pointer":"/section1"},{"severity":"info","message":"overall good"}]}' > "$ETUDE_OUTPUT_FILE"`)

	j := &ExecJudge{Command: []string{script}}
	resp, err := j.Judge(context.Background(), JudgeRequest{
		Method:  "rubric",
		Targets: []JudgeInput{{Role: "out", Content: []byte("x")}},
		Rubric:  []byte("rubric"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(resp.Findings))
	}
	if resp.Findings[0].Severity != SeverityWarning || resp.Findings[0].Message != "minor issue" {
		t.Errorf("finding[0] = %+v", resp.Findings[0])
	}
	if resp.Findings[0].Pointer != "/section1" {
		t.Errorf("finding[0].Pointer = %q, want %q", resp.Findings[0].Pointer, "/section1")
	}
	if resp.Findings[1].Severity != SeverityInfo || resp.Findings[1].Message != "overall good" {
		t.Errorf("finding[1] = %+v", resp.Findings[1])
	}
}
