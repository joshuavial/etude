package cli

import (
	"bytes"
	"strings"
	"testing"
)

func execute(args ...string) (string, string, error) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	err := ExecuteWithWriters(&out, &errOut, args)
	return out.String(), errOut.String(), err
}

func TestRootHelp(t *testing.T) {
	output, stderr, err := execute("--help")
	if err != nil {
		t.Fatalf("help returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("help wrote to stderr: %q", stderr)
	}

	for _, want := range []string{
		"etude captures stage artifacts",
		"bench",
		"Usage:",
		"etude [flags]",
		"capture",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
}

func TestRootWithoutArgsShowsHelp(t *testing.T) {
	output, stderr, err := execute()
	if err != nil {
		t.Fatalf("root command returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("root command wrote to stderr: %q", stderr)
	}

	if !strings.Contains(output, "Usage:") {
		t.Fatalf("root output did not show help:\n%s", output)
	}
}

func TestVersion(t *testing.T) {
	output, stderr, err := execute("--version")
	if err != nil {
		t.Fatalf("version returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("version wrote to stderr: %q", stderr)
	}

	if strings.TrimSpace(output) != "etude dev" {
		t.Fatalf("unexpected version output %q", output)
	}
}

func TestCommandMetadata(t *testing.T) {
	cmd := NewRootCommand(&bytes.Buffer{}, &bytes.Buffer{})

	if cmd.Use != "etude" {
		t.Fatalf("Use = %q, want etude", cmd.Use)
	}
	if cmd.Version != "dev" {
		t.Fatalf("Version = %q, want dev", cmd.Version)
	}
	if !cmd.SilenceUsage || !cmd.SilenceErrors {
		t.Fatal("root command should silence usage and errors for caller-controlled output")
	}
}

func TestBenchCommandIsRegistered(t *testing.T) {
	stdout, stderr, err := execute("bench", "--help")
	if err != nil {
		t.Fatalf("bench --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "bench") {
		t.Fatalf("bench --help output does not mention 'bench':\n%s", stdout)
	}
}
