package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/nudge"
)

// writeNudgeWorkflow writes a workflow.yaml under repo with the retro nudge
// enabled (or disabled when on=false) and the given threshold.
func writeNudgeWorkflow(t *testing.T, repo string, on bool, threshold int) {
	t.Helper()
	body := "name: default\n" +
		"stages:\n" +
		"  - name: plan\n" +
		"    produces: plan\n" +
		"    inputs:\n" +
		"      - task\n" +
		"    skill: dev-planner\n" +
		"retros:\n" +
		"  on_run_close: false\n" +
		"  nudge:\n"
	if on {
		body += "    enabled: true\n"
	} else {
		body += "    enabled: false\n"
	}
	body += "    threshold: " + strconv.Itoa(threshold) + "\n"
	writeFile(t, repo, ".etude/workflow.yaml", body)
}

func TestRetroNudgeDismissDefaultsToOne(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	// Seed 3 runs so dismiss has a non-zero count to anchor on.
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	seedRunWithTimestamp(t, repo, "n-a", base)
	seedRunWithTimestamp(t, repo, "n-b", base.Add(time.Hour))
	seedRunWithTimestamp(t, repo, "n-c", base.Add(2*time.Hour))

	stdout, stderr, err := execute("retro", "nudge", "dismiss")
	if err != nil {
		t.Fatalf("dismiss: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "snoozed retro nudge for 1 more bead(s)") {
		t.Fatalf("dismiss stdout missing expected confirmation:\n%s", stdout)
	}
	// Snooze file must be written under .git/etude/.
	snoozePath := filepath.Join(repo, ".git", "etude", "retro-nudge-snooze.json")
	raw, err := os.ReadFile(snoozePath)
	if err != nil {
		t.Fatalf("read snooze: %v", err)
	}
	var sn nudge.Snooze
	if err := json.Unmarshal(raw, &sn); err != nil {
		t.Fatalf("parse snooze: %v", err)
	}
	if sn.SnoozeFor != 1 {
		t.Fatalf("SnoozeFor = %d, want 1", sn.SnoozeFor)
	}
	if sn.RunsAtSnooze != 3 {
		t.Fatalf("RunsAtSnooze = %d, want 3", sn.RunsAtSnooze)
	}
}

func TestRetroNudgeDismissForN(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	seedRunWithTimestamp(t, repo, "n-a", time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))

	if _, stderr, err := execute("retro", "nudge", "dismiss", "--for", "3"); err != nil {
		t.Fatalf("dismiss --for 3: %v\nstderr: %s", err, stderr)
	}
	snoozePath := filepath.Join(repo, ".git", "etude", "retro-nudge-snooze.json")
	raw, err := os.ReadFile(snoozePath)
	if err != nil {
		t.Fatalf("read snooze: %v", err)
	}
	var sn nudge.Snooze
	if err := json.Unmarshal(raw, &sn); err != nil {
		t.Fatalf("parse snooze: %v", err)
	}
	if sn.SnoozeFor != 3 {
		t.Fatalf("SnoozeFor = %d, want 3", sn.SnoozeFor)
	}
}

func TestRetroNudgeDismissRejectsZeroAndNegative(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	for _, n := range []string{"0", "-1"} {
		_, _, err := execute("retro", "nudge", "dismiss", "--for", n)
		if err == nil {
			t.Fatalf("dismiss --for %s should error", n)
		}
	}
}

func TestRetroNudgeStatusEmitsContractKeys(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 3)

	stdout, stderr, err := execute("retro", "nudge", "status")
	if err != nil {
		t.Fatalf("status: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("status should write nothing to stderr; got %q", stderr)
	}
	// Parse into a generic map to assert on the EXACT key set.
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("status JSON parse: %v\n%s", err, stdout)
	}
	required := []string{
		"enabled", "threshold", "runs_since_last_retro", "last_retro_id",
		"overdue", "snoozed_until_runs", "would_emit",
	}
	for _, k := range required {
		if _, ok := got[k]; !ok {
			t.Fatalf("status JSON missing %q in: %s", k, stdout)
		}
	}
	if v, _ := got["enabled"].(bool); !v {
		t.Fatalf("status enabled should be true: %s", stdout)
	}
	if v, _ := got["threshold"].(float64); int(v) != 3 {
		t.Fatalf("status threshold should be 3: %s", stdout)
	}
}

func TestRetroNudgeStatusOmittedConfig(t *testing.T) {
	// No workflow.yaml at all — status still returns a JSON object with
	// enabled=false rather than erroring.
	repo := initCaptureRepo(t)
	chdir(t, repo)
	stdout, _, err := execute("retro", "nudge", "status")
	if err != nil {
		t.Fatalf("status without workflow.yaml: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	if v, _ := got["enabled"].(bool); v {
		t.Fatalf("enabled should default to false without workflow.yaml: %s", stdout)
	}
	if v, _ := got["would_emit"].(bool); v {
		t.Fatalf("would_emit should be false when nudge is disabled: %s", stdout)
	}
}

func TestRetroNudgeStatusDoesNotEmitStderrNudge(t *testing.T) {
	// Even when the nudge IS overdue, calling `retro nudge status` must not
	// also emit the stderr reminder line (the entire `retro nudge` subtree is
	// silenced).
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 1)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	_, stderr, err := execute("retro", "nudge", "status")
	if err != nil {
		t.Fatalf("status: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("status should not emit the stderr nudge: %q", stderr)
	}
}

func TestRetroNudgeDismissDoesNotEmitStderrNudge(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 1)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	_, stderr, err := execute("retro", "nudge", "dismiss")
	if err != nil {
		t.Fatalf("dismiss: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("dismiss should not emit the stderr nudge in its own invocation: %q", stderr)
	}
}

// ---- root-stderr emission ----

func TestRootNudgeOffByDefault(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	_, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge should be off without workflow.yaml: %q", stderr)
	}
}

func TestRootNudgeEmitsWhenOverdue(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 2)
	base := time.Now().UTC()
	seedRunWithTimestamp(t, repo, "r1", base)
	seedRunWithTimestamp(t, repo, "r2", base.Add(time.Second))

	stdout, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stderr, "etude: retro nudge: 2 bead(s) since last retro (threshold 2)") {
		t.Fatalf("expected nudge on stderr; got %q", stderr)
	}
	// Nudge must not leak into stdout.
	if strings.Contains(stdout, "retro nudge:") {
		t.Fatalf("nudge should not leak into stdout: %s", stdout)
	}
}

func TestRootNudgeBelowThresholdSilent(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 5)
	seedRunWithTimestamp(t, repo, "r1", time.Now().UTC())

	_, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge should not fire below threshold: %q", stderr)
	}
}

func TestRootNudgeSilencedByFreshSnooze(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 2)
	base := time.Now().UTC()
	seedRunWithTimestamp(t, repo, "r1", base)
	seedRunWithTimestamp(t, repo, "r2", base.Add(time.Second))

	// Dismiss for 2 beads.
	if _, stderr, err := execute("retro", "nudge", "dismiss", "--for", "2"); err != nil {
		t.Fatalf("dismiss: %v\nstderr: %s", err, stderr)
	}
	// Next command must be silent.
	_, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge should be suppressed by snooze: %q", stderr)
	}
}

func TestRootNudgeReturnsAfterSnoozeWindow(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 2)
	base := time.Now().UTC()
	seedRunWithTimestamp(t, repo, "r1", base)
	seedRunWithTimestamp(t, repo, "r2", base.Add(time.Second))

	if _, _, err := execute("retro", "nudge", "dismiss", "--for", "1"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	// First post-dismiss invocation: still snoozed (count = 2, snoozed at 2,
	// SnoozeFor=1 ⇒ suppress while count < 3).
	if _, stderr, err := execute("run", "list"); err != nil || strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("expected suppression; err=%v stderr=%q", err, stderr)
	}
	// Add one more run → count = 3 ⇒ snooze window expired.
	seedRunWithTimestamp(t, repo, "r3", base.Add(2*time.Second))
	_, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("expected nudge to fire after snooze window expired: %q", stderr)
	}
}

func TestRootNudgeStaleSnoozeAfterNewRetro(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 2)
	base := time.Now().UTC()
	seedRunWithTimestamp(t, repo, "r1", base)
	seedRunWithTimestamp(t, repo, "r2", base.Add(time.Second))
	if _, _, err := execute("retro", "nudge", "dismiss", "--for", "10"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	// Land a new retro AFTER the snooze. The snooze records the previous
	// last_retro_id ("" since there were no retros); now there is one, so the
	// stale snooze must be invalidated by Decide and the next overdue
	// invocation must fire.
	seedRetroWithTimestamp(t, repo, "retro-run-r2-fresh", "r2", base.Add(2*time.Second))
	// Add 2 more runs so the post-retro count >= threshold.
	seedRunWithTimestamp(t, repo, "r3", base.Add(3*time.Second))
	seedRunWithTimestamp(t, repo, "r4", base.Add(4*time.Second))

	_, stderr, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("expected nudge to fire after stale snooze invalidated by new retro: %q", stderr)
	}
}

func TestRootNudgeSilentOnHelp(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 1)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	_, stderr, err := execute("--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge must not appear on --help stderr: %q", stderr)
	}
}

func TestRootNudgeSilentOnVersion(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 1)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	_, stderr, err := execute("--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge must not appear on --version stderr: %q", stderr)
	}
}

func TestRootNudgeSilentOnCompletion(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 1)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	_, stderr, err := execute("completion", "bash")
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge must not appear on completion stderr: %q", stderr)
	}
}

func TestRootNudgeSilentOnHiddenComplete(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 1)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	_, stderr, err := execute("__complete", "ru", "")
	if err != nil {
		t.Fatalf("__complete: %v", err)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge must not appear on __complete stderr: %q", stderr)
	}
}

// TestRootNudgeEmitsOnSubcommandFailure locks AC#4's "regardless of whether
// the subcommand succeeded or failed" — the nudge must surface on stderr
// even when the parent command exits with an error.
func TestRootNudgeEmitsOnSubcommandFailure(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 1)
	seedRunWithTimestamp(t, repo, "x", time.Now().UTC())

	// `retro show <bogus-id>` fails because the retro id is not found; the
	// nudge should still appear on stderr.
	_, stderr, err := execute("retro", "show", "retro-run-doesnotexist-20260101T000000Z")
	if err == nil {
		t.Fatal("expected subcommand error for unknown retro id")
	}
	if !strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge must fire even when the subcommand fails; stderr=%q", stderr)
	}
}

// TestRootNudgeBestEffortOnCorruptSnooze locks the best-effort contract: a
// corrupted .git/etude/retro-nudge-snooze.json must not crash the parent
// command or alter its exit code.
func TestRootNudgeBestEffortOnCorruptSnooze(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)
	writeNudgeWorkflow(t, repo, true, 2)
	base := time.Now().UTC()
	seedRunWithTimestamp(t, repo, "r1", base)
	seedRunWithTimestamp(t, repo, "r2", base.Add(time.Second))

	snoozeDir := filepath.Join(repo, ".git", "etude")
	if err := os.MkdirAll(snoozeDir, 0o755); err != nil {
		t.Fatalf("mkdir snooze dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snoozeDir, "retro-nudge-snooze.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt snooze: %v", err)
	}

	// Parent command must succeed despite the corrupt snooze.
	_, _, err := execute("run", "list")
	if err != nil {
		t.Fatalf("run list crashed on corrupt snooze: %v", err)
	}
}

// TestRootNudgeBestEffortOutsideGitRepo locks the best-effort contract: when
// the cwd is not a git repository (so nudgeRepoRoot fails), the nudge path
// must silently no-op. The parent command (one that does not need a repo,
// like `etude --version`-style help) must still succeed cleanly.
func TestRootNudgeBestEffortOutsideGitRepo(t *testing.T) {
	dir := t.TempDir() // NOT a git repo
	chdir(t, dir)
	// `etude retro --help` exercises the post-Execute emit path with a
	// resolved command but no git repo. The nudge emitter must silent-no-op.
	_, stderr, err := execute("retro", "--help")
	if err != nil {
		t.Fatalf("retro --help outside git repo: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "retro nudge:") {
		t.Fatalf("nudge must not appear outside a git repo: %q", stderr)
	}
}
