package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// loadManifestForRef trims prefix from ref to recover the id, reads and parses
// the ref's manifest.json, and wraps any error as "<kind> %q: %w". It centralizes
// the read+parse loop shared by run list, retro list, and log.
func loadManifestForRef(ctx context.Context, store refstore.Store, ref, prefix, kind string) (string, runmanifest.Manifest, error) {
	id := strings.TrimPrefix(ref, prefix)
	manifestBytes, err := store.ReadFile(ctx, ref, "manifest.json")
	if err != nil {
		return id, runmanifest.Manifest{}, fmt.Errorf("%s %q: %w", kind, id, err)
	}
	m, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return id, runmanifest.Manifest{}, fmt.Errorf("%s %q: %w", kind, id, err)
	}
	return id, m, nil
}
