// Package liverun executes live workflow runs and forward replays.
package liverun

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// GenerateRunID produces "<workflow>-<UTC:20060102T150405Z>-<4-byte hex>".
func GenerateRunID(workflowName string) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	id := fmt.Sprintf("%s-%s-%s", workflowName, time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b))
	if !runmanifest.IsValidRunID(id) {
		return "", fmt.Errorf("generated id %q is not a valid run id", id)
	}
	return id, nil
}

// ResolveStageRunner returns a runner for the stage. It prefers stage.Runner,
// then wf.DefaultRunner. Returns an error when neither is configured.
// envAllowlist is the list of env var NAMES (never values) to pass through to
// the runner; nil/empty means hermetic (default, unchanged behavior).
func ResolveStageRunner(wf workflow.Workflow, reg registry.Registry, stage workflow.Stage, timeout time.Duration, envAllowlist []string) (replay.Runner, error) {
	r := stage.Runner
	if r == nil || (r.Name == "" && r.Command == "") {
		r = wf.DefaultRunner
	}
	if r == nil {
		return nil, fmt.Errorf("stage %q: no runner configured; set stage.runner or workflow.default_runner", stage.Name)
	}
	var cmd string
	if r.Command != "" {
		cmd = r.Command
	} else {
		seat, ok := reg.Seats[r.Name]
		if !ok {
			return nil, fmt.Errorf("stage %q: runner name %q not found in registry", stage.Name, r.Name)
		}
		cmd = seat.Invoke
	}
	return &replay.ExecRunner{
		Command:        strings.Fields(cmd),
		Timeout:        timeout,
		MaxOutputBytes: 64 << 20,
		EnvAllowlist:   envAllowlist,
	}, nil
}

// ResolveCheckRunner returns a CheckRunner for the given gate check runner config.
// Inline Command runners are wrapped in execCheckRunner; Name runners are looked
// up in the registry seats map to get the Invoke command.
//
// envAllowlist is the list of env var NAMES (never values) to pass through, the
// same list the seats get. Checks are real build/test commands — `make test`
// needs HOME to locate the Go module cache — so a strictly hermetic env fails
// them for reasons unrelated to the code under review.
func ResolveCheckRunner(reg registry.Registry, r workflow.Runner, timeout time.Duration, envAllowlist []string) (CheckRunner, error) {
	var cmd string
	if r.Command != "" {
		cmd = r.Command
	} else {
		seat, ok := reg.Seats[r.Name]
		if !ok {
			return nil, fmt.Errorf("check runner name %q not found in registry", r.Name)
		}
		cmd = seat.Invoke
	}
	return &execCheckRunner{
		command:      strings.Fields(cmd),
		timeout:      timeout,
		envAllowlist: envAllowlist,
	}, nil
}

// ResolveGateSeat returns a replay.Runner and SeatMeta for a named seat.
// The runner is an ExecRunner built from the seat's Invoke command with
// Timeout and MaxOutputBytes set (matching buildRunnerFactory).
// SeatMeta carries the split provider name and model from Seat.Provider.
// envAllowlist is the list of env var NAMES (never values) to pass through to
// the seat runner; nil/empty means hermetic (default, unchanged behavior).
// Note: checks use ResolveCheckRunner (not this function), which takes the same
// allowlist.

// InHarnessPrefix marks a seat candidate that etude MUST NOT exec: it names work
// only the calling host can perform, such as spawning an in-harness reviewer
// sub-agent. A consumer that exec'd one would get ENOENT and record a spurious
// outage, so the prefix is checked before any attempt to run it.
const InHarnessPrefix = "in-harness:"

// SeatCandidate is one rung of a seat's invocation ladder: the runner to try (nil
// for an in-harness candidate, which etude cannot run) plus the identity to
// record if this rung is the one that produces the verdict.
//
// Meta is per-CANDIDATE, not per-seat: RequireSessionEvidence is derived from the
// harness, and the harness changes down the ladder, so resolving it once would
// apply the primary's evidence requirement to a fallback with a different one.
type SeatCandidate struct {
	Harness   string
	Invoke    string
	InHarness bool
	Runner    replay.Runner
	Meta      SeatMeta
}

// ResolveGateSeatCandidates returns a seat's full invocation ladder in retry
// order: the primary followed by its configured fallbacks. Callers walk it,
// skipping in-harness rungs, until one yields a usable verdict.
//
// The seat's provider and model identity are constant down the ladder — only the
// harness and command change — so a fallback verdict is still that seat's
// verdict, recorded against the harness that actually produced it.
func ResolveGateSeatCandidates(reg registry.Registry, seatName string, timeout time.Duration, envAllowlist []string) ([]SeatCandidate, error) {
	seat, ok := reg.Seats[seatName]
	if !ok {
		return nil, fmt.Errorf("seat %q not found in registry", seatName)
	}
	providerName, model := splitProvider(seat.Provider)

	invocations := seat.Invocations()
	candidates := make([]SeatCandidate, 0, len(invocations))
	for _, inv := range invocations {
		c := SeatCandidate{
			Harness:   inv.Harness,
			Invoke:    inv.Invoke,
			InHarness: strings.HasPrefix(inv.Invoke, InHarnessPrefix),
			Meta: SeatMeta{
				HarnessName:            inv.Harness,
				ProviderName:           providerName,
				Model:                  model,
				RequireSessionEvidence: requiresSessionEvidence(providerName, inv.Harness),
			},
		}
		if !c.InHarness {
			c.Runner = &replay.ExecRunner{
				Command:        strings.Fields(inv.Invoke),
				Timeout:        timeout,
				MaxOutputBytes: 64 << 20,
				EnvAllowlist:   envAllowlist,
			}
		}
		candidates = append(candidates, c)
	}
	return candidates, nil
}

func ResolveGateSeat(reg registry.Registry, seatName string, timeout time.Duration, envAllowlist []string) (replay.Runner, SeatMeta, error) {
	seat, ok := reg.Seats[seatName]
	if !ok {
		return nil, SeatMeta{}, fmt.Errorf("seat %q not found in registry", seatName)
	}
	providerName, model := splitProvider(seat.Provider)
	meta := SeatMeta{
		HarnessName:            seat.Harness,
		ProviderName:           providerName,
		Model:                  model,
		RequireSessionEvidence: requiresSessionEvidence(providerName, seat.Harness),
	}
	runner := &replay.ExecRunner{
		Command:        strings.Fields(seat.Invoke),
		Timeout:        timeout,
		MaxOutputBytes: 64 << 20,
		EnvAllowlist:   envAllowlist,
	}
	return runner, meta, nil
}

func requiresSessionEvidence(providerName, harnessName string) bool {
	provider := strings.ToLower(strings.TrimSpace(providerName))
	harness := strings.ToLower(strings.TrimSpace(harnessName))
	if provider == "" || harness == "" {
		return false
	}
	if provider == "deterministic" || harness == "shell" {
		return false
	}
	return true
}

// ResolveTiers returns a Tiers function that resolves tier seat lists and
// next-stronger tier names from the registry. The tier ladder is ordered by
// numeric suffix toward L1 (strongest): L3 → L2 → L1.
func ResolveTiers(reg registry.Registry) func(string) ([]string, string, bool) {
	return func(name string) ([]string, string, bool) {
		tier, ok := reg.Tiers[name]
		if !ok {
			return nil, "", false
		}
		// Compute next stronger tier by decrementing the numeric suffix.
		nextStronger := ""
		if len(name) >= 2 && name[0] == 'L' {
			n, err := strconv.Atoi(name[1:])
			if err == nil && n > 1 {
				candidate := fmt.Sprintf("L%d", n-1)
				if _, exists := reg.Tiers[candidate]; exists {
					nextStronger = candidate
				}
			}
		}
		return tier.Seats, nextStronger, true
	}
}

// DeriveFrontier returns the index of the first workflow stage whose Produces role
// is absent from the manifest's completed stages. Returns len(wf.Stages) when complete.
func DeriveFrontier(wf workflow.Workflow, manifest runmanifest.Manifest) int {
	produced := make(map[string]bool, len(manifest.Stages))
	for _, s := range manifest.Stages {
		produced[s.Output.Role] = true
	}
	for i, s := range wf.Stages {
		if !produced[s.Produces] {
			return i
		}
	}
	return len(wf.Stages)
}
