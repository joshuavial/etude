package retro

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

const (
	retrosPrefix         = "refs/etude/retros/"
	retroWorkflow        = "retro"
	retroWorkflowVersion = "retro-v1"
)

// IsValidRetroID reports whether s is a valid retro identifier. It delegates
// to runmanifest.IsValidRunID so the same charset and structural rules apply.
func IsValidRetroID(s string) bool {
	return runmanifest.IsValidRunID(s)
}

// RetroIDBase formats the base retro id from scope, primarySubject, and time.
// Format: retro-<scope>-<subject>-<UTC yyyymmddThhmmssZ>
// The timestamp uses compact UTC layout with no colons (colons are rejected by
// refstore validateFilePath and the identifier charset). Mirrors EvalIDBase.
func RetroIDBase(scope, primarySubject string, t time.Time) string {
	return fmt.Sprintf("retro-%s-%s-%s", scope, primarySubject, t.UTC().Format("20060102T150405Z"))
}

// AllocateRetroId probes for a free retro id, trying base then base-2..base-10.
// Returns an error if none are free. Mirrors eval.AllocateEvalID.
func AllocateRetroId(ctx context.Context, store refstore.Store, base string) (string, error) {
	if !IsValidRetroID(base) {
		return "", fmt.Errorf("derived retro id %q is not a valid retro id", base)
	}

	candidates := make([]string, 0, 11)
	candidates = append(candidates, base)
	for n := 2; n <= 10; n++ {
		candidates = append(candidates, fmt.Sprintf("%s-%d", base, n))
	}

	for _, id := range candidates {
		ref := retrosPrefix + id
		_, err := store.Resolve(ctx, ref)
		if errors.Is(err, refstore.ErrNotFound) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("probe retro id %q: %w", id, err)
		}
	}
	return "", fmt.Errorf("could not allocate unique retro id after 10 attempts (base: %s)", base)
}

// Writer persists retro manifests under refs/etude/retros/.
type Writer struct {
	Store refstore.Store
}

// WriteOptions configures a Write call. Retros are create-only; there is no
// ExpectedOld (collision is a hard error returning ErrRefExists).
type WriteOptions struct {
	Message string
}

// Write validates and persists manifest to refs/etude/retros/<manifest.RunID>.
// It runs the same artifact-verification and unreferenced-file guard that
// runmanifest.Writer.Write uses (verifyArtifactFile / referencedArtifactPaths),
// accessed by exporting those helpers from the runmanifest package.
// Returns refstore.ErrRefExists on collision (create-only).
func (w Writer) Write(ctx context.Context, manifest runmanifest.Manifest, files map[string][]byte, opts WriteOptions) (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	if _, ok := files["manifest.json"]; ok {
		return "", runmanifest.ErrManifestCollision
	}

	// Verify all artifacts referenced in the manifest are present and hash-correct.
	referenced := runmanifest.ReferencedArtifactPaths(manifest)
	hashes := make(map[string]string, len(referenced))
	for _, stage := range manifest.Stages {
		for _, input := range stage.Inputs {
			if err := runmanifest.VerifyArtifactFile(input, files, hashes); err != nil {
				return "", err
			}
		}
		if err := runmanifest.VerifyArtifactFile(stage.Output, files, hashes); err != nil {
			return "", err
		}
	}

	// Guard against files that are not referenced by the manifest.
	const manifestPath = "manifest.json"
	out := make(map[string][]byte, len(files)+1)
	for filePath, content := range files {
		if _, ok := referenced[filePath]; !ok {
			return "", fmt.Errorf("%w: %s", runmanifest.ErrUnreferencedArtifact, filePath)
		}
		outBytes := make([]byte, len(content))
		copy(outBytes, content)
		out[filePath] = outBytes
	}

	manifestBytes, err := manifest.JSON()
	if err != nil {
		return "", err
	}
	out[manifestPath] = manifestBytes

	msg := opts.Message
	if strings.TrimSpace(msg) == "" {
		msg = fmt.Sprintf("retro: %s", manifest.RunID)
	}

	return w.Store.WriteCommit(ctx, retrosPrefix+manifest.RunID, out, refstore.WriteOptions{
		// No ExpectedOld: create-only. Collision surfaces as ErrRefExists from refstore.
		Message: msg,
	})
}
