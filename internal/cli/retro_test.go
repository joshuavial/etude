package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
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
