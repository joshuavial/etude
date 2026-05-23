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
		"Replay and bench commands are planned but not implemented yet.",
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

func TestFutureCommandNamesAreRejected(t *testing.T) {
	for _, args := range [][]string{
		{"replay"},
		{"bench"},
	} {
		output, stderr, err := execute(args...)
		if err == nil {
			t.Fatalf("execute(%v) returned nil error and output %q", args, output)
		}
		if output != "" {
			t.Fatalf("execute(%v) wrote stdout %q", args, output)
		}
		if !strings.Contains(err.Error(), args[0]) {
			t.Fatalf("execute(%v) error %q does not name rejected command", args, err.Error())
		}
		if !strings.Contains(stderr, args[0]) {
			t.Fatalf("execute(%v) stderr %q does not name rejected command", args, stderr)
		}
		if strings.Count(stderr, "unknown command") != 1 {
			t.Fatalf("execute(%v) stderr should contain one error, got %q", args, stderr)
		}
	}
}
