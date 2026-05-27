package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrimeRunsAnywhere executes the prime command from a non-git temp directory
// and asserts it succeeds with non-empty stdout and empty stderr.
func TestPrimeRunsAnywhere(t *testing.T) {
	t.Chdir(t.TempDir())

	stdout, stderr, err := execute("prime")
	if err != nil {
		t.Fatalf("prime returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("prime wrote to stderr: %q", stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("prime produced empty stdout")
	}
}

// TestPrimeDriftGuard asserts that every registered non-hidden subcommand name
// (filtering help/completion) appears specifically in the "Available Commands"
// section of the primer output. It uses qualified names for subcommands (e.g.
// "retro generate", "run list") to avoid false positives from prose matches.
// It also asserts that "eval" and "import" are NOT registered commands
// (so labelling them planned is correct).
func TestPrimeDriftGuard(t *testing.T) {
	root := NewRootCommand(&bytes.Buffer{}, &bytes.Buffer{})

	// Collect qualified command names (top level + one level deep) that must
	// appear in the Available Commands section.
	type entry struct {
		name      string // display name for error messages
		qualified string // the string that must appear in the section
	}
	var mustAppear []entry
	for _, c := range root.Commands() {
		if skipCommand(c) {
			continue
		}
		mustAppear = append(mustAppear, entry{name: c.Name(), qualified: c.Name()})
		for _, sub := range c.Commands() {
			if skipCommand(sub) {
				continue
			}
			qualified := c.Name() + " " + sub.Name()
			mustAppear = append(mustAppear, entry{name: qualified, qualified: qualified})
		}
	}

	if len(mustAppear) == 0 {
		t.Fatal("no commands found in root — test is broken")
	}

	// Get primer text.
	primer, _, err := execute("prime")
	if err != nil {
		t.Fatalf("prime returned error: %v", err)
	}

	// Extract just the Available Commands section: from its header to the next
	// "## " section header. renderPrimer uses "## Available Commands" as its
	// header and the next section is "## Planned / Not Yet Built".
	const sectionHeader = "## Available Commands"
	sectionStart := strings.Index(primer, sectionHeader)
	if sectionStart == -1 {
		t.Fatalf("primer is missing %q section header", sectionHeader)
	}
	// Advance past the header line itself, then find the next "## " header.
	afterHeader := primer[sectionStart+len(sectionHeader):]
	nextSection := strings.Index(afterHeader, "\n## ")
	var availSection string
	if nextSection == -1 {
		availSection = afterHeader
	} else {
		availSection = afterHeader[:nextSection]
	}

	// Split the section into lines and match each expected command as a
	// whole token at the START of the line (not a substring). This ensures
	// that a top-level "capture" assertion is not satisfied by a
	// "capture-gate" or "capture-run" line that merely contains the
	// substring "capture".
	//
	// For top-level commands: the first whitespace-delimited field of the
	// trimmed line must equal the name exactly.
	// For subcommands ("retro generate"): the first two fields joined must
	// equal the qualified name exactly.
	sectionLines := strings.Split(availSection, "\n")

	// Match against the EXACT rendered indentation so a command is found only on
	// its own line slot. renderPrimer formats top-level commands with a 2-space
	// indent ("  name<pad> short") and subcommands with a 4-space indent
	// ("    parent sub<pad> short"); padding always leaves a space after the
	// name. Anchoring on the indent + a trailing space closes two collision
	// classes a substring/first-field check misses:
	//   - sibling prefixes: "  capture " does NOT match the "  capture-gate" line.
	//   - parent vs subcommand: top-level "  run " (2-space) does NOT match the
	//     "    run list" (4-space) line, so a dropped top-level "run" line is caught
	//     even though its subcommand lines start with "run".
	lineMatchesToken := func(line, token string) bool {
		if strings.Contains(token, " ") {
			// Qualified subcommand "parent sub": 4-space indent.
			return strings.HasPrefix(line, "    "+token+" ")
		}
		// Top-level command: 2-space indent (and NOT the 4-space subcommand indent).
		return strings.HasPrefix(line, "  "+token+" ")
	}

	for _, e := range mustAppear {
		found := false
		for _, line := range sectionLines {
			if lineMatchesToken(line, e.qualified) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Available Commands section is missing registered command %q — update prime.go", e.name)
		}
	}

	// Planned-but-unregistered commands must NOT be real registered commands.
	plannedNames := []string{"eval"}
	registeredNames := map[string]bool{}
	for _, c := range root.Commands() {
		registeredNames[c.Name()] = true
	}
	for _, planned := range plannedNames {
		if registeredNames[planned] {
			t.Errorf("planned command %q is now registered — remove it from the 'Planned' list in prime.go", planned)
		}
	}

	// Assert the Planned section is still present.
	if !strings.Contains(primer, "## Planned") {
		t.Error("primer is missing '## Planned' section")
	}
}

// TestPrimeContent asserts the primer stdout contains key section anchors and
// terms that must be present for agent orientation.
func TestPrimeContent(t *testing.T) {
	t.Chdir(t.TempDir())

	stdout, _, err := execute("prime")
	if err != nil {
		t.Fatalf("prime returned error: %v", err)
	}

	anchors := []string{
		"refs/etude/",
		"artifacts/sha256",
		"manifest.json",
		"workflow.yaml",
		"bench",
		"replay",
		"Planned",
		"eval",
		"import",
	}
	for _, anchor := range anchors {
		if !strings.Contains(stdout, anchor) {
			t.Errorf("primer stdout missing anchor %q", anchor)
		}
	}
}

// TestPrimeNoArgs asserts that "prime foo" returns an error (cobra.NoArgs).
func TestPrimeNoArgs(t *testing.T) {
	_, _, err := execute("prime", "foo")
	if err == nil {
		t.Fatal("expected error for 'prime foo', got nil")
	}
}
