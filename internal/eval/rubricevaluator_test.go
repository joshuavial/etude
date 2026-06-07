package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// validArtifactSource returns a valid ArtifactSource for test use.
// Uses a fixed 40-char hex commit OID and a 64-char hex artifact SHA256.
func validArtifactSource() ArtifactSource {
	return ArtifactSource{
		RunID:    "rubric-test-run",
		Stage:    "output",
		Commit:   "aabbccddeeff00112233445566778899aabbccdd",                         // 40 hex chars
		Artifact: "0000000000000000000000000000000000000000000000000000000000000001", // 64 hex chars
	}
}

// writeRubricFile writes content to a file named "rubric.md" in dir and returns its path.
func writeRubricFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "rubric.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRubricEvaluator_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	rubricContent := "Score the response from 0 to 10."
	writeRubricFile(t, dir, rubricContent)

	ref, err := PinRubric(dir, "rubric.md")
	if err != nil {
		t.Fatalf("PinRubric: %v", err)
	}

	v := 7.5
	m := 10.0
	stub := &StubJudge{Canned: JudgeResponse{
		Value: &v,
		Max:   &m,
		Findings: []Finding{
			{Severity: SeverityInfo, Message: "well done"},
		},
	}}

	re := &RubricEvaluator{Judge: stub, Root: dir}
	src := validArtifactSource()
	eval_, err := re.Evaluate(context.Background(), EvalRequest{
		Method: "rubric",
		Targets: []EvalInput{
			{Role: "output", Content: []byte("model response"), Source: src},
		},
		Rubric:   &ref,
		Producer: testProducer(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval_.Score.Kind != ScoreRubric {
		t.Errorf("Score.Kind = %q, want %q", eval_.Score.Kind, ScoreRubric)
	}
	if eval_.Score.Value == nil || *eval_.Score.Value != v {
		t.Errorf("Score.Value = %v, want %v", eval_.Score.Value, v)
	}
	if eval_.Score.Max == nil || *eval_.Score.Max != m {
		t.Errorf("Score.Max = %v, want %v", eval_.Score.Max, m)
	}
	if len(eval_.Findings) != 1 || eval_.Findings[0].Message != "well done" {
		t.Errorf("Findings = %v, want one finding", eval_.Findings)
	}
}

func TestRubricEvaluator_EvalResultValidate(t *testing.T) {
	dir := t.TempDir()
	rubricContent := "Rubric text."
	writeRubricFile(t, dir, rubricContent)

	ref, err := PinRubric(dir, "rubric.md")
	if err != nil {
		t.Fatalf("PinRubric: %v", err)
	}

	v := 8.0
	m := 10.0
	stub := &StubJudge{Canned: JudgeResponse{Value: &v, Max: &m}}

	re := &RubricEvaluator{Judge: stub, Root: dir}
	src := validArtifactSource()
	eval_, err := re.Evaluate(context.Background(), EvalRequest{
		Method: "rubric",
		Targets: []EvalInput{
			{Role: "output", Content: []byte("response"), Source: src},
		},
		Rubric:   &ref,
		Producer: testProducer(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	// Wrap into EvalResult and assert Validate() passes.
	base := EvalIDBase("rubric", "rubric-test-run", "output", time.Now())
	evalResult := EvalResult{
		EvalResultVersion: 1,
		EvalID:            base,
		Method:            "rubric",
		Score:             eval_.Score,
		Findings:          eval_.Findings,
		Rubric:            &ref,
		Targets:           []ArtifactSource{src},
		Producer:          testProducer(),
		Created:           time.Now().UTC(),
	}
	if err := evalResult.Validate(); err != nil {
		t.Fatalf("EvalResult.Validate() failed: %v", err)
	}
}

func TestPinRubric_VersionMatchAndMismatch(t *testing.T) {
	dir := t.TempDir()
	writeRubricFile(t, dir, "version A content")

	ref, err := PinRubric(dir, "rubric.md")
	if err != nil {
		t.Fatalf("PinRubric: %v", err)
	}
	if ref.Path != "rubric.md" {
		t.Errorf("Path = %q, want rubric.md", ref.Path)
	}
	if ref.Version == "" {
		t.Error("Version should not be empty")
	}

	v := 5.0
	m := 10.0
	stub := &StubJudge{Canned: JudgeResponse{Value: &v, Max: &m}}
	re := &RubricEvaluator{Judge: stub, Root: dir}

	src := validArtifactSource()

	// Evaluate with correct version — must succeed.
	_, err = re.Evaluate(context.Background(), EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{{Role: "out", Content: []byte("x"), Source: src}},
		Rubric:  &ref,
	})
	if err != nil {
		t.Fatalf("Evaluate with matching version: %v", err)
	}

	// Mutate the file on disk.
	if err := os.WriteFile(filepath.Join(dir, "rubric.md"), []byte("version B — different content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Evaluate again with the old pinned version — must return ErrRubricVersionMismatch.
	_, err = re.Evaluate(context.Background(), EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{{Role: "out", Content: []byte("x"), Source: src}},
		Rubric:  &ref,
	})
	if !errors.Is(err, ErrRubricVersionMismatch) {
		t.Fatalf("want ErrRubricVersionMismatch after on-disk edit, got %v", err)
	}
}

func TestLoadRubric_PathEscape(t *testing.T) {
	dir := t.TempDir()
	// Create a real file just outside root — tests that the escape guard rejects
	// it even when the file exists (not caught by ReadFile-missing).
	parent := filepath.Dir(dir)
	secretPath := filepath.Join(parent, "secret_probe.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	escapeTests := []struct {
		name string
		path string
	}{
		{"relative dot-dot", "../escape"},
		{"relative dot-dot chain", "../../secret.txt"},
		// real readable file just outside root — proves the escape guard, not ReadFile
		{"relative escape to real file", "../secret_probe.txt"},
		// absolute path is now rejected outright regardless of location
		{"absolute outside root", filepath.Join(parent, "secret.txt")},
	}

	for _, tc := range escapeTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadRubric(dir, tc.path)
			if !errors.Is(err, ErrRubricLoad) {
				t.Errorf("path %q: want ErrRubricLoad, got %v", tc.path, err)
			}
		})
	}
}

func TestLoadRubric_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // sibling temp dir, definitely outside root
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink inside root pointing at the outside directory. "link/secret.txt"
	// is lexically under root but resolves outside it.
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := loadRubric(root, "link/secret.txt"); !errors.Is(err, ErrRubricLoad) {
		t.Fatalf("want ErrRubricLoad for symlink escape, got %v", err)
	}

	// Positive control: an in-root symlink to an in-root file must still load,
	// so the resolved-containment check does not over-reject legitimate links.
	target := filepath.Join(root, "real_rubric.md")
	if err := os.WriteFile(target, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "alias.md")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	data, err := loadRubric(root, "alias.md")
	if err != nil {
		t.Fatalf("in-root symlink should load, got %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("data = %q, want %q", data, "ok")
	}
}

func TestRubricEvaluator_NilJudge(t *testing.T) {
	dir := t.TempDir()
	writeRubricFile(t, dir, "rubric text")
	ref, err := PinRubric(dir, "rubric.md")
	if err != nil {
		t.Fatalf("PinRubric: %v", err)
	}

	re := &RubricEvaluator{Judge: nil, Root: dir}
	src := validArtifactSource()
	_, err = re.Evaluate(context.Background(), EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{{Role: "out", Content: []byte("x"), Source: src}},
		Rubric:  &ref,
	})
	if !errors.Is(err, ErrJudgeNotConfigured) {
		t.Fatalf("want ErrJudgeNotConfigured for nil Judge, got %v", err)
	}
}

func TestLoadRubric_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := loadRubric(dir, "nonexistent.md")
	if !errors.Is(err, ErrRubricLoad) {
		t.Fatalf("want ErrRubricLoad for missing file, got %v", err)
	}
}

func TestRubricEvaluator_RequiresRubricMethod(t *testing.T) {
	re := &RubricEvaluator{Judge: &StubJudge{}, Root: t.TempDir()}
	_, err := re.Evaluate(context.Background(), EvalRequest{Method: "pairwise"})
	if err == nil {
		t.Fatal("want error for non-rubric method, got nil")
	}
}

func TestRubricEvaluator_RequiresNonNilRubric(t *testing.T) {
	re := &RubricEvaluator{Judge: &StubJudge{}, Root: t.TempDir()}
	_, err := re.Evaluate(context.Background(), EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{{Role: "out", Content: []byte("x")}},
		Rubric:  nil,
	})
	if err == nil {
		t.Fatal("want error for nil Rubric, got nil")
	}
}

func TestRubricEvaluator_RequiresExactlyOneTarget(t *testing.T) {
	dir := t.TempDir()
	writeRubricFile(t, dir, "rubric text")
	ref, _ := PinRubric(dir, "rubric.md")

	re := &RubricEvaluator{Judge: &StubJudge{}, Root: dir}

	// Zero targets.
	_, err := re.Evaluate(context.Background(), EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{},
		Rubric:  &ref,
	})
	if err == nil {
		t.Error("want error for zero targets, got nil")
	}

	// Two targets.
	src := validArtifactSource()
	_, err = re.Evaluate(context.Background(), EvalRequest{
		Method: "rubric",
		Targets: []EvalInput{
			{Role: "A", Content: []byte("a"), Source: src},
			{Role: "B", Content: []byte("b"), Source: src},
		},
		Rubric: &ref,
	})
	if err == nil {
		t.Error("want error for two targets, got nil")
	}
}

func TestRubricEvaluator_MalformedJudgeResponse_Matrix(t *testing.T) {
	dir := t.TempDir()
	writeRubricFile(t, dir, "rubric text")
	ref, _ := PinRubric(dir, "rubric.md")

	src := validArtifactSource()
	baseReq := EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{{Role: "out", Content: []byte("x"), Source: src}},
		Rubric:  &ref,
	}

	v := 5.0
	m := 10.0
	winnerA := WinnerA
	conf := 0.9
	passed := true

	tests := []struct {
		name string
		resp JudgeResponse
	}{
		{
			name: "value nil",
			resp: JudgeResponse{Max: &m},
		},
		{
			name: "max nil",
			resp: JudgeResponse{Value: &v},
		},
		{
			name: "max zero",
			resp: JudgeResponse{Value: func() *float64 { z := 0.0; return &z }(), Max: func() *float64 { z := 0.0; return &z }()},
		},
		{
			name: "value exceeds max",
			resp: JudgeResponse{Value: func() *float64 { x := 11.0; return &x }(), Max: &m},
		},
		{
			name: "winner set for rubric",
			resp: JudgeResponse{Value: &v, Max: &m, Winner: winnerA},
		},
		{
			name: "passed set for rubric",
			resp: JudgeResponse{Value: &v, Max: &m, Passed: &passed},
		},
		{
			name: "confidence set for rubric",
			resp: JudgeResponse{Value: &v, Max: &m, Confidence: &conf},
		},
		{
			name: "bad severity finding",
			resp: JudgeResponse{
				Value: &v, Max: &m,
				Findings: []Finding{{Severity: "critical", Message: "bad"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			re := &RubricEvaluator{
				Judge: &StubJudge{Canned: tc.resp},
				Root:  dir,
			}
			_, err := re.Evaluate(context.Background(), baseReq)
			if !errors.Is(err, ErrJudgeOutputInvalid) {
				t.Errorf("%s: want ErrJudgeOutputInvalid, got %v", tc.name, err)
			}
		})
	}
}

func TestRubricEvaluator_JudgeErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	writeRubricFile(t, dir, "rubric text")
	ref, _ := PinRubric(dir, "rubric.md")

	sentinel := errors.New("judge down")
	re := &RubricEvaluator{
		Judge: &StubJudge{Err: sentinel},
		Root:  dir,
	}
	src := validArtifactSource()
	_, err := re.Evaluate(context.Background(), EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{{Role: "out", Content: []byte("x"), Source: src}},
		Rubric:  &ref,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error propagated, got %v", err)
	}
}

func TestPinRubric_DeterministicHash(t *testing.T) {
	dir := t.TempDir()
	writeRubricFile(t, dir, "deterministic content")

	ref1, _ := PinRubric(dir, "rubric.md")
	ref2, _ := PinRubric(dir, "rubric.md")

	if ref1.Version != ref2.Version {
		t.Errorf("PinRubric not deterministic: %q != %q", ref1.Version, ref2.Version)
	}
	if len(ref1.Version) != 64 {
		t.Errorf("Version should be 64 hex chars (SHA-256), got %d: %q", len(ref1.Version), ref1.Version)
	}
}

// testProducer returns a minimal Producer for test use.
func testProducer() runmanifest.Producer {
	return runmanifest.Producer{Model: "test-model"}
}
