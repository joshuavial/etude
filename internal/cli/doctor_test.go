//go:build !windows

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/workflow"
)

func TestDoctorIsRegisteredSubcommand(t *testing.T) {
	stdout, stderr, err := execute("doctor", "--help")
	if err != nil {
		t.Fatalf("doctor --help: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "read-only") && !strings.Contains(stdout, "safe and working") {
		t.Fatalf("doctor help does not describe the command: %s", stdout)
	}
}

func TestDoctorHealthyInitConfiguration(t *testing.T) {
	repo, _ := doctorRepo(t)
	chdir(t, repo)

	stdout, stderr, err := execute("doctor")
	if err != nil {
		t.Fatalf("doctor: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	for _, want := range []string{
		"OK config",
		"OK fetch-refspec[origin]",
		"OK fetch-mirror[origin]",
		"OK push-refspec[origin]",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("healthy output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "FAIL") {
		t.Fatalf("healthy doctor emitted FAIL:\n%s", stdout)
	}
}

func TestDoctorDangerousFetchFailsWithExactScopedRemediation(t *testing.T) {
	repo, _ := doctorRepo(t)
	danger := "+refs/etude/*:refs/etude/*"
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", danger)
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil {
		t.Fatalf("doctor succeeded with dangerous fetch:\n%s", stdout)
	}
	realRepo, resolveErr := filepath.EvalSymlinks(repo)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	origin := filepath.Join(realRepo, ".git", "config")
	want := "git config --file '" + origin + "' --unset-all 'remote.origin.fetch' '" + regexpLiteral(danger) + "'"
	if !strings.Contains(stdout, "FAIL fetch-refspec[origin]") || !strings.Contains(stdout, want) {
		t.Fatalf("dangerous finding/remediation missing\nwant: %s\ngot:\n%s", want, stdout)
	}
}

func TestDoctorMissingPushRefspecFailsWithInitRemediation(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--unset-all", "remote.origin.push")
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil {
		t.Fatalf("doctor succeeded without push mappings:\n%s", stdout)
	}
	if !strings.Contains(stdout, "FAIL push-refspec[origin]") || !strings.Contains(stdout, "remedy: etude init --remote 'origin'") {
		t.Fatalf("missing push finding/remediation absent:\n%s", stdout)
	}
}

func TestDoctorRejectsNestedRemoteMirrorLayout(t *testing.T) {
	repo, _ := doctorRepo(t)
	for _, value := range doctorConfigValues(t, repo, "remote.origin.fetch") {
		if strings.Contains(value, "refs/etude-mirror/") {
			gitCapture(t, repo, "config", "--local", "--unset-all", "remote.origin.fetch", "^"+regexpLiteral(value)+"$")
		}
	}
	for _, kind := range []string{"runs", "retros", "evals"} {
		gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "+refs/etude/"+kind+"/*:refs/etude/remotes/origin/"+kind+"/*")
	}
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil {
		t.Fatalf("doctor accepted nested mirror layout:\n%s", stdout)
	}
	if !strings.Contains(stdout, "FAIL fetch-refspec[origin]") ||
		!strings.Contains(stdout, "WARN fetch-mirror[origin]") ||
		strings.Contains(stdout, "OK fetch-mirror[origin]") {
		t.Fatalf("nested layout was not diagnosed as both dangerous and missing:\n%s", stdout)
	}
}

func TestDoctorOldSingleNamespaceLayoutFails(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "+refs/etude/runs/*:refs/etude/runs/*")
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL fetch-refspec[origin]") {
		t.Fatalf("old single namespace was not rejected: err=%v\n%s", err, stdout)
	}
}

func TestDoctorMissingRubricRequiresHumanAuthorship(t *testing.T) {
	repo, _ := doctorRepo(t)
	rubric := filepath.Join(repo, ".etude", "evals", "plan-rubric.md")
	if err := os.Remove(rubric); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil {
		t.Fatalf("doctor succeeded with missing rubric:\n%s", stdout)
	}
	if !strings.Contains(stdout, `stage "plan" rubric "evals/plan-rubric.md"`) || !strings.Contains(stdout, "HUMAN AUTHORSHIP REQUIRED") {
		t.Fatalf("missing rubric is not precise/human-authored:\n%s", stdout)
	}
	if strings.Contains(stdout, "touch ") || strings.Contains(stdout, "mkdir ") {
		t.Fatalf("doctor fabricated a rubric repair command:\n%s", stdout)
	}
}

func TestDoctorReportsMissingAndMalformedConfigIndependently(t *testing.T) {
	repo, _ := doctorRepo(t)
	if err := os.Remove(filepath.Join(repo, ".etude", "workflow.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".etude", "registry.yaml"), []byte("seats: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "workflow.yaml cannot be read") || !strings.Contains(stdout, "registry.yaml does not parse") {
		t.Fatalf("independent config errors were suppressed: err=%v\n%s", err, stdout)
	}
}

func TestDoctorChecksWorkflowPathsWhenRegistryIsMalformed(t *testing.T) {
	repo, _ := doctorRepo(t)
	if err := os.Remove(filepath.Join(repo, ".etude", "evals", "plan-rubric.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".etude", "registry.yaml"), []byte("seats: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "registry.yaml does not parse") || !strings.Contains(stdout, "FAIL path[rubric]") {
		t.Fatalf("workflow path failure was suppressed by registry parse error: err=%v\n%s", err, stdout)
	}
}

func TestDoctorConfigCrossResolutionFails(t *testing.T) {
	repo, _ := doctorRepo(t)
	path := filepath.Join(repo, ".etude", "workflow.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), "    skill: dev-planner\n", "    skill: dev-planner\n    runner:\n      name: absent-seat\n", 1)
	if updated == string(data) {
		t.Fatal("default workflow has no plan skill to augment")
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, `references undefined registry seat "absent-seat"`) {
		t.Fatalf("cross-resolution failure absent: err=%v\n%s", err, stdout)
	}
}

func TestDoctorSeatResolutionLooksThroughAdapterAndEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(dir, "scripts", "seat-adapter.sh")
	writeExecutable(t, adapter)
	base := map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH")}

	for _, invoke := range []string{
		"scripts/seat-adapter.sh codex missing-reviewer exec --json",
		"env -u TOKEN -- scripts/seat-adapter.sh opus missing-reviewer -p",
	} {
		res := doctorResolveCommand(invoke, dir, base)
		if res.err == "" || !strings.Contains(res.err, `real executable "missing-reviewer"`) {
			t.Fatalf("%q resolved the wrapper instead of rejecting missing reviewer: %+v", invoke, res)
		}
	}

	reviewer := filepath.Join(dir, "reviewer")
	writeExecutable(t, reviewer)
	res := doctorResolveCommand("env -- scripts/seat-adapter.sh codex reviewer exec", dir, base)
	if res.err != "" || res.adapter == "" || res.resolved != reviewer {
		t.Fatalf("adapter/reviewer resolution = %+v", res)
	}
}

func TestDoctorAdapterPreservesQuotedReviewerPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(dir, "scripts", "seat-adapter.sh"))
	reviewer := filepath.Join(dir, "my reviewer")
	writeExecutable(t, reviewer)
	res := doctorResolveCommand(`scripts/seat-adapter.sh codex "`+reviewer+`" exec`, dir, map[string]string{"PATH": dir})
	if res.err != "" || res.adapter == "" || res.resolved != reviewer {
		t.Fatalf("quoted adapter reviewer resolution = %+v", res)
	}
}

func TestDoctorOpaqueShellWrapperDoesNotClaimReviewerReachability(t *testing.T) {
	res := doctorResolveCommand("sh -c definitely-missing-reviewer", t.TempDir(), map[string]string{"PATH": os.Getenv("PATH")})
	if res.err != "" || !res.opaqueWrapper || res.resolved == "" {
		t.Fatalf("opaque wrapper resolution = %+v", res)
	}
}

func TestDoctorOpaqueInterpreterAndAdapterChainsAreNotClaimed(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "ksh"))
	res := doctorResolveCommand("ksh -c missing-reviewer", dir, map[string]string{"PATH": dir})
	if res.err != "" || !res.opaqueWrapper {
		t.Fatalf("ksh wrapper resolution = %+v", res)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(root, "scripts", "seat-adapter.sh"))
	writeExecutable(t, filepath.Join(root, "sh"))
	res = doctorResolveCommand("scripts/seat-adapter.sh codex sh -c missing-reviewer", root, map[string]string{"PATH": root})
	if res.err != "" || res.adapter == "" || !res.opaqueWrapper {
		t.Fatalf("adapter wrapper resolution = %+v", res)
	}
}

func TestDoctorUnrelatedSeatAdapterBasenameIsNotReinterpreted(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(t.TempDir(), "seat-adapter.sh")
	writeExecutable(t, other)
	res := doctorResolveCommand(other+" --ordinary-flag", root, map[string]string{"PATH": os.Getenv("PATH")})
	if res.err != "" || res.adapter != "" || res.resolved != other {
		t.Fatalf("unrelated executable was treated as Etude adapter: %+v", res)
	}
}

func TestDoctorDoesNotCaseFoldEnvOrGuessReviewerFlags(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ENV", "codex", "python3", "python3.12", "python.", "python3.", "python3..12"} {
		writeExecutable(t, filepath.Join(dir, name))
	}
	base := map[string]string{"PATH": dir}
	upper := doctorResolveCommand("ENV", dir, base)
	if upper.err != "" || upper.resolved != filepath.Join(dir, "ENV") || upper.indeterminate != "" {
		t.Fatalf("POSIX ENV executable was treated as env: %+v", upper)
	}
	codex := doctorResolveCommand("codex -c model_reasoning_effort=high", dir, base)
	if codex.err != "" || codex.opaqueWrapper {
		t.Fatalf("reviewer -c flag was treated as an interpreter: %+v", codex)
	}
	python := doctorResolveCommand("python3 -I -c 'import missing_reviewer'", dir, base)
	if python.err != "" || !python.opaqueWrapper {
		t.Fatalf("Python interpreter was claimed as reviewer: %+v", python)
	}
	versionedPython := doctorResolveCommand("python3.12 -I -c 'import missing_reviewer'", dir, base)
	if versionedPython.err != "" || !versionedPython.opaqueWrapper {
		t.Fatalf("versioned Python interpreter was claimed as reviewer: %+v", versionedPython)
	}
	for _, name := range []string{"python.", "python3.", "python3..12"} {
		malformed := doctorResolveCommand(name+" -c script", dir, base)
		if malformed.err != "" || malformed.opaqueWrapper {
			t.Fatalf("ordinary executable %q was classified as a versioned interpreter: %+v", name, malformed)
		}
	}
}

func TestDoctorShellQuoteIsLiteral(t *testing.T) {
	value := `+refs/etude/runs/o'hare/*:refs/etude/runs/o'hare/*`
	entry := doctorConfigEntry{origin: "file:/tmp/config with ' quote", value: value}
	command := doctorConfigUnsetRemediation("/repo", "remote.origin.fetch", entry)
	want := `git config --file '/tmp/config with '"'"' quote' --unset-all 'remote.origin.fetch' '^\+refs/etude/runs/o'"'"'hare/\*:refs/etude/runs/o'"'"'hare/\*$'`
	if command != want {
		t.Fatalf("quoted command\n got: %s\nwant: %s", command, want)
	}
}

func TestDoctorDoesNotMutateRepository(t *testing.T) {
	repo, _ := doctorRepo(t)
	before := snapshotDoctorRepo(t, repo)
	chdir(t, repo)
	if stdout, stderr, err := execute("doctor"); err != nil {
		t.Fatalf("doctor: %v\n%s\n%s", err, stdout, stderr)
	}
	after := snapshotDoctorRepo(t, repo)
	if before != after {
		t.Fatalf("doctor mutated repository\nbefore %s\nafter  %s", before, after)
	}
}

func TestDoctorDoesNotContactConfiguredRemote(t *testing.T) {
	repo, _ := doctorRepo(t)
	marker := filepath.Join(t.TempDir(), "remote-helper-ran")
	gitCapture(t, repo, "remote", "set-url", "origin", "ext::touch "+marker)
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil {
		t.Fatalf("offline doctor failed because of unreachable URL: %v\n%s", err, stdout)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("doctor executed configured transport; marker stat = %v", statErr)
	}
	if !strings.Contains(stdout, "NOT CHECKED") || !strings.Contains(stdout, "last fetch time Git does not record") {
		t.Fatalf("offline/staleness limit was not reported:\n%s", stdout)
	}
}

func TestDoctorReportsBothMalformedConfigFiles(t *testing.T) {
	repo, _ := doctorRepo(t)
	for _, name := range []string{"workflow.yaml", "registry.yaml"} {
		if err := os.WriteFile(filepath.Join(repo, ".etude", name), []byte("[not: valid"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "workflow.yaml does not parse") || !strings.Contains(stdout, "registry.yaml does not parse") {
		t.Fatalf("independent parse failures were suppressed: err=%v\n%s", err, stdout)
	}
}

func TestDoctorMissingBareReviewerFails(t *testing.T) {
	root := t.TempDir()
	res := doctorResolveCommand("definitely-not-installed", root, map[string]string{"PATH": root})
	if res.err == "" {
		t.Fatalf("missing bare reviewer resolved: %+v", res)
	}
	findings := []doctorFinding{}
	wf := workflow.Workflow{}
	reg := registry.Registry{Seats: map[string]registry.Seat{"missing": {Invoke: "definitely-not-installed"}}}
	(doctorRunner{}).checkReferencedPathsAndCommands(root, &wf, &reg, &findings)
	if len(findings) == 0 || findings[len(findings)-1].status != doctorFail {
		t.Fatalf("missing bare reviewer did not fail: %+v", findings)
	}
}

func TestDoctorCommandResolutionPreservesQuotedAssignment(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "reviewer"))
	res := doctorResolveCommand("env TOKEN='a b' PATH="+bin+" reviewer", root, map[string]string{"PATH": os.Getenv("PATH")})
	if res.err != "" || res.env["TOKEN"] != "a b" || filepath.Base(res.resolved) != "reviewer" {
		t.Fatalf("quoted invocation resolution = %+v", res)
	}
}

func TestDoctorControlDetectionIncludesTabAndEscape(t *testing.T) {
	for _, value := range []string{"https://example.test/\tbad", "https://example.test/\x1bbad"} {
		if !doctorContainsControl(value) {
			t.Fatalf("control-bearing value accepted: %q", value)
		}
	}
}

func TestDoctorEscapesTerminalControlBytes(t *testing.T) {
	got := doctorTerminalSafe("evil\x1b[2J")
	if got != `evil\x1b[2J` {
		t.Fatalf("terminal-safe rendering = %q", got)
	}
}

func TestDoctorReportsUnpushedRunRef(t *testing.T) {
	repo, _ := doctorRepo(t)
	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/local-only", head)
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err != nil {
		t.Fatalf("unpushed run ref is a warning, got %v:\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "refs/etude/runs/local-only is absent or behind in the last fetched mirror") || !strings.Contains(stdout, "last update time NOT RECORDED) absent") || !strings.Contains(stdout, "current remote may already match or be newer") || !strings.Contains(stdout, "remedy: etude sync --remote 'origin'") {
		t.Fatalf("unpushed ref/remedy absent:\n%s", stdout)
	}
}

func TestDoctorComparesAgainstLocalMirrorWithoutClaimingFreshness(t *testing.T) {
	repo, _ := doctorRepo(t)
	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/mirrored", head)
	gitCapture(t, repo, "update-ref", "refs/etude-mirror/origin/runs/mirrored", head)
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || !strings.Contains(stdout, "match the last fetched mirror") || !strings.Contains(stdout, "current remote may have changed") {
		t.Fatalf("mirror comparison overclaimed freshness: err=%v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "remedy: etude sync") {
		t.Fatalf("matching mirror received sync advice:\n%s", stdout)
	}
}

func TestDoctorPinsLandedSiblingMirrorNamespace(t *testing.T) {
	if got := refstore.MirrorPrefix("origin", "runs"); got != "refs/etude-mirror/origin/runs/" {
		t.Fatalf("mirror prefix = %q", got)
	}
}

func TestDoctorBroadPartialGlobAlwaysFails(t *testing.T) {
	repo, _ := doctorRepo(t)
	partial := "+refs/*/unpushed:refs/*/unpushed"
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", partial)
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL fetch-refspec[origin]") {
		t.Fatalf("broad hazard did not fail before a future ref is captured: err=%v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "HUMAN AUTHORSHIP REQUIRED") {
		t.Fatalf("broad user mapping received no human-only remediation:\n%s", stdout)
	}
}

func TestDoctorMissingRunnerScriptFails(t *testing.T) {
	repo, _ := doctorRepo(t)
	registryPath := filepath.Join(repo, ".etude", "registry.yaml")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "claude -p --model opus", "scripts/missing-runner.sh", 1))
	if err := os.WriteFile(registryPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)

	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL command[seat") || !strings.Contains(stdout, "regular executable runner script") {
		t.Fatalf("missing referenced runner script was not a failure: err=%v\n%s", err, stdout)
	}
}

func TestDoctorPushCoverageRejectsMisleadingShapes(t *testing.T) {
	for name, value := range map[string]string{
		"delete":        ":refs/etude/runs/x",
		"single":        "refs/etude/runs/x:refs/etude/runs/x",
		"name-changing": "refs/etude/*:refs/archive/*",
	} {
		t.Run(name, func(t *testing.T) {
			repo, _ := doctorRepo(t)
			gitCapture(t, repo, "config", "--local", "--unset-all", "remote.origin.push")
			gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", value)
			chdir(t, repo)
			stdout, _, err := execute("doctor")
			if err == nil || !strings.Contains(stdout, "FAIL push-refspec[origin]") || !strings.Contains(stdout, name) {
				t.Fatalf("shape %q was not diagnosed: err=%v\n%s", name, err, stdout)
			}
		})
	}
}

func TestDoctorMirrorPushBooleanProvidesCoverage(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--unset-all", "remote.origin.push")
	gitCapture(t, repo, "config", "--local", "remote.origin.mirror", "yes")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || !strings.Contains(stdout, "mirror-push semantics cover") {
		t.Fatalf("mirror push was not accepted: err=%v\n%s", err, stdout)
	}
}

func TestDoctorExplicitEmptyMirrorBooleanIsFalse(t *testing.T) {
	repo := initCaptureRepo(t)
	gitCapture(t, repo, "config", "--local", "remote.origin.mirror", "")
	value, err := (doctorGit{root: repo}).configBool(context.Background(), "remote.origin.mirror")
	if err != nil || value {
		t.Fatalf("explicit empty Git boolean = %v, %v; want false", value, err)
	}
}

func TestDoctorEnvChdirIsNotGuessed(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "tools")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "reviewer"))
	res := doctorResolveCommand("env -i --chdir tools PATH=. -- reviewer", root, map[string]string{"PATH": os.Getenv("PATH")})
	if res.err != "" || !strings.Contains(res.indeterminate, "NOT CHECKED") {
		t.Fatalf("implementation-specific env chdir was guessed: %+v", res)
	}
}

func TestDoctorGitAllowlistRejectsWriteCapableCalls(t *testing.T) {
	for _, args := range [][]string{{"fetch", "origin"}, {"ls-remote", "origin"}, {"remote", "get-url", "origin"}, {"update-ref", "refs/etude/runs/x", "HEAD"}, {"config", "--unset", "x"}} {
		if err := doctorValidateGitArgs(args); err == nil {
			t.Errorf("allowlist accepted %q", args)
		}
	}
}

func TestDoctorRubricDirectoryFails(t *testing.T) {
	repo, _ := doctorRepo(t)
	path := filepath.Join(repo, ".etude", "evals", "plan-rubric.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "not a regular file") {
		t.Fatalf("rubric directory accepted: err=%v\n%s", err, stdout)
	}
}

func TestDoctorExactSentinelsDoNotCountAsNamespaceCoverage(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--unset-all", "remote.origin.push")
	for _, kind := range []string{"runs", "retros", "evals"} {
		ref := "refs/etude/" + kind + "/__doctor_probe__"
		gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", ref+":"+ref)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL push-refspec[origin]") {
		t.Fatalf("exact sentinel mappings counted as full coverage: err=%v\n%s", err, stdout)
	}
}

func TestDoctorNegativeFetchCancelsMirrorCoverage(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "^refs/etude/runs/*")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil {
		t.Fatalf("negative mirror exclusion should warn, not fail: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "WARN fetch-mirror[origin]") || !strings.Contains(stdout, "negative fetch entries exclude runs") || !strings.Contains(stdout, "HUMAN AUTHORSHIP REQUIRED") {
		t.Fatalf("negative exclusion was ignored or given a fake init fix:\n%s", stdout)
	}
}

func TestDoctorAcceptsHEADPushSource(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", "HEAD:refs/heads/backup")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || strings.Contains(stdout, `invalid push refspec "HEAD:refs/heads/backup"`) {
		t.Fatalf("valid HEAD push source rejected: err=%v\n%s", err, stdout)
	}
}

func TestDoctorAcceptsGitMatchingBranchesAndFetchHEAD(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", ":")
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "HEAD:refs/remotes/origin/HEAD")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || strings.Contains(stdout, "invalid push refspec") || strings.Contains(stdout, "invalid fetch refspec") {
		t.Fatalf("valid Git refspec forms rejected: err=%v\n%s", err, stdout)
	}
}

func TestDoctorFindsPushURLOnlyRemote(t *testing.T) {
	repo, remote := doctorRepo(t)
	gitCapture(t, repo, "remote", "remove", "origin")
	gitCapture(t, repo, "config", "--local", "remote.backup.pushurl", remote)
	for _, kind := range []string{"runs", "retros", "evals"} {
		gitCapture(t, repo, "config", "--local", "--add", "remote.backup.push", "+refs/etude/"+kind+"/*:refs/etude/"+kind+"/*")
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "OK push-refspec[backup]") || !strings.Contains(stdout, "FAIL remote-config[backup]") {
		t.Fatalf("pushurl-only remote did not report missing fetch URL: err=%v\n%s", err, stdout)
	}
}

func TestDoctorRemoteWithoutFetchURLFailsWithoutSyncAdvice(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "remote", "remove", "origin")
	for _, kind := range []string{"runs", "retros", "evals"} {
		gitCapture(t, repo, "config", "--local", "--add", "remote.backup.push", "+refs/etude/"+kind+"/*:refs/etude/"+kind+"/*")
		gitCapture(t, repo, "config", "--local", "--add", "remote.backup.fetch", "+refs/etude/"+kind+"/*:refs/etude-mirror/backup/"+kind+"/*")
	}
	gitCapture(t, repo, "config", "--local", "remote.backup.mirror", "false")
	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/local-only", head)
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL remote-config[backup]") || !strings.Contains(stdout, "no fetch URL is configured") {
		t.Fatalf("missing fetch URL was not diagnosed: err=%v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "remedy: etude sync") {
		t.Fatalf("unusable remote received sync advice:\n%s", stdout)
	}
}

func TestDoctorPreservesRemoteNameWhitespace(t *testing.T) {
	repo, remote := doctorRepo(t)
	configPath := filepath.Join(repo, ".git", "config")
	config, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	config = append(config, []byte("\n[remote \"my remote\"]\n\turl = "+remote+"\n\tpush = +refs/etude/runs/*:refs/etude/runs/*\n\tpush = +refs/etude/retros/*:refs/etude/retros/*\n\tpush = +refs/etude/evals/*:refs/etude/evals/*\n")...)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || !strings.Contains(stdout, "OK push-refspec[my remote]") || strings.Contains(stdout, "run-refs[my]") {
		t.Fatalf("remote name containing a space was split: err=%v\nremote=%s\n%s", err, remote, stdout)
	}
}

func TestDoctorRejectsControlBearingRemoteNameBeforeRemediation(t *testing.T) {
	repo, remote := doctorRepo(t)
	configPath := filepath.Join(repo, ".git", "config")
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := fmt.Fprintf(file, "\n[remote \"bad\tname\"]\n\turl = %s\n", remote)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append config: %v / %v", writeErr, closeErr)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL remotes") || strings.Contains(stdout, "remedy: git config") {
		t.Fatalf("control-bearing remote reached runnable remediation: err=%v\n%s", err, stdout)
	}
}

func TestDoctorMultiplePushEndpointsNeverRecommendOverwritingNewerRef(t *testing.T) {
	repo := initCaptureRepo(t)
	first := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	writeFile(t, repo, "later.txt", "later\n")
	gitCapture(t, repo, "add", "later.txt")
	gitCapture(t, repo, "commit", "-m", "later")
	second := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	r := doctorRunner{git: doctorGit{root: repo}}
	findings := []doctorFinding{}
	atRisk := r.checkUnpushedRefs(context.Background(), "origin", map[string]string{"refs/etude/runs/x": first}, []doctorRemoteEndpoint{
		{url: "one", refs: map[string]string{"refs/etude/runs/x": first}},
		{url: "two", refs: map[string]string{"refs/etude/runs/x": second}},
	}, false, &findings)
	if atRisk["refs/etude/runs/x"] {
		t.Fatalf("ancestor-local ref incorrectly classified as unique local data: %+v", findings)
	}
	for _, finding := range findings {
		if strings.Contains(finding.remediation, "etude sync") {
			t.Fatalf("unsafe sync recommendation for newer push endpoint: %+v", findings)
		}
	}
}

func TestDoctorConfiguredPushURLSuppressesSyncAdvice(t *testing.T) {
	repo, remote := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "remote.origin.pushurl", remote+"-push")
	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/local-only", head)
	chdir(t, repo)
	stdout, _, _ := execute("doctor")
	if !strings.Contains(stdout, "PROXY push-endpoint[origin]") || strings.Contains(stdout, "remedy: etude sync") {
		t.Fatalf("pushurl uncertainty received unsafe sync advice:\n%s", stdout)
	}
}

func TestDoctorMultipleRemoteURLsSuppressSyncAdvice(t *testing.T) {
	repo, remote := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.url", remote+"-second")
	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/local-only", head)
	chdir(t, repo)
	stdout, _, _ := execute("doctor")
	if !strings.Contains(stdout, "multiple remote URL entries") || strings.Contains(stdout, "remedy: etude sync") {
		t.Fatalf("multiple push destinations received unsafe sync advice:\n%s", stdout)
	}
}

func TestDoctorEmptyPushURLFails(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "remote.origin.pushurl", "")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL remote-config[origin]") || !strings.Contains(stdout, "configured pushurl is empty") {
		t.Fatalf("empty pushurl was not a failure: err=%v\n%s", err, stdout)
	}
}

func TestDoctorNonUTF8FetchURLMessageIsAccurate(t *testing.T) {
	repo, _ := doctorRepo(t)
	bad := string([]byte{'/', 't', 'm', 'p', '/', 0xff})
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.url", bad)
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "non-UTF-8") {
		t.Fatalf("non-UTF-8 fetch URL received inaccurate diagnosis: err=%v\n%s", err, stdout)
	}
}

func TestDoctorHonorsGitConfigEnvironment(t *testing.T) {
	repo, _ := doctorRepo(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.fetch")
	t.Setenv("GIT_CONFIG_VALUE_0", "+refs/etude/runs/*:refs/etude/runs/*")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL fetch-refspec[origin]") {
		t.Fatalf("environment-supplied dangerous config was ignored: err=%v\n%s", err, stdout)
	}
}

func TestDoctorHonorsGitConfigParameters(t *testing.T) {
	repo, _ := doctorRepo(t)
	t.Setenv("GIT_CONFIG_PARAMETERS", "'remote.origin.fetch=+refs/etude/runs/*:refs/etude/runs/*'")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL fetch-refspec[origin]") {
		t.Fatalf("GIT_CONFIG_PARAMETERS-supplied dangerous config was ignored: err=%v\n%s", err, stdout)
	}
}

func TestDoctorIgnoresGitTraceAndRepositoryRedirection(t *testing.T) {
	repo, _ := doctorRepo(t)
	other := initCaptureRepo(t)
	t.Setenv("GIT_TRACE", "1")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || !strings.Contains(stdout, "OK config") || !strings.Contains(stdout, "OK push-refspec[origin]") {
		t.Fatalf("trace or repository redirection polluted doctor: err=%v\n%s", err, stdout)
	}
}

func TestDoctorBrokenRefStderrCannotBecomeNoRefsOK(t *testing.T) {
	repo, _ := doctorRepo(t)
	broken := filepath.Join(repo, ".git", "refs", "etude", "runs", "broken")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte(strings.Repeat("0", 40)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "FAIL run-refs") {
		t.Fatalf("broken ref diagnostic was discarded: err=%v\n%s", err, stdout)
	}
}

func TestDoctorControlBearingConfigHasNoFabricatedCommand(t *testing.T) {
	got := doctorConfigUnsetRemediation("/repo", "remote.origin.fetch", doctorConfigEntry{origin: "file:/repo/.git/config", value: "+refs/x:refs/y\nmalformed"})
	if !strings.HasPrefix(got, "HUMAN AUTHORSHIP REQUIRED:") || strings.Contains(got, "git config") {
		t.Fatalf("control-bearing remediation = %q", got)
	}
}

func TestDoctorNonUTF8ConfigHasNoFabricatedCommand(t *testing.T) {
	value := string([]byte{'+', 'x', ':', 'y', 0xff})
	got := doctorConfigUnsetRemediation("/repo", "remote.origin.fetch", doctorConfigEntry{origin: "file:/repo/.git/config", value: value})
	if !strings.HasPrefix(got, "HUMAN AUTHORSHIP REQUIRED:") || strings.Contains(got, "git config") {
		t.Fatalf("non-UTF-8 remediation = %q", got)
	}
}

func TestDoctorConfigUnsetQuotingHasIndependentLiteralOracle(t *testing.T) {
	got := doctorConfigUnsetRemediation("/repo", "remote.origin.fetch", doctorConfigEntry{origin: "file:/tmp/a'b/config", value: "+refs/x/*:refs/y/*"})
	want := "git config --file '/tmp/a'\"'\"'b/config' --unset-all 'remote.origin.fetch' '^\\+refs/x/\\*:refs/y/\\*$'"
	if got != want {
		t.Fatalf("config remediation = %q, want independent literal %q", got, want)
	}
}

func TestDoctorTerminalSafeEscapesC1Controls(t *testing.T) {
	if got := doctorTerminalSafe("before\u009bafter"); got != `before\x9bafter` {
		t.Fatalf("terminal-safe C1 output = %q", got)
	}
}

func TestDoctorLimitedBufferReportsTruncation(t *testing.T) {
	var buffer doctorLimitedBuffer
	data := bytes.Repeat([]byte("x"), doctorRemoteOutputLimit+1)
	if _, err := buffer.Write(data); err != nil {
		t.Fatal(err)
	}
	if !buffer.truncated || len(buffer.String()) != doctorRemoteOutputLimit {
		t.Fatalf("limited buffer: truncated=%t size=%d", buffer.truncated, len(buffer.String()))
	}
}

func TestDoctorRubricSymlinkCannotEscapeEtude(t *testing.T) {
	repo, _ := doctorRepo(t)
	path := filepath.Join(repo, ".etude", "evals", "plan-rubric.md")
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("rubric\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "path escapes") {
		t.Fatalf("escaping rubric symlink accepted: err=%v\n%s", err, stdout)
	}
}

func TestDoctorMissingExplicitEnvAndChdirAreFailures(t *testing.T) {
	root := t.TempDir()
	invoke := filepath.Join(root, "missing", "env") + " PATH=/usr/bin true"
	res := doctorResolveCommand(invoke, root, map[string]string{"PATH": os.Getenv("PATH")})
	if res.err == "" || res.missingPath == "" {
		t.Fatalf("unrunnable env invocation accepted: %q => %+v", invoke, res)
	}
	res = doctorResolveCommand("env --chdir="+filepath.Join(root, "missing")+" PATH=/usr/bin true", root, map[string]string{"PATH": os.Getenv("PATH")})
	if res.err != "" || !strings.Contains(res.indeterminate, "NOT CHECKED") {
		t.Fatalf("implementation-specific env chdir was guessed: %+v", res)
	}
}

func TestDoctorNegativeFetchSuppressesExcludedBroadHazard(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "+refs/*:refs/*")
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "^refs/etude/*")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || strings.Contains(stdout, "FAIL fetch-refspec[origin]") {
		t.Fatalf("fully excluded broad mapping reported as a hazard: err=%v\n%s", err, stdout)
	}
}

func TestDoctorEnvIgnoreEnvironmentUsesPlatformDefaultPath(t *testing.T) {
	root := t.TempDir()
	res := doctorResolveCommand("env -i true", root, map[string]string{"PATH": os.Getenv("PATH")})
	if res.err != "" || filepath.Base(res.resolved) != "true" {
		t.Fatalf("env -i default PATH resolution = %+v", res)
	}
}

func TestDoctorMirrorPushNeverRecommendsSync(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "remote.origin.mirror", "true")
	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/local-only", head)
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil {
		t.Fatalf("mirror-push hazard should warn, not fail: %v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "remedy: etude sync") || !strings.Contains(stdout, "replace mirror-push semantics") {
		t.Fatalf("mirror-push emitted unsafe sync advice:\n%s", stdout)
	}
}

func TestDoctorInvalidMirrorNeverRecommendsSync(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "remote.origin.mirror", "bogus")
	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	gitCapture(t, repo, "update-ref", "refs/etude/runs/local-only", head)
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "invalid boolean value") {
		t.Fatalf("invalid mirror boolean was not diagnosed: err=%v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "remedy: etude sync") {
		t.Fatalf("invalid mirror boolean emitted unsafe sync advice:\n%s", stdout)
	}
}

func TestDoctorRefspecWhitespaceIsNotTrimmed(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--unset-all", "remote.origin.push")
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.push", "+refs/etude/*:refs/etude/* ")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err == nil || !strings.Contains(stdout, "invalid push refspec") {
		t.Fatalf("whitespace-bearing refspec accepted: err=%v\n%s", err, stdout)
	}
}

func TestDoctorIdenticalSuffixNegativeSuppressesHazard(t *testing.T) {
	repo, _ := doctorRepo(t)
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "+refs/*/foo:refs/etude/*/bar")
	gitCapture(t, repo, "config", "--local", "--add", "remote.origin.fetch", "^refs/*/foo")
	chdir(t, repo)
	stdout, _, err := execute("doctor")
	if err != nil || strings.Contains(stdout, "FAIL fetch-refspec[origin]") {
		t.Fatalf("identically excluded suffix mapping reported hazardous: err=%v\n%s", err, stdout)
	}
}

func TestDoctorEnvChdirDoesNotAssumeGNUEnv(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o600); err != nil {
		t.Fatal(err)
	}
	res := doctorResolveCommand("env --chdir="+blocked+" /usr/bin/true", root, map[string]string{"PATH": os.Getenv("PATH")})
	if res.err != "" || !strings.Contains(res.indeterminate, "NOT CHECKED") {
		t.Fatalf("env chdir implementation was assumed: %+v", res)
	}
}

func doctorRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := initCaptureRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCapture(t, filepath.Dir(remote), "init", "--bare", remote)
	gitCapture(t, repo, "remote", "add", "origin", remote)
	chdir(t, repo)
	if stdout, stderr, err := execute("init"); err != nil {
		t.Fatalf("init: %v\n%s\n%s", err, stdout, stderr)
	}
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(repo, "scripts", "seat-adapter.sh"))
	fakeBin := filepath.Join(repo, "test-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "claude"))
	writeExecutable(t, filepath.Join(fakeBin, "codex"))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return repo, remote
}

func doctorConfigValues(t *testing.T, repo, key string) []string {
	t.Helper()
	out := strings.TrimSpace(gitCapture(t, repo, "config", "--local", "--get-all", key))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func regexpLiteral(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `.`, `\.`, `+`, `\+`, `*`, `\*`, `?`, `\?`, `(`, `\(`, `)`, `\)`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`, `^`, `\^`, `$`, `\$`, `|`, `\|`)
	return "^" + replacer.Replace(value) + "$"
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func snapshotDoctorRepo(t *testing.T, root string) string {
	t.Helper()
	entries := make([]string, 0, 64)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		metadata := fmt.Sprintf("%s:%s:%o:%d", rel, info.Mode().Type(), info.Mode().Perm(), info.ModTime().UnixNano())
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries = append(entries, metadata+":"+target)
			return nil
		}
		if entry.IsDir() {
			entries = append(entries, metadata)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		entries = append(entries, fmt.Sprintf("%s:%x", metadata, sum))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	refs := gitCapture(t, root, "for-each-ref", "--format=%(refname) %(objectname)")
	sort.Strings(entries)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(entries, "\n")+"\n"+refs)))
}

func TestDoctorRefspecParserRejectsMalformedValues(t *testing.T) {
	g := doctorGit{root: initCaptureRepo(t)}
	for _, value := range []string{"refs/etude/*:refs/etude/runs/*:*", "refs/etude/*:refs/etude/runs", "^refs/etude/*:refs/x/*", "--help:refs/etude/runs/*"} {
		if _, err := g.parseRefspec(context.Background(), value, true); err == nil {
			t.Errorf("parseRefspec(%q) succeeded", value)
		}
	}
}

func TestDoctorRefspecParserAcceptsMatchingBranchesAndFetchHEAD(t *testing.T) {
	g := doctorGit{root: initCaptureRepo(t)}
	for _, value := range []string{":", "+:"} {
		rs, err := g.parseRefspec(context.Background(), value, false)
		if err != nil || !rs.matching {
			t.Errorf("matching-branches push %q = %+v, %v", value, rs, err)
		}
	}
	if _, err := g.parseRefspec(context.Background(), "HEAD:refs/remotes/origin/HEAD", true); err != nil {
		t.Fatalf("valid fetch HEAD rejected: %v", err)
	}
}

func TestDoctorPOSIXDoubleQuotesPreserveBackslashBeforeSpace(t *testing.T) {
	fields, err := doctorSplitPOSIX(`"./my\ reviewer"`)
	if err != nil || len(fields) != 1 || fields[0] != `./my\ reviewer` {
		t.Fatalf("POSIX split = %#v, %v", fields, err)
	}
}

func TestDoctorRepoRootPreservesTrailingSpace(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo ")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCapture(t, repo, "init")
	chdir(t, repo)
	got, err := doctorRepoRoot(context.Background())
	want, resolveErr := filepath.EvalSymlinks(repo)
	if err != nil || resolveErr != nil || got != want {
		t.Fatalf("repository root = %q, %v; want %q (%v)", got, err, want, resolveErr)
	}
}

func TestDoctorRepoRootPreservesTrailingCarriageReturn(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo\r")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCapture(t, repo, "init")
	chdir(t, repo)
	got, err := doctorRepoRoot(context.Background())
	want, resolveErr := filepath.EvalSymlinks(repo)
	if err != nil || resolveErr != nil || got != want {
		t.Fatalf("repository root = %q, %v; want %q (%v)", got, err, want, resolveErr)
	}
}
