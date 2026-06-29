package cli

import (
	"fmt"
	"os"
	"path/filepath"
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

	// First run: all created + 2 configured (fetch + push).
	stdout, stderr, err := execute("init")
	if err != nil {
		t.Fatalf("first init failed: %v\nstderr: %s", err, stderr)
	}
	wantSummary1 := fmt.Sprintf("init: %d created, 0 skipped, 2 configured", expectedCreated)
	if !strings.Contains(stdout, wantSummary1) {
		t.Fatalf("first run summary mismatch: want %q in %q", wantSummary1, stdout)
	}

	// Second run: all skipped + 2 configured (already-configured → still in configured bucket).
	stdout2, stderr2, err := execute("init")
	if err != nil {
		t.Fatalf("second init failed: %v\nstderr: %s", err, stderr2)
	}
	wantSummary2 := fmt.Sprintf("init: 0 created, %d skipped, 2 configured", expectedCreated)
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
