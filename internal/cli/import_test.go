package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// stubGHClient is a test double for ghClient that returns fixture data.
type stubGHClient struct {
	authErr  error          // if non-nil, AuthStatus returns this error
	prs      []ghPR         // returned by ListPRs
	listErr  error          // if non-nil, ListPRs returns this error
	diffs    map[int][]byte // keyed by PR number
	diffErrs map[int]error  // per-PR diff errors
	version  string
}

func (s *stubGHClient) AuthStatus(_ context.Context) error {
	return s.authErr
}

func (s *stubGHClient) ListPRs(_ context.Context, _, _ string, _ int) ([]ghPR, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.prs, nil
}

func (s *stubGHClient) Diff(_ context.Context, _ string, number int) ([]byte, error) {
	if s.diffErrs != nil {
		if err, ok := s.diffErrs[number]; ok {
			return nil, err
		}
	}
	if s.diffs != nil {
		if d, ok := s.diffs[number]; ok {
			return d, nil
		}
	}
	return []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -0,0 +1 @@\n+line\n"), nil
}

func (s *stubGHClient) Version(_ context.Context) string {
	if s.version != "" {
		return s.version
	}
	return "gh version test"
}

// loadFixturePRList loads the pr-list.json fixture.
func loadFixturePRList(t *testing.T) []ghPR {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "import", "pr-list.json"))
	if err != nil {
		t.Fatalf("read pr-list.json: %v", err)
	}
	var prs []ghPR
	if err := json.Unmarshal(data, &prs); err != nil {
		t.Fatalf("parse pr-list.json: %v", err)
	}
	return prs
}

// loadFixtureDiff loads a per-PR diff fixture (returns empty bytes if missing).
func loadFixtureDiff(t *testing.T, number int) []byte {
	t.Helper()
	path := filepath.Join("testdata", "import", "pr-"+strconv.Itoa(number)+".diff")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -0,0 +1 @@\n+fallback\n")
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// buildFixtureStub builds a stubGHClient backed by the fixture files.
// PR 103 has no merge commit (set in fixture JSON). PR 104 has a diff fetch failure.
func buildFixtureStub(t *testing.T) *stubGHClient {
	t.Helper()
	prs := loadFixturePRList(t)
	diffs := map[int][]byte{
		101: loadFixtureDiff(t, 101),
		102: loadFixtureDiff(t, 102),
	}
	diffErrs := map[int]error{
		104: errors.New("gh pr diff 104 failed: HTTP 404"),
	}
	return &stubGHClient{
		prs:      prs,
		diffs:    diffs,
		diffErrs: diffErrs,
		version:  "gh version test",
	}
}

// runImport executes the import command with the given stub and args, returns
// (stdout, stderr, error).
func runImport(t *testing.T, repo string, client ghClient, extraArgs ...string) (string, string, error) {
	t.Helper()
	chdir(t, repo)

	// Build args: always include required flags.
	args := append([]string{"import", "--from-github", "--repo", "example/repo"}, extraArgs...)

	var outBuf, errBuf strings.Builder
	cmd := buildImportCommand(&outBuf, &errBuf, client)
	cmd.SetArgs(args[1:]) // strip "import" since cobra matches by command name
	err := cmd.ExecuteContext(context.Background())
	return outBuf.String(), errBuf.String(), err
}

// TestImportHappyPath verifies the normal import of 3 valid PRs (2 merged with
// content, 1 empty-body, 1 no-merge-commit skipped, 1 diff-fail skipped).
func TestImportHappyPath(t *testing.T) {
	repo := initCaptureRepo(t)
	stub := buildFixtureStub(t)

	stdout, stderr, err := runImport(t, repo, stub)
	if err != nil {
		t.Fatalf("import returned error: %v\nstderr: %s", err, stderr)
	}

	// PR 103 (no merge commit) should be warned about.
	if !strings.Contains(stderr, "no merge commit") {
		t.Errorf("stderr missing 'no merge commit' warning; got: %s", stderr)
	}
	// PR 104 (diff failure) should be warned about.
	if !strings.Contains(stderr, "diff fetch failed") {
		t.Errorf("stderr missing 'diff fetch failed' warning; got: %s", stderr)
	}

	// PR 101: normal merged with body.
	runID101 := buildImportRunID("example", "repo", 101)
	if !strings.Contains(stdout, "imported pr 101") {
		t.Errorf("stdout missing 'imported pr 101'; got: %s", stdout)
	}
	manifest101 := readRunManifest(t, repo, runID101)

	// run_id correct.
	if manifest101.RunID != runID101 {
		t.Errorf("RunID = %q, want %q", manifest101.RunID, runID101)
	}
	// Workflow.
	if manifest101.Workflow != importWorkflow || manifest101.WorkflowVersion != importWorkflowVersion {
		t.Errorf("workflow = %q/%q", manifest101.Workflow, manifest101.WorkflowVersion)
	}
	// occurred_at == mergedAt.
	want101OccurredAt := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	if !manifest101.OccurredAt.Equal(want101OccurredAt) {
		t.Errorf("OccurredAt = %v, want %v", manifest101.OccurredAt, want101OccurredAt)
	}
	// git_sha == mergeCommit.oid.
	if manifest101.Stages[0].GitSHA != "aabbccddee112233445566778899aabbccddee11" {
		t.Errorf("stage GitSHA = %q", manifest101.Stages[0].GitSHA)
	}
	// refs.
	if manifest101.Refs["pr"] != "101" {
		t.Errorf("refs[pr] = %q", manifest101.Refs["pr"])
	}
	if manifest101.Refs["repo"] != "example/repo" {
		t.Errorf("refs[repo] = %q", manifest101.Refs["repo"])
	}
	if manifest101.Refs["source"] != "github" {
		t.Errorf("refs[source] = %q", manifest101.Refs["source"])
	}
	if manifest101.Refs["author"] != "alice" {
		t.Errorf("refs[author] = %q", manifest101.Refs["author"])
	}
	// Stage shape: one "review" stage with diff input + pr-body output. NO plan/implement.
	if len(manifest101.Stages) != 1 {
		t.Fatalf("stages count = %d, want 1", len(manifest101.Stages))
	}
	s0 := manifest101.Stages[0]
	if s0.Name != "review" {
		t.Errorf("stage name = %q, want review", s0.Name)
	}
	if s0.ProducedBy != importProducedBy {
		t.Errorf("produced_by = %q, want %q", s0.ProducedBy, importProducedBy)
	}
	if len(s0.Inputs) != 1 || s0.Inputs[0].Role != "diff" {
		t.Errorf("stage inputs = %+v (want one diff input)", s0.Inputs)
	}
	if s0.Output.Role != "pr-body" {
		t.Errorf("stage output role = %q, want pr-body", s0.Output.Role)
	}
	// NO plan or implement stage.
	for _, stage := range manifest101.Stages {
		if stage.Name == "plan" || stage.Name == "implement" {
			t.Errorf("unexpected stage %q (imported PRs must NOT have plan/implement stages)", stage.Name)
		}
	}

	// PR 102: empty body → final-diff-only stage.
	runID102 := buildImportRunID("example", "repo", 102)
	manifest102 := readRunManifest(t, repo, runID102)
	if len(manifest102.Stages) != 1 {
		t.Fatalf("pr102 stages count = %d, want 1", len(manifest102.Stages))
	}
	s102 := manifest102.Stages[0]
	if s102.Name != "final-diff" {
		t.Errorf("pr102 stage name = %q, want final-diff", s102.Name)
	}
	if s102.Output.Role != "diff" {
		t.Errorf("pr102 stage output role = %q, want diff", s102.Output.Role)
	}
	if len(s102.Inputs) != 0 {
		t.Errorf("pr102 stage inputs = %v, want empty", s102.Inputs)
	}
	// occurred_at for pr102.
	want102OccurredAt := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	if !manifest102.OccurredAt.Equal(want102OccurredAt) {
		t.Errorf("pr102 OccurredAt = %v, want %v", manifest102.OccurredAt, want102OccurredAt)
	}

	// PR 103 (no merge commit): no ref created.
	runID103 := buildImportRunID("example", "repo", 103)
	refs103, _ := refstore.New(repo).List(context.Background(), "refs/etude/runs")
	for _, ref := range refs103 {
		if strings.HasSuffix(ref, runID103) {
			t.Errorf("pr103 should have been skipped but ref exists: %s", ref)
		}
	}

	// PR 104 (diff failure): no ref created.
	runID104 := buildImportRunID("example", "repo", 104)
	for _, ref := range refs103 {
		if strings.HasSuffix(ref, runID104) {
			t.Errorf("pr104 should have been skipped but ref exists: %s", ref)
		}
	}
}

// TestImportEmptyBodyPR verifies the final-diff stage shape for a PR with no body.
func TestImportEmptyBodyPR(t *testing.T) {
	repo := initCaptureRepo(t)
	stub := &stubGHClient{
		prs: []ghPR{
			{
				Number:   200,
				Title:    "Empty body PR",
				Body:     "",
				MergedAt: "2026-05-01T09:00:00Z",
				MergeCommit: struct {
					OID string `json:"oid"`
				}{OID: "1234567890abcdef1234567890abcdef12345678"},
				Author: struct {
					Login string `json:"login"`
				}{Login: "eve"},
				URL:   "https://github.com/test/repo/pull/200",
				State: "MERGED",
			},
		},
		diffs: map[int][]byte{
			200: []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -0,0 +1 @@\n+newline\n"),
		},
	}

	stdout, stderr, err := runImport(t, repo, stub)
	if err != nil {
		t.Fatalf("import returned error: %v\nstderr: %s", err, stderr)
	}
	_ = stdout

	runID := buildImportRunID("example", "repo", 200)
	manifest := readRunManifest(t, repo, runID)

	if len(manifest.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(manifest.Stages))
	}
	stage := manifest.Stages[0]
	if stage.Name != "final-diff" {
		t.Errorf("stage name = %q, want final-diff", stage.Name)
	}
	if stage.Output.Role != "diff" {
		t.Errorf("output role = %q, want diff", stage.Output.Role)
	}
}

// TestImportNoMergeCommitSkipped verifies that a PR with no merge commit is skipped.
func TestImportNoMergeCommitSkipped(t *testing.T) {
	repo := initCaptureRepo(t)
	stub := &stubGHClient{
		prs: []ghPR{
			{
				Number:   300,
				Title:    "Closed PR",
				Body:     "some body",
				MergedAt: "",
				// MergeCommit.OID is empty (zero value)
				Author: struct {
					Login string `json:"login"`
				}{Login: "frank"},
				URL:   "https://github.com/test/repo/pull/300",
				State: "CLOSED",
			},
		},
	}

	stdout, stderr, err := runImport(t, repo, stub)
	if err != nil {
		t.Fatalf("import returned error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stderr, "no merge commit") {
		t.Errorf("expected 'no merge commit' warning; stderr: %s", stderr)
	}
	if strings.Contains(stdout, "imported pr") {
		t.Errorf("should not have imported; stdout: %s", stdout)
	}

	// Confirm no ref was written.
	runID := buildImportRunID("example", "repo", 300)
	refs, _ := refstore.New(repo).List(context.Background(), "refs/etude/runs")
	for _, ref := range refs {
		if strings.HasSuffix(ref, runID) {
			t.Errorf("expected no ref for pr300, but found: %s", ref)
		}
	}
}

// TestImportDiffFailureSkipped verifies that a per-PR diff failure is skipped (not fatal).
func TestImportDiffFailureSkipped(t *testing.T) {
	repo := initCaptureRepo(t)
	stub := &stubGHClient{
		prs: []ghPR{
			{
				Number:   401,
				Title:    "Good PR",
				Body:     "good body",
				MergedAt: "2026-05-01T10:00:00Z",
				MergeCommit: struct {
					OID string `json:"oid"`
				}{OID: "aaaa0000bbbb1111cccc2222dddd3333eeee4444"},
				Author: struct {
					Login string `json:"login"`
				}{Login: "grace"},
				URL:   "https://github.com/test/repo/pull/401",
				State: "MERGED",
			},
			{
				Number:   402,
				Title:    "Diff-fail PR",
				Body:     "body content",
				MergedAt: "2026-05-01T11:00:00Z",
				MergeCommit: struct {
					OID string `json:"oid"`
				}{OID: "bbbb1111cccc2222dddd3333eeee4444ffff5555"},
				Author: struct {
					Login string `json:"login"`
				}{Login: "henry"},
				URL:   "https://github.com/test/repo/pull/402",
				State: "MERGED",
			},
		},
		diffs: map[int][]byte{
			401: []byte("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -0,0 +1 @@\n+line\n"),
		},
		diffErrs: map[int]error{
			402: errors.New("gh pr diff 402 failed: HTTP 404"),
		},
	}

	stdout, stderr, err := runImport(t, repo, stub)
	if err != nil {
		t.Fatalf("import returned error: %v\nstderr: %s", err, stderr)
	}

	// PR 401 imported, PR 402 skipped with warning.
	if !strings.Contains(stdout, "imported pr 401") {
		t.Errorf("expected 'imported pr 401'; stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "diff fetch failed") {
		t.Errorf("expected diff fetch failed warning; stderr: %s", stderr)
	}
	runID402 := buildImportRunID("example", "repo", 402)
	refs, _ := refstore.New(repo).List(context.Background(), "refs/etude/runs")
	for _, ref := range refs {
		if strings.HasSuffix(ref, runID402) {
			t.Errorf("pr402 should be skipped but ref exists: %s", ref)
		}
	}
}

// TestImportIdempotent verifies that re-importing the same PRs skips existing refs.
func TestImportIdempotent(t *testing.T) {
	repo := initCaptureRepo(t)
	stub := buildFixtureStub(t)

	// First import.
	_, stderr1, err := runImport(t, repo, stub)
	if err != nil {
		t.Fatalf("first import returned error: %v\nstderr: %s", err, stderr1)
	}
	refs1, _ := refstore.New(repo).List(context.Background(), "refs/etude/runs")

	// Get commit OIDs to verify they are unchanged after second import.
	commitsBefore := map[string]string{}
	for _, ref := range refs1 {
		commit, _ := refstore.New(repo).Resolve(context.Background(), ref)
		commitsBefore[ref] = commit
	}

	// Second import — all existing PRs should be skipped.
	stdout2, stderr2, err := runImport(t, repo, stub)
	if err != nil {
		t.Fatalf("second import returned error: %v\nstderr: %s", err, stderr2)
	}
	if !strings.Contains(stdout2, "already imported") {
		t.Errorf("expected 'already imported' message on second run; stdout: %s", stdout2)
	}

	// Refs should be unchanged.
	refs2, _ := refstore.New(repo).List(context.Background(), "refs/etude/runs")
	if len(refs1) != len(refs2) {
		t.Errorf("refs count changed from %d to %d", len(refs1), len(refs2))
	}
	for _, ref := range refs2 {
		commit, _ := refstore.New(repo).Resolve(context.Background(), ref)
		if commitsBefore[ref] != commit {
			t.Errorf("ref %s commit changed from %s to %s", ref, commitsBefore[ref], commit)
		}
	}
}

// TestImportDryRun verifies that --dry-run prints the plan but writes nothing.
func TestImportDryRun(t *testing.T) {
	repo := initCaptureRepo(t)
	stub := buildFixtureStub(t)

	stdout, stderr, err := runImport(t, repo, stub, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run import returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "dry-run") {
		t.Errorf("stdout missing 'dry-run'; got: %s", stdout)
	}

	// No refs should have been written.
	refs, _ := refstore.New(repo).List(context.Background(), "refs/etude/runs")
	if len(refs) != 0 {
		t.Errorf("dry-run wrote %d refs, want 0", len(refs))
	}
}

// TestImportGHNotAuthed verifies that an auth failure returns a non-zero error
// with an actionable message. The stub injects the error via AuthStatus so this
// test is fully hermetic — gh does NOT need to be present on PATH.
func TestImportGHNotAuthed(t *testing.T) {
	repo := initCaptureRepo(t)
	stub := &stubGHClient{
		authErr: errors.New("not logged in"),
	}

	_, _, err := runImport(t, repo, stub)
	if err == nil {
		t.Fatal("expected error when gh not authed, got nil")
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error missing 'gh auth login' hint; got: %v", err)
	}
}

// TestImportRunIDSanitization is a table-driven unit test for buildImportRunID.
func TestImportRunIDSanitization(t *testing.T) {
	cases := []struct {
		owner  string
		repo   string
		number int
		want   string
	}{
		{"myorg", "myrepo", 1, "gh-myorg-myrepo-pr1"},
		{"my-org", "my-repo", 42, "gh-my-org-my-repo-pr42"},
		{"my.org", "my.repo", 7, "gh-my.org-my.repo-pr7"},
		// Characters that need sanitizing
		{"org/sub", "repo name", 3, "gh-org-sub-repo-name-pr3"},
		{"org", "repo", 999, "gh-org-repo-pr999"},
		// Consecutive dots: "foo..bar" must collapse to "foo.bar" (IsValidRunID rejects "..")
		{"owner", "foo..bar", 5, "gh-owner-foo.bar-pr5"},
	}

	for _, tc := range cases {
		got := buildImportRunID(tc.owner, tc.repo, tc.number)
		// Must satisfy IsValidRunID.
		if !runmanifest.IsValidRunID(got) {
			t.Errorf("buildImportRunID(%q, %q, %d) = %q is not a valid run ID", tc.owner, tc.repo, tc.number, got)
		}
		if got != tc.want {
			t.Errorf("buildImportRunID(%q, %q, %d) = %q, want %q", tc.owner, tc.repo, tc.number, got, tc.want)
		}
	}
}

// TestImportParseOwnerRepo verifies --repo validation.
func TestImportParseOwnerRepo(t *testing.T) {
	cases := []struct {
		input   string
		wantErr bool
		owner   string
		repo    string
	}{
		{"myorg/myrepo", false, "myorg", "myrepo"},
		{"owner/name", false, "owner", "name"},
		{"", true, "", ""},
		{"noslash", true, "", ""},
		{"/norepo", true, "", ""},
		{"noowner/", true, "", ""},
		{"a/b/c", true, "", ""}, // repo part "b/c" contains slash — rejected
	}
	for _, tc := range cases {
		owner, repo, err := parseOwnerRepo(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseOwnerRepo(%q) expected error, got nil (owner=%q repo=%q)", tc.input, owner, repo)
			}
		} else {
			if err != nil {
				t.Errorf("parseOwnerRepo(%q) unexpected error: %v", tc.input, err)
			} else if owner != tc.owner || repo != tc.repo {
				t.Errorf("parseOwnerRepo(%q) = (%q, %q), want (%q, %q)", tc.input, owner, repo, tc.owner, tc.repo)
			}
		}
	}
}

// TestImportMergedAtMapping verifies that occurred_at is set from mergedAt.
func TestImportMergedAtMapping(t *testing.T) {
	repo := initCaptureRepo(t)
	mergedAt := "2025-12-31T23:59:59Z"
	stub := &stubGHClient{
		prs: []ghPR{
			{
				Number:   500,
				Title:    "Mapping test",
				Body:     "body text for occurred_at mapping test",
				MergedAt: mergedAt,
				MergeCommit: struct {
					OID string `json:"oid"`
				}{OID: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
				Author: struct {
					Login string `json:"login"`
				}{Login: "ivan"},
				URL:   "https://github.com/test/repo/pull/500",
				State: "MERGED",
			},
		},
		diffs: map[int][]byte{
			500: []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -0,0 +1 @@\n+line\n"),
		},
	}

	_, stderr, err := runImport(t, repo, stub)
	if err != nil {
		t.Fatalf("import returned error: %v\nstderr: %s", err, stderr)
	}

	runID := buildImportRunID("example", "repo", 500)
	manifest := readRunManifest(t, repo, runID)

	want, _ := time.Parse(time.RFC3339, mergedAt)
	want = want.UTC()
	if !manifest.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want %v", manifest.OccurredAt, want)
	}
}

// TestImportMissingFromGithubFlag verifies --from-github is required.
func TestImportMissingFromGithubFlag(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	stub := &stubGHClient{}

	var outBuf, errBuf strings.Builder
	cmd := buildImportCommand(&outBuf, &errBuf, stub)
	cmd.SetArgs([]string{"--repo", "owner/name"}) // no --from-github
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when --from-github absent, got nil")
	}
	if !strings.Contains(err.Error(), "--from-github") {
		t.Errorf("error missing '--from-github' hint; got: %v", err)
	}
}

// TestImportMissingRepoFlag verifies --repo is required.
func TestImportMissingRepoFlag(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	stub := &stubGHClient{}

	var outBuf, errBuf strings.Builder
	cmd := buildImportCommand(&outBuf, &errBuf, stub)
	cmd.SetArgs([]string{"--from-github"}) // no --repo
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when --repo absent, got nil")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error missing '--repo' hint; got: %v", err)
	}
}
