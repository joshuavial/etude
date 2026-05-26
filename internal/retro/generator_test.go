package retro

import (
	"context"
	"errors"
	"testing"

	"github.com/joshuavial/etude/internal/runmanifest"
)

func TestStubGenerator_CannedMode(t *testing.T) {
	want := []byte("# Retro body\nSome findings.\n")
	stub := &StubGenerator{CannedBody: want, CannedMediaType: "text/markdown; charset=utf-8"}
	res, err := stub.Generate(context.Background(), GenerateRequest{Scope: "cohort"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Body) != string(want) {
		t.Errorf("Body = %q, want %q", res.Body, want)
	}
	if res.MediaType != "text/markdown; charset=utf-8" {
		t.Errorf("MediaType = %q, want text/markdown; charset=utf-8", res.MediaType)
	}
}

func TestStubGenerator_ErrorMode(t *testing.T) {
	sentinel := errors.New("generate failed")
	stub := &StubGenerator{Err: sentinel}
	_, err := stub.Generate(context.Background(), GenerateRequest{Scope: "cohort"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

func TestStubGenerator_EchoesProducer(t *testing.T) {
	producer := runmanifest.Producer{
		Harness: runmanifest.Harness{Name: "claude-code", Version: "2.0"},
		Model:   "claude-sonnet-4-6",
	}
	stub := &StubGenerator{CannedBody: []byte("body")}
	res, err := stub.Generate(context.Background(), GenerateRequest{Producer: producer})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Producer != producer {
		t.Errorf("Producer = %v, want %v", res.Producer, producer)
	}
}

func TestStubGenerator_ProducerOverride(t *testing.T) {
	req := GenerateRequest{
		Producer: runmanifest.Producer{Model: "original"},
	}
	override := runmanifest.Producer{Model: "override"}
	stub := &StubGenerator{CannedBody: []byte("body"), ProducerOverride: override}
	res, err := stub.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Producer.Model != "override" {
		t.Errorf("Producer.Model = %q, want override", res.Producer.Model)
	}
}

func TestStubGenerator_ConcatMode(t *testing.T) {
	stub := &StubGenerator{Concat: true}
	req := GenerateRequest{
		Subjects: []SubjectArtifact{
			{RunID: "r1", OutputContent: []byte("alpha")},
			{RunID: "r2", OutputContent: []byte("beta")},
		},
	}
	res, err := stub.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "alpha\nbeta"
	if string(res.Body) != want {
		t.Errorf("Body = %q, want %q", res.Body, want)
	}
}

func TestStubGenerator_CompileTimeAssertion(t *testing.T) {
	// var _ Generator = (*StubGenerator)(nil) is in generator.go; this test
	// documents that the assertion is present without re-asserting it.
	var g Generator = &StubGenerator{CannedBody: []byte("ok")}
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
