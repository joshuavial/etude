package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joshuavial/etude/internal/workflow"
)

// Exercise checked-in profiles with the real strict schemas, not a YAML-only lint.
func TestProjectDevelopmentProfiles(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ParseYAML(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev-claude", "dev-codex"} {
		data, err := os.ReadFile(filepath.Join(root, ".etude", "workflows", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		wf, err := workflow.ParseYAML(data)
		if err != nil {
			t.Fatal(err)
		}
		if wf.Name != name {
			t.Fatalf("profile %s has name %s", name, wf.Name)
		}
		for _, stage := range wf.Stages {
			if stage.Gate == nil {
				continue
			}
			tier, ok := reg.Tiers[stage.Gate.Tier]
			if !ok || len(tier.Seats) == 0 {
				t.Fatalf("unresolved gate tier %s", stage.Gate.Tier)
			}
			if stage.Gate.PassThreshold == nil || *stage.Gate.PassThreshold != 1 {
				t.Fatalf("%s gate must require unanimous pass", stage.Name)
			}
			for _, seat := range tier.Seats {
				if _, ok := reg.Seats[seat]; !ok {
					t.Fatalf("unresolved seat %s", seat)
				}
			}
		}
	}
}
