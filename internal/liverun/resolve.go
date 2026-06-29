// Package liverun executes live workflow runs and forward replays.
package liverun

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
func ResolveStageRunner(wf workflow.Workflow, reg registry.Registry, stage workflow.Stage, timeout time.Duration) (replay.Runner, error) {
	r := stage.Runner
	if r == nil {
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
	}, nil
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
