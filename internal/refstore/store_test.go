package refstore

import (
	"context"
	"errors"
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
