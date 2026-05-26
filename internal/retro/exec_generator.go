package retro

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Sentinel errors for ExecGenerator.
var (
	ErrGeneratorNotConfigured    = errors.New("generator not configured")
	ErrGeneratorFailed           = errors.New("generator failed")
	ErrGeneratorOutputMissing    = errors.New("generator output missing")
	ErrGeneratorOutputNotRegular = errors.New("generator output is not a regular file")
)

// ExecGenerator satisfies Generator by invoking a configured external command
// headlessly. The command is launched with a strict environment (PATH,
// ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE) — the same exec contract as
// replay.ExecRunner and eval.ExecJudge.
//
// # Generator I/O contract
//
// Environment variables passed to the script:
//
//	PATH              — extracted from the parent environment (exact-key match)
//	ETUDE_INPUTS_DIR  — directory containing materialized subject artifacts:
//	                      <NN>-<runid>-<role>  for each subject's output (ordered %02d)
//	                      then <NN>-<runid>-input-<role> for each subject's inputs
//	ETUDE_OUTPUT_FILE — path the script MUST write the retro markdown body to
//
// The working directory is a fresh temp dir created for each invocation.
// Non-zero exit => ErrGeneratorFailed with trimmed stderr.
// Missing/irregular output file => ErrGeneratorOutputMissing/NotRegular.
// Context cancel/timeout takes precedence over process errors.
type ExecGenerator struct {
	// Command is the executable and its arguments. Command[0] is the binary.
	// Must be non-empty to run.
	Command []string
}

// compile-time interface satisfaction assertion.
var _ Generator = (*ExecGenerator)(nil)

// Generate implements Generator for ExecGenerator.
func (g *ExecGenerator) Generate(ctx context.Context, req GenerateRequest) (GenerateResult, error) {
	if len(g.Command) == 0 {
		return GenerateResult{}, ErrGeneratorNotConfigured
	}

	// Create a fresh temp dir for this invocation; clean it up on return.
	scratch, err := os.MkdirTemp("", "etude-generator-*")
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%w: create scratch dir: %v", ErrGeneratorFailed, err)
	}
	defer os.RemoveAll(scratch)

	inputsDir := filepath.Join(scratch, "inputs")
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		return GenerateResult{}, fmt.Errorf("%w: create inputs dir: %v", ErrGeneratorFailed, err)
	}

	outputPath := filepath.Join(scratch, "output")

	// Materialize subject artifacts into ETUDE_INPUTS_DIR.
	// Each subject contributes:
	//   <NN>-<runid>-output  (the stage output)
	//   <NN>-<runid>-input-<role>  (each stage input)
	// The NN index disambiguates multiple subjects with the same role names.
	for i, subj := range req.Subjects {
		// Sanitize runID for use in filenames: replace characters unsafe for
		// filesystem names with '-'. The runID charset is [a-z0-9-] per
		// runmanifest.IsValidRunID, so this is defensive only.
		safeRunID := sanitizeForFilename(subj.RunID)

		// Output artifact.
		outputName := fmt.Sprintf("%02d-%s-output", i, safeRunID)
		outPath := filepath.Join(inputsDir, outputName)
		if err := os.WriteFile(outPath, subj.OutputContent, 0o644); err != nil {
			return GenerateResult{}, fmt.Errorf("%w: write subject output %s: %v", ErrGeneratorFailed, outputName, err)
		}

		// Input artifacts.
		for _, inp := range subj.Inputs {
			safeRole := filepath.Base(inp.Role)
			inpName := fmt.Sprintf("%02d-%s-input-%s", i, safeRunID, safeRole)
			inpPath := filepath.Join(inputsDir, inpName)
			if err := os.WriteFile(inpPath, inp.Content, 0o644); err != nil {
				return GenerateResult{}, fmt.Errorf("%w: write subject input %s: %v", ErrGeneratorFailed, inpName, err)
			}
		}
	}

	// Build strict env — only PATH, ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE.
	pathVal := extractPATH(os.Environ())
	env := []string{
		"PATH=" + pathVal,
		"ETUDE_INPUTS_DIR=" + inputsDir,
		"ETUDE_OUTPUT_FILE=" + outputPath,
	}

	var stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, g.Command[0], g.Command[1:]...)
	cmd.Dir = scratch
	cmd.Env = env
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	// Context cancellation/timeout takes precedence over generic exit-status errors.
	if ctx.Err() != nil {
		return GenerateResult{}, fmt.Errorf("generator: context done: %w", ctx.Err())
	}
	if runErr != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		return GenerateResult{}, fmt.Errorf("%w: %s: %v: %s", ErrGeneratorFailed, g.Command[0], runErr, stderr)
	}

	// Read output (symlink-safe via Lstat).
	fi, statErr := os.Lstat(outputPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return GenerateResult{}, ErrGeneratorOutputMissing
		}
		return GenerateResult{}, fmt.Errorf("%w: %v", ErrGeneratorOutputMissing, statErr)
	}
	if !fi.Mode().IsRegular() {
		return GenerateResult{}, ErrGeneratorOutputNotRegular
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%w: read output: %v", ErrGeneratorOutputMissing, err)
	}

	return GenerateResult{
		Body:      data,
		MediaType: "text/markdown; charset=utf-8",
		Producer:  req.Producer,
	}, nil
}

// extractPATH returns the value of the PATH variable from the provided
// environment slice. It splits each entry on the FIRST '=' and matches
// by exact key "PATH" — using HasPrefix("PATH=") would match MYPATH=
// and similar variables. Mirrors replay.extractPATH.
func extractPATH(environ []string) string {
	for _, entry := range environ {
		idx := strings.IndexByte(entry, '=')
		if idx < 0 {
			continue
		}
		if entry[:idx] == "PATH" {
			return entry[idx+1:]
		}
	}
	return ""
}

// sanitizeForFilename replaces characters that are not safe for filesystem
// names with '-'. Since run IDs use [a-z0-9-], this is defensive.
func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// NewExecGenerator builds an ExecGenerator from a command spec string
// (e.g. "./retro.sh arg1"). Uses strings.Fields to split.
func NewExecGenerator(spec string) *ExecGenerator {
	return &ExecGenerator{Command: strings.Fields(spec)}
}
