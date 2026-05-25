package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecJudge satisfies Judge by invoking a configured external command headlessly.
//
// # Judge I/O contract
//
// The command is launched with a strict environment (no parent env leaks):
//
//	PATH              — extracted from the parent environment (exact-key match)
//	ETUDE_INPUTS_DIR  — directory containing materialised inputs:
//	                      <NN>-target-<role>  for each Targets entry (ordered, %02d)
//	                      <NN>-context-<role> for each Context entry (ordered, %02d)
//	ETUDE_OUTPUT_FILE — path the judge MUST write its JSON verdict to
//	ETUDE_RUBRIC_FILE — path to the materialised Rubric bytes (only set when Rubric is non-nil)
//	ETUDE_MODEL       — req.Producer.Model (always set, even when empty)
//
// The working directory is the scratch temp dir created for this invocation.
//
// # Output JSON shape
//
// The judge writes a JSON object to ETUDE_OUTPUT_FILE. Go field → JSON tag:
//
//	Value      → "value"      (*float64, omitempty) — required for rubric method
//	Max        → "max"        (*float64, omitempty) — required for rubric method
//	Winner     → "winner"     (string,   omitempty) — required for pairwise method (future)
//	Confidence → "confidence" (*float64, omitempty) — optional, pairwise only
//	Findings   → "findings"   ([]object, omitempty) — optional structured observations
//	  severity → "severity"   (string) — "info" | "warning" | "error"
//	  message  → "message"    (string) — required, non-empty
//	  pointer  → "pointer"    (string, omitempty) — optional artifact locator
//
// Unknown fields and trailing data are rejected (ErrJudgeOutputInvalid).
// Per-method validation is applied in ExecJudge before returning.
type ExecJudge struct {
	// Command is the executable and arguments. Command[0] is the binary.
	// Must be non-empty to invoke the judge.
	Command []string
	// Model is passed as ETUDE_MODEL to the judge command. May be empty.
	Model string
}

// compile-time interface satisfaction assertion.
var _ Judge = (*ExecJudge)(nil)

// judgeOutputJSON is the wire type for the judge's output file.
// It uses explicit JSON tags and pointer fields so absent != zero under
// DisallowUnknownFields decoding.
//
// Go field → JSON tag mapping:
//
//	Value      → "value"
//	Max        → "max"
//	Winner     → "winner"
//	Confidence → "confidence"
//	Findings   → "findings"
type judgeOutputJSON struct {
	Value      *float64      `json:"value,omitempty"`
	Max        *float64      `json:"max,omitempty"`
	Winner     string        `json:"winner,omitempty"`
	Confidence *float64      `json:"confidence,omitempty"`
	Findings   []findingWire `json:"findings,omitempty"`
}

// findingWire is the JSON wire type for a single finding in judge output.
type findingWire struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Pointer  string `json:"pointer,omitempty"`
}

// Judge implements Judge for ExecJudge. It materialises inputs, runs the
// command, reads the output file, and validates the result per method.
func (e *ExecJudge) Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
	if len(e.Command) == 0 {
		return JudgeResponse{}, ErrJudgeNotConfigured
	}

	// Create a fresh temp dir for this invocation; clean it up on return.
	scratch, err := os.MkdirTemp("", "etude-judge-*")
	if err != nil {
		return JudgeResponse{}, fmt.Errorf("%w: create scratch dir: %v", ErrJudgeFailed, err)
	}
	defer os.RemoveAll(scratch)

	inputsDir := filepath.Join(scratch, "inputs")
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		return JudgeResponse{}, fmt.Errorf("%w: create inputs dir: %v", ErrJudgeFailed, err)
	}

	// Materialise targets as <NN>-target-<role>.
	// filepath.Base(inp.Role) prevents a role like "a/b" from creating a subdir.
	for i, inp := range req.Targets {
		name := fmt.Sprintf("%02d-target-%s", i, filepath.Base(inp.Role))
		path := filepath.Join(inputsDir, name)
		if err := os.WriteFile(path, inp.Content, 0o644); err != nil {
			return JudgeResponse{}, fmt.Errorf("%w: write target %s: %v", ErrJudgeFailed, name, err)
		}
	}

	// Materialise context inputs as <NN>-context-<role>.
	// filepath.Base(inp.Role) prevents a role like "a/b" from creating a subdir.
	for i, inp := range req.Context {
		name := fmt.Sprintf("%02d-context-%s", i, filepath.Base(inp.Role))
		path := filepath.Join(inputsDir, name)
		if err := os.WriteFile(path, inp.Content, 0o644); err != nil {
			return JudgeResponse{}, fmt.Errorf("%w: write context %s: %v", ErrJudgeFailed, name, err)
		}
	}

	outputPath := filepath.Join(scratch, "output")

	// Materialise rubric bytes when present.
	rubricPath := filepath.Join(scratch, "rubric")
	if req.Rubric != nil {
		if err := os.WriteFile(rubricPath, req.Rubric, 0o644); err != nil {
			return JudgeResponse{}, fmt.Errorf("%w: write rubric: %v", ErrJudgeFailed, err)
		}
	}

	// Build strict env: extract PATH via exact-key match (mirrors exec_runner.go).
	pathVal := extractPATHFromEnv(os.Environ())
	env := []string{
		"PATH=" + pathVal,
		"ETUDE_INPUTS_DIR=" + inputsDir,
		"ETUDE_OUTPUT_FILE=" + outputPath,
		"ETUDE_MODEL=" + e.Model,
	}
	if req.Rubric != nil {
		env = append(env, "ETUDE_RUBRIC_FILE="+rubricPath)
	}

	var stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Command[0], e.Command[1:]...)
	cmd.Dir = scratch
	cmd.Env = env
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	// Context cancellation/timeout takes precedence over generic exit-status errors.
	if ctx.Err() != nil {
		return JudgeResponse{}, fmt.Errorf("judge: context done: %w", ctx.Err())
	}
	if runErr != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		return JudgeResponse{}, fmt.Errorf("%w: %s: %v: %s", ErrJudgeFailed, e.Command[0], runErr, stderr)
	}

	// Read output (symlink-safe via Lstat).
	fi, statErr := os.Lstat(outputPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return JudgeResponse{}, ErrJudgeOutputMissing
		}
		return JudgeResponse{}, fmt.Errorf("%w: %v", ErrJudgeOutputMissing, statErr)
	}
	if !fi.Mode().IsRegular() {
		return JudgeResponse{}, ErrJudgeOutputNotRegular
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return JudgeResponse{}, fmt.Errorf("%w: read output: %v", ErrJudgeOutputMissing, err)
	}

	// Decode with DisallowUnknownFields + trailing-data check (reuses ensureEOF).
	var wire judgeOutputJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return JudgeResponse{}, fmt.Errorf("%w: decode: %v", ErrJudgeOutputInvalid, err)
	}
	if err := ensureEOFJudge(dec); err != nil {
		return JudgeResponse{}, err
	}

	// Per-method output validation.
	if err := validateJudgeOutput(req.Method, wire); err != nil {
		return JudgeResponse{}, err
	}

	// Map wire -> JudgeResponse.
	resp := JudgeResponse{
		Value:      wire.Value,
		Max:        wire.Max,
		Winner:     Winner(wire.Winner),
		Confidence: wire.Confidence,
	}
	if len(wire.Findings) > 0 {
		resp.Findings = make([]Finding, 0, len(wire.Findings))
		for _, fw := range wire.Findings {
			resp.Findings = append(resp.Findings, Finding{
				Severity: Severity(fw.Severity),
				Message:  fw.Message,
				Pointer:  fw.Pointer,
			})
		}
	}
	return resp, nil
}

// validateJudgeOutput applies per-method shape validation to the decoded wire output.
func validateJudgeOutput(method string, w judgeOutputJSON) error {
	switch method {
	case "rubric":
		if w.Value == nil {
			return fmt.Errorf("%w: rubric method requires value", ErrJudgeOutputInvalid)
		}
		if w.Max == nil {
			return fmt.Errorf("%w: rubric method requires max", ErrJudgeOutputInvalid)
		}
		if *w.Max <= 0 {
			return fmt.Errorf("%w: rubric max must be > 0", ErrJudgeOutputInvalid)
		}
		if *w.Value < 0 || *w.Value > *w.Max {
			return fmt.Errorf("%w: rubric value must be in [0, max]", ErrJudgeOutputInvalid)
		}
		if w.Winner != "" {
			return fmt.Errorf("%w: rubric method must not set winner", ErrJudgeOutputInvalid)
		}
		if w.Confidence != nil {
			return fmt.Errorf("%w: rubric method must not set confidence", ErrJudgeOutputInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported method %q", ErrJudgeOutputInvalid, method)
	}
	// Validate findings severity and message for all methods.
	for i, fw := range w.Findings {
		validSeverities := map[string]bool{"info": true, "warning": true, "error": true}
		if !validSeverities[fw.Severity] {
			return fmt.Errorf("%w: findings[%d].severity %q must be info, warning, or error", ErrJudgeOutputInvalid, i, fw.Severity)
		}
		if strings.TrimSpace(fw.Message) == "" {
			return fmt.Errorf("%w: findings[%d].message required", ErrJudgeOutputInvalid, i)
		}
	}
	return nil
}

// ensureEOFJudge checks that no trailing data follows the decoded JSON value.
// It mirrors ensureEOF in result.go but wraps with ErrJudgeOutputInvalid.
func ensureEOFJudge(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing data: %v", ErrJudgeOutputInvalid, err)
	}
	return fmt.Errorf("%w: trailing data", ErrJudgeOutputInvalid)
}

// extractPATHFromEnv returns the value of PATH from the environment slice.
// It uses exact-key matching (mirrors extractPATH in exec_runner.go).
func extractPATHFromEnv(environ []string) string {
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
