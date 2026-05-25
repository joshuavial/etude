package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestCaptureValidatesGitSHA(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "out")
	chdir(t, repo)

	// A valid 40-char hex sha is accepted and recorded verbatim.
	sha40 := "0123456789abcdef0123456789abcdef01234567"
	if _, stderr, err := execute("capture", "plan", "--run", "ok40", "--output", "output=out.md", "--git-sha", sha40); err != nil {
		t.Fatalf("valid 40-hex --git-sha rejected: %v\nstderr: %s", err, stderr)
	}
	if got := readRunManifest(t, repo, "ok40").Stages[0].GitSHA; got != sha40 {
		t.Fatalf("recorded git sha = %q, want %q", got, sha40)
	}

	// A valid 64-char hex sha (SHA-256) is accepted.
	if _, stderr, err := execute("capture", "plan", "--run", "ok64", "--output", "output=out.md", "--git-sha", strings.Repeat("a", 64)); err != nil {
		t.Fatalf("valid 64-hex --git-sha rejected: %v\nstderr: %s", err, stderr)
	}

	// Invalid values are rejected with a clear error before the run is written.
	for _, bad := range []string{"not-a-sha", "12345", strings.ToUpper(sha40), "z" + sha40[1:]} {
		_, stderr, err := execute("capture", "plan", "--run", "badsha", "--output", "output=out.md", "--git-sha", bad)
		if err == nil {
			t.Fatalf("--git-sha %q was accepted, want rejection", bad)
		}
		if combined := err.Error() + " " + stderr; !strings.Contains(combined, "invalid --git-sha") {
			t.Fatalf("--git-sha %q error = %q, want 'invalid --git-sha'", bad, combined)
		}
	}
}

func TestInferMediaType(t *testing.T) {
	cases := map[string]string{
		"a.txt":      "text/plain; charset=utf-8",
		"a.md":       "text/markdown; charset=utf-8",
		"a.markdown": "text/markdown; charset=utf-8",
		"a.json":     "application/json",
		"a.yaml":     "application/yaml",
		"a.yml":      "application/yaml",
		"a.diff":     "text/x-diff; charset=utf-8",
		"a.patch":    "text/x-diff; charset=utf-8",
		"a.html":     "text/html; charset=utf-8",
		"a.htm":      "text/html; charset=utf-8",
		"a.png":      "image/png",
		"a.jpg":      "image/jpeg",
		"a.jpeg":     "image/jpeg",
		"a.gif":      "image/gif",
		"a.svg":      "image/svg+xml",
		"a.bin":      "application/octet-stream",
		"noext":      "application/octet-stream",
		"a.MD":       "text/markdown; charset=utf-8", // extension match is case-insensitive
		"dir/x.JSON": "application/json",
	}
	for path, want := range cases {
		if got := inferMediaType(path); got != want {
			t.Errorf("inferMediaType(%q) = %q, want %q", path, got, want)
		}
	}
}

func readRawManifest(t *testing.T, repo, runID string) []byte {
	t.Helper()
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/"+runID, "manifest.json")
	if err != nil {
		t.Fatalf("ReadFile raw manifest returned error: %v", err)
	}
	return content
}

// rawProducerProbe decodes only the provenance-bearing fields of a manifest so
// tests can assert the SERIALIZED shape structurally (rather than by substring,
// which a leaked legacy top-level skill block could satisfy by accident).
type rawProducerProbe struct {
	Stages []struct {
		Skill    *json.RawMessage `json:"skill"`
		Producer struct {
			Harness *struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"harness"`
			Model string `json:"model"`
			Skill *struct {
				ID      string `json:"id"`
				Repo    string `json:"repo"`
				Version string `json:"version"`
			} `json:"skill"`
		} `json:"producer"`
	} `json:"stages"`
}

func parseRawProducer(t *testing.T, raw []byte) rawProducerProbe {
	t.Helper()
	var probe rawProducerProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal raw manifest: %v\n%s", err, raw)
	}
	return probe
}

func TestCaptureRecordsProducer(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output")
	chdir(t, repo)

	_, stderr, err := execute("capture", "plan",
		"--run", "run-1",
		"--output", "output=out.md",
		"--skill-id", "dev-planner",
		"--skill-repo", "myrepo",
		"--skill-version", "v3",
		"--harness", "claude-code",
		"--harness-version", "1.0",
		"--model", "claude-opus-4-7",
	)
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	wantSkill := runmanifest.Skill{ID: "dev-planner", Repo: "myrepo", Version: "v3"}

	manifest := readRunManifest(t, repo, "run-1")
	if manifest.ManifestVersion != 2 {
		t.Fatalf("ManifestVersion = %d, want 2", manifest.ManifestVersion)
	}
	stage := manifest.Stages[0]
	if stage.Producer.Harness.Name != "claude-code" || stage.Producer.Harness.Version != "1.0" {
		t.Fatalf("Producer.Harness = %#v, want {claude-code 1.0}", stage.Producer.Harness)
	}
	if stage.Producer.Model != "claude-opus-4-7" {
		t.Fatalf("Producer.Model = %q, want claude-opus-4-7", stage.Producer.Model)
	}
	if stage.Producer.Skill != wantSkill {
		t.Fatalf("Producer.Skill = %#v, want %#v", stage.Producer.Skill, wantSkill)
	}
	if stage.Skill != stage.Producer.Skill {
		t.Fatalf("mirror invariant violated: Stage.Skill = %#v, Producer.Skill = %#v", stage.Skill, stage.Producer.Skill)
	}

	// Structurally verify the SERIALIZED shape: producer.skill must carry the
	// resolved skill identity, and no top-level stages[].skill may exist (so the
	// provenance can only be coming from producer.skill, not a leaked legacy block).
	raw := readRawManifest(t, repo, "run-1")
	ps := parseRawProducer(t, raw).Stages[0]
	if ps.Skill != nil {
		t.Fatalf("raw stages[0].skill must be absent (skill lives inside producer), got %s", *ps.Skill)
	}
	if ps.Producer.Skill == nil {
		t.Fatalf("raw stages[0].producer.skill is missing:\n%s", raw)
	}
	if ps.Producer.Skill.ID != "dev-planner" || ps.Producer.Skill.Repo != "myrepo" || ps.Producer.Skill.Version != "v3" {
		t.Fatalf("raw producer.skill = %#v, want {dev-planner myrepo v3}", *ps.Producer.Skill)
	}
	if ps.Producer.Harness == nil || ps.Producer.Harness.Name != "claude-code" || ps.Producer.Harness.Version != "1.0" {
		t.Fatalf("raw producer.harness = %#v, want {claude-code 1.0}", ps.Producer.Harness)
	}
	if ps.Producer.Model != "claude-opus-4-7" {
		t.Fatalf("raw producer.model = %q, want claude-opus-4-7", ps.Producer.Model)
	}
}

func TestCaptureOmitsHarnessWithoutFlags(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output")
	chdir(t, repo)

	_, stderr, err := execute("capture", "plan",
		"--run", "run-1",
		"--output", "output=out.md",
	)
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "run-1")
	stage := manifest.Stages[0]
	if stage.Producer.Harness.Name != "" {
		t.Fatalf("Producer.Harness.Name = %q, want empty", stage.Producer.Harness.Name)
	}
	if stage.Producer.Model != "" {
		t.Fatalf("Producer.Model = %q, want empty", stage.Producer.Model)
	}
	if stage.Skill != stage.Producer.Skill {
		t.Fatalf("mirror invariant violated: Stage.Skill = %#v, Producer.Skill = %#v", stage.Skill, stage.Producer.Skill)
	}

	// Structural check: harness omitted, producer.skill present, no leaked
	// top-level stages[].skill — independent of substring coincidence.
	raw := readRawManifest(t, repo, "run-1")
	ps := parseRawProducer(t, raw).Stages[0]
	if ps.Producer.Harness != nil {
		t.Fatalf("raw stages[0].producer.harness must be omitted when unset, got %#v", ps.Producer.Harness)
	}
	if ps.Producer.Model != "" {
		t.Fatalf("raw stages[0].producer.model = %q, want empty/omitted", ps.Producer.Model)
	}
	if ps.Skill != nil {
		t.Fatalf("raw stages[0].skill must be absent (skill lives inside producer), got %s", *ps.Skill)
	}
	if ps.Producer.Skill == nil || ps.Producer.Skill.ID == "" {
		t.Fatalf("raw stages[0].producer.skill is missing or empty:\n%s", raw)
	}
}

func TestCapturePartialHarnessName(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output")
	chdir(t, repo)

	_, stderr, err := execute("capture", "plan",
		"--run", "run-1",
		"--output", "output=out.md",
		"--harness", "claude-code",
	)
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "run-1")
	stage := manifest.Stages[0]
	if stage.Producer.Harness.Name != "claude-code" {
		t.Fatalf("Producer.Harness.Name = %q, want claude-code", stage.Producer.Harness.Name)
	}
	if stage.Producer.Harness.Version != "" {
		t.Fatalf("Producer.Harness.Version = %q, want empty", stage.Producer.Harness.Version)
	}

	raw := readRawManifest(t, repo, "run-1")
	ps := parseRawProducer(t, raw).Stages[0]
	if ps.Producer.Harness == nil || ps.Producer.Harness.Name != "claude-code" {
		t.Fatalf("raw stages[0].producer.harness = %#v, want name claude-code", ps.Producer.Harness)
	}
	if ps.Producer.Harness.Version != "" {
		t.Fatalf("raw producer.harness.version = %q, want empty", ps.Producer.Harness.Version)
	}
}

func TestCaptureModelWithoutHarness(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output")
	chdir(t, repo)

	_, stderr, err := execute("capture", "plan",
		"--run", "run-1",
		"--output", "output=out.md",
		"--model", "claude-sonnet-4-6",
	)
	if err != nil {
		t.Fatalf("capture returned error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "run-1")
	stage := manifest.Stages[0]
	if stage.Producer.Model != "claude-sonnet-4-6" {
		t.Fatalf("Producer.Model = %q, want claude-sonnet-4-6", stage.Producer.Model)
	}
	if stage.Producer.Harness.Name != "" {
		t.Fatalf("Producer.Harness.Name = %q, want empty", stage.Producer.Harness.Name)
	}

	raw := readRawManifest(t, repo, "run-1")
	ps := parseRawProducer(t, raw).Stages[0]
	if ps.Producer.Model != "claude-sonnet-4-6" {
		t.Fatalf("raw stages[0].producer.model = %q, want claude-sonnet-4-6", ps.Producer.Model)
	}
	if ps.Producer.Harness != nil {
		t.Fatalf("raw stages[0].producer.harness must be omitted, got %#v", ps.Producer.Harness)
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
