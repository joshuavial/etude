package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/sessionevidence"
)

// Sentinel errors for ExecRunner.
var (
	ErrRunnerNotConfigured = errors.New("runner not configured")
	ErrInvalidWorktreeDir  = errors.New("invalid worktree dir")
	ErrInvalidScratchDir   = errors.New("invalid scratch dir")
	ErrInvalidInputRole    = errors.New("invalid input role")
	ErrRunnerFailed        = errors.New("runner failed")
	ErrOutputMissing       = errors.New("output missing")
	ErrOutputNotRegular    = errors.New("output is not a regular file")
	ErrOutputTooLarge      = errors.New("output too large")
	ErrSessionInvalid      = errors.New("session sidecar invalid")
)

// runnerWaitDelay is the grace period after context cancellation or process
// exit before cmd.Wait forcibly closes I/O pipes. This bounds the hang class
// caused by a script that backgrounds a child holding inherited pipe
// write-ends open. Declared as var so tests can override for speed.
var runnerWaitDelay = 10 * time.Second

// ExecRunner satisfies Runner by invoking a configured external command
// headlessly. The command is launched with a strict environment (PATH,
// ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE) and its working directory set to
// the resolved WorktreeDir.
type ExecRunner struct {
	// Command is the executable and its arguments. Command[0] is the binary;
	// Command[1:] are arguments. Must be non-empty to run.
	Command []string
	// Timeout, when > 0, wraps the execution context with a per-invocation
	// deadline. Zero means unlimited (default, backward compatible).
	Timeout time.Duration
	// MaxOutputBytes, when > 0, caps how many bytes are read from the output
	// file. Outputs exceeding the cap are rejected with ErrOutputTooLarge.
	// Zero means unlimited (default, backward compatible).
	MaxOutputBytes int64
	// EnvAllowlist is the list of env var NAMES (never values) whose values
	// are resolved from os.Environ() at run time and appended to the child's
	// strict environment.  Nil/empty = current hermetic behavior (unchanged).
	// Callers pass NAMES only; ExecRunner reads values itself so values never
	// live in a caller-visible struct.
	EnvAllowlist []string
}

// compile-time interface satisfaction assertion.
var _ Runner = (*ExecRunner)(nil)

// Run implements Runner for ExecRunner. It materializes inputs into
// <ScratchDir>/inputs/<NN>-<role>, invokes the configured command, and reads
// the output from <ScratchDir>/output.
//
// When Timeout > 0, the execution context is wrapped with a per-invocation
// deadline. WaitDelay is always set on the exec.Cmd to bound pipe-drain after
// the process exits or the context fires, preventing hangs from backgrounded
// grandchild processes that hold inherited pipe write-ends open.
func (r *ExecRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	// Step 1: command must be configured.
	if len(r.Command) == 0 {
		return RunResult{}, ErrRunnerNotConfigured
	}

	// Step 2: validate and resolve WorktreeDir.
	resolvedWorktree, err := resolveDir(req.WorktreeDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: %v", ErrInvalidWorktreeDir, err)
	}

	// Step 2: validate and resolve ScratchDir.
	resolvedScratch, err := resolveDir(req.ScratchDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: %v", ErrInvalidScratchDir, err)
	}

	// Step 3: ScratchDir must NOT be at or under WorktreeDir.
	rel, relErr := filepath.Rel(resolvedWorktree, resolvedScratch)
	if relErr == nil {
		// rel == "." means ScratchDir IS WorktreeDir (reject).
		// A path that doesn't start with ".." means ScratchDir is inside WorktreeDir (reject).
		// A true sibling yields a rel starting with ".." (e.g. "../sibling") and is accepted.
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return RunResult{}, fmt.Errorf("%w: scratch dir must not be at or under worktree dir", ErrInvalidScratchDir)
		}
	}

	// Step 4: validate all input roles BEFORE any filesystem write.
	for _, inp := range req.Inputs {
		if !runmanifest.IsValidIdentifier(inp.Role) || inp.Role != filepath.Base(inp.Role) || inp.Role == ".." {
			return RunResult{}, fmt.Errorf("%w: %q", ErrInvalidInputRole, inp.Role)
		}
	}

	// Step 5: scratch hygiene — remove stale output/session and reset inputs dir.
	outputPath := filepath.Join(resolvedScratch, "output")
	sessionPath := filepath.Join(resolvedScratch, "session.json")
	inputsDir := filepath.Join(resolvedScratch, "inputs")

	_ = os.Remove(outputPath) // ignore os.IsNotExist; stale output must not survive
	if err := os.Remove(sessionPath); err != nil && !os.IsNotExist(err) {
		return RunResult{}, fmt.Errorf("%w: remove stale session sidecar: %v", ErrInvalidScratchDir, err)
	}

	if err := os.RemoveAll(inputsDir); err != nil {
		return RunResult{}, fmt.Errorf("%w: remove inputs dir: %v", ErrInvalidScratchDir, err)
	}
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		return RunResult{}, fmt.Errorf("%w: create inputs dir: %v", ErrInvalidScratchDir, err)
	}

	// Write each input to <inputs>/<NN>-<role>. Nil/empty Inputs slice is
	// intentional — range over nil is a no-op.
	for i, inp := range req.Inputs {
		name := fmt.Sprintf("%02d-%s", i, inp.Role)
		path := filepath.Join(inputsDir, name)
		if err := os.WriteFile(path, inp.Content, 0o644); err != nil {
			return RunResult{}, fmt.Errorf("%w: write input %s: %v", ErrInvalidScratchDir, name, err)
		}
	}

	// Step 6: apply per-invocation timeout when configured.
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	// Step 7: build strict env — PATH, ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE,
	// then any allowlisted names resolved from the orchestrator's own env.
	//
	// Reserved names (PATH, ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE) are always
	// set from the base vars above; allowlist entries for these names are
	// silently skipped so the allowlist cannot shadow or duplicate them.
	// This skip is defense-in-depth: workflow.Validate already rejects
	// reserved names in env_allowlist at parse time (fail-fast).
	//
	// SECURITY: cmd.Env is the ONLY place where allowlisted secret values
	// cross the process boundary.  NEVER log cmd.Env, cmd.String(), or any
	// other representation that includes cmd.Env — doing so would leak secret
	// values to log sinks.  This comment is a durable guardrail for future
	// maintainers; do not remove it.
	environ := os.Environ()
	env := []string{
		"PATH=" + extractEnv(environ, "PATH"),
		"ETUDE_INPUTS_DIR=" + inputsDir,
		"ETUDE_OUTPUT_FILE=" + outputPath,
		"ETUDE_SESSION_FILE=" + sessionPath,
	}
	reservedEnv := map[string]bool{
		"PATH":               true,
		"ETUDE_INPUTS_DIR":   true,
		"ETUDE_OUTPUT_FILE":  true,
		"ETUDE_SESSION_FILE": true,
	}
	for _, name := range r.EnvAllowlist {
		if reservedEnv[name] {
			continue // defense-in-depth: cannot shadow the 3 base vars
		}
		if val, ok := lookupEnv(environ, name); ok {
			env = append(env, name+"="+val)
		}
		// When a name is listed but not present in os.Environ(), it is silently
		// skipped — the manifest records the configured NAMES (policy/intent),
		// not which values were actually delivered.
	}

	stdoutBuf := newBoundedStream(true, MaxCapturedStreamBytes)
	stderrBuf := newBoundedStream(false, MaxCapturedStreamBytes)
	cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
	cmd.Dir = resolvedWorktree
	cmd.Env = env
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf
	// WaitDelay bounds cmd.Wait after ctx fires or the process exits.
	// Without it, a backgrounded grandchild holding inherited pipe write-ends
	// open can cause cmd.Run to hang indefinitely.
	cmd.WaitDelay = runnerWaitDelay

	runErr := cmd.Run()

	// Step 8: ctx taxonomy — context cancellation/timeout takes precedence.
	if ctx.Err() != nil {
		var base error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			base = fmt.Errorf("runner: timed out after %v: %w", r.Timeout, ctx.Err())
		} else {
			base = fmt.Errorf("runner: context done: %w", ctx.Err())
		}
		return RunResult{}, withStderrDiagnostic(base, stderrBuf.Bytes())
	}
	if runErr != nil {
		base := fmt.Errorf("%w: %s: %v", ErrRunnerFailed, r.Command[0], runErr)
		return RunResult{}, withStderrDiagnostic(base, stderrBuf.Bytes())
	}

	var session *SessionInfo
	if strings.ToLower(strings.TrimSpace(req.Producer.Harness.Name)) != "shell" {
		session, err = readSessionSidecar(resolvedScratch, sessionPath)
		if err != nil {
			return RunResult{}, err
		}
	}

	// Step 9: read output (symlink-safe via Lstat).
	fi, statErr := os.Lstat(outputPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return RunResult{}, ErrOutputMissing
		}
		return RunResult{}, fmt.Errorf("%w: %v", ErrOutputMissing, statErr)
	}
	if !fi.Mode().IsRegular() {
		return RunResult{}, ErrOutputNotRegular
	}

	// Cheap pre-check: reject early if the file is already known to exceed the cap.
	if r.MaxOutputBytes > 0 && fi.Size() > r.MaxOutputBytes {
		return RunResult{}, fmt.Errorf("%w: file size %d exceeds cap %d: %s", ErrOutputTooLarge, fi.Size(), r.MaxOutputBytes, outputPath)
	}

	// Read via LimitReader for a TOCTOU-safe hard cap: a concurrent writer
	// (e.g. backgrounded grandchild) cannot sneak bytes past the cap between
	// the Lstat and the read. An empty-but-present regular file is valid.
	var data []byte
	if r.MaxOutputBytes > 0 {
		f, openErr := os.Open(outputPath)
		if openErr != nil {
			return RunResult{}, fmt.Errorf("%w: read output: %v", ErrOutputMissing, openErr)
		}
		data, err = io.ReadAll(io.LimitReader(f, r.MaxOutputBytes+1))
		f.Close()
		if err != nil {
			return RunResult{}, fmt.Errorf("%w: read output: %v", ErrOutputMissing, err)
		}
		if int64(len(data)) > r.MaxOutputBytes {
			return RunResult{}, fmt.Errorf("%w: read %d bytes, cap %d: %s", ErrOutputTooLarge, int64(len(data)), r.MaxOutputBytes, outputPath)
		}
	} else {
		data, err = os.ReadFile(outputPath)
		if err != nil {
			return RunResult{}, fmt.Errorf("%w: read output: %v", ErrOutputMissing, err)
		}
	}

	// Step 10: assemble result and apply defaults.
	res := RunResult{
		Output:   data,
		Log:      frameStageLog(stdoutBuf, stderrBuf),
		Producer: req.Producer,
		Session:  session,
	}
	applyResultDefaults(req, &res)
	return res, nil
}

type boundedStream struct {
	buf     []byte
	dropped int64
	head    bool
	limit   int
}

func newBoundedStream(head bool, limit int) *boundedStream {
	return &boundedStream{head: head, limit: limit}
}

func (w *boundedStream) Write(p []byte) (int, error) {
	n := len(p)
	if w.head {
		remaining := w.limit - len(w.buf)
		if remaining > 0 {
			keep := min(remaining, len(p))
			w.buf = append(w.buf, p[:keep]...)
			w.dropped += int64(len(p) - keep)
		} else {
			w.dropped += int64(len(p))
		}
		return n, nil
	}
	if len(p) >= w.limit {
		w.dropped += int64(len(w.buf) + len(p) - w.limit)
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return n, nil
	}
	over := len(w.buf) + len(p) - w.limit
	if over > 0 {
		w.dropped += int64(over)
		copy(w.buf, w.buf[over:])
		w.buf = w.buf[:len(w.buf)-over]
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

func (w *boundedStream) Bytes() []byte { return w.buf }

func frameStageLog(stdout, stderr *boundedStream) []byte {
	var out bytes.Buffer
	write := func(name, retained string, stream *boundedStream) {
		if len(stream.buf) == 0 && stream.dropped == 0 {
			return
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "[%s bytes=%d dropped=%d retained=%s]\n", name, len(stream.buf), stream.dropped, retained)
		out.Write(stream.buf)
	}
	write("stdout", "head", stdout)
	write("stderr", "tail", stderr)
	if out.Len() == 0 {
		return nil
	}
	return out.Bytes()
}

func withStderrDiagnostic(base error, stderr []byte) error {
	const maxDiagnosticBytes = 8 << 10
	if len(stderr) > maxDiagnosticBytes {
		stderr = stderr[len(stderr)-maxDiagnosticBytes:]
	}
	diagnostic := strings.TrimSpace(string(stderr))
	if diagnostic == "" {
		return base
	}
	return fmt.Errorf("%w: stderr: %s", base, diagnostic)
}

func readSessionSidecar(root, path string) (*SessionInfo, error) {
	content, err := sessionevidence.ReadRegularFileUnderLimit(root, path, 64<<10)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrSessionInvalid, err)
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("%w: sidecar is not valid UTF-8", ErrSessionInvalid)
	}
	var session SessionInfo
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&session); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrSessionInvalid, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing JSON", ErrSessionInvalid)
	}
	if err := ValidateSessionInfo(&session); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSessionInvalid, err)
	}
	if filepath.IsAbs(session.TranscriptPath) {
		rel, err := filepath.Rel(root, session.TranscriptPath)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			// Return scratch-local transcript paths in relative form. Besides
			// making the containment boundary explicit, this avoids macOS's
			// /var -> /private/var alias making the engine misclassify the same
			// attempt scratch directory as an external absolute path.
			session.TranscriptPath = rel
		}
	}
	return &session, nil
}

// ValidateSessionInfo applies the durable engine boundary to any Runner.
func ValidateSessionInfo(session *SessionInfo) error {
	if session == nil {
		return nil
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return errors.New("session_id is required")
	}
	for name, value := range map[string]string{
		"session_id":      session.SessionID,
		"transcript_uri":  session.TranscriptURI,
		"transcript_path": session.TranscriptPath,
	} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, utf8.RuneError) {
			return fmt.Errorf("%s contains invalid Unicode", name)
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s contains a control character", name)
		}
		if len(value) > MaxSessionFieldBytes {
			return fmt.Errorf("%s exceeds %d bytes", name, MaxSessionFieldBytes)
		}
	}
	return nil
}

// resolveDir validates that path is non-empty, absolute, exists, and is a
// directory, then returns its symlink-resolved form.
func resolveDir(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q is not absolute", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("eval symlinks %q: %w", path, err)
	}
	return resolved, nil
}

// extractEnv returns the value of the given key from the provided environment
// slice. It splits each entry on the FIRST '=' and matches by exact key —
// using HasPrefix would match MYKEY= or KEYEXT= instead of the exact KEY.
// Returns "" when the key is absent.
func extractEnv(environ []string, key string) string {
	val, _ := lookupEnv(environ, key)
	return val
}

// lookupEnv returns the value and true when key is present in environ,
// or ("", false) when absent. Matches by exact key (split on first '=').
func lookupEnv(environ []string, key string) (string, bool) {
	for _, entry := range environ {
		idx := strings.IndexByte(entry, '=')
		if idx < 0 {
			continue
		}
		if entry[:idx] == key {
			return entry[idx+1:], true
		}
	}
	return "", false
}
