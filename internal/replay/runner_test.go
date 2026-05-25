package replay

import (
	"context"
	"errors"
	"testing"

	"github.com/joshuavial/etude/internal/runmanifest"
)

var testProducer = runmanifest.Producer{
	Harness: runmanifest.Harness{Name: "claude-code", Version: "2.1.150"},
	Model:   "claude-opus-4-7",
	Skill:   runmanifest.Skill{ID: "test-skill", Repo: "test-repo", Version: "v1"},
}

func testReq(inputs []RunInput) RunRequest {
	return RunRequest{
		WorktreeDir:     "/fake/worktree",
		ScratchDir:      "/fake/scratch",
		Inputs:          inputs,
		OutputRole:      "output",
		OutputMediaType: "text/plain",
		Producer:        testProducer,
	}
}

// Compile-time interface satisfaction is asserted by:
//
//	var _ Runner = (*StubRunner)(nil)
//
// in runner.go.

func TestStubRunner_CannedMode_Empty(t *testing.T) {
	stub := &StubRunner{CannedOutput: []byte{}}
	res, err := stub.Run(context.Background(), testReq(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Output) != "" {
		t.Errorf("want empty output, got %q", res.Output)
	}
	// CannedMediaType is "" so default kicks in.
	if res.MediaType != "text/plain" {
		t.Errorf("want MediaType %q (default), got %q", "text/plain", res.MediaType)
	}
	if res.Producer != testProducer {
		t.Errorf("want Producer echoed as %v, got %v", testProducer, res.Producer)
	}
}

func TestStubRunner_CannedMode_Single(t *testing.T) {
	stub := &StubRunner{CannedOutput: []byte("hello")}
	req := testReq([]RunInput{{Role: "r", MediaType: "text/plain", Content: []byte("ignored")}})
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Output) != "hello" {
		t.Errorf("want %q, got %q", "hello", res.Output)
	}
	// CannedMediaType is "" so default kicks in.
	if res.MediaType != "text/plain" {
		t.Errorf("want MediaType default %q, got %q", "text/plain", res.MediaType)
	}
}

func TestStubRunner_CannedMode_Multi(t *testing.T) {
	stub := &StubRunner{CannedOutput: []byte("canned"), CannedMediaType: "application/json"}
	req := testReq([]RunInput{
		{Role: "a", MediaType: "text/plain", Content: []byte("x")},
		{Role: "b", MediaType: "text/plain", Content: []byte("y")},
	})
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Output) != "canned" {
		t.Errorf("want %q, got %q", "canned", res.Output)
	}
	// Explicit CannedMediaType should NOT be overridden.
	if res.MediaType != "application/json" {
		t.Errorf("want explicit MediaType %q, got %q", "application/json", res.MediaType)
	}
}

func TestStubRunner_CannedMode_MediaTypeDefault(t *testing.T) {
	// CannedMediaType empty => defaults to req.OutputMediaType.
	stub := &StubRunner{CannedOutput: []byte("out"), CannedMediaType: ""}
	req := testReq(nil)
	req.OutputMediaType = "image/png"
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MediaType != "image/png" {
		t.Errorf("want %q, got %q", "image/png", res.MediaType)
	}
}

func TestStubRunner_ConcatMode_Order(t *testing.T) {
	stub := &StubRunner{Concat: true}
	req := testReq([]RunInput{
		{Role: "a", MediaType: "text/plain", Content: []byte("foo")},
		{Role: "b", MediaType: "text/plain", Content: []byte("bar")},
		{Role: "c", MediaType: "text/plain", Content: []byte("baz")},
	})
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "foo" + stubConcatSeparator + "bar" + stubConcatSeparator + "baz"
	if string(res.Output) != want {
		t.Errorf("want %q, got %q", want, res.Output)
	}
	// MediaType defaults to req.OutputMediaType in concat mode.
	if res.MediaType != "text/plain" {
		t.Errorf("want MediaType default %q in concat mode, got %q", "text/plain", res.MediaType)
	}
}

func TestStubRunner_ConcatMode_Empty(t *testing.T) {
	stub := &StubRunner{Concat: true}
	req := testReq(nil)
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Output) != 0 {
		t.Errorf("want empty output for no inputs, got %q", res.Output)
	}
}

func TestStubRunner_InjectedError(t *testing.T) {
	sentinel := errors.New("stub failure")
	stub := &StubRunner{Err: sentinel}
	_, err := stub.Run(context.Background(), testReq(nil))
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error, got %v", err)
	}
}

func TestStubRunner_ProducerEcho(t *testing.T) {
	stub := &StubRunner{CannedOutput: []byte("x")}
	req := testReq(nil)
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Producer != testProducer {
		t.Errorf("want Producer echoed as %v, got %v", testProducer, res.Producer)
	}
}

func TestStubRunner_ProducerOverride(t *testing.T) {
	override := runmanifest.Producer{
		Harness: runmanifest.Harness{Name: "codex", Version: "x"},
		Model:   "gpt-5.5",
		Skill:   runmanifest.Skill{ID: "newskill", Repo: "r", Version: "v2"},
	}
	stub := &StubRunner{
		CannedOutput:     []byte("x"),
		ProducerOverride: override,
	}
	req := testReq(nil)
	// req.Producer == testProducer, which is distinct from override on all three axes
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Producer == req.Producer {
		t.Errorf("want ProducerOverride applied, but got req.Producer echoed: %v", res.Producer)
	}
	if res.Producer != override {
		t.Errorf("want Producer %v, got %v", override, res.Producer)
	}
}

func TestStubRunner_PassThrough(t *testing.T) {
	stub := &StubRunner{} // CannedOutput nil, Concat false, Err nil
	req := testReq(nil)
	req.OutputMediaType = "application/octet-stream"
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != nil {
		t.Errorf("want nil output (pass-through), got %q", res.Output)
	}
	if res.MediaType != req.OutputMediaType {
		t.Errorf("want MediaType %q (default), got %q", req.OutputMediaType, res.MediaType)
	}
}

func TestStubRunner_CannedWinsOverConcat(t *testing.T) {
	stub := &StubRunner{
		CannedOutput: []byte("canned"),
		Concat:       true,
	}
	req := testReq([]RunInput{
		{Role: "a", MediaType: "text/plain", Content: []byte("foo")},
		{Role: "b", MediaType: "text/plain", Content: []byte("bar")},
	})
	res, err := stub.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Output) != "canned" {
		t.Errorf("want CannedOutput to win, got %q", res.Output)
	}
	// Verify concat was NOT applied (would produce "foo\nbar")
	concatResult := "foo" + stubConcatSeparator + "bar"
	if string(res.Output) == concatResult {
		t.Errorf("concat was applied when CannedOutput should have won")
	}
}
