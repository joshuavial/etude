package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/retro"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// readRetroManifest reads and parses manifest.json from refs/etude/retros/<retroID>.
func readRetroManifest(t *testing.T, repo, retroID string) runmanifest.Manifest {
	t.Helper()
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/retros/"+retroID, "manifest.json")
	if err != nil {
		t.Fatalf("ReadFile retro manifest returned error: %v", err)
	}
	manifest, err := runmanifest.ParseJSON(content)
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	return manifest
}

// TestRetroCaptureWritesRefAndManifest is the main happy-path test.
func TestRetroCaptureWritesRefAndManifest(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\nSome content.\n")
	chdir(t, repo)

	stdout, stderr, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--subject-run", "r2",
		"--bead", "etude-q87",
		"--trigger", "manual",
		"--decision", "informational",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "captured ") {
		t.Fatalf("stdout missing 'captured': %q", stdout)
	}
	if !strings.Contains(stdout, "ref refs/etude/retros/") {
		t.Fatalf("stdout missing retros ref: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}

	// Extract the retroID from the ref line.
	var retroID string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "ref refs/etude/retros/") {
			retroID = strings.TrimPrefix(line, "ref refs/etude/retros/")
			break
		}
	}
	if retroID == "" {
		t.Fatalf("could not extract retro id from stdout: %q", stdout)
	}

	manifest := readRetroManifest(t, repo, retroID)

	// Workflow and version.
	if manifest.Workflow != "retro" {
		t.Errorf("manifest.Workflow = %q, want retro", manifest.Workflow)
	}
	if manifest.WorkflowVersion != "retro-v1" {
		t.Errorf("manifest.WorkflowVersion = %q, want retro-v1", manifest.WorkflowVersion)
	}

	// Exactly one stage named "retro".
	if len(manifest.Stages) != 1 {
		t.Fatalf("len(manifest.Stages) = %d, want 1", len(manifest.Stages))
	}
	if manifest.Stages[0].Name != "retro" {
		t.Errorf("stage name = %q, want retro", manifest.Stages[0].Name)
	}

	// Output artifact has role retro.
	stage := manifest.Stages[0]
	if stage.Output.Role != "retro" {
		t.Errorf("stage.Output.Role = %q, want retro", stage.Output.Role)
	}
	if stage.Output.MediaType != "text/markdown; charset=utf-8" {
		t.Errorf("stage.Output.MediaType = %q, want text/markdown; charset=utf-8", stage.Output.MediaType)
	}

	// Indexed subject_run and bead Refs.
	if manifest.Refs["subject_run.1"] != "r1" {
		t.Errorf("Refs[subject_run.1] = %q, want r1", manifest.Refs["subject_run.1"])
	}
	if manifest.Refs["subject_run.2"] != "r2" {
		t.Errorf("Refs[subject_run.2] = %q, want r2", manifest.Refs["subject_run.2"])
	}
	if manifest.Refs["bead.1"] != "etude-q87" {
		t.Errorf("Refs[bead.1] = %q, want etude-q87", manifest.Refs["bead.1"])
	}
	if manifest.Refs["decision"] != "informational" {
		t.Errorf("Refs[decision] = %q, want informational", manifest.Refs["decision"])
	}
	if manifest.Refs["trigger"] != "manual" {
		t.Errorf("Refs[trigger] = %q, want manual", manifest.Refs["trigger"])
	}
	if manifest.Refs["scope"] != "cohort" {
		t.Errorf("Refs[scope] = %q, want cohort", manifest.Refs["scope"])
	}

	// Verify the body artifact is readable.
	bodyContent, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/retros/"+retroID, stage.Output.Path)
	if err != nil {
		t.Fatalf("ReadFile body artifact: %v", err)
	}
	if string(bodyContent) != "# Retro\nSome content.\n" {
		t.Errorf("body content = %q", bodyContent)
	}
}

// TestRetroCaptureStdinFile verifies --file - reads from stdin.
func TestRetroCaptureStdinFile(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Write body to a temp file and pipe it via stdin redirection.
	bodyFile := repo + "/body.md"
	if err := os.WriteFile(bodyFile, []byte("# Stdin retro\n"), 0o644); err != nil {
		t.Fatalf("write body: %v", err)
	}

	// Use a pipe to simulate stdin.
	oldStdin := os.Stdin
	f, err := os.Open(bodyFile)
	if err != nil {
		t.Fatalf("open stdin file: %v", err)
	}
	os.Stdin = f
	t.Cleanup(func() {
		f.Close()
		os.Stdin = oldStdin
	})

	stdout, stderr, err := execute("retro", "capture", "run",
		"--file", "-",
		"--subject-run", "r1",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture stdin returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "ref refs/etude/retros/") {
		t.Fatalf("stdout missing retros ref: %q", stdout)
	}
}

// TestRetroCaptureWorkflowScopeNoSubjectRequired verifies scope=workflow needs no --subject-run.
func TestRetroCaptureWorkflowScopeNoSubjectRequired(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Workflow retro\n")
	chdir(t, repo)

	stdout, stderr, err := execute("retro", "capture", "workflow",
		"--file", "retro.md",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture workflow returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "ref refs/etude/retros/") {
		t.Fatalf("stdout missing retros ref: %q", stdout)
	}
}

// TestRetroCaptureValidationErrors covers scope, file, and subject-run errors.
func TestRetroCaptureValidationErrors(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\n")
	chdir(t, repo)

	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing scope arg",
			args:    []string{"retro", "capture"},
			wantErr: "accepts 1 arg",
		},
		{
			name:    "invalid scope",
			args:    []string{"retro", "capture", "badscope", "--file", "retro.md", "--subject-run", "r1", "--skill-id", "retro"},
			wantErr: "invalid scope",
		},
		{
			name:    "missing --file",
			args:    []string{"retro", "capture", "cohort", "--subject-run", "r1", "--skill-id", "retro"},
			wantErr: "--file is required",
		},
		{
			name:    "missing --subject-run for non-workflow scope",
			args:    []string{"retro", "capture", "cohort", "--file", "retro.md", "--skill-id", "retro"},
			wantErr: "--subject-run is required",
		},
		{
			name:    "unreadable --file",
			args:    []string{"retro", "capture", "cohort", "--file", "noexist.md", "--subject-run", "r1", "--skill-id", "retro"},
			wantErr: "read retro file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := execute(tc.args...)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			combined := err.Error() + " " + stderr
			if !strings.Contains(combined, tc.wantErr) {
				t.Errorf("error = %q, want containing %q", combined, tc.wantErr)
			}
		})
	}
}

// TestRetroCapturePassthroughRefs verifies --ref key=value passthrough.
func TestRetroCapturePassthroughRefs(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\n")
	chdir(t, repo)

	stdout, stderr, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--skill-id", "retro",
		"--ref", "custom_key=custom_value",
	)
	if err != nil {
		t.Fatalf("retro capture returned error: %v\nstderr: %s", err, stderr)
	}

	var retroID string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "ref refs/etude/retros/") {
			retroID = strings.TrimPrefix(line, "ref refs/etude/retros/")
			break
		}
	}

	manifest := readRetroManifest(t, repo, retroID)
	if manifest.Refs["custom_key"] != "custom_value" {
		t.Errorf("Refs[custom_key] = %q, want custom_value", manifest.Refs["custom_key"])
	}
}

// TestRetroCaptureReservedRefKeys verifies that --ref keys colliding with
// reserved/generated keys are rejected.
func TestRetroCaptureReservedRefKeys(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\n")
	chdir(t, repo)

	cases := []struct {
		name   string
		refArg string
	}{
		{"reserved exact: scope", "scope=workflow"},
		{"reserved exact: trigger", "trigger=custom"},
		{"reserved exact: decision", "decision=accepted"},
		{"reserved exact: supersedes", "supersedes=retro-x"},
		{"reserved prefix: subject_run.", "subject_run.1=bad/value"},
		{"reserved prefix: bead.", "bead.1=x"},
		{"reserved prefix: gate.", "gate.1=x"},
		{"reserved prefix: bench.", "bench.1=x"},
		{"reserved prefix: eval.", "eval.1=x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := execute("retro", "capture", "cohort",
				"--file", "retro.md",
				"--subject-run", "r1",
				"--skill-id", "retro",
				"--ref", tc.refArg,
			)
			if err == nil {
				t.Fatalf("expected error for reserved --ref %q, got nil", tc.refArg)
			}
			if !strings.Contains(err.Error(), "is reserved") {
				t.Errorf("error = %q, want containing 'is reserved'", err.Error())
			}
		})
	}

	// Benign custom key must still work and appear in the manifest.
	t.Run("benign ref note=foo succeeds", func(t *testing.T) {
		stdout, stderr, err := execute("retro", "capture", "cohort",
			"--file", "retro.md",
			"--subject-run", "r1",
			"--skill-id", "retro",
			"--ref", "note=foo",
		)
		if err != nil {
			t.Fatalf("retro capture returned error: %v\nstderr: %s", err, stderr)
		}
		var retroID string
		for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
			if strings.HasPrefix(line, "ref refs/etude/retros/") {
				retroID = strings.TrimPrefix(line, "ref refs/etude/retros/")
				break
			}
		}
		if retroID == "" {
			t.Fatalf("could not extract retro id from stdout: %q", stdout)
		}
		manifest := readRetroManifest(t, repo, retroID)
		if manifest.Refs["note"] != "foo" {
			t.Errorf("Refs[note] = %q, want foo", manifest.Refs["note"])
		}
		// Generated keys are still present.
		if manifest.Refs["subject_run.1"] != "r1" {
			t.Errorf("Refs[subject_run.1] = %q, want r1", manifest.Refs["subject_run.1"])
		}
		if manifest.Refs["scope"] != "cohort" {
			t.Errorf("Refs[scope] = %q, want cohort", manifest.Refs["scope"])
		}
	})
}

// ---------------------------------------------------------------------------
// retro list
// ---------------------------------------------------------------------------

// seedRetroWithBadManifest writes an unparseable manifest under
// refs/etude/retros/<retroID> for use in corrupt-manifest tests.
func seedRetroWithBadManifest(t *testing.T, repo, retroID string) {
	t.Helper()
	store := refstore.New(repo)
	bad := []byte(`not valid json`)
	if _, err := store.WriteCommit(context.Background(), "refs/etude/retros/"+retroID, map[string][]byte{"manifest.json": bad}, refstore.WriteOptions{}); err != nil {
		t.Fatalf("WriteCommit bad retro manifest returned error: %v", err)
	}
}

// extractRetroID parses the retro id from a "ref refs/etude/retros/<id>" line
// in capture stdout.
func extractRetroID(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "ref refs/etude/retros/") {
			return strings.TrimPrefix(line, "ref refs/etude/retros/")
		}
	}
	t.Fatalf("could not extract retro id from stdout: %q", stdout)
	return ""
}

func TestRetroListZeroRetros(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("retro", "list")
	if err != nil {
		t.Fatalf("retro list returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "no retros found") {
		t.Fatalf("expected 'no retros found', got: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRetroListOneRetro(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\nContent.\n")
	chdir(t, repo)

	_, stderr, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("retro", "list")
	if err != nil {
		t.Fatalf("retro list returned error: %v\nstderr: %s", err, stderr)
	}

	// Header row must contain all column names.
	for _, col := range []string{"RETRO ID", "SCOPE", "TRIGGER", "SUBJECTS", "CREATED"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("expected column %q in header:\n%s", col, stdout)
		}
	}

	// Must have a data row.
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines (header + data), got %d:\n%s", len(lines), stdout)
	}

	// The data row should contain the scope.
	if !strings.Contains(stdout, "cohort") {
		t.Fatalf("expected 'cohort' in output:\n%s", stdout)
	}

	// Created must be parseable as RFC3339.
	var foundTime bool
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			// Last field should be the RFC3339 timestamp.
			lastField := fields[len(fields)-1]
			if _, parseErr := time.Parse(time.RFC3339, lastField); parseErr == nil {
				if strings.HasSuffix(lastField, "Z") {
					foundTime = true
				}
			}
		}
	}
	if !foundTime {
		t.Fatalf("no valid RFC3339Z timestamp found in data rows:\n%s", stdout)
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRetroListMultipleRetrosDeterministicOrder(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\n")
	chdir(t, repo)

	// Capture retro-b first, then retro-a — output must be retro-a before retro-b (lexical).
	// We use fixed retro IDs by relying on the allocator; instead, capture two and
	// check their relative order in the list output. Since IDs include timestamps,
	// we just capture two and check both appear and the list is non-empty.
	_, stderr, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("capture r1 returned error: %v\nstderr: %s", err, stderr)
	}
	_, stderr, err = execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r2",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("capture r2 returned error: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := execute("retro", "list")
	if err != nil {
		t.Fatalf("retro list returned error: %v\nstderr: %s", err, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	// At least header + 2 data rows.
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d:\n%s", len(lines), stdout)
	}

	// All data rows must appear in sorted order (lexical by retro id = first field).
	dataLines := lines[1:]
	for i := 1; i < len(dataLines); i++ {
		prevID := strings.Fields(dataLines[i-1])[0]
		currID := strings.Fields(dataLines[i])[0]
		if prevID > currID {
			t.Fatalf("expected lexical order but %q > %q:\n%s", prevID, currID, stdout)
		}
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRetroListCorruptManifest(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\n")
	chdir(t, repo)

	// Seed a good retro so the list is non-empty.
	_, stderr, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture returned error: %v\nstderr: %s", err, stderr)
	}

	// Write a corrupt manifest under a separate retro ref.
	seedRetroWithBadManifest(t, repo, "bad-retro")

	stdout, stderr, err := execute("retro", "list")
	if err == nil {
		t.Fatal("retro list with corrupt manifest returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "bad-retro") {
		t.Fatalf("error does not name the offending retro id 'bad-retro': %q", combined)
	}
	// tabwriter only flushes on success, so a mid-list failure must leave stdout empty.
	if stdout != "" {
		t.Fatalf("expected empty stdout on corrupt-manifest failure, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// retro show
// ---------------------------------------------------------------------------

func TestRetroShowExistingRetro(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\nSome findings.\n")
	chdir(t, repo)

	captureStdout, stderr, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--bead", "etude-q87",
		"--trigger", "manual",
		"--decision", "informational",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture returned error: %v\nstderr: %s", err, stderr)
	}
	retroID := extractRetroID(t, captureStdout)

	stdout, stderr, err := execute("retro", "show", retroID)
	if err != nil {
		t.Fatalf("retro show returned error: %v\nstderr: %s", err, stderr)
	}

	// Header metadata.
	if !strings.Contains(stdout, retroID) {
		t.Fatalf("expected retro id %q in output:\n%s", retroID, stdout)
	}
	if !strings.Contains(stdout, "scope:") || !strings.Contains(stdout, "cohort") {
		t.Fatalf("expected 'scope: cohort' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "trigger:") || !strings.Contains(stdout, "manual") {
		t.Fatalf("expected 'trigger: manual' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "decision:") || !strings.Contains(stdout, "informational") {
		t.Fatalf("expected 'decision: informational' in output:\n%s", stdout)
	}

	// Subjects.
	if !strings.Contains(stdout, "r1") {
		t.Fatalf("expected subject 'r1' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "etude-q87") {
		t.Fatalf("expected subject 'etude-q87' in output:\n%s", stdout)
	}

	// Inline body.
	if !strings.Contains(stdout, "--- retro body ---") {
		t.Fatalf("expected '--- retro body ---' divider in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "# Retro") {
		t.Fatalf("expected inline body text '# Retro' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Some findings.") {
		t.Fatalf("expected inline body text 'Some findings.' in output:\n%s", stdout)
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

func TestRetroShowUnknownRetroID(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("retro", "show", "no-such-retro")
	if err == nil {
		t.Fatal("retro show unknown retro returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "not found") {
		t.Fatalf("error does not mention 'not found': %q", combined)
	}
	if !strings.Contains(combined, "no-such-retro") {
		t.Fatalf("error does not mention the retro id 'no-such-retro': %q", combined)
	}
	if stdout != "" {
		t.Fatalf("stdout not empty: %q", stdout)
	}
}

func TestRetroShowInvalidRetroIDBeforeGit(t *testing.T) {
	// Validation must happen before any git call — prove it by running in a non-repo dir.
	dir := t.TempDir()
	chdir(t, dir)

	cases := []struct {
		name string
		id   string
		want string
	}{
		{"slash in id", "bad/id", "invalid retro id"},
		{"double dot", "..", "invalid retro id"},
		{"lock suffix", "x.lock", "invalid retro id"},
		{"leading dot", ".hidden", "invalid retro id"},
		{"trailing dot", "myretro.", "invalid retro id"},
		{"all dots", "...", "invalid retro id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := execute("retro", "show", tc.id)
			if err == nil {
				t.Fatalf("retro show %q returned nil error", tc.id)
			}
			combined := err.Error() + " " + stderr
			if !strings.Contains(combined, tc.want) {
				t.Fatalf("error %q does not contain %q", combined, tc.want)
			}
			if stdout != "" {
				t.Fatalf("stdout not empty: %q", stdout)
			}
			// Must NOT say "not a git repository" — that would mean we hit git before validation.
			if strings.Contains(combined, "not a git repository") {
				t.Fatalf("validation ran after git check: %q", combined)
			}
		})
	}
}

func TestRetroShowWorkflowScopeNoSubjects(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Workflow retro\nNo subjects here.\n")
	chdir(t, repo)

	captureStdout, stderr, err := execute("retro", "capture", "workflow",
		"--file", "retro.md",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture workflow returned error: %v\nstderr: %s", err, stderr)
	}
	retroID := extractRetroID(t, captureStdout)

	stdout, stderr, err := execute("retro", "show", retroID)
	if err != nil {
		t.Fatalf("retro show workflow-scope returned error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "scope:") || !strings.Contains(stdout, "workflow") {
		t.Fatalf("expected 'scope: workflow' in output:\n%s", stdout)
	}
	// No subject line should appear (workflow scope has no subjects).
	if strings.Contains(stdout, "subject:") {
		t.Fatalf("did not expect 'subject:' line for workflow-scope retro:\n%s", stdout)
	}
	// Body must still render.
	if !strings.Contains(stdout, "--- retro body ---") {
		t.Fatalf("expected body divider in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "# Workflow retro") {
		t.Fatalf("expected body text in output:\n%s", stdout)
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

// TestRetroShowFlatMetadata verifies that retro show renders all persisted flat
// metadata keys — gate.1, bench.1, eval.1, and arbitrary custom --ref keys —
// that are not in the known set (scope/trigger/decision/supersedes) and not
// subject keys. Output must be deterministically ordered (lexical).
func TestRetroShowFlatMetadata(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Gate retro\nFindings.\n")
	chdir(t, repo)

	captureStdout, stderr, err := execute("retro", "capture", "gate",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--gate", "plan@1",
		"--bench", "bench-abc",
		"--eval", "eval-xyz",
		"--ref", "note=something",
		"--skill-id", "retro",
	)
	if err != nil {
		t.Fatalf("retro capture returned error: %v\nstderr: %s", err, stderr)
	}
	retroID := extractRetroID(t, captureStdout)

	stdout, stderr, err := execute("retro", "show", retroID)
	if err != nil {
		t.Fatalf("retro show returned error: %v\nstderr: %s", err, stderr)
	}

	// All four extra keys must appear with their values.
	cases := []struct{ key, value string }{
		{"bench.1", "bench-abc"},
		{"eval.1", "eval-xyz"},
		{"gate.1", "plan@1"},
		{"note", "something"},
	}
	for _, tc := range cases {
		want := tc.key + ": " + tc.value
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in retro show output:\n%s", want, stdout)
		}
	}

	// The metadata section header must be present.
	if !strings.Contains(stdout, "metadata:") {
		t.Errorf("expected 'metadata:' header in retro show output:\n%s", stdout)
	}

	// Deterministic order: bench.1 < eval.1 < gate.1 < note (lexical).
	benchPos := strings.Index(stdout, "bench.1:")
	evalPos := strings.Index(stdout, "eval.1:")
	gatePos := strings.Index(stdout, "gate.1:")
	notePos := strings.Index(stdout, "note:")
	if benchPos < 0 || evalPos < 0 || gatePos < 0 || notePos < 0 {
		t.Fatalf("one or more metadata keys missing from output:\n%s", stdout)
	}
	if !(benchPos < evalPos && evalPos < gatePos && gatePos < notePos) {
		t.Errorf("metadata keys not in lexical order (bench.1, eval.1, gate.1, note) in output:\n%s", stdout)
	}

	// Known flat keys must still be rendered correctly (not duplicated).
	if !strings.Contains(stdout, "scope:") {
		t.Errorf("expected 'scope:' in output:\n%s", stdout)
	}
	if strings.Count(stdout, "scope:") > 1 {
		t.Errorf("'scope:' appears more than once — duplication bug:\n%s", stdout)
	}

	// Subject must still appear.
	if !strings.Contains(stdout, "r1") {
		t.Errorf("expected subject 'r1' in output:\n%s", stdout)
	}

	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// retro generate
// ---------------------------------------------------------------------------

// executeRetroGenerate runs 'retro generate' with an injected StubGenerator.
// This mirrors executeReplay's pattern for test-double injection.
func executeRetroGenerate(gen retro.Generator, args ...string) (string, string, error) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	r := &retroGenerateRunner{
		generator: gen,
		now:       time.Now,
		store:     refstore.New(""),
		stdout:    &out,
	}
	cmd := buildRetroGenerateCommand(&out, &errOut, r)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}
	return out.String(), errOut.String(), err
}

// seedRunForGenerate creates a real run ref (via capture) that generate can
// use as a --subject-run. Returns the run id.
func seedRunForGenerate(t *testing.T, repo, runID string) {
	t.Helper()
	writeFile(t, repo, runID+"-output.md", "# Subject output\n")
	chdir(t, repo)
	_, stderr, err := execute("capture", "plan",
		"--run", runID,
		"--output", "output="+runID+"-output.md",
	)
	if err != nil {
		t.Fatalf("capture setup for generate returned error: %v\nstderr: %s", err, stderr)
	}
}

// TestRetroGenerateHappyPath verifies that generate writes a retro ref with
// the stub body, produced_via=generate, and the correct scope.
func TestRetroGenerateHappyPath(t *testing.T) {
	repo := initCaptureRepo(t)
	seedRunForGenerate(t, repo, "gen-run-1")
	chdir(t, repo)

	wantBody := []byte("# Generated Retro\nFindings from generator.\n")
	stub := &retro.StubGenerator{CannedBody: wantBody}

	stdout, stderr, err := executeRetroGenerate(stub,
		"cohort",
		"--subject-run", "gen-run-1",
	)
	if err != nil {
		t.Fatalf("retro generate returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "generated ") {
		t.Fatalf("stdout missing 'generated': %q", stdout)
	}
	if !strings.Contains(stdout, "ref refs/etude/retros/") {
		t.Fatalf("stdout missing retros ref: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr not empty: %q", stderr)
	}

	// Extract retro id and verify manifest.
	retroID := extractRetroID(t, stdout)
	manifest := readRetroManifest(t, repo, retroID)

	if manifest.Refs["scope"] != "cohort" {
		t.Errorf("Refs[scope] = %q, want cohort", manifest.Refs["scope"])
	}
	if manifest.Refs["produced_via"] != "generate" {
		t.Errorf("Refs[produced_via] = %q, want generate", manifest.Refs["produced_via"])
	}
	if manifest.Refs["subject_run.1"] != "gen-run-1" {
		t.Errorf("Refs[subject_run.1] = %q, want gen-run-1", manifest.Refs["subject_run.1"])
	}

	// Verify the body artifact content.
	if len(manifest.Stages) != 1 {
		t.Fatalf("len(manifest.Stages) = %d, want 1", len(manifest.Stages))
	}
	bodyBytes, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/retros/"+retroID, manifest.Stages[0].Output.Path)
	if err != nil {
		t.Fatalf("ReadFile body: %v", err)
	}
	if string(bodyBytes) != string(wantBody) {
		t.Errorf("body = %q, want %q", bodyBytes, wantBody)
	}
}

// TestRetroGenerateMultiSubject verifies two --subject-run args are accepted
// and produce the correct indexed subject_run.N refs.
func TestRetroGenerateMultiSubject(t *testing.T) {
	repo := initCaptureRepo(t)
	seedRunForGenerate(t, repo, "gen-run-a")
	seedRunForGenerate(t, repo, "gen-run-b")
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("multi body")}

	stdout, stderr, err := executeRetroGenerate(stub,
		"cohort",
		"--subject-run", "gen-run-a",
		"--subject-run", "gen-run-b",
	)
	if err != nil {
		t.Fatalf("retro generate multi-subject returned error: %v\nstderr: %s", err, stderr)
	}

	retroID := extractRetroID(t, stdout)
	manifest := readRetroManifest(t, repo, retroID)

	if manifest.Refs["subject_run.1"] != "gen-run-a" {
		t.Errorf("subject_run.1 = %q, want gen-run-a", manifest.Refs["subject_run.1"])
	}
	if manifest.Refs["subject_run.2"] != "gen-run-b" {
		t.Errorf("subject_run.2 = %q, want gen-run-b", manifest.Refs["subject_run.2"])
	}
}

// TestRetroGenerateWorkflowScopeNoSubjectRequired verifies scope=workflow needs no --subject-run.
func TestRetroGenerateWorkflowScopeNoSubjectRequired(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("workflow retro")}
	stdout, stderr, err := executeRetroGenerate(stub, "workflow")
	if err != nil {
		t.Fatalf("retro generate workflow returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "ref refs/etude/retros/") {
		t.Fatalf("stdout missing retros ref: %q", stdout)
	}
}

// TestRetroGenerateNoGeneratorError verifies that the command errors when no
// generator is injected and no --generator flag or git config is set.
func TestRetroGenerateNoGeneratorError(t *testing.T) {
	repo := initCaptureRepo(t)
	seedRunForGenerate(t, repo, "gen-run-x")
	chdir(t, repo)

	// No injected generator — will resolve to nil and return an error.
	var out bytes.Buffer
	var errOut bytes.Buffer
	r := &retroGenerateRunner{
		generator: nil, // no injected generator
		now:       time.Now,
		store:     refstore.New(""),
		stdout:    &out,
	}
	cmd := buildRetroGenerateCommand(&out, &errOut, r)
	cmd.SetArgs([]string{"cohort", "--subject-run", "gen-run-x"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no generator configured, got nil")
	}
	combined := err.Error() + " " + errOut.String()
	if !strings.Contains(combined, "no generator configured") {
		t.Errorf("error = %q, want containing 'no generator configured'", combined)
	}
}

// TestRetroGenerateInvalidScope verifies scope validation.
func TestRetroGenerateInvalidScope(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("body")}
	_, _, err := executeRetroGenerate(stub, "badscope", "--subject-run", "r1")
	if err == nil {
		t.Fatal("expected error for invalid scope, got nil")
	}
	if !strings.Contains(err.Error(), "invalid scope") {
		t.Errorf("error = %q, want containing 'invalid scope'", err.Error())
	}
}

// TestRetroGenerateSubjectRunRequired verifies that non-workflow scope without
// --subject-run returns an error.
func TestRetroGenerateSubjectRunRequired(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("body")}
	_, _, err := executeRetroGenerate(stub, "cohort")
	if err == nil {
		t.Fatal("expected error when --subject-run missing, got nil")
	}
	if !strings.Contains(err.Error(), "--subject-run is required") {
		t.Errorf("error = %q, want containing '--subject-run is required'", err.Error())
	}
}

// TestRetroGenerateGeneratorError verifies that a generator error is propagated.
func TestRetroGenerateGeneratorError(t *testing.T) {
	repo := initCaptureRepo(t)
	seedRunForGenerate(t, repo, "gen-run-err")
	chdir(t, repo)

	stub := &retro.StubGenerator{Err: retro.ErrGeneratorFailed}
	_, _, err := executeRetroGenerate(stub, "cohort", "--subject-run", "gen-run-err")
	if err == nil {
		t.Fatal("expected error from generator, got nil")
	}
}

// TestRetroGenerateProducedViaReserved verifies that --ref produced_via=... is rejected.
func TestRetroGenerateProducedViaReserved(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("body")}
	_, _, err := executeRetroGenerate(stub, "workflow",
		"--ref", "produced_via=fakegen",
	)
	if err == nil {
		t.Fatal("expected error for reserved --ref produced_via, got nil")
	}
	if !strings.Contains(err.Error(), "is reserved") {
		t.Errorf("error = %q, want containing 'is reserved'", err.Error())
	}
}

// TestRetroCaptureProducedViaReserved verifies that capture also rejects
// --ref produced_via=... (parity guard for the reserved key).
func TestRetroCaptureProducedViaReserved(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\n")
	chdir(t, repo)

	_, _, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--skill-id", "retro",
		"--ref", "produced_via=manual",
	)
	if err == nil {
		t.Fatal("expected error for reserved --ref produced_via in capture, got nil")
	}
	if !strings.Contains(err.Error(), "is reserved") {
		t.Errorf("error = %q, want containing 'is reserved'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Defect 1: generator ref key is reserved
// ---------------------------------------------------------------------------

// TestRetroGenerateGeneratorRefReserved verifies that --ref generator=x is
// rejected on retro generate (provenance spoofing guard).
func TestRetroGenerateGeneratorRefReserved(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("body")}
	_, _, err := executeRetroGenerate(stub, "workflow",
		"--ref", "generator=malicious",
	)
	if err == nil {
		t.Fatal("expected error for reserved --ref generator, got nil")
	}
	if !strings.Contains(err.Error(), "is reserved") {
		t.Errorf("error = %q, want containing 'is reserved'", err.Error())
	}
}

// TestRetroCaptureGeneratorRefReserved verifies that --ref generator=x is
// also rejected on retro capture.
func TestRetroCaptureGeneratorRefReserved(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "retro.md", "# Retro\n")
	chdir(t, repo)

	_, _, err := execute("retro", "capture", "cohort",
		"--file", "retro.md",
		"--subject-run", "r1",
		"--skill-id", "retro",
		"--ref", "generator=malicious",
	)
	if err == nil {
		t.Fatal("expected error for reserved --ref generator in capture, got nil")
	}
	if !strings.Contains(err.Error(), "is reserved") {
		t.Errorf("error = %q, want containing 'is reserved'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Defect 2: resolveSubjectStage — explicit stage selection
// ---------------------------------------------------------------------------

// seedMultiStageRun creates a run with two stages (plan + implement) for
// testing multi-stage resolution behaviour.
func seedMultiStageRun(t *testing.T, repo, runID string) {
	t.Helper()
	writeFile(t, repo, runID+"-plan.md", "# Plan\n")
	writeFile(t, repo, runID+"-impl.md", "# Implement\n")
	chdir(t, repo)
	_, stderr, err := execute("capture", "plan",
		"--run", runID,
		"--output", "output="+runID+"-plan.md",
	)
	if err != nil {
		t.Fatalf("seedMultiStageRun capture plan: %v\nstderr: %s", err, stderr)
	}
	_, stderr, err = execute("capture", "implement",
		"--run", runID,
		"--output", "output="+runID+"-impl.md",
	)
	if err != nil {
		t.Fatalf("seedMultiStageRun capture implement: %v\nstderr: %s", err, stderr)
	}
}

// seedDuplicateStageRun creates a run with two stages that share the same
// name (plan captured twice), triggering ErrAmbiguousStage.
func seedDuplicateStageRun(t *testing.T, repo, runID string) {
	t.Helper()
	writeFile(t, repo, runID+"-plan1.md", "# Plan v1\n")
	writeFile(t, repo, runID+"-plan2.md", "# Plan v2\n")
	chdir(t, repo)
	_, stderr, err := execute("capture", "plan",
		"--run", runID,
		"--output", "output="+runID+"-plan1.md",
	)
	if err != nil {
		t.Fatalf("seedDuplicateStageRun first capture: %v\nstderr: %s", err, stderr)
	}
	_, stderr, err = execute("capture", "plan",
		"--run", runID,
		"--output", "output="+runID+"-plan2.md",
	)
	if err != nil {
		t.Fatalf("seedDuplicateStageRun second capture: %v\nstderr: %s", err, stderr)
	}
}

// TestRetroGenerateSingleStageNoStageFlag verifies that a single-stage subject
// run works without --stage (the stage is unambiguous).
func TestRetroGenerateSingleStageNoStageFlag(t *testing.T) {
	repo := initCaptureRepo(t)
	seedRunForGenerate(t, repo, "single-stage-run")
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("# Retro\nSingle stage.\n")}
	stdout, stderr, err := executeRetroGenerate(stub,
		"cohort",
		"--subject-run", "single-stage-run",
	)
	if err != nil {
		t.Fatalf("retro generate single-stage (no --stage) returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "ref refs/etude/retros/") {
		t.Fatalf("stdout missing retros ref: %q", stdout)
	}
}

// TestRetroGenerateMultiStageNoStageFlag verifies that a multi-stage subject
// run without --stage returns a clear "multiple stages, specify --stage" error.
func TestRetroGenerateMultiStageNoStageFlag(t *testing.T) {
	repo := initCaptureRepo(t)
	seedMultiStageRun(t, repo, "multi-stage-run")
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("body")}
	_, _, err := executeRetroGenerate(stub,
		"cohort",
		"--subject-run", "multi-stage-run",
	)
	if err == nil {
		t.Fatal("expected error for multi-stage run without --stage, got nil")
	}
	if !strings.Contains(err.Error(), "multiple stages") {
		t.Errorf("error = %q, want containing 'multiple stages'", err.Error())
	}
	if !strings.Contains(err.Error(), "--stage") {
		t.Errorf("error = %q, want containing '--stage'", err.Error())
	}
}

// TestRetroGenerateMultiStageWithStageFlag verifies that providing --stage
// selects the named stage from a multi-stage subject run.
func TestRetroGenerateMultiStageWithStageFlag(t *testing.T) {
	repo := initCaptureRepo(t)
	seedMultiStageRun(t, repo, "multi-stage-explicit")
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("# Retro\nImplement stage.\n")}
	stdout, stderr, err := executeRetroGenerate(stub,
		"cohort",
		"--subject-run", "multi-stage-explicit",
		"--stage", "implement",
	)
	if err != nil {
		t.Fatalf("retro generate multi-stage --stage implement returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "ref refs/etude/retros/") {
		t.Fatalf("stdout missing retros ref: %q", stdout)
	}
}

// TestRetroGenerateStageNotFound verifies that --stage with a non-existent
// stage name returns a clear not-found error.
func TestRetroGenerateStageNotFound(t *testing.T) {
	repo := initCaptureRepo(t)
	seedRunForGenerate(t, repo, "stage-not-found-run")
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("body")}
	_, _, err := executeRetroGenerate(stub,
		"cohort",
		"--subject-run", "stage-not-found-run",
		"--stage", "nosuch",
	)
	if err == nil {
		t.Fatal("expected error for --stage nosuch, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want containing 'not found'", err.Error())
	}
}

// TestRetroGenerateAmbiguousStage verifies that providing a stage name that
// appears more than once in a run surfaces ErrAmbiguousStage, not a masked error.
func TestRetroGenerateAmbiguousStage(t *testing.T) {
	repo := initCaptureRepo(t)
	seedDuplicateStageRun(t, repo, "dup-stage-run")
	chdir(t, repo)

	stub := &retro.StubGenerator{CannedBody: []byte("body")}
	_, _, err := executeRetroGenerate(stub,
		"cohort",
		"--subject-run", "dup-stage-run",
		"--stage", "plan",
	)
	if err == nil {
		t.Fatal("expected error for ambiguous stage, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want containing 'ambiguous'", err.Error())
	}
}
