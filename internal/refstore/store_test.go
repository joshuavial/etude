package refstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestWriteCommitCreatesCustomRefWithNestedFiles(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	commit, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json":      []byte(`{"run_id":"run-1"}`),
		"artifacts/abc12345": []byte("plan text"),
	}, WriteOptions{Message: "capture run-1"})
	if err != nil {
		t.Fatalf("WriteCommit returned error: %v", err)
	}

	resolved, err := store.Resolve(ctx, "refs/etude/runs/run-1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != commit {
		t.Fatalf("Resolve = %q, want %q", resolved, commit)
	}

	content, err := store.ReadFile(ctx, "refs/etude/runs/run-1", "artifacts/abc12345")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != "plan text" {
		t.Fatalf("ReadFile content = %q", content)
	}

	assertClean(t, repo)
}

func TestCreateOnlyWriteDoesNotOverwriteExistingRef(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	first, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("first WriteCommit returned error: %v", err)
	}
	_, err = store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":2}`),
	}, WriteOptions{})
	if !errors.Is(err, ErrRefExists) {
		t.Fatalf("second WriteCommit error = %v, want ErrRefExists", err)
	}

	resolved, err := store.Resolve(ctx, "refs/etude/runs/run-1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != first {
		t.Fatalf("ref moved to %q, want original %q", resolved, first)
	}
}

func TestCASUpdateAdvancesRefAndParentsPreviousTip(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	first, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("first WriteCommit returned error: %v", err)
	}
	second, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":2}`),
	}, WriteOptions{ExpectedOld: first})
	if err != nil {
		t.Fatalf("CAS WriteCommit returned error: %v", err)
	}
	if second == first {
		t.Fatal("CAS WriteCommit did not create a new commit")
	}

	resolved, err := store.Resolve(ctx, "refs/etude/runs/run-1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != second {
		t.Fatalf("Resolve = %q, want %q", resolved, second)
	}

	parent := git(t, repo, "rev-parse", second+"^")
	if strings.TrimSpace(parent) != first {
		t.Fatalf("parent = %q, want %q", strings.TrimSpace(parent), first)
	}

	content, err := store.ReadFile(ctx, "refs/etude/runs/run-1", "manifest.json")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != `{"version":2}` {
		t.Fatalf("manifest content = %q", content)
	}
}

func TestStaleCASLeavesRefUnchanged(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	first, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("first WriteCommit returned error: %v", err)
	}
	second, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":2}`),
	}, WriteOptions{ExpectedOld: first})
	if err != nil {
		t.Fatalf("second WriteCommit returned error: %v", err)
	}
	_, err = store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":3}`),
	}, WriteOptions{ExpectedOld: first})
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("stale CAS error = %v, want ErrStaleRef", err)
	}

	resolved, err := store.Resolve(ctx, "refs/etude/runs/run-1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != second {
		t.Fatalf("stale CAS moved ref to %q, want %q", resolved, second)
	}
}

func TestCASRejectsMalformedAndMissingExpectedOld(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	_, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{ExpectedOld: "not-a-commit"})
	if !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("malformed ExpectedOld error = %v, want ErrInvalidRef", err)
	}

	uppercase := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err = store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{ExpectedOld: uppercase})
	if !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("uppercase ExpectedOld error = %v, want ErrInvalidRef", err)
	}

	missing := "1111111111111111111111111111111111111111"
	_, err = store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{ExpectedOld: missing})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ExpectedOld error = %v, want ErrNotFound", err)
	}

	refs := git(t, repo, "for-each-ref", "--format=%(refname)", "refs/etude")
	if strings.TrimSpace(refs) != "" {
		t.Fatalf("failed CAS validation created refs:\n%s", refs)
	}
}

func TestSymbolicEtudeRefCannotMutateTargetRef(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	seed, err := store.WriteCommit(ctx, "refs/etude/evals/seed", map[string][]byte{
		"manifest.json": []byte(`{"seed":true}`),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("seed WriteCommit returned error: %v", err)
	}
	git(t, repo, "update-ref", "refs/heads/main", seed)
	git(t, repo, "symbolic-ref", "refs/etude/runs/evil", "refs/heads/main")

	_, err = store.WriteCommit(ctx, "refs/etude/runs/evil", map[string][]byte{
		"manifest.json": []byte(`{"evil":true}`),
	}, WriteOptions{ExpectedOld: seed})
	if !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("WriteCommit through symbolic ref error = %v, want ErrInvalidRef", err)
	}
	if _, err := store.Resolve(ctx, "refs/etude/runs/evil"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("Resolve symbolic ref error = %v, want ErrInvalidRef", err)
	}

	head := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	if head != seed {
		t.Fatalf("refs/heads/main changed to %q, want %q", head, seed)
	}

	refs, err := store.List(ctx, "refs/etude/runs")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("List returned symbolic refs: %#v", refs)
	}
}

func TestListReturnsSortedRunAndEvalRefs(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	for _, ref := range []string{
		"refs/etude/runs/b",
		"refs/etude/evals/eval-1",
		"refs/etude/runs/a",
	} {
		if _, err := store.WriteCommit(ctx, ref, map[string][]byte{"manifest.json": []byte(ref)}, WriteOptions{}); err != nil {
			t.Fatalf("WriteCommit(%s) returned error: %v", ref, err)
		}
	}

	runs, err := store.List(ctx, "refs/etude/runs")
	if err != nil {
		t.Fatalf("List runs returned error: %v", err)
	}
	wantRuns := []string{"refs/etude/runs/a", "refs/etude/runs/b"}
	if !reflect.DeepEqual(runs, wantRuns) {
		t.Fatalf("runs = %#v, want %#v", runs, wantRuns)
	}

	evals, err := store.List(ctx, "refs/etude/evals")
	if err != nil {
		t.Fatalf("List evals returned error: %v", err)
	}
	wantEvals := []string{"refs/etude/evals/eval-1"}
	if !reflect.DeepEqual(evals, wantEvals) {
		t.Fatalf("evals = %#v, want %#v", evals, wantEvals)
	}
}

func TestMissingRefAndFileReturnNotFound(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	if _, err := store.Resolve(ctx, "refs/etude/runs/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve missing error = %v, want ErrNotFound", err)
	}
	if _, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{"manifest.json": []byte("{}")}, WriteOptions{}); err != nil {
		t.Fatalf("WriteCommit returned error: %v", err)
	}
	if _, err := store.ReadFile(ctx, "refs/etude/runs/run-1", "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadFile missing error = %v, want ErrNotFound", err)
	}
}

func TestReadCommitFileReadsSnapshotAfterRefMoves(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	first, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("first WriteCommit returned error: %v", err)
	}
	second, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":2}`),
	}, WriteOptions{ExpectedOld: first})
	if err != nil {
		t.Fatalf("second WriteCommit returned error: %v", err)
	}

	oldContent, err := store.ReadCommitFile(ctx, first, "manifest.json")
	if err != nil {
		t.Fatalf("ReadCommitFile first returned error: %v", err)
	}
	if string(oldContent) != `{"version":1}` {
		t.Fatalf("first content = %q", oldContent)
	}
	newContent, err := store.ReadCommitFile(ctx, second, "manifest.json")
	if err != nil {
		t.Fatalf("ReadCommitFile second returned error: %v", err)
	}
	if string(newContent) != `{"version":2}` {
		t.Fatalf("second content = %q", newContent)
	}
}

func TestReadCommitFileRejectsInvalidCommitPathAndMissingFile(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	commit, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit returned error: %v", err)
	}
	if _, err := store.ReadCommitFile(ctx, "not-a-commit", "manifest.json"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("invalid commit error = %v, want ErrInvalidRef", err)
	}
	if _, err := store.ReadCommitFile(ctx, commit, "../manifest.json"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid path error = %v, want ErrInvalidPath", err)
	}
	if _, err := store.ReadCommitFile(ctx, strings.Repeat("1", 40), "manifest.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing commit error = %v, want ErrNotFound", err)
	}
	if _, err := store.ReadCommitFile(ctx, commit, "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file error = %v, want ErrNotFound", err)
	}
}

func TestRejectsInvalidRefsPathsAndEmptyFiles(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	for _, ref := range []string{
		"",
		"refs/heads/main",
		"refs/etude/other/id",
		"refs/etude/runs",
		"refs/etude/runs/has space",
		"refs/etude/runs/../bad",
	} {
		_, err := store.WriteCommit(ctx, ref, map[string][]byte{"manifest.json": []byte("{}")}, WriteOptions{})
		if !errors.Is(err, ErrInvalidRef) {
			t.Fatalf("WriteCommit(%q) error = %v, want ErrInvalidRef", ref, err)
		}
	}

	for _, filePath := range []string{
		"",
		"/absolute",
		"../outside",
		"nested/../outside",
		".git/config",
		"bad:path",
		"bad,path",
	} {
		_, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{filePath: []byte("bad")}, WriteOptions{})
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("WriteCommit path %q error = %v, want ErrInvalidPath", filePath, err)
		}
	}

	if _, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{}, WriteOptions{}); !errors.Is(err, ErrEmptyTree) {
		t.Fatalf("empty WriteCommit error = %v, want ErrEmptyTree", err)
	}

	refs := git(t, repo, "for-each-ref", "--format=%(refname)", "refs/etude")
	if strings.TrimSpace(refs) != "" {
		t.Fatalf("invalid writes created refs:\n%s", refs)
	}
}

func TestConcurrentWritesUseIsolatedIndexes(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, ref := range []string{"refs/etude/runs/a", "refs/etude/runs/b"} {
		ref := ref
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.WriteCommit(ctx, ref, map[string][]byte{
				"manifest.json": []byte(ref),
			}, WriteOptions{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent WriteCommit returned error: %v", err)
		}
	}

	refs, err := store.List(ctx, "refs/etude/runs")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	want := []string{"refs/etude/runs/a", "refs/etude/runs/b"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
	assertClean(t, repo)
}

func TestFallbackIdentityAllowsCommitWithoutRepoConfig(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	commit, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte("{}"),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit returned error: %v", err)
	}

	author := strings.TrimSpace(git(t, repo, "show", "-s", "--format=%an <%ae> %aI", commit))
	if author != "etude <etude@example.invalid> 1970-01-01T00:00:00Z" {
		t.Fatalf("author = %q", author)
	}
}

func TestRepoConfigIdentityWinsOverFallbackIdentity(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	git(t, repo, "config", "user.name", "Configured User")
	git(t, repo, "config", "user.email", "configured@example.invalid")
	store := New(repo)

	commit, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"manifest.json": []byte("{}"),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit returned error: %v", err)
	}

	author := strings.TrimSpace(git(t, repo, "show", "-s", "--format=%an <%ae> %aI", commit))
	if author != "Configured User <configured@example.invalid> 1970-01-01T00:00:00Z" {
		t.Fatalf("author = %q", author)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	unsetEnv(t, "GIT_AUTHOR_NAME")
	unsetEnv(t, "GIT_AUTHOR_EMAIL")
	unsetEnv(t, "GIT_AUTHOR_DATE")
	unsetEnv(t, "GIT_COMMITTER_NAME")
	unsetEnv(t, "GIT_COMMITTER_EMAIL")
	unsetEnv(t, "GIT_COMMITTER_DATE")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git(t, dir, "init")
	// Leave user.name and user.email unset in most tests so the store fallback
	// identity is exercised.
	return dir
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	oldValue, hadOldValue := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadOldValue {
			os.Setenv(key, oldValue)
		} else {
			os.Unsetenv(key)
		}
	})
}

func assertClean(t *testing.T, repo string) {
	t.Helper()
	status := git(t, repo, "status", "--short")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("repo status is dirty:\n%s", status)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=",
		"GIT_AUTHOR_EMAIL=",
		"GIT_AUTHOR_DATE=",
		"GIT_COMMITTER_NAME=",
		"GIT_COMMITTER_EMAIL=",
		"GIT_COMMITTER_DATE=",
		"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "global.gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// initRepoFormat creates a temp git repo with the given object format.
// If git does not support --object-format=<format> the test is skipped.
func initRepoFormat(t *testing.T, format string) string {
	t.Helper()
	dir := t.TempDir()
	unsetEnv(t, "GIT_AUTHOR_NAME")
	unsetEnv(t, "GIT_AUTHOR_EMAIL")
	unsetEnv(t, "GIT_AUTHOR_DATE")
	unsetEnv(t, "GIT_COMMITTER_NAME")
	unsetEnv(t, "GIT_COMMITTER_EMAIL")
	unsetEnv(t, "GIT_COMMITTER_DATE")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	cmd := exec.Command("git", "init", "--object-format="+format, dir)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, "global.gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("git init --object-format=%s not supported (git too old?): %v\n%s", format, err, out)
	}
	return dir
}

func TestValidateOIDAcceptsSHA1AndSHA256(t *testing.T) {
	sha1 := strings.Repeat("a", 40)
	sha256 := strings.Repeat("b", 64)

	tests := []struct {
		name    string
		oid     string
		wantErr bool
		errMsg  string
	}{
		{"40-hex lowercase", sha1, false, ""},
		{"64-hex lowercase", sha256, false, ""},
		{"39 chars", strings.Repeat("a", 39), true, "40 or 64 hex characters"},
		{"41 chars", strings.Repeat("a", 41), true, "40 or 64 hex characters"},
		{"63 chars", strings.Repeat("a", 63), true, "40 or 64 hex characters"},
		{"65 chars", strings.Repeat("a", 65), true, "40 or 64 hex characters"},
		{"0 chars", "", true, "40 or 64 hex characters"},
		{"uppercase 40", strings.Repeat("A", 40), true, "lowercase hex"},
		{"uppercase 64", strings.Repeat("A", 64), true, "lowercase hex"},
		{"non-hex 40", strings.Repeat("g", 40), true, "lowercase hex"},
		{"non-hex 64", strings.Repeat("g", 64), true, "lowercase hex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOID(tt.oid)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateOID(%q) = nil, want error containing %q", tt.oid, tt.errMsg)
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("validateOID(%q) error = %q, want it to contain %q", tt.oid, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("validateOID(%q) = %v, want nil", tt.oid, err)
				}
			}
		})
	}
}

func TestValidateFilePathRejectsControlChars(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"newline", "bad\npath", true},
		{"tab", "bad\tpath", true},
		{"carriage return", "bad\rpath", true},
		{"SOH control", "bad\x01path", true},
		{"NUL", "bad\x00path", true},
		{"DEL control", "bad\x7fpath", true},
		{"C1 control U+0085", "badpath", true},
		{"valid manifest.json", "manifest.json", false},
		{"valid nested path", "artifacts/sha256/ab/cd", false},
		{"valid artifacts path", "artifacts/sha256/ab/abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFilePath(tt.path)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidPath) {
					t.Fatalf("validateFilePath(%q) = %v, want ErrInvalidPath", tt.path, err)
				}
			} else {
				if err != nil {
					t.Fatalf("validateFilePath(%q) = %v, want nil", tt.path, err)
				}
			}
		})
	}
}

func TestWriteCommitRejectsControlCharFilePath(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	_, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{
		"bad\npath.json": []byte("data"),
	}, WriteOptions{})
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("WriteCommit with control-char path = %v, want ErrInvalidPath", err)
	}

	refs := git(t, repo, "for-each-ref", "--format=%(refname)", "refs/etude")
	if strings.TrimSpace(refs) != "" {
		t.Fatalf("control-char path write created refs:\n%s", refs)
	}
}

func TestWriteCommitAndCASInSHA256Repo(t *testing.T) {
	repo := initRepoFormat(t, "sha256")
	ctx := context.Background()
	store := New(repo)

	// Create: empty-old path must work in a SHA-256 repo.
	commit1, err := store.WriteCommit(ctx, "refs/etude/runs/sha256-run", map[string][]byte{
		"manifest.json": []byte(`{"version":1}`),
	}, WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit (create) returned error: %v", err)
	}
	if len(commit1) != 64 {
		t.Fatalf("expected 64-hex commit OID, got %q (len %d)", commit1, len(commit1))
	}

	// Resolve must return the same 64-hex OID.
	resolved, err := store.Resolve(ctx, "refs/etude/runs/sha256-run")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != commit1 {
		t.Fatalf("Resolve = %q, want %q", resolved, commit1)
	}

	// CAS advance with 64-hex ExpectedOld.
	commit2, err := store.WriteCommit(ctx, "refs/etude/runs/sha256-run", map[string][]byte{
		"manifest.json": []byte(`{"version":2}`),
	}, WriteOptions{ExpectedOld: commit1})
	if err != nil {
		t.Fatalf("CAS WriteCommit returned error: %v", err)
	}
	if len(commit2) != 64 {
		t.Fatalf("expected 64-hex commit OID, got %q (len %d)", commit2, len(commit2))
	}
	if commit2 == commit1 {
		t.Fatal("CAS WriteCommit did not advance the commit")
	}

	// Stale CAS must return ErrStaleRef.
	_, err = store.WriteCommit(ctx, "refs/etude/runs/sha256-run", map[string][]byte{
		"manifest.json": []byte(`{"version":3}`),
	}, WriteOptions{ExpectedOld: commit1})
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("stale CAS error = %v, want ErrStaleRef", err)
	}

	// ReadCommitFile with 64-hex commit.
	content, err := store.ReadCommitFile(ctx, commit1, "manifest.json")
	if err != nil {
		t.Fatalf("ReadCommitFile returned error: %v", err)
	}
	if string(content) != `{"version":1}` {
		t.Fatalf("ReadCommitFile content = %q", content)
	}
}

// ---------------------------------------------------------------------------
// DeleteRef tests
// ---------------------------------------------------------------------------

func TestDeleteRefDeletesExistingRunRef(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	if _, err := store.WriteCommit(ctx, "refs/etude/runs/run-1", map[string][]byte{"manifest.json": []byte("{}")}, WriteOptions{}); err != nil {
		t.Fatalf("WriteCommit returned error: %v", err)
	}

	if err := store.DeleteRef(ctx, "refs/etude/runs/run-1"); err != nil {
		t.Fatalf("DeleteRef returned error: %v", err)
	}

	if _, err := store.Resolve(ctx, "refs/etude/runs/run-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve after DeleteRef = %v, want ErrNotFound", err)
	}
}

func TestDeleteRefErrorsOnMissingRef(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	err := store.DeleteRef(ctx, "refs/etude/runs/nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRef missing ref = %v, want ErrNotFound", err)
	}
}

func TestDeleteRefRejectsOutOfNamespaceRef(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	for _, ref := range []string{
		"refs/heads/main",
		"refs/etude/other/id",
		"HEAD",
	} {
		err := store.DeleteRef(ctx, ref)
		if !errors.Is(err, ErrInvalidRef) {
			t.Fatalf("DeleteRef(%q) = %v, want ErrInvalidRef", ref, err)
		}
	}
}

func TestDeleteRefRejectsSymbolicRef(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	// Create a real ref to use as the symbolic ref target.
	seed, err := store.WriteCommit(ctx, "refs/etude/evals/seed", map[string][]byte{"data": []byte("seed")}, WriteOptions{})
	if err != nil {
		t.Fatalf("WriteCommit seed returned error: %v", err)
	}
	git(t, repo, "update-ref", "refs/heads/main", seed)
	git(t, repo, "symbolic-ref", "refs/etude/runs/symlink", "refs/heads/main")

	err = store.DeleteRef(ctx, "refs/etude/runs/symlink")
	if !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("DeleteRef symbolic ref = %v, want ErrInvalidRef", err)
	}
}

func TestDeleteRefLeavesOtherRefsIntact(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	for _, ref := range []string{"refs/etude/runs/a", "refs/etude/runs/b", "refs/etude/runs/c"} {
		if _, err := store.WriteCommit(ctx, ref, map[string][]byte{"manifest.json": []byte(ref)}, WriteOptions{}); err != nil {
			t.Fatalf("WriteCommit(%s) returned error: %v", ref, err)
		}
	}

	if err := store.DeleteRef(ctx, "refs/etude/runs/b"); err != nil {
		t.Fatalf("DeleteRef returned error: %v", err)
	}

	refs, err := store.List(ctx, "refs/etude/runs")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	want := []string{"refs/etude/runs/a", "refs/etude/runs/c"}
	if len(refs) != len(want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
	for i, r := range refs {
		if r != want[i] {
			t.Fatalf("refs[%d] = %q, want %q", i, r, want[i])
		}
	}
}

// ---- refs/etude/retros namespace tests ----

// TestRetrosNamespaceAccepted verifies that the retros namespace is accepted by
// WriteCommit, Resolve, List, and DeleteRef, parallel to the runs/evals namespaces.
func TestRetrosNamespaceAccepted(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	// WriteCommit and Resolve accept refs/etude/retros/<id>.
	commit, err := store.WriteCommit(ctx, "refs/etude/retros/retro-cohort-r1-20260526T100000Z", map[string][]byte{
		"manifest.json": []byte(`{"retro":true}`),
	}, WriteOptions{Message: "test retro"})
	if err != nil {
		t.Fatalf("WriteCommit retros ref returned error: %v", err)
	}

	resolved, err := store.Resolve(ctx, "refs/etude/retros/retro-cohort-r1-20260526T100000Z")
	if err != nil {
		t.Fatalf("Resolve retros ref returned error: %v", err)
	}
	if resolved != commit {
		t.Errorf("Resolve = %q, want %q", resolved, commit)
	}

	// List with the retros prefix works.
	refs, err := store.List(ctx, "refs/etude/retros")
	if err != nil {
		t.Fatalf("List retros returned error: %v", err)
	}
	want := []string{"refs/etude/retros/retro-cohort-r1-20260526T100000Z"}
	if len(refs) != len(want) || refs[0] != want[0] {
		t.Errorf("List = %#v, want %#v", refs, want)
	}

	// DeleteRef accepts the retros ref.
	if err := store.DeleteRef(ctx, "refs/etude/retros/retro-cohort-r1-20260526T100000Z"); err != nil {
		t.Fatalf("DeleteRef retros ref returned error: %v", err)
	}

	if _, err := store.Resolve(ctx, "refs/etude/retros/retro-cohort-r1-20260526T100000Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve after DeleteRef = %v, want ErrNotFound", err)
	}
}

// TestUnknownNamespaceRejected is a regression guard: unknown namespaces under
// refs/etude/ must still be rejected even after adding retrosNS.
func TestUnknownNamespaceRejected(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	store := New(repo)

	for _, ref := range []string{
		"refs/etude/foo/some-id",
		"refs/etude/retros", // bare namespace (no id) must be rejected
	} {
		_, err := store.WriteCommit(ctx, ref, map[string][]byte{"manifest.json": []byte("{}")}, WriteOptions{})
		if !errors.Is(err, ErrInvalidRef) {
			t.Errorf("WriteCommit(%q) = %v, want ErrInvalidRef", ref, err)
		}
	}
}

// TestMain handles the helper-process pattern for TestStdoutStderrSplit.
// When GO_WANT_HELPER_PROCESS=1 the binary acts as a fake git stub.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		runHelperProcess()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runHelperProcess is the fake git for the stdout/stderr split test.
// It prints a warning to stderr and, for "rev-parse", the OID to stdout.
// All other subcommands succeed silently (needed for validateRef, rejectSymbolicRef, etc).
func runHelperProcess() {
	args := os.Args
	// Find the git subcommand: skip the test binary path and any "--" separator.
	subArgs := args[1:]
	for len(subArgs) > 0 && (subArgs[0] == "--" || subArgs[0] == "") {
		subArgs = subArgs[1:]
	}
	if len(subArgs) == 0 {
		os.Exit(0)
	}
	sub := subArgs[0]

	// Emit a warning on stderr for all calls so we can prove it doesn't bleed.
	fmt.Fprintf(os.Stderr, "warning: fake git stderr noise for %s\n", sub)

	switch sub {
	case "rev-parse":
		// Return a valid 40-hex OID on stdout only.
		fmt.Fprintf(os.Stdout, "aabbccddeeff00112233445566778899aabbccdd\n")
	case "check-ref-format":
		// Accept any ref.
		os.Exit(0)
	case "symbolic-ref":
		// Exit non-zero so rejectSymbolicRef passes (no symbolic ref).
		os.Exit(1)
	default:
		// Everything else (read-tree, hash-object, update-index, write-tree,
		// commit-tree, update-ref, cat-file, config, for-each-ref): exit 0 silently.
		os.Exit(0)
	}
}

func TestStdoutStderrSplit(t *testing.T) {
	// Build the helper-process path: the current test binary with a flag that
	// triggers runHelperProcess() in TestMain.
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Use a real git repo for RepoDir so validateRef doesn't fail at the FS level.
	repo := initRepo(t)
	store := Store{
		RepoDir: repo,
		GitPath: selfExe,
	}

	// Set the env var that triggers the helper-process path.
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	// Resolve calls git rev-parse under the hood; the stub emits stderr noise
	// but returns a clean OID on stdout.
	// validateRef calls check-ref-format (exits 0 via helper) and
	// rejectSymbolicRef calls symbolic-ref (exits 1 via helper, meaning no sym ref).
	oid, err := store.Resolve(context.Background(), "refs/etude/runs/any")
	if err != nil {
		t.Fatalf("Resolve via stub = %v", err)
	}
	want := "aabbccddeeff00112233445566778899aabbccdd"
	if oid != want {
		t.Fatalf("Resolve = %q, want %q (stderr contamination?)", oid, want)
	}
}
