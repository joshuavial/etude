// Package nudge implements the "retro nudge" decision logic and snooze state.
//
// The nudge is the best-effort reminder etude emits on stderr when, according
// to .etude/workflow.yaml, the number of captured runs since the most recent
// retro has reached a configured threshold. The package is split into:
//
//   - CountRunsSinceLastRetro: a refstore walk that returns (count, lastRetroID).
//   - ReadSnooze / WriteSnooze: a JSON snooze file under .git/etude/.
//   - Decide: a pure function combining the above with a workflow config.
//
// The package deliberately exposes no cobra/CLI types; the CLI wires it in.
package nudge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

const (
	runsPrefix   = "refs/etude/runs/"
	retrosPrefix = "refs/etude/retros/"

	// snoozeSubdir is the per-repo directory holding nudge state. It lives
	// under .git/ because the state is local-only (per checkout) and not
	// meant to be committed. Callers compose the full path via SnoozePath
	// rather than referencing this constant directly.
	snoozeSubdir = ".git/etude"

	// snoozeFilename is the JSON file holding the snooze record.
	snoozeFilename = "retro-nudge-snooze.json"
)

// Status is the full decision record. Field tags pin the JSON keys to the
// names the CLI contract promises (etude retro nudge status). SnoozedAt is a
// pointer so that omitempty correctly drops the field when no snooze is
// active (time.Time's zero value is not the JSON zero value, so a value-typed
// field with omitempty would still serialize "0001-01-01T00:00:00Z").
type Status struct {
	Enabled            bool       `json:"enabled"`
	Threshold          int        `json:"threshold"`
	RunsSinceLastRetro int        `json:"runs_since_last_retro"`
	LastRetroID        string     `json:"last_retro_id"`
	Overdue            bool       `json:"overdue"`
	SnoozedUntilRuns   int        `json:"snoozed_until_runs"`
	SnoozedAt          *time.Time `json:"snoozed_at,omitempty"`
	WouldEmit          bool       `json:"would_emit"`
}

// Snooze is the JSON shape of the snooze file. It carries enough context to
// auto-invalidate when a fresh retro lands (LastRetroIDAtSnooze changes).
type Snooze struct {
	RunsAtSnooze        int       `json:"runs_at_snooze"`
	SnoozeFor           int       `json:"snooze_for"`
	SnoozedAt           time.Time `json:"snoozed_at"`
	LastRetroIDAtSnooze string    `json:"last_retro_id_at_snooze"`
}

// SnoozePath returns the absolute path to the snooze file inside repoRoot.
//
// Linked worktrees (where repoRoot/.git is a file, not a directory) are not
// supported by v1: WriteSnooze will fail loudly there because os.MkdirAll
// cannot create a directory under a file path. A follow-up bead can resolve
// the real gitdir via `git rev-parse --git-common-dir`.
func SnoozePath(repoRoot string) string {
	return filepath.Join(repoRoot, snoozeSubdir, snoozeFilename)
}

// ReadSnooze returns the snooze stored under repoRoot.
//
//   - (zero, false, nil) when the file is absent: no snooze is in effect.
//   - (zero, false, err) on a read or parse error: callers in the etude root
//     command treat this as silent-no-op so a corrupted snooze cannot crash
//     the parent command, but other callers (e.g. retro nudge status) can
//     surface the error.
//   - (snooze, true, nil) on a clean read.
func ReadSnooze(repoRoot string) (Snooze, bool, error) {
	path := SnoozePath(repoRoot)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snooze{}, false, nil
		}
		return Snooze{}, false, fmt.Errorf("read snooze %s: %w", path, err)
	}
	var s Snooze
	if err := json.Unmarshal(raw, &s); err != nil {
		return Snooze{}, false, fmt.Errorf("parse snooze %s: %w", path, err)
	}
	return s, true, nil
}

// WriteSnooze writes the snooze JSON to repoRoot, creating .git/etude/ when
// missing. The file uses indented JSON to stay diffable when an operator
// pokes at it for debugging.
func WriteSnooze(repoRoot string, s Snooze) error {
	path := SnoozePath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snooze: %w", err)
	}
	// Trailing newline so the file plays nicely with shell tools.
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write snooze %s: %w", path, err)
	}
	return nil
}

// CountRunsSinceLastRetro walks refs/etude/runs/* and refs/etude/retros/*,
// finds the most recent retro by Created time (max), then counts runs whose
// Created is strictly after that maximum. With zero retros the count is the
// total run count.
//
// Returns (count, lastRetroID, error). lastRetroID is "" when no retros exist.
// Any I/O or parse error is returned unchanged so callers can decide whether
// to surface it (CLI status) or swallow it (root-command emitter).
func CountRunsSinceLastRetro(ctx context.Context, store refstore.Store) (int, string, error) {
	retroRefs, err := store.List(ctx, strings.TrimSuffix(retrosPrefix, "/"))
	if err != nil {
		return 0, "", fmt.Errorf("list retros: %w", err)
	}
	var maxRetroCreated time.Time
	var lastRetroID string
	for _, ref := range retroRefs {
		id := strings.TrimPrefix(ref, retrosPrefix)
		raw, err := store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return 0, "", fmt.Errorf("read retro %q manifest: %w", id, err)
		}
		m, err := runmanifest.ParseJSON(raw)
		if err != nil {
			return 0, "", fmt.Errorf("parse retro %q manifest: %w", id, err)
		}
		if m.Created.After(maxRetroCreated) {
			maxRetroCreated = m.Created
			lastRetroID = id
		}
	}

	runRefs, err := store.List(ctx, strings.TrimSuffix(runsPrefix, "/"))
	if err != nil {
		return 0, "", fmt.Errorf("list runs: %w", err)
	}
	count := 0
	for _, ref := range runRefs {
		id := strings.TrimPrefix(ref, runsPrefix)
		raw, err := store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return 0, "", fmt.Errorf("read run %q manifest: %w", id, err)
		}
		m, err := runmanifest.ParseJSON(raw)
		if err != nil {
			return 0, "", fmt.Errorf("parse run %q manifest: %w", id, err)
		}
		if maxRetroCreated.IsZero() || m.Created.After(maxRetroCreated) {
			count++
		}
	}
	return count, lastRetroID, nil
}

// Decide combines a workflow config snapshot with a runs-since-last-retro
// count and snooze state into a fully-resolved Status.
//
// Inputs are passed in rather than fetched here so the function stays pure
// and testable without a refstore or filesystem. The caller (root-command
// emitter or status subcommand) does the I/O and threads the values in.
func Decide(
	enabled bool,
	threshold int,
	runsSinceLastRetro int,
	lastRetroID string,
	snooze Snooze,
	snoozePresent bool,
) Status {
	// A snooze is "active" only when it was recorded against the same retro
	// the caller is now seeing. A fresh retro since the snooze means the
	// counter has reset and the stale snooze should not silence the next
	// over-threshold event.
	snoozedActive := snoozePresent &&
		snooze.LastRetroIDAtSnooze == lastRetroID &&
		(runsSinceLastRetro-snooze.RunsAtSnooze) < snooze.SnoozeFor

	overdue := enabled && runsSinceLastRetro >= threshold

	st := Status{
		Enabled:            enabled,
		Threshold:          threshold,
		RunsSinceLastRetro: runsSinceLastRetro,
		LastRetroID:        lastRetroID,
		Overdue:            overdue,
		WouldEmit:          overdue && !snoozedActive,
	}
	if snoozedActive {
		st.SnoozedUntilRuns = snooze.RunsAtSnooze + snooze.SnoozeFor
		at := snooze.SnoozedAt
		st.SnoozedAt = &at
	}
	return st
}

// NudgeLine is the canonical stderr line shape the root command writes when
// Status.WouldEmit is true. Exported so the test layer can assert on it.
func NudgeLine(runsSinceLastRetro, threshold int) string {
	return fmt.Sprintf(
		"etude: retro nudge: %d bead(s) since last retro (threshold %d); run `etude retro generate workflow` or `etude retro nudge dismiss` to silence for now\n",
		runsSinceLastRetro, threshold,
	)
}
