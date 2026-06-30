package workflow_test

import (
	"fmt"

	"github.com/joshuavial/etude/internal/workflow"
)

// ExampleParseYAML shows how to parse a .etude/workflow.yaml-style document.
func ExampleParseYAML() {
	yaml := `name: simple
stages:
  - name: plan
    produces: plan
    inputs:
      - task
    skill: dev-planner
  - name: implement
    produces: diff
    inputs:
      - plan
      - repo-state
    skill: dev-coder
`
	wf, err := workflow.ParseYAML([]byte(yaml))
	if err != nil {
		panic(err)
	}
	fmt.Println(wf.Name)
	fmt.Println(len(wf.Stages))
	fmt.Println(wf.Stages[0].Name)
	// Output:
	// simple
	// 2
	// plan
}

// ExampleParseYAML_withRunnerAndGate shows how to parse a workflow that
// includes the optional per-stage runner and gate blocks.
func ExampleParseYAML_withRunnerAndGate() {
	yaml := `name: reviewed
stages:
  - name: implement
    produces: diff
    inputs:
      - task
      - repo-state
    skill: dev-coder
    runner:
      name: opus
    gate:
      tier: L3
      max_rounds: 3
      abstraction: "code correctness"
`
	wf, err := workflow.ParseYAML([]byte(yaml))
	if err != nil {
		panic(err)
	}
	fmt.Println(wf.Name)
	fmt.Println(wf.Stages[0].Runner.Name)
	fmt.Println(wf.Stages[0].Gate.Tier)
	// Output:
	// reviewed
	// opus
	// L3
}

// ExampleDefault shows the canonical five-stage workflow returned by Default.
func ExampleDefault() {
	wf := workflow.Default()
	fmt.Println(wf.Name)
	fmt.Println(len(wf.Stages))
	for _, s := range wf.Stages {
		fmt.Println(s.Name)
	}
	// Output:
	// default
	// 5
	// plan
	// implement
	// verify
	// docs
	// review
}
