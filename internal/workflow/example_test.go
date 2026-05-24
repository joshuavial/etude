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

// ExampleDefault shows the canonical six-stage workflow returned by Default.
func ExampleDefault() {
	wf := workflow.Default()
	fmt.Println(wf.Name)
	fmt.Println(len(wf.Stages))
	for _, s := range wf.Stages {
		fmt.Println(s.Name)
	}
	// Output:
	// default
	// 6
	// plan
	// implement
	// test-plan
	// test
	// review
	// docs
}
