package registry_test

import (
	"fmt"

	"github.com/joshuavial/etude/internal/registry"
)

// ExampleParseYAML shows how to parse a .etude/registry.yaml-style document.
func ExampleParseYAML() {
	yaml := `quorum: unanimous
seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
    mode: inline
tiers:
  L4:
    name: Light single-seat gate
    seats:
      - opus
`
	reg, err := registry.ParseYAML([]byte(yaml))
	if err != nil {
		panic(err)
	}
	fmt.Println(reg.EffectiveQuorum())
	fmt.Println(len(reg.Seats))
	fmt.Println(len(reg.Tiers))
	// Output:
	// unanimous
	// 1
	// 1
}

// ExampleDefault shows the canonical scaffold registry returned by Default.
func ExampleDefault() {
	reg := registry.Default()
	fmt.Println(reg.EffectiveQuorum())
	fmt.Println(len(reg.Seats))
	fmt.Println(len(reg.Tiers))
	// Output:
	// unanimous
	// 3
	// 4
}
