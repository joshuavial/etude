package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// isNullOID reports whether s is an all-zero object id (hash-length agnostic, so
// it holds for both SHA-1 and a future SHA-256 repo).
func isNullOID(s string) bool {
	return s != "" && strings.Trim(s, "0") == ""
}

func newSyncCommand(out, errOut io.Writer) *cobra.Command {
	var remote string

	cmd := &cobra.Command{
		Use:           "sync",
		Short:         "Push and fetch refs/etude/* with a git remote",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := syncRunner{stdout: out, stderr: errOut}
			return runner.run(cmd.Context(), remote)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().StringVar(&remote, "remote", "origin", "git remote to sync with (default: origin)")
	return cmd
}

type syncRunner struct {
	stdout io.Writer
	stderr io.Writer
}

func (r syncRunner) run(ctx context.Context, remote string) error {
	// Step 1: validate remote name before any git call.
	if err := validateRemoteName(remote); err != nil {
		return err
	}

	// Step 2: resolve repo root.
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	// Step 3: require remote to exist — sync always errors on a missing remote.
	if !remoteExists(ctx, root, remote) {
		return remoteNotFoundErr(remote)
	}

	// Step 4: fetch.
	if err := r.fetch(ctx, root, remote); err != nil {
		return err
	}

	// Step 5: pre-check — skip push if no local refs/etude/* exist.
	hasRefs, err := r.hasLocalEtudeRefs(ctx, root)
	if err != nil {
		return err
	}
	if !hasRefs {
		fmt.Fprintln(r.stdout, "no local refs/etude/* to push")
		return nil
	}

	// Step 6: push.
	return r.push(ctx, root, remote)
}

// syncEnv returns the hardened environment for all sync git calls.
func syncEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
		"LC_ALL=C",
		"LANG=C",
	)
}

// fetch runs git fetch --porcelain and classifies the result.
func (r syncRunner) fetch(ctx context.Context, root, remote string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "fetch", "--porcelain", remote, "refs/etude/*:refs/etude/*")
	cmd.Env = syncEnv()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	fetchErr := cmd.Run()
	stdoutStr := stdoutBuf.String()
	stderrStr := stderrBuf.String()

	if fetchErr == nil {
		// Exit 0: success. Parse and report fast-forwarded refs.
		updated := parseFetchUpdated(stdoutStr)
		if len(updated) > 0 {
			fmt.Fprintf(r.stdout, "fetched %s from %s (fast-forwarded: %s)\n",
				"refs/etude/*", remote, strings.Join(updated, ", "))
		} else {
			fmt.Fprintf(r.stdout, "fetched refs/etude/* from %s\n", remote)
		}
		return nil
	}

	// Non-zero exit: apply classification.

	// Backstop first: specific hard-error markers in stderr.
	stderrLower := strings.ToLower(stderrStr)
	if containsAny(stderrLower, "cannot lock", "unable to", "error: cannot", "fatal:") {
		return fmt.Errorf("fetch failed: %s", strings.TrimSpace(stderrStr))
	}

	// Parse `!` lines from stdout.
	bangLines := parseBangLines(stdoutStr)
	if len(bangLines) == 0 {
		// No accounting at all — transport or other real failure.
		return fmt.Errorf("fetch failed: %s", strings.TrimSpace(stderrStr))
	}

	// Classify each `!` line by ancestry.
	var benignRefs []string
	for _, bl := range bangLines {
		result, classifyErr := classifyFetchBang(ctx, root, bl)
		if classifyErr != nil {
			return classifyErr
		}
		switch result {
		case fetchBangRealFailure:
			return fmt.Errorf("fetch failed: %s", strings.TrimSpace(stderrStr))
		case fetchBangAbort:
			return fmt.Errorf("fetch aborted: cannot classify ref %s", bl.ref)
		case fetchBangBenign:
			benignRefs = append(benignRefs, bl.ref)
		}
	}

	// All `!` lines are benign (genuine non-ff). Continue to push.
	if len(benignRefs) > 0 {
		fmt.Fprintf(r.stdout, "fetched refs/etude/* from %s (some refs not fast-forwardable: %s)\n",
			remote, strings.Join(benignRefs, ", "))
	} else {
		fmt.Fprintf(r.stdout, "fetched refs/etude/* from %s\n", remote)
	}
	return nil
}

type fetchBangResult int

const (
	fetchBangBenign      fetchBangResult = iota // genuine non-ff, continue
	fetchBangRealFailure                        // real local failure, abort
	fetchBangAbort                              // merge-base error or unknown, abort
)

type bangLine struct {
	old string
	new string
	ref string
}

// parseBangLines extracts `!`-flagged lines from fetch --porcelain stdout.
// Each line format: `! <old> <new> <ref>` (flag is first char; rest split on whitespace).
func parseBangLines(stdout string) []bangLine {
	var lines []bangLine
	for _, line := range strings.Split(stdout, "\n") {
		if len(line) == 0 {
			continue
		}
		flag := line[0]
		if flag != '!' {
			continue
		}
		// Remainder after the flag character; split on whitespace.
		parts := strings.Fields(line[1:])
		if len(parts) < 3 {
			continue // defensive: malformed line
		}
		lines = append(lines, bangLine{old: parts[0], new: parts[1], ref: parts[2]})
	}
	return lines
}

// parseFetchUpdated returns refs that were fast-forwarded (space or `*` flag) in fetch stdout.
func parseFetchUpdated(stdout string) []string {
	var refs []string
	for _, line := range strings.Split(stdout, "\n") {
		if len(line) == 0 {
			continue
		}
		flag := line[0]
		if flag == ' ' || flag == '*' {
			parts := strings.Fields(line[1:])
			if len(parts) >= 3 {
				refs = append(refs, parts[2])
			}
		}
	}
	return refs
}

// classifyFetchBang determines whether a fetch `!` line is a real failure or benign.
func classifyFetchBang(ctx context.Context, root string, bl bangLine) (fetchBangResult, error) {
	// A null old OID means a new-ref creation was rejected — always a real
	// failure. Checked before merge-base, which errors (exit 128) on a null OID.
	if isNullOID(bl.old) {
		return fetchBangRealFailure, nil
	}

	// Run merge-base --is-ancestor <old> <new>.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "merge-base", "--is-ancestor", bl.old, bl.new)
	cmd.Env = syncEnv()
	err := cmd.Run()

	if err == nil {
		// Exit 0: old IS ancestor of new — a would-be fast-forward was rejected,
		// which a non-forced fetch should have applied → real local failure.
		return fetchBangRealFailure, nil
	}

	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 1 {
			// Exit 1: old is NOT ancestor of new — genuine non-ff → benign.
			return fetchBangBenign, nil
		}
		// Any other exit (e.g. 128) → abort with the exit code for debugging.
		return fetchBangAbort, fmt.Errorf("fetch aborted: cannot classify ref %s (merge-base exit %d)", bl.ref, exitErr.ExitCode())
	}

	// Non-ExitError (e.g. process could not start) → abort.
	return fetchBangAbort, fmt.Errorf("fetch aborted: cannot classify ref %s: %w", bl.ref, err)
}

// hasLocalEtudeRefs returns true if refs/etude/* has any entries.
func (r syncRunner) hasLocalEtudeRefs(ctx context.Context, root string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "for-each-ref", "refs/etude/")
	cmd.Env = syncEnv()
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("for-each-ref refs/etude/: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// push runs git push --porcelain and classifies the result.
func (r syncRunner) push(ctx context.Context, root, remote string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "push", "--porcelain", remote, "refs/etude/*:refs/etude/*")
	cmd.Env = syncEnv()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	pushErr := cmd.Run()
	stdoutStr := stdoutBuf.String()
	stderrStr := stderrBuf.String()

	var diverged []string
	var genericFail []string
	var advanced []string

	for _, line := range strings.Split(stdoutStr, "\n") {
		if len(line) == 0 {
			continue
		}
		// Skip header ("To <url>") and trailing "Done".
		if strings.HasPrefix(line, "To ") || strings.TrimSpace(line) == "Done" {
			continue
		}

		// Push porcelain lines are TAB-separated: flag\tsrcdst\tsummary
		// Use SplitN to limit splits; guard defensively.
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue // malformed line, skip
		}

		// The porcelain flag is exactly one character and may be a literal
		// space (a successful fast-forward), so do not trim it.
		flag := parts[0]
		srcdst := parts[1]
		summary := ""
		if len(parts) >= 3 {
			summary = parts[2]
		}

		// Extract destination ref from srcdst (format: "src:dst").
		dstParts := strings.SplitN(srcdst, ":", 2)
		dst := srcdst
		if len(dstParts) == 2 {
			dst = dstParts[1]
		}

		switch flag {
		case "!":
			summaryLower := strings.ToLower(summary)
			if containsAny(summaryLower, "non-fast-forward", "fetch first", "stale info") {
				diverged = append(diverged, fmt.Sprintf("%s diverged from %s; manual resolution required", dst, remote))
			} else {
				genericFail = append(genericFail, fmt.Sprintf("push failed for %s: %s", dst, summary))
			}
		case " ", "*", "=", "+", "-":
			advanced = append(advanced, dst)
		}
	}

	if len(diverged) > 0 {
		return fmt.Errorf("push rejected (diverged refs):\n%s", strings.Join(diverged, "\n"))
	}

	if len(genericFail) > 0 {
		return fmt.Errorf("push failed:\n%s\n%s", strings.Join(genericFail, "\n"), strings.TrimSpace(stderrStr))
	}

	if pushErr != nil {
		return fmt.Errorf("push failed: %s", strings.TrimSpace(stderrStr))
	}

	if len(advanced) > 0 {
		fmt.Fprintf(r.stdout, "pushed refs/etude/* to %s (%s)\n", remote, strings.Join(advanced, ", "))
	} else {
		fmt.Fprintf(r.stdout, "pushed refs/etude/* to %s (up to date)\n", remote)
	}
	return nil
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
