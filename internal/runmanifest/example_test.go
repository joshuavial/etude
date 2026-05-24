package runmanifest_test

import (
	"fmt"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ExampleParseJSON demonstrates the full write-then-read cycle for a run
// manifest: build a Manifest, serialise it with JSON, and parse it back.
func ExampleParseJSON() {
	store := artifactstore.New()
	out, err := store.AddContent("output", "text/plain", []byte("result\n"))
	if err != nil {
		panic(err)
	}

	m := runmanifest.Manifest{
		RunID:           "20260522-run.1",
		Workflow:        "default",
		WorkflowVersion: "abc123",
		Created:         time.Date(2026, 5, 22, 9, 0, 0, 0, time.UTC),
		Stages: []runmanifest.Stage{{
			Name:       "plan",
			ProducedBy: "dev-workflow",
			GitSHA:     "deadbeef",
			Skill: runmanifest.Skill{
				ID:      "dev-planner",
				Repo:    "github.com/example/skills",
				Version: "v1.0.0",
			},
			Output:    runmanifest.ArtifactFromManifestArtifact(out),
			Timestamp: time.Date(2026, 5, 22, 9, 1, 0, 0, time.UTC),
		}},
	}

	data, err := m.JSON()
	if err != nil {
		panic(err)
	}

	parsed, err := runmanifest.ParseJSON(data)
	if err != nil {
		panic(err)
	}

	fmt.Println(parsed.RunID)
	fmt.Println(len(parsed.Stages))
	// Output:
	// 20260522-run.1
	// 1
}
