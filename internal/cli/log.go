package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/spf13/cobra"
)

// logEvent is one timeline entry, built from a single run or retro manifest.
type logEvent struct {
	Timestamp  time.Time
	Kind       string // "run" or "retro"
	ID         string
	Summary    string
	subjectIDs string // comma-joined subjects (retro only); used for --subject filtering
}

// logRunner holds the read-only dependencies for the log command.
type logRunner struct {
	store  refstore.Store
	stdout io.Writer
}

func newLogCommand(out, errOut io.Writer) *cobra.Command {
	var kinds []string
	var subjects []string
	var limit int

	runner := &logRunner{
		store:  refstore.New(""),
		stdout: out,
	}

	cmd := &cobra.Command{
		Use:           "log",
		Short:         "Show a chronological timeline of runs and retros",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate --kind values before any git call.
			for _, k := range kinds {
				if k != "run" && k != "retro" {
					return fmt.Errorf("invalid --kind %q: must be one of: run, retro", k)
				}
			}
			// Reject negative --limit.
			if limit < 0 {
				return fmt.Errorf("--limit must be >= 0")
			}
			return runner.run(cmd.Context(), kinds, subjects, limit)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	cmd.Flags().StringArrayVar(&kinds, "kind", nil, "include only this event kind (run|retro); repeatable")
	cmd.Flags().StringArrayVar(&subjects, "subject", nil, "include only events whose subject set contains this id; repeatable")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap to most-recent N events after sort (0 = unlimited)")

	return cmd
}

func (r *logRunner) run(ctx context.Context, kinds, subjects []string, limit int) error {
	events, err := r.buildEvents(ctx)
	if err != nil {
		return err
	}

	// Filter by kind.
	if len(kinds) > 0 {
		kindSet := make(map[string]bool, len(kinds))
		for _, k := range kinds {
			kindSet[k] = true
		}
		filtered := events[:0]
		for _, e := range events {
			if kindSet[e.Kind] {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	// Filter by subject.
	if len(subjects) > 0 {
		subjectSet := make(map[string]bool, len(subjects))
		for _, s := range subjects {
			subjectSet[s] = true
		}
		filtered := events[:0]
		for _, e := range events {
			// A run's subject set is its own run id; a retro's subject set is
			// its subject_run.N/bead.N values (NOT its own retro id).
			if e.Kind == "run" {
				if subjectSet[e.ID] {
					filtered = append(filtered, e)
				}
				continue
			}
			for _, sid := range strings.Split(e.subjectIDs, ",") {
				if sid != "" && subjectSet[sid] {
					filtered = append(filtered, e)
					break
				}
			}
		}
		events = filtered
	}

	// Apply --limit (tail of the ascending-sorted list = most recent N).
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}

	if len(events) == 0 {
		fmt.Fprintln(r.stdout, "no events found")
		return nil
	}

	w := tabwriter.NewWriter(r.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIMESTAMP\tKIND\tID\tSUMMARY")
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			e.Timestamp.UTC().Format(time.RFC3339),
			e.Kind,
			e.ID,
			e.Summary,
		)
	}
	return w.Flush()
}

// buildEvents lists both run and retro refs, reads each manifest, and returns
// a slice sorted ascending by (Timestamp, Kind, ID). It fails fast on the
// first malformed manifest and writes nothing to stdout before the sort.
func (r *logRunner) buildEvents(ctx context.Context) ([]logEvent, error) {
	var events []logEvent

	// Collect run events.
	runRefs, err := r.store.List(ctx, strings.TrimSuffix(runsPrefix, "/"))
	if err != nil {
		return nil, err
	}
	for _, ref := range runRefs {
		id := strings.TrimPrefix(ref, runsPrefix)
		manifestBytes, err := r.store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return nil, fmt.Errorf("run %q: %w", id, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return nil, fmt.Errorf("run %q: %w", id, err)
		}
		summary := fmt.Sprintf("%s (%d stages)", manifest.Workflow, len(manifest.Stages))
		if len(manifest.Gates) > 0 {
			summary += fmt.Sprintf("; %d gates", len(manifest.Gates))
		}
		events = append(events, logEvent{
			Timestamp: manifest.Created,
			Kind:      "run",
			ID:        id,
			Summary:   summary,
		})
	}

	// Collect retro events.
	retroRefs, err := r.store.List(ctx, strings.TrimSuffix(retrosPrefix, "/"))
	if err != nil {
		return nil, err
	}
	for _, ref := range retroRefs {
		id := strings.TrimPrefix(ref, retrosPrefix)
		manifestBytes, err := r.store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return nil, fmt.Errorf("retro %q: %w", id, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return nil, fmt.Errorf("retro %q: %w", id, err)
		}
		subjects := retroSubjectsList(manifest.Refs)
		// Build summary: scope=<v> trigger=<v> subjects=<v>, trimming trailing
		// empty fields so blanks never print.
		var summaryParts []string
		if v := manifest.Refs["scope"]; v != "" {
			summaryParts = append(summaryParts, "scope="+v)
		}
		if v := manifest.Refs["trigger"]; v != "" {
			summaryParts = append(summaryParts, "trigger="+v)
		}
		if subjects != "" {
			summaryParts = append(summaryParts, "subjects="+subjects)
		}
		summary := strings.Join(summaryParts, " ")
		events = append(events, logEvent{
			Timestamp:  manifest.Created,
			Kind:       "retro",
			ID:         id,
			Summary:    summary,
			subjectIDs: subjects,
		})
	}

	// Sort ascending by (Timestamp, Kind, ID) for a stable, readable narration.
	sort.Slice(events, func(i, j int) bool {
		ti, tj := events[i].Timestamp, events[j].Timestamp
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if events[i].Kind != events[j].Kind {
			return events[i].Kind < events[j].Kind
		}
		return events[i].ID < events[j].ID
	})

	return events, nil
}
