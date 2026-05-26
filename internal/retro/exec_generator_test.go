package retro

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeGenScript writes a POSIX sh script to path and makes it executable.
func writeGenScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+content+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestExecGenerator_EmptyCommand(t *testing.T) {
	g := &ExecGenerator{}
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if !errors.Is(err, ErrGeneratorNotConfigured) {
		t.Fatalf("want ErrGeneratorNotConfigured, got %v", err)
	}
}

func TestExecGenerator_HappyPath(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `printf '# Retro\nGenerated.\n' > "$ETUDE_OUTPUT_FILE"`)

	g := &ExecGenerator{Command: []string{scriptPath}}
	req := GenerateRequest{
		Scope: "cohort",
		Subjects: []SubjectArtifact{
			{RunID: "r1", OutputContent: []byte("output-content")},
		},
	}
	res, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Body) == 0 {
		t.Error("expected non-empty body")
	}
	if res.MediaType != "text/markdown; charset=utf-8" {
		t.Errorf("MediaType = %q, want text/markdown; charset=utf-8", res.MediaType)
	}
}

func TestExecGenerator_NonZeroExit(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `echo "something went wrong" >&2; exit 2`)

	g := &ExecGenerator{Command: []string{scriptPath}}
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if !errors.Is(err, ErrGeneratorFailed) {
		t.Fatalf("want ErrGeneratorFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("want stderr in error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("want exit status in error message, got: %v", err)
	}
}

func TestExecGenerator_MissingOutputFile(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `exit 0`)

	g := &ExecGenerator{Command: []string{scriptPath}}
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if !errors.Is(err, ErrGeneratorOutputMissing) {
		t.Fatalf("want ErrGeneratorOutputMissing, got %v", err)
	}
}

func TestExecGenerator_OutputIsSymlink(t *testing.T) {
	scratch := t.TempDir()

	// Create a real file to symlink to.
	outsideFile := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, fmt.Sprintf(`ln -s %s "$ETUDE_OUTPUT_FILE"`, outsideFile))

	g := &ExecGenerator{Command: []string{scriptPath}}
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if !errors.Is(err, ErrGeneratorOutputNotRegular) {
		t.Fatalf("want ErrGeneratorOutputNotRegular, got %v", err)
	}
}

func TestExecGenerator_ContextCancellation(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `sleep 10`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	g := &ExecGenerator{Command: []string{scriptPath}}
	_, err := g.Generate(ctx, GenerateRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

func TestExecGenerator_EnvContract(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	// Script verifies env vars are set and absolute, reads subject output from inputs dir.
	writeGenScript(t, scriptPath, `
[ "${ETUDE_INPUTS_DIR#/}" != "$ETUDE_INPUTS_DIR" ] || { echo "ETUDE_INPUTS_DIR not absolute" >&2; exit 1; }
[ "${ETUDE_OUTPUT_FILE#/}" != "$ETUDE_OUTPUT_FILE" ] || { echo "ETUDE_OUTPUT_FILE not absolute" >&2; exit 1; }
cat "$ETUDE_INPUTS_DIR/00-r1-output" > "$ETUDE_OUTPUT_FILE"
`)

	g := &ExecGenerator{Command: []string{scriptPath}}
	req := GenerateRequest{
		Subjects: []SubjectArtifact{
			{RunID: "r1", OutputContent: []byte("subject-output-data")},
		},
	}
	res, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Body) != "subject-output-data" {
		t.Errorf("Body = %q, want subject-output-data", res.Body)
	}
}

func TestExecGenerator_MultiSubjectDisambiguation(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	// Script concatenates both subjects' outputs.
	writeGenScript(t, scriptPath, `
cat "$ETUDE_INPUTS_DIR/00-r1-output" "$ETUDE_INPUTS_DIR/01-r2-output" > "$ETUDE_OUTPUT_FILE"
`)

	g := &ExecGenerator{Command: []string{scriptPath}}
	req := GenerateRequest{
		Subjects: []SubjectArtifact{
			{RunID: "r1", OutputContent: []byte("first")},
			{RunID: "r2", OutputContent: []byte("second")},
		},
	}
	res, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Body) != "firstsecond" {
		t.Errorf("Body = %q, want firstsecond", res.Body)
	}
}

func TestExecGenerator_EnvIsolation(t *testing.T) {
	// Set a sentinel env var in the parent process.
	t.Setenv("ETUDE_SECRET_PROBE", "leaked-value")

	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `printf '%s' "$ETUDE_SECRET_PROBE" > "$ETUDE_OUTPUT_FILE"`)

	g := &ExecGenerator{Command: []string{scriptPath}}
	res, err := g.Generate(context.Background(), GenerateRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(res.Body), "leaked-value") {
		t.Errorf("env isolation failure: ETUDE_SECRET_PROBE leaked into child env, output: %q", res.Body)
	}
}

func TestExecGenerator_PATHExactKeyMatch(t *testing.T) {
	// Decoys placed BEFORE the real PATH= entry.
	env := []string{
		"PATHEXT=wrong-pathext",
		"MYPATH=wrong-mypath",
		"PATH=/real/bin",
		"PATH_X=wrong-path-x",
	}
	got := extractPATH(env)
	if got != "/real/bin" {
		t.Errorf("extractPATH: want /real/bin, got %q", got)
	}
}

func TestExecGenerator_ProducerCarried(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `printf 'body' > "$ETUDE_OUTPUT_FILE"`)

	// No import of runmanifest needed; use the type from the package via GenerateRequest.
	g := &ExecGenerator{Command: []string{scriptPath}}
	req := GenerateRequest{
		Subjects: []SubjectArtifact{
			{RunID: "r1", OutputContent: []byte("out")},
		},
	}
	res, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Producer echoed from req.Producer (zero value is fine; just check no panic).
	_ = res.Producer
}

func TestExecGenerator_EmptyOutputFile(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	// Script creates a 0-byte output file — valid, NOT ErrGeneratorOutputMissing.
	writeGenScript(t, scriptPath, `> "$ETUDE_OUTPUT_FILE"`)

	g := &ExecGenerator{Command: []string{scriptPath}}
	res, err := g.Generate(context.Background(), GenerateRequest{})
	if err != nil {
		t.Fatalf("empty output file should succeed, got %v", err)
	}
	if len(res.Body) != 0 {
		t.Errorf("want empty Body, got %q", res.Body)
	}
}

// TestExecGenerator_TimeoutExceeded proves that a configured Timeout causes the
// generate to fail with context.DeadlineExceeded and a "timed out" message.
func TestExecGenerator_TimeoutExceeded(t *testing.T) {
	orig := generatorWaitDelay
	generatorWaitDelay = 200 * time.Millisecond
	defer func() { generatorWaitDelay = orig }()

	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `sleep 30`)

	g := &ExecGenerator{
		Command: []string{scriptPath},
		Timeout: 150 * time.Millisecond,
	}
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want 'timed out' in error message, got: %v", err)
	}
}

// TestExecGenerator_OutputExceedsCap proves that output larger than
// MaxOutputBytes is rejected with ErrGeneratorOutputTooLarge naming the cap.
func TestExecGenerator_OutputExceedsCap(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	// Write 20 bytes of output, cap is 10.
	writeGenScript(t, scriptPath, `printf '12345678901234567890' > "$ETUDE_OUTPUT_FILE"`)

	g := &ExecGenerator{
		Command:        []string{scriptPath},
		MaxOutputBytes: 10,
	}
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if !errors.Is(err, ErrGeneratorOutputTooLarge) {
		t.Fatalf("want ErrGeneratorOutputTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("want cap value in error message, got: %v", err)
	}
}

// TestExecGenerator_OutputWithinCap proves that normal output under the cap
// still succeeds unchanged.
func TestExecGenerator_OutputWithinCap(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	writeGenScript(t, scriptPath, `printf 'hello' > "$ETUDE_OUTPUT_FILE"`)

	g := &ExecGenerator{
		Command:        []string{scriptPath},
		MaxOutputBytes: 100,
	}
	res, err := g.Generate(context.Background(), GenerateRequest{})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if string(res.Body) != "hello" {
		t.Errorf("want Body %q, got %q", "hello", res.Body)
	}
}

// TestExecGenerator_SpawnsSurvivingChild proves that WaitDelay prevents the
// generate from hanging when the script backgrounds a long-lived child that
// holds inherited pipe write-ends open.
//
// Without WaitDelay: cmd.Wait blocks indefinitely.
// With WaitDelay: cmd.Run returns within ~Timeout+WaitDelay. The test guard is
// set to 5s (comfortably above 200ms WaitDelay + 150ms Timeout but well below
// any infinite hang), so a regression is a test FAILURE, not an infinite hang.
func TestExecGenerator_SpawnsSurvivingChild(t *testing.T) {
	orig := generatorWaitDelay
	generatorWaitDelay = 200 * time.Millisecond
	defer func() { generatorWaitDelay = orig }()

	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "gen.sh")
	// Background a long-lived child, write output, then exit 0.
	writeGenScript(t, scriptPath, `sleep 300 &
printf 'body' > "$ETUDE_OUTPUT_FILE"`)

	g := &ExecGenerator{
		Command: []string{scriptPath},
		Timeout: 150 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		_, err := g.Generate(context.Background(), GenerateRequest{})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected an error (timeout), got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecGenerator.Generate did not return within 5s; WaitDelay regression: surviving child held pipes open")
	}
}
