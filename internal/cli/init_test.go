package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/workflow"
)

// ---------------------------------------------------------------------------
// Directive B: positive registration test — init must appear as a registered
// subcommand (inverse of the dropped TestFutureCommandNamesAreRejected entry).
// ---------------------------------------------------------------------------

func TestInitIsRegisteredSubcommand(t *testing.T) {
	// Running "init --help" must succeed (exit 0) and print usage.
	stdout, stderr, err := execute("init", "--help")
	if err != nil {
		t.Fatalf("init --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "init") {
		t.Fatalf("init --help output does not mention 'init':\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Happy path: scaffold + refspecs
// ---------------------------------------------------------------------------

func TestInitCreatesScaffoldAndRefspecs(t *testing.T) {
	repo := initCaptureRepo(t)
	// Add origin remote so the refspec step runs.
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("init wrote to stderr: %q", stderr)
	}

	// workflow.yaml must exist and be parseable.
	wfPath := filepath.Join(repo, ".etude", "workflow.yaml")
	assertFileContains(t, wfPath, "default")

	// Round-trip: parsed workflow must equal Default().
	content, err := os.ReadFile(wfPath)
	if err != nil {
		t.Fatalf("read workflow.yaml: %v", err)
	}
	parsed, err := workflow.ParseYAML(content)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	def := workflow.Default()
	if parsed.Name != def.Name || len(parsed.Stages) != len(def.Stages) {
		t.Fatalf("round-trip mismatch: got name=%q stages=%d, want name=%q stages=%d",
			parsed.Name, len(parsed.Stages), def.Name, len(def.Stages))
	}

	// Rubric placeholders must exist for each rubric eval stage.
	for _, s := range def.Stages {
		if s.Eval != nil && s.Eval.Method == "rubric" {
			rubricPath := filepath.Join(repo, ".etude", s.Eval.Rubric)
			// Directive F content check.
			assertFileContains(t, rubricPath, "# Rubric for "+s.Name)
			assertFileContains(t, rubricPath, "TODO: define evaluation criteria.")
		}
	}

	// Refspecs must be configured.
	fetchVal := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")
	if !strings.Contains(fetchVal, "+refs/etude/*:refs/etude/*") {
		t.Fatalf("fetch refspec not configured: %q", fetchVal)
	}
	pushVal := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push")
	if !strings.Contains(pushVal, "refs/etude/*:refs/etude/*") {
		t.Fatalf("push refspec not configured: %q", pushVal)
	}

	// Output must mention "created" lines.
	if !strings.Contains(stdout, "created") {
		t.Fatalf("init stdout did not mention 'created': %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// Idempotency: run init twice → exactly one refspec entry per key, files skipped.
// ---------------------------------------------------------------------------

func TestInitIdempotency(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("first init error: %v\nstderr: %s", err, stderr)
	}
	stdout2, stderr2, err := execute("init")
	if err != nil {
		t.Fatalf("second init error: %v\nstderr: %s", err, stderr2)
	}

	// Second run must report files as skipped.
	if !strings.Contains(stdout2, "skipped") {
		t.Fatalf("second init did not report skipped files: %q", stdout2)
	}

	// Each refspec key must have exactly one etude entry.
	fetchOut := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")
	etudeFetch := 0
	for _, line := range strings.Split(strings.TrimSpace(fetchOut), "\n") {
		if strings.Contains(line, "refs/etude") {
			etudeFetch++
		}
	}
	if etudeFetch != 1 {
		t.Fatalf("fetch refspec duplicated: found %d etude entries in %q", etudeFetch, fetchOut)
	}

	pushOut := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push")
	etudePush := 0
	for _, line := range strings.Split(strings.TrimSpace(pushOut), "\n") {
		if strings.Contains(line, "refs/etude") {
			etudePush++
		}
	}
	if etudePush != 1 {
		t.Fatalf("push refspec duplicated: found %d etude entries in %q", etudePush, pushOut)
	}
}

// ---------------------------------------------------------------------------
// --force overwrites files but does NOT touch git config.
// ---------------------------------------------------------------------------

func TestInitForceOverwritesFilesNotConfig(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	// First run: scaffold + refspecs.
	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("first init error: %v\nstderr: %s", err, stderr)
	}

	// Overwrite workflow.yaml with different content.
	wfPath := filepath.Join(repo, ".etude", "workflow.yaml")
	if err := os.WriteFile(wfPath, []byte("name: custom\n"), 0o644); err != nil {
		t.Fatalf("write custom workflow: %v", err)
	}

	// Record git config state before --force run.
	fetchBefore := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")

	stdout, stderr, err := execute("init", "--force")
	if err != nil {
		t.Fatalf("init --force error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("init --force wrote to stderr: %q", stderr)
	}

	// File must be restored to canonical content.
	content, err := os.ReadFile(wfPath)
	if err != nil {
		t.Fatalf("read workflow.yaml: %v", err)
	}
	if !strings.Contains(string(content), "default") {
		t.Fatalf("--force did not restore canonical workflow.yaml")
	}

	// Stdout must say "created".
	if !strings.Contains(stdout, "created") {
		t.Fatalf("--force stdout missing 'created': %q", stdout)
	}

	// Git config must be unchanged: exactly same fetch entries as before.
	fetchAfter := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")
	if fetchBefore != fetchAfter {
		t.Fatalf("--force modified git config: before=%q after=%q", fetchBefore, fetchAfter)
	}
}

// ---------------------------------------------------------------------------
// Not a git repository → clean error.
// ---------------------------------------------------------------------------

func TestInitNotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	_, stderr, err := execute("init")
	if err == nil {
		t.Fatal("init returned nil error in non-repo dir")
	}
	if !strings.Contains(err.Error(), "not a git repository") && !strings.Contains(stderr, "not a git repository") {
		t.Fatalf("error %q stderr %q do not mention 'not a git repository'", err, stderr)
	}
}

// A malformed --remote must be rejected BEFORE any git invocation: in a non-git
// dir the error is the validation error, not "not a git repository", proving
// validateRemoteName runs ahead of repoRoot.
func TestInitMalformedRemoteFailsBeforeGit(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	_, stderr, err := execute("init", "--remote", "bad name")
	if err == nil {
		t.Fatal("init with malformed remote in non-repo dir returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "invalid remote name") {
		t.Fatalf("expected validation error before git, got %q", combined)
	}
	if strings.Contains(combined, "not a git repository") {
		t.Fatalf("validation should precede the repo check, got %q", combined)
	}
}

// ---------------------------------------------------------------------------
// Default origin absent → skip refspecs, init still succeeds.
// ---------------------------------------------------------------------------

func TestInitNoOriginSkipsRefspecs(t *testing.T) {
	repo := initCaptureRepo(t)
	// Intentionally do NOT add origin remote.
	chdir(t, repo)

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init (no origin) returned error: %v\nstderr: %s", err, stderr)
	}

	// Files must still be created.
	wfPath := filepath.Join(repo, ".etude", "workflow.yaml")
	if _, statErr := os.Stat(wfPath); statErr != nil {
		t.Fatalf("workflow.yaml not created: %v", statErr)
	}

	// Output must note that origin was not found.
	if !strings.Contains(stdout, "not found") && !strings.Contains(stdout, "skipping") {
		t.Fatalf("stdout did not mention skipping refspecs: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// Explicit --remote pointing at a missing remote → error.
// ---------------------------------------------------------------------------

func TestInitExplicitMissingRemoteErrors(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	_, stderr, err := execute("init", "--remote", "upstream")
	if err == nil {
		t.Fatal("init with explicit missing remote returned nil error")
	}
	if !strings.Contains(err.Error(), "upstream") && !strings.Contains(stderr, "upstream") {
		t.Fatalf("error %q stderr %q do not mention remote name", err, stderr)
	}
}

// The explicit-missing-remote invariant must hold even under --force (which
// otherwise skips git-config writes): a typo'd remote should still error.
func TestInitForceExplicitMissingRemoteErrors(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	_, stderr, err := execute("init", "--force", "--remote", "upstream")
	if err == nil {
		t.Fatal("init --force with explicit missing remote returned nil error")
	}
	if !strings.Contains(err.Error(), "upstream") && !strings.Contains(stderr, "upstream") {
		t.Fatalf("error %q stderr %q do not mention remote name", err, stderr)
	}
}

// Running init from a subdirectory must resolve the repo root via
// --show-toplevel and scaffold .etude/ at the ROOT, not in the subdir.
func TestInitFromSubdirectoryScaffoldsAtRoot(t *testing.T) {
	repo := initCaptureRepo(t)
	sub := filepath.Join(repo, "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, sub)

	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("init from subdir errored: %v (stderr %q)", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, ".etude", "workflow.yaml")); err != nil {
		t.Fatalf(".etude/workflow.yaml not scaffolded at repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, ".etude")); !os.IsNotExist(err) {
		t.Fatalf(".etude must not be created in the subdir (stat err=%v)", err)
	}
}

// --force with the default origin absent must succeed silently and write NO git
// config (locks directive D against a refactor accidentally touching config).
func TestInitForceMissingDefaultOriginSucceeds(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	if _, stderr, err := execute("init", "--force"); err != nil {
		t.Fatalf("init --force with no origin errored: %v (stderr %q)", err, stderr)
	}
	// --list always exits 0 (local config exists from initCaptureRepo); assert no
	// etude refspec was written anywhere.
	if out := gitCapture(t, repo, "config", "--local", "--list"); strings.Contains(out, "refs/etude") {
		t.Fatalf("--force should not write refspec config, got %q", out)
	}
}

// A valid name with an embedded (non-leading) dash must be accepted — the dash
// guard is anchored to the prefix only.
func TestInitAcceptsEmbeddedDashRemote(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "my-origin", "https://example.com/x.git")

	if _, stderr, err := execute("init", "--remote", "my-origin"); err != nil {
		t.Fatalf("init --remote my-origin errored: %v (stderr %q)", err, stderr)
	}
	got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.my-origin.fetch")
	if !strings.Contains(got, "+refs/etude/*:refs/etude/*") {
		t.Fatalf("fetch refspec not configured on my-origin: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Directive E: malformed remote name → error before git is called.
// ---------------------------------------------------------------------------

func TestInitMalformedRemoteErrors(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	cases := []struct {
		name   string
		remote string
	}{
		{"space in name", "or igin"},
		{"tab in name", "ori\tgin"},
		{"nbsp in name", "or igin"},
		{"empty via whitespace", " "},
		{"explicit empty", ""},
		{"leading dot", ".origin"},
		{"leading slash", "/origin"},
		{"double dot", "a..b"},
		{"lock suffix", "origin.lock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := execute("init", "--remote", tc.remote)
			if err == nil {
				t.Fatalf("init --remote %q returned nil error", tc.remote)
			}
			combined := err.Error() + " " + stderr
			if !strings.Contains(combined, "invalid remote name") {
				t.Fatalf("error %q stderr %q do not mention 'invalid remote name'", err, stderr)
			}
		})
	}

	// Leading "-" must be rejected (otherwise git could treat the name as a
	// flag). Use the --remote=VALUE form so pflag does not misparse the dash.
	t.Run("leading dash", func(t *testing.T) {
		_, stderr, err := execute("init", "--remote=-origin")
		if err == nil {
			t.Fatal("init --remote=-origin returned nil error")
		}
		if !strings.Contains(err.Error()+" "+stderr, "invalid remote name") {
			t.Fatalf("error %q stderr %q do not mention 'invalid remote name'", err, stderr)
		}
	})
}

// ---------------------------------------------------------------------------
// Partial .etude/: only some files present → missing ones created, present skipped.
// ---------------------------------------------------------------------------

func TestInitPartialEtude(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Pre-create workflow.yaml but not rubric files.
	etudDir := filepath.Join(repo, ".etude")
	if err := os.MkdirAll(etudDir, 0o755); err != nil {
		t.Fatalf("mkdir .etude: %v", err)
	}
	wfPath := filepath.Join(etudDir, "workflow.yaml")
	if err := os.WriteFile(wfPath, []byte("existing\n"), 0o644); err != nil {
		t.Fatalf("write workflow.yaml: %v", err)
	}

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init (partial .etude) error: %v\nstderr: %s", err, stderr)
	}

	// workflow.yaml should be skipped (not overwritten).
	content, err := os.ReadFile(wfPath)
	if err != nil {
		t.Fatalf("read workflow.yaml: %v", err)
	}
	if string(content) != "existing\n" {
		t.Fatalf("workflow.yaml was overwritten without --force")
	}
	if !strings.Contains(stdout, "skipped") {
		t.Fatalf("stdout did not say skipped: %q", stdout)
	}

	// Rubric files should have been created.
	def := workflow.Default()
	for _, s := range def.Stages {
		if s.Eval != nil && s.Eval.Method == "rubric" {
			rubricPath := filepath.Join(etudDir, s.Eval.Rubric)
			if _, statErr := os.Stat(rubricPath); statErr != nil {
				t.Fatalf("rubric file not created: %s", rubricPath)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Directive C: .etude exists as a regular file → clear error.
// ---------------------------------------------------------------------------

func TestInitEtudeIsAFile(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Create .etude as a regular file.
	etudePath := filepath.Join(repo, ".etude")
	if err := os.WriteFile(etudePath, []byte("oops"), 0o644); err != nil {
		t.Fatalf("create .etude file: %v", err)
	}

	_, stderr, err := execute("init")
	if err == nil {
		t.Fatal("init returned nil error when .etude is a file")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, ".etude") {
		t.Fatalf("error %q stderr %q do not mention .etude", err, stderr)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertFileContains(t *testing.T, path, substr string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(content), substr) {
		t.Fatalf("file %s does not contain %q:\n%s", path, substr, string(content))
	}
}
