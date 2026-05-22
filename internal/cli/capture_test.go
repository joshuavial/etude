package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

func TestCaptureCreatesRun(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "PLAN.MD", "# plan\n")
	chdir(t, repo)

	stdout, stderr, err := execute("capture", "plan", "--run", "run-1", "--output", "output=PLAN.MD", "--ref", "pr=469")
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "captured ") || !strings.Contains(stdout, "ref refs/etude/runs/run-1") {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}

	manifest := readRunManifest(t, repo, "run-1")
	if manifest.RunID != "run-1" || manifest.Workflow != defaultWorkflow || manifest.WorkflowVersion != defaultWorkflowVersion {
		t.Fatalf("manifest metadata = %#v", manifest)
	}
	if manifest.Refs["pr"] != "469" {
		t.Fatalf("refs = %#v", manifest.Refs)
	}
	stage := manifest.Stages[0]
	if stage.Name != "plan" || stage.ProducedBy != defaultProducedBy || stage.Skill.ID != "plan" || stage.Skill.Repo != defaultSkillRepo {
		t.Fatalf("stage = %#v", stage)
	}
	if stage.Output.Role != "output" || stage.Output.MediaType != "text/markdown; charset=utf-8" {
		t.Fatalf("output artifact = %#v", stage.Output)
	}
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/run-1", stage.Output.Path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != "# plan\n" {
		t.Fatalf("artifact content = %q", content)
	}
}

func TestCaptureRecordsInputsAndUnknownMediaType(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "task.txt", "task")
	writeFile(t, repo, "blob.unknown", "blob")
	writeFile(t, repo, "out.bin", "out")
	chdir(t, repo)

	_, stderr, err := execute("capture", "implement", "--run", "run-1", "--input", "task=task.txt", "--input", "blob=blob.unknown", "--output", "output=out.bin")
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}
	stage := readRunManifest(t, repo, "run-1").Stages[0]
	if len(stage.Inputs) != 2 {
		t.Fatalf("inputs = %#v", stage.Inputs)
	}
	if stage.Inputs[0].MediaType != "text/plain; charset=utf-8" || stage.Inputs[1].MediaType != "application/octet-stream" {
		t.Fatalf("input media types = %#v", stage.Inputs)
	}
	if stage.Output.MediaType != "application/octet-stream" {
		t.Fatalf("output media type = %q", stage.Output.MediaType)
	}
}

func TestCaptureAppendPreservesPriorArtifactsAndMergesRefs(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "plan.md", "plan")
	writeFile(t, repo, "review.txt", "review")
	chdir(t, repo)

	if _, stderr, err := execute("capture", "plan", "--run", "run-1", "--output", "plan=plan.md", "--ref", "pr=1"); err != nil {
		t.Fatalf("first capture returned error: %v\nstderr: %s", err, stderr)
	}
	first := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "refs/etude/runs/run-1"))
	if _, stderr, err := execute("capture", "review", "--run", "run-1", "--output", "review=review.txt", "--ref", "pr=2", "--ref", "branch=main"); err != nil {
		t.Fatalf("second capture returned error: %v\nstderr: %s", err, stderr)
	}
	second := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "refs/etude/runs/run-1"))
	parent := strings.TrimSpace(gitCapture(t, repo, "rev-parse", second+"^"))
	if parent != first {
		t.Fatalf("append parent = %q, want %q", parent, first)
	}
	manifest := readRunManifest(t, repo, "run-1")
	if len(manifest.Stages) != 2 {
		t.Fatalf("stages = %#v", manifest.Stages)
	}
	if manifest.Refs["pr"] != "2" || manifest.Refs["branch"] != "main" {
		t.Fatalf("refs = %#v", manifest.Refs)
	}
	for _, stage := range manifest.Stages {
		if _, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/run-1", stage.Output.Path); err != nil {
			t.Fatalf("missing artifact %s: %v", stage.Output.Path, err)
		}
	}
}

func TestCaptureRejectsInvalidInputs(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "out")
	chdir(t, repo)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing run", []string{"capture", "plan", "--output", "out=out.md"}, "--run is required"},
		{"missing output", []string{"capture", "plan", "--run", "run-1"}, "exactly one --output"},
		{"duplicate output", []string{"capture", "plan", "--run", "run-1", "--output", "a=out.md", "--output", "b=out.md"}, "exactly one --output"},
		{"invalid stage", []string{"capture", "bad/stage", "--run", "run-1", "--output", "out=out.md"}, "invalid stage"},
		{"invalid ref key", []string{"capture", "plan", "--run", "run-1", "--output", "out=out.md", "--ref", "bad/key=value"}, "invalid ref key"},
		{"empty ref value", []string{"capture", "plan", "--run", "run-1", "--output", "out=out.md", "--ref", "pr="}, "invalid ref"},
		{"malformed artifact", []string{"capture", "plan", "--run", "run-1", "--output", "out.md"}, "invalid artifact"},
		{"missing file", []string{"capture", "plan", "--run", "run-1", "--output", "out=missing.md"}, "read missing.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := execute(tc.args...)
			if err == nil {
				t.Fatal("capture returned nil error")
			}
			if !strings.Contains(err.Error(), tc.want) && !strings.Contains(stderr, tc.want) {
				t.Fatalf("error %q stderr %q do not contain %q", err, stderr, tc.want)
			}
		})
	}
}

func TestCaptureRejectsAppendConflictsAndBadExistingManifest(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "out")
	chdir(t, repo)

	if _, stderr, err := execute("capture", "plan", "--run", "run-1", "--workflow", "alpha", "--output", "out=out.md"); err != nil {
		t.Fatalf("first capture returned error: %v\nstderr: %s", err, stderr)
	}
	_, stderr, err := execute("capture", "review", "--run", "run-1", "--workflow", "beta", "--output", "out=out.md")
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflict error = %v stderr %q", err, stderr)
	}

	bad := []byte(`{"run_id":"different","workflow":"manual","workflow_version":"manual-v1","created":"2026-05-22T00:00:00Z","refs":{},"stages":[]}`)
	if _, err := refstore.New(repo).WriteCommit(context.Background(), "refs/etude/runs/bad-run", map[string][]byte{"manifest.json": bad}, refstore.WriteOptions{}); err != nil {
		t.Fatalf("WriteCommit bad manifest returned error: %v", err)
	}
	_, stderr, err = execute("capture", "plan", "--run", "bad-run", "--output", "out=out.md")
	if err == nil {
		t.Fatal("bad manifest capture returned nil error")
	}
}

func TestCaptureRejectsUnresolvedHEADWithoutGitSHA(t *testing.T) {
	repo := t.TempDir()
	gitCapture(t, repo, "init")
	writeFile(t, repo, "out.md", "out")
	chdir(t, repo)

	_, stderr, err := execute("capture", "plan", "--run", "run-1", "--output", "out=out.md")
	if err == nil {
		t.Fatal("capture returned nil error")
	}
	if !strings.Contains(err.Error(), "could not resolve HEAD") && !strings.Contains(stderr, "could not resolve HEAD") {
		t.Fatalf("error = %v stderr = %q", err, stderr)
	}
}

func readRunManifest(t *testing.T, repo, runID string) runmanifest.Manifest {
	t.Helper()
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/"+runID, "manifest.json")
	if err != nil {
		t.Fatalf("ReadFile manifest returned error: %v", err)
	}
	manifest, err := runmanifest.ParseJSON(content)
	if err != nil {
		t.Fatalf("ParseJSON returned error: %v", err)
	}
	return manifest
}

func initCaptureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCapture(t, dir, "init")
	gitCapture(t, dir, "config", "user.name", "Test User")
	gitCapture(t, dir, "config", "user.email", "test@example.invalid")
	writeFile(t, dir, "README.md", "test\n")
	gitCapture(t, dir, "add", "README.md")
	gitCapture(t, dir, "commit", "-m", "initial")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(original)
	})
}

func gitCapture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}
