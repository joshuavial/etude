package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joshuavial/etude/internal/registry"
)

// TestAnchorSeatCommandsAnchorsPrimaryAndFallbacks pins the round-6 gate finding:
// the fallback-anchoring loop originally sat inside the block that ran only when
// the PRIMARY invoke had been rewritten, so a seat whose primary is a bare PATH
// command kept unanchored fallbacks. exec resolves a relative program against the
// CALLER's cwd, not the child's dir, so an unanchored repo-relative fallback
// fails with a confusing "file not found" that reads as a seat outage.
func TestAnchorSeatCommandsAnchorsPrimaryAndFallbacks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "seat-adapter.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	reg := registry.Registry{Seats: map[string]registry.Seat{
		// Primary is a BARE PATH COMMAND — it needs no anchoring itself, which is
		// exactly the case that used to skip its fallbacks entirely.
		"bare": {
			Provider: "p/m", Harness: "h", Invoke: "somecmd --flag",
			InvocationFallbacks: []registry.SeatInvocation{
				{Harness: "h2", Invoke: "scripts/seat-adapter.sh bare somecmd"},
				{Harness: "h3", Invoke: "in-harness:task subagent_type=general-purpose model=opus"},
			},
		},
		"relative": {
			Provider: "p/m", Harness: "h", Invoke: "scripts/seat-adapter.sh relative somecmd",
		},
		"absolute": {
			Provider: "p/m", Harness: "h", Invoke: "/usr/bin/true --flag",
		},
	}}

	anchorSeatCommands(&reg, root)

	bare := reg.Seats["bare"]
	if bare.Invoke != "somecmd --flag" {
		t.Errorf("a bare PATH command must be left alone, got %q", bare.Invoke)
	}
	wantFallback := script + " bare somecmd"
	if bare.InvocationFallbacks[0].Invoke != wantFallback {
		t.Errorf("fallback of a bare-primary seat was not anchored:\n got %q\nwant %q",
			bare.InvocationFallbacks[0].Invoke, wantFallback)
	}
	if bare.InvocationFallbacks[1].Invoke != "in-harness:task subagent_type=general-purpose model=opus" {
		t.Errorf("an in-harness candidate is not a command and must be untouched, got %q",
			bare.InvocationFallbacks[1].Invoke)
	}

	if got, want := reg.Seats["relative"].Invoke, script+" relative somecmd"; got != want {
		t.Errorf("relative primary not anchored:\n got %q\nwant %q", got, want)
	}
	if got := reg.Seats["absolute"].Invoke; got != "/usr/bin/true --flag" {
		t.Errorf("absolute primary must be left alone, got %q", got)
	}
}

// TestAnchorCommandLeavesUnresolvablePathsAlone: a repo-relative path that does
// not exist under root is left as-is, so a config error surfaces as the seat's
// own failure rather than as a silently rewritten command.
func TestAnchorCommandLeavesUnresolvablePathsAlone(t *testing.T) {
	root := t.TempDir()
	const invoke = "scripts/does-not-exist.sh arg"
	if got := anchorCommand(invoke, root); got != invoke {
		t.Errorf("anchorCommand rewrote a non-existent path: %q", got)
	}
	if got := anchorCommand("", root); got != "" {
		t.Errorf("anchorCommand mangled an empty invoke: %q", got)
	}
}
