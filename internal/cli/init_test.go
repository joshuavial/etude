package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/registry"
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

	// The push refspec is configured; a FETCH refspec into refs/etude/* must
	// NOT be, because it would make every local run ref prunable by a bare
	// `git fetch --prune` (etude-i19).
	fetchVal := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")
	if strings.Contains(fetchVal, ":refs/etude/") {
		t.Fatalf("init configured a fetch refspec into refs/etude/*: %q", fetchVal)
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
// Registry.yaml scaffold tests
// ---------------------------------------------------------------------------

// TestInitCreatesRegistryYAML asserts that etude init creates .etude/registry.yaml
// and that it is parseable by registry.ParseYAML.
func TestInitCreatesRegistryYAML(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("init returned error: %v\nstderr: %s", err, stderr)
	}

	regPath := filepath.Join(repo, ".etude", "registry.yaml")
	content, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("registry.yaml not created: %v", err)
	}
	if _, err := registry.ParseYAML(content); err != nil {
		t.Fatalf("registry.ParseYAML failed on scaffolded file: %v", err)
	}
}

// TestInitDryRunListsRegistryYAML asserts that --dry-run lists registry.yaml in
// its output (plan: create ... registry.yaml).
func TestInitDryRunListsRegistryYAML(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	stdout, stderr, err := execute("init", "--dry-run")
	if err != nil {
		t.Fatalf("init --dry-run returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "registry.yaml") {
		t.Fatalf("--dry-run output missing 'registry.yaml': %q", stdout)
	}
}

// TestInitForceRegeneratesRegistryYAML asserts that --force recreates registry.yaml
// even when it already exists.
func TestInitForceRegeneratesRegistryYAML(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// First run: create registry.yaml.
	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("first init error: %v\nstderr: %s", err, stderr)
	}
	regPath := filepath.Join(repo, ".etude", "registry.yaml")

	// Overwrite with garbage.
	if err := os.WriteFile(regPath, []byte("garbage\n"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	// --force must regenerate.
	if _, stderr, err := execute("init", "--force"); err != nil {
		t.Fatalf("init --force error: %v\nstderr: %s", err, stderr)
	}
	content, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry.yaml after --force: %v", err)
	}
	if _, err := registry.ParseYAML(content); err != nil {
		t.Fatalf("registry.yaml not valid after --force: %v", err)
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

	// The fetch key must have no etude entry at all, on any run.
	fetchOut := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")
	if strings.Contains(fetchOut, ":refs/etude/") {
		t.Fatalf("fetch refspec into refs/etude/* present after repeat init: %q", fetchOut)
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
// Strengthened: also asserts the skip-note is ABSENT (--force is silent on refspecs).
func TestInitForceMissingDefaultOriginSucceeds(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("init", "--force")
	if err != nil {
		t.Fatalf("init --force with no origin errored: %v (stderr %q)", err, stderr)
	}
	// --list always exits 0 (local config exists from initCaptureRepo); assert no
	// etude refspec was written anywhere.
	if out := gitCapture(t, repo, "config", "--local", "--list"); strings.Contains(out, "refs/etude") {
		t.Fatalf("--force should not write refspec config, got %q", out)
	}
	// --force must be silent on refspecs: no skip-note, no configure line.
	// Assert against specific prefixes (NOT bare "configured"/"skipped" which appear in summary).
	if strings.Contains(stdout, "not found, skipping") {
		t.Fatalf("--force should not emit skip-note, got %q", stdout)
	}
	if strings.Contains(stdout, "plan: configure") {
		t.Fatalf("--force should not emit configure plan line, got %q", stdout)
	}
	if strings.Contains(stdout, "configured remote.") {
		t.Fatalf("--force should not emit configured refspec line, got %q", stdout)
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
	got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.my-origin.push")
	if !strings.Contains(got, "refs/etude/*:refs/etude/*") {
		t.Fatalf("push refspec not configured on my-origin: %q", got)
	}
	fetchGot := gitCapture(t, repo, "config", "--local", "--get-all", "remote.my-origin.fetch")
	if strings.Contains(fetchGot, ":refs/etude/") {
		t.Fatalf("init configured a fetch refspec into refs/etude/* on my-origin: %q", fetchGot)
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
// --dry-run: writes nothing, reports plan lines and summary.
// ---------------------------------------------------------------------------

func TestInitDryRunWritesNothing(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	stdout, stderr, err := execute("init", "--dry-run")
	if err != nil {
		t.Fatalf("init --dry-run returned error: %v\nstderr: %s", err, stderr)
	}

	// No files should be written.
	if _, statErr := os.Stat(filepath.Join(repo, ".etude", "workflow.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("--dry-run must not write workflow.yaml (stat err=%v)", statErr)
	}

	// No refspecs should be configured.
	if out := gitCapture(t, repo, "config", "--local", "--list"); strings.Contains(out, "refs/etude") {
		t.Fatalf("--dry-run must not modify git config, got %q", out)
	}

	// Stdout must show plan lines and dry-run summary.
	if !strings.Contains(stdout, "plan: create") {
		t.Fatalf("--dry-run stdout missing 'plan: create': %q", stdout)
	}
	if !strings.Contains(stdout, "dry-run:") {
		t.Fatalf("--dry-run stdout missing summary 'dry-run:': %q", stdout)
	}
	if !strings.Contains(stdout, "to create") {
		t.Fatalf("--dry-run stdout missing 'to create': %q", stdout)
	}
}

// TestInitDryRunMissingRemoteReports: missing remote under --dry-run exits 0
// and reports would-skip; nothing is written.
func TestInitDryRunMissingRemoteReports(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("init", "--dry-run", "--remote", "upstream")
	if err != nil {
		t.Fatalf("init --dry-run --remote upstream should not error: %v\nstderr: %s", err, stderr)
	}

	// Nothing written.
	if _, statErr := os.Stat(filepath.Join(repo, ".etude", "workflow.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("--dry-run must not write workflow.yaml")
	}

	// Should report the would-skip note for the remote.
	if !strings.Contains(stdout, "would skip refspec") {
		t.Fatalf("--dry-run with missing remote should report would-skip, got %q", stdout)
	}
}

// TestInitExplicitMissingRemoteWritesThenErrors: non-dry-run with an explicit
// missing remote errors BUT the scaffold files are written first (write-then-error).
func TestInitExplicitMissingRemoteWritesThenErrors(t *testing.T) {
	// Non-force case.
	t.Run("non-force", func(t *testing.T) {
		repo := initCaptureRepo(t)
		chdir(t, repo)

		_, _, err := execute("init", "--remote", "upstream")
		if err == nil {
			t.Fatal("init --remote upstream should have errored")
		}

		// workflow.yaml must exist despite the error (write-then-error ordering).
		if _, statErr := os.Stat(filepath.Join(repo, ".etude", "workflow.yaml")); statErr != nil {
			t.Fatalf("workflow.yaml must exist after write-then-error, got: %v", statErr)
		}
	})

	// --force case: same invariant.
	t.Run("force", func(t *testing.T) {
		repo := initCaptureRepo(t)
		chdir(t, repo)

		_, _, err := execute("init", "--force", "--remote", "upstream")
		if err == nil {
			t.Fatal("init --force --remote upstream should have errored")
		}

		if _, statErr := os.Stat(filepath.Join(repo, ".etude", "workflow.yaml")); statErr != nil {
			t.Fatalf("workflow.yaml must exist after write-then-error, got: %v", statErr)
		}
	})
}

// TestInitDryRunExistingScaffoldSkips: dry-run in a populated repo shows skip
// lines and skip counts; nothing mutated.
func TestInitDryRunExistingScaffoldSkips(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	// Pre-populate scaffold files via a real init.
	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("first init failed: %v\nstderr: %s", err, stderr)
	}

	// Record git config state.
	configBefore := gitCapture(t, repo, "config", "--local", "--list")

	stdout, stderr, err := execute("init", "--dry-run")
	if err != nil {
		t.Fatalf("init --dry-run on populated repo returned error: %v\nstderr: %s", err, stderr)
	}

	// Must show skip lines, not create.
	if !strings.Contains(stdout, "plan: skip") {
		t.Fatalf("--dry-run on populated repo missing 'plan: skip': %q", stdout)
	}

	// Config must be unchanged.
	configAfter := gitCapture(t, repo, "config", "--local", "--list")
	if configBefore != configAfter {
		t.Fatalf("--dry-run mutated git config: before=%q after=%q", configBefore, configAfter)
	}

	// Summary must report to-skip count > 0.
	if !strings.Contains(stdout, "to skip") {
		t.Fatalf("--dry-run summary missing 'to skip': %q", stdout)
	}
}

// TestInitForceDryRun: force + dry-run + present remote → 0 to configure, nothing written.
func TestInitForceDryRun(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	stdout, stderr, err := execute("init", "--force", "--dry-run")
	if err != nil {
		t.Fatalf("init --force --dry-run returned error: %v\nstderr: %s", err, stderr)
	}

	// Nothing written.
	if _, statErr := os.Stat(filepath.Join(repo, ".etude", "workflow.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("--force --dry-run must not write files")
	}

	// No git config changes.
	if out := gitCapture(t, repo, "config", "--local", "--list"); strings.Contains(out, "refs/etude") {
		t.Fatalf("--force --dry-run must not modify git config, got %q", out)
	}

	// Summary must show 0 to configure (--force is silent on refspecs).
	if !strings.Contains(stdout, "0 to configure") {
		t.Fatalf("--force --dry-run should show '0 to configure': %q", stdout)
	}

	// No refspec-related lines (no plan: configure, no skip-note).
	if strings.Contains(stdout, "plan: configure") {
		t.Fatalf("--force --dry-run should not emit plan: configure, got %q", stdout)
	}
	if strings.Contains(stdout, "not found, skipping") {
		t.Fatalf("--force --dry-run should not emit skip-note, got %q", stdout)
	}
}

// TestInitSummaryCounts: verifies the summary counts on first and second runs.
func TestInitSummaryCounts(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
	chdir(t, repo)

	// Count how many files will be created.
	wf := workflow.Default()
	rubricCount := 0
	for _, s := range wf.Stages {
		if s.Eval != nil && s.Eval.Method == "rubric" {
			rubricCount++
		}
	}
	expectedCreated := 1 + 1 + rubricCount // workflow.yaml + registry.yaml + rubrics

	// First run: all created + 1 configured (push only — no fetch refspec).
	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("first init failed: %v\nstderr: %s", err, stderr)
	}
	wantSummary1 := fmt.Sprintf("init: %d created, 0 skipped, 1 configured", expectedCreated)
	if !strings.Contains(stdout, wantSummary1) {
		t.Fatalf("first run summary mismatch: want %q in %q", wantSummary1, stdout)
	}

	// Second run: all skipped + 1 configured (already-configured → still in configured bucket).
	stdout2, stderr2, err := execute("init")
	if err != nil {
		t.Fatalf("second init failed: %v\nstderr: %s", err, stderr2)
	}
	wantSummary2 := fmt.Sprintf("init: 0 created, %d skipped, 1 configured", expectedCreated)
	if !strings.Contains(stdout2, wantSummary2) {
		t.Fatalf("second run summary mismatch: want %q in %q", wantSummary2, stdout2)
	}
}

// TestInitDryRunForceMissingRemoteReports: --dry-run --force --remote <missing>
// must exit 0 and report the condition, writing nothing. This locks the invariant
// that dry-run NEVER errors on a missing remote, even under --force.
func TestInitDryRunForceMissingRemoteReports(t *testing.T) {
	repo := initCaptureRepo(t)
	// Intentionally do NOT add "missing" remote.
	chdir(t, repo)

	stdout, stderr, err := execute("init", "--dry-run", "--force", "--remote", "missing")
	if err != nil {
		t.Fatalf("init --dry-run --force --remote missing should not error: %v\nstderr: %s", err, stderr)
	}

	// Nothing written.
	if _, statErr := os.Stat(filepath.Join(repo, ".etude", "workflow.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("--dry-run must not write files (stat err=%v)", statErr)
	}

	// No git config changes.
	if out := gitCapture(t, repo, "config", "--local", "--list"); strings.Contains(out, "refs/etude") {
		t.Fatalf("--dry-run must not modify git config, got %q", out)
	}

	// Must report the condition (real run would error).
	if !strings.Contains(stdout, "would error") {
		t.Fatalf("--dry-run --force missing remote should report would-error condition: %q", stdout)
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

// ---------------------------------------------------------------------------
// etude-i19: a fetch refspec whose destination is refs/etude/* makes every
// local run ref a remote-tracking ref, so any `git fetch --prune` deletes every
// run ref not yet pushed. init must never register one and must remove one left
// behind by an older etude init.
// ---------------------------------------------------------------------------

// seedHazardousFetchRefspec configures the refspec older etude versions added.
func seedHazardousFetchRefspec(t *testing.T, repo, remote string) {
	t.Helper()
	gitCapture(t, repo, "config", "--local", "--add", "remote."+remote+".fetch", "+refs/etude/*:refs/etude/*")
}

func TestInitRemovesPreexistingEtudeFetchRefspec(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	seedHazardousFetchRefspec(t, repo, "origin")

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init errored: %v\nstderr: %s", err, stderr)
	}

	got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")
	if strings.Contains(got, ":refs/etude/") {
		t.Fatalf("hazardous fetch refspec survived init: %q", got)
	}
	// The remote's ordinary branch refspec must be untouched.
	if !strings.Contains(got, "refs/remotes/origin/*") {
		t.Fatalf("init removed the branch fetch refspec too: %q", got)
	}
	if !strings.Contains(stdout, "removed remote.origin.fetch") {
		t.Fatalf("init did not report the removal: %q", stdout)
	}
	// Push is still configured — pushing cannot delete a local ref.
	if push := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push"); !strings.Contains(push, "refs/etude/*:refs/etude/*") {
		t.Fatalf("push refspec not configured: %q", push)
	}
}

func TestInitForceRemovesPreexistingEtudeFetchRefspec(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	seedHazardousFetchRefspec(t, repo, "origin")

	if _, stderr, err := execute("init", "--force"); err != nil {
		t.Fatalf("init --force errored: %v\nstderr: %s", err, stderr)
	}
	got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")
	if strings.Contains(got, ":refs/etude/") {
		t.Fatalf("--force left the hazardous fetch refspec in place: %q", got)
	}
}

func TestInitDryRunPreviewsRemovalWithoutChangingConfig(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	seedHazardousFetchRefspec(t, repo, "origin")
	before := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch")

	stdout, stderr, err := execute("init", "--dry-run")
	if err != nil {
		t.Fatalf("init --dry-run errored: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "plan: remove remote.origin.fetch") {
		t.Fatalf("dry run did not preview the removal: %q", stdout)
	}
	if after := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch"); after != before {
		t.Fatalf("dry run modified git config: before=%q after=%q", before, after)
	}
}

// TestInitLeavesRunRefsSafeFromFetchPrune is the acceptance test for the whole
// bead: it drives a REAL `git fetch --prune` against a real remote and asserts a
// local run ref that has not been pushed survives it.
func TestInitLeavesRunRefsSafeFromFetchPrune(t *testing.T) {
	origin := t.TempDir()
	gitCapture(t, origin, "init", "--bare")

	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", origin)
	gitCapture(t, repo, "push", "origin", "HEAD:main")
	// A repo initialised by an older etude carries the hazardous refspec.
	seedHazardousFetchRefspec(t, repo, "origin")

	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/unpushed", head)

	// Before the fix this deletes the run ref. Prove the hazard is real first,
	// so the assertion after init cannot pass vacuously.
	gitCapture(t, repo, "fetch", "--prune", "origin")
	if out := strings.TrimSpace(gitCapture(t, repo, "for-each-ref", "--format=%(refname)", "refs/etude/runs")); out != "" {
		t.Fatalf("negative control failed: the hazardous refspec did not prune %q — this test no longer proves anything", out)
	}

	// Now run init, which must remove the refspec, and re-create the run ref.
	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("init errored: %v\nstderr: %s", err, stderr)
	}
	gitCapture(t, repo, "update-ref", "refs/etude/runs/unpushed", head)

	gitCapture(t, repo, "fetch", "--prune", "origin")
	if out := strings.TrimSpace(gitCapture(t, repo, "for-each-ref", "--format=%(refname)", "refs/etude/runs")); out != "refs/etude/runs/unpushed" {
		t.Fatalf("git fetch --prune deleted the unpushed run ref after init; refs = %q", out)
	}
}

// TestInitKeepsPushRefspecWhenRemovingFetchRefspec pins the distinction the fix
// turns on. Only the FETCH refspec is dangerous: it makes local run refs
// prunable. The PUSH refspec is what carries run refs to the remote at all, so
// removing both would be the same data loss by another route.
func TestInitKeepsPushRefspecWhenRemovingFetchRefspec(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	seedHazardousFetchRefspec(t, repo, "origin")
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", "refs/etude/*:refs/etude/*")

	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("init errored: %v\nstderr: %s", err, stderr)
	}

	if fetch := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch"); strings.Contains(fetch, ":refs/etude/") {
		t.Fatalf("fetch refspec into refs/etude/* survived: %q", fetch)
	}
	push := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push")
	if !strings.Contains(push, "refs/etude/*:refs/etude/*") {
		t.Fatalf("init removed the etude PUSH refspec; run refs could no longer be pushed: %q", push)
	}
	// Exactly one push entry — removal must not have dropped-and-re-added a dup.
	if n := strings.Count(push, "refs/etude/*:refs/etude/*"); n != 1 {
		t.Fatalf("etude push refspec count = %d, want 1: %q", n, push)
	}
}

func TestInitWarnsWhenRemoteMissing(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init errored: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, `warning: remote "origin" not found`) {
		t.Fatalf("init did not warn about the missing remote: %q\nstderr: %s", stdout, stderr)
	}
	// init warnings deliberately embed NO runnable shell command — three gate
	// rounds produced a defect in exactly that surface and nowhere else. They
	// name the condition and point at the docs; `etude doctor` owns remediation.
	if strings.Contains(stdout, "git config --local") || strings.Contains(stdout, "YOUR_REMOTE_URL") {
		t.Fatalf("warning embeds a runnable command: %q", stdout)
	}
	if !strings.Contains(stdout, "docs/init.md") {
		t.Fatalf("warning lacks the exact fix command: %q", stdout)
	}
}

func TestInitEmitsNoWarningsWhenSafe(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init errored: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stdout, "warning:") {
		t.Fatalf("init warned on a safe repo: %q\nstderr: %s", stdout, stderr)
	}
}

// A push refspec with an EMPTY source (":refs/etude/runs/foo") is git's syntax
// for DELETING that ref on the remote. It mentions the namespace but uploads
// nothing, so it must not be mistaken for "push is configured" — otherwise the
// warning is suppressed on a repo whose run refs never leave the machine.
func TestInitWarnsWhenPushRefspecDeletesInsteadOfUploading(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", ":refs/etude/runs/foo")

	stdout, stderr, err := execute("init", "--force")
	if err != nil {
		t.Fatalf("init --force errored: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "has no refs/etude/*:refs/etude/* push refspec") {
		t.Fatalf("delete-only push refspec was accepted as configured: %q", stdout)
	}
}

// Lanes share one .git/config, so two inits can race: both read the hazardous
// entry, the first unsets it, and the second's --unset-all finds nothing and
// exits 5. That is the outcome we wanted, not a failure.
func TestRemoveEtudeFetchRefspecsIsConcurrencySafe(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	seedHazardousFetchRefspec(t, repo, "origin")

	const workers = 8
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			_, err := removeEtudeFetchRefspecs(context.Background(), repo, "remote.origin.fetch")
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent removal returned error: %v", err)
		}
	}
	if got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch"); strings.Contains(got, ":refs/etude/") {
		t.Fatalf("hazardous refspec survived concurrent removal: %q", got)
	}
}

// TestInitWarningsEmbedNoRunnableCommands is the regression for the CUT: init
// warnings must not contain a shell command at all. Three consecutive gate
// rounds each found a different defect in embedded remediation strings — a
// placeholder URL git accepts, a preview that dropped the --remote selection,
// and a nested-single-quote command that will not parse. Removing the surface
// removes the class; `etude doctor` (etude-ldf) owns remediation.
func TestInitWarningsEmbedNoRunnableCommands(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	gitCapture(t, repo, "remote", "add", "backup", "https://example.com/y.git")
	seedHazardousFetchRefspec(t, repo, "backup")

	for _, args := range [][]string{
		{"init"},
		{"init", "--dry-run", "--remote", "backup"},
		{"init", "--force", "--remote", "backup"},
	} {
		stdout, stderr, err := execute(args...)
		if err != nil {
			t.Fatalf("%v errored: %v\nstderr: %s", args, err, stderr)
		}
		for _, line := range strings.Split(stdout, "\n") {
			if !strings.HasPrefix(line, "warning:") {
				continue
			}
			for _, banned := range []string{"git config", "git remote add", "--unset-all", "--replace-all", "YOUR_REMOTE_URL"} {
				if strings.Contains(line, banned) {
					t.Errorf("%v warning embeds a runnable command fragment %q: %s", args, banned, line)
				}
			}
		}
	}
}

func TestParseRefspec(t *testing.T) {
	cases := []struct {
		in       string
		force    bool
		src, dst string
	}{
		{"+refs/etude/*:refs/etude/*", true, "refs/etude/*", "refs/etude/*"},
		{"refs/etude/*:refs/etude/*", false, "refs/etude/*", "refs/etude/*"},
		{"+refs/etude/*", true, "refs/etude/*", ""}, // no destination recorded
		{":refs/etude/runs/foo", false, "", "refs/etude/runs/foo"},
	}
	for _, tc := range cases {
		got := parseRefspec(tc.in)
		if got.force != tc.force || got.src != tc.src || got.dst != tc.dst {
			t.Errorf("parseRefspec(%q) = %+v, want force=%v src=%q dst=%q", tc.in, got, tc.force, tc.src, tc.dst)
		}
	}
}

func TestRegexpQuoteMetaEscapesRefspecs(t *testing.T) {
	// The value-pattern handed to git config is a POSIX ERE, and refspecs are
	// full of metacharacters. regexp.QuoteMeta escapes the same set POSIX ERE
	// treats as special, so a backslash-escaped punctuation character is literal
	// in both. Pinned because the whole removal targets an anchored literal.
	got := regexp.QuoteMeta("+refs/etude/*:refs/etude/*")
	want := `\+refs/etude/\*:refs/etude/\*`
	if got != want {
		t.Fatalf("regexp.QuoteMeta = %q, want %q", got, want)
	}
}

// etudeOwnedFetchRefspec decides what init may remove. It is parsed rather than
// substring-matched because the same mapping has several spellings; a bare
// "+refs/etude/*" has no colon, so a ":refs/etude/" substring test misses it.
func TestEtudeOwnedFetchRefspec(t *testing.T) {
	cases := []struct {
		refspec string
		owned   bool
	}{
		{"+refs/etude/*:refs/etude/*", true}, // the canonical hazard
		{"refs/etude/*:refs/etude/*", true},  // unforced variant
		// No destination: git fetches to FETCH_HEAD and updates no local ref, so
		// nothing is prunable and init must NOT delete it. Reading it as
		// "dst = src" would destroy harmless user configuration.
		{"+refs/etude/*", false},
		{"refs/etude/runs/foo", false},
		{"+refs/heads/*:refs/etude/runs/x", true},      // single etude destination
		{"+refs/heads/*:refs/remotes/origin/*", false}, // ordinary, harmless
		{"+refs/tags/*:refs/tags/*", false},
		{"+refs/*:refs/*", false}, // broader than the namespace: doctor's call, not init's
	}
	for _, tc := range cases {
		if got := etudeOwnedFetchRefspec(parseRefspec(tc.refspec)); got != tc.owned {
			t.Errorf("etudeOwnedFetchRefspec(%q) = %v, want %v", tc.refspec, got, tc.owned)
		}
	}
}

// The refspec phase makes TWO durable config writes, and the removal test only
// covered the first. addRefspecIfAbsent reads-then-writes, so two concurrent
// inits could both see "absent" and both add, leaving DUPLICATE push entries.
// It uses --replace-all with a pattern matching exactly this value, which
// collapses matching lines to one and adds when none match, so racing callers
// converge on exactly one entry.
func TestAddRefspecIfAbsentConvergesUnderConcurrency(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")

	const workers = 8
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			_, err := addRefspecIfAbsent(context.Background(), repo, "remote.origin.push", canonicalPushRefspec)
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent addRefspecIfAbsent returned error: %v", err)
		}
	}

	out := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push")
	if n := strings.Count(out, canonicalPushRefspec); n != 1 {
		t.Fatalf("push refspec entry count = %d, want exactly 1 after concurrent adds:\n%s", n, out)
	}
}

// A dry-run preview must name the SAME remote it was pointed at, or the operator
// runs a bare `etude init` that repairs origin and leaves this remote exposed.
func TestInitDryRunRemediationCarriesRemoteSelection(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	gitCapture(t, repo, "remote", "add", "backup", "https://example.com/y.git")
	seedHazardousFetchRefspec(t, repo, "backup")

	stdout, stderr, err := execute("init", "--dry-run", "--remote", "backup")
	if err != nil {
		t.Fatalf("init --dry-run --remote backup errored: %v\nstderr: %s", err, stderr)
	}
	// The preview must still identify WHICH remote it is previewing, or the
	// operator repairs origin and leaves this one exposed. It says so in prose
	// rather than as a pasteable command.
	if !strings.Contains(stdout, `"backup"`) {
		t.Fatalf("dry-run preview does not name the selected remote: %q", stdout)
	}
	if strings.Contains(stdout, "'etude init --remote 'backup''") {
		t.Fatalf("nested-quote command regression: %q", stdout)
	}
}

// An older version of addRefspecIfAbsent used a racy read-then-add, so a repo
// can already carry DUPLICATE canonical push entries. Returning on the first
// match would report "already configured" and preserve the duplicate forever;
// falling through to --replace-all collapses it, making the phase self-healing.
func TestAddRefspecIfAbsentCollapsesPreexistingDuplicates(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", canonicalPushRefspec)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", canonicalPushRefspec)
	if n := strings.Count(gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push"), canonicalPushRefspec); n != 2 {
		t.Fatalf("precondition: wanted 2 duplicate entries, got %d", n)
	}

	if _, err := addRefspecIfAbsent(context.Background(), repo, "remote.origin.push", canonicalPushRefspec); err != nil {
		t.Fatalf("addRefspecIfAbsent returned error: %v", err)
	}

	out := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push")
	if n := strings.Count(out, canonicalPushRefspec); n != 1 {
		t.Fatalf("duplicates not collapsed: count = %d\n%s", n, out)
	}
}

// init configures ONE remote, so a hazardous fetch refspec on a sibling remote
// survives it — and `git fetch --prune <that remote>` deletes unpushed run refs
// just the same. Reporting only the target remote would leave a real exposure
// silent, which is the failure mode the safety phase exists to prevent.
// Warn only: --remote named one remote, so another's config is not edited.
func TestInitWarnsAboutHazardousRefspecOnOtherRemotes(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	gitCapture(t, repo, "remote", "add", "backup", "https://example.com/y.git")
	seedHazardousFetchRefspec(t, repo, "backup")

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init errored: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, `remote "backup" also has a fetch refspec`) {
		t.Fatalf("init did not warn about the sibling remote: %q", stdout)
	}
	if !strings.Contains(stdout, `--remote "backup"`) {
		t.Fatalf("warning does not name the remote to re-run against: %q", stdout)
	}
	// Warn only — the sibling's config must be untouched.
	got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.backup.fetch")
	if !strings.Contains(got, "+refs/etude/*:refs/etude/*") {
		t.Fatalf("init edited a remote it was not pointed at: %q", got)
	}
	// And it must still carry no runnable command.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "warning:") && strings.Contains(line, "git config") {
			t.Errorf("warning embeds a runnable command: %s", line)
		}
	}
}

// The most exposed configuration is a MISSING target remote plus a hazardous
// sibling: an early return on "remote not found" made that the one case that
// reported nothing at all, while `git fetch --prune backup` would still delete
// unpushed run refs.
func TestInitWarnsAboutOtherRemotesEvenWhenTargetRemoteMissing(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	// No origin. Only a sibling remote, carrying the hazard.
	gitCapture(t, repo, "remote", "add", "backup", "https://example.com/y.git")
	seedHazardousFetchRefspec(t, repo, "backup")

	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("init errored: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, `remote "origin" not found`) {
		t.Fatalf("missing-remote warning absent: %q", stdout)
	}
	if !strings.Contains(stdout, `remote "backup" also has a fetch refspec`) {
		t.Fatalf("sibling-remote exposure went unreported when the target remote was missing: %q", stdout)
	}
	if got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.backup.fetch"); !strings.Contains(got, "+refs/etude/*:refs/etude/*") {
		t.Fatalf("init edited a remote it was not pointed at: %q", got)
	}
}

// --force removes a hazardous fetch refspec (a data-loss setting is never left
// in place) but is silent on the PUSH refspec by long-standing design. So a
// --force-only operator stays without the push refspec — which is reported on
// every run, because the safety phase ignores --force, and repaired by one
// non-force init. Pinned because a gate seat correctly caught the declaration
// claiming any re-run repairs it.
func TestInitForceReportsButDoesNotAddPushRefspec(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	gitCapture(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	seedHazardousFetchRefspec(t, repo, "origin")

	stdout, stderr, err := execute("init", "--force")
	if err != nil {
		t.Fatalf("init --force errored: %v\nstderr: %s", err, stderr)
	}
	// The hazard is removed even under --force.
	if got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.fetch"); strings.Contains(got, ":refs/etude/") {
		t.Fatalf("--force left the hazardous fetch refspec: %q", got)
	}
	// The push refspec is NOT added under --force, and that is reported. git
	// exits 1 when the key is absent entirely, which is the expected state here,
	// so read it tolerantly rather than through the fail-on-error helper.
	pushOut, _ := exec.Command("git", "-C", repo, "config", "--local", "--get-all", "remote.origin.push").Output()
	if strings.Contains(string(pushOut), canonicalPushRefspec) {
		t.Fatalf("--force added the push refspec, contrary to its documented silence: %q", pushOut)
	}
	if !strings.Contains(stdout, "has no refs/etude/*:refs/etude/* push refspec") {
		t.Fatalf("--force did not report the missing push refspec: %q", stdout)
	}

	// One non-force run repairs it.
	if _, stderr, err := execute("init"); err != nil {
		t.Fatalf("non-force init errored: %v\nstderr: %s", err, stderr)
	}
	if got := gitCapture(t, repo, "config", "--local", "--get-all", "remote.origin.push"); !strings.Contains(got, canonicalPushRefspec) {
		t.Fatalf("non-force init did not repair the push refspec: %q", got)
	}
}
