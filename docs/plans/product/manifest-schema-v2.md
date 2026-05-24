# Manifest Schema v2 — Deferred Decisions

Status: planning note. These are manifest-schema changes intentionally deferred
out of the v1 run-manifest API so they do not block Phase 0. They are NOT shipped
behavior. Revisit only after `etude` run capture, show, and import create real
migration pressure — not speculatively.

The v1 manifest (`internal/runmanifest`) is the contract today: `ParseJSON`,
`Validate`, `Manifest.JSON`, and `Writer.Write` are stable. Any change here is a
versioned schema migration, so it waits until a consumer actually needs it.

## 1. Typed external refs

**v1 today:** `Manifest.Refs` is `map[string]string` (serialized as the JSON
`refs` object) — opaque key→value pairs, e.g. `{"pr": "469"}`. The key and value
are arbitrary strings; nothing interprets them.

**v2 option:** give external refs a type so consumers can distinguish a PR ref
from an issue ref from a commit/URL ref (e.g. a small typed record per ref rather
than a bare string), enabling richer `run show` rendering and import-time
resolution.

**Why deferred:** the flat string map is sufficient for capture and round-trips
today, and `run show` only prints the pairs. Typing them adds schema surface and
a migration with no current consumer that needs the distinction.

**Revisit when:** `run show`/import (or replay/eval) needs to interpret a ref's
kind — e.g. to link a PR, resolve an issue, or fetch a URL artifact — rather than
just display the string.

## 2. Distinct unknown-size representation for pointer artifacts

**v1 today:** `runmanifest.ArtifactRef.Size` is a plain `int64`. For content
artifacts it is the byte length. For pointer artifacts it is the external object
size "or zero when unknown" (see `artifactstore.ManifestArtifact.Size`). So an
unknown external size is currently indistinguishable from a genuine zero-byte
object at the manifest level.

Note the underlying pointer record already distinguishes the two:
`artifactstore`'s `pointerRecord.Size` is `*int64` with `json:"size,omitempty"`,
so `nil` (omitted) means "unknown" and `0` means "empty". That distinction is
**lost** when the size is flattened into the manifest `ArtifactRef.Size int64`.

**v2 option:** carry an explicit unknown-size representation into `ArtifactRef`
(e.g. a nullable `*int64`/omitempty, mirroring the pointer record) so a 0-byte
pointer object is not conflated with an unmeasured one.

**Why deferred:** no current consumer branches on the unknown-vs-zero
distinction; `Validate` only rejects negative sizes. Changing `ArtifactRef.Size`
to a pointer/nullable type is a breaking schema + Go API change for a distinction
nothing yet reads.

**Revisit when:** a consumer (show/import/eval) needs to report or act on
"size unknown" differently from "size zero" — at which point the manifest should
preserve the distinction the pointer record already makes.

## Migration posture

Both changes are additive-with-migration, not bug fixes. When the trigger
arrives, prefer a single explicit schema version bump (manifest
`workflow_version` / a schema marker) over silently widening the v1 shape, so
older manifests remain parseable or are migrated deliberately.
