package cli

import (
	"context"
	"os"
	"strings"
	"testing"

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
