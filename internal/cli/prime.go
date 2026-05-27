package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newPrimeCommand(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "prime",
		Short:         "Print a structured agent-onboarding primer to stdout",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		fmt.Fprint(cmd.OutOrStdout(), renderPrimer(cmd.Root()))
		return nil
	}
	return cmd
}

// renderPrimer builds the full primer text. It derives the available-commands
// list at runtime by walking root.Commands() so it cannot drift from reality.
func renderPrimer(root *cobra.Command) string {
	var sb strings.Builder

	sb.WriteString(`# etude — Agent Onboarding Primer

## Purpose

etude is an AI-agent-first CLI primitive that empirically tests AI coding-agent
skills by capturing stage artifacts as git-native run records and replaying past
work. Instead of guesswork ("this skill feels better"), etude gives you a win
rate: "my upgraded planning skill beat the original on 8 of 10 recorded runs."

## Core Concepts

| Concept  | Definition |
|----------|------------|
| Workflow | Ordered set of stages declared in .etude/workflow.yaml. The pipeline itself — not the runtime. |
| Stage    | A unit of work modelled as (input artifacts) → (output artifact). Has a name, declared inputs, an associated skill, and an optional eval config. |
| Artifact | An immutable, content-addressed blob: a plan, a diff, a test plan, a review, a retro. |
| Run      | One pass of a workflow for one unit of work (≈ one PR/bead). A run has a manifest.json tying each stage to its input artifacts, output artifact, producer identity, and the git sha of the repo at that stage. |
| Replay   | Re-executing one stage of an existing run with a (possibly new) skill version, producing a new artifact from the original recorded inputs. |
| Eval     | Scoring an artifact against a rubric, comparing two artifacts pairwise, or running deterministic assertions. Eval is a library — there is no standalone eval CLI yet (see Planned below). |
| Cohort   | A selected set of runs to operate on as a batch (e.g. "last 10 runs with a plan artifact"). |
| Bench    | Replay + eval one stage across a cohort to measure the effect of a skill change. The headline use case. |

## Storage Model

etude stores all data under a custom git ref namespace so captured data travels
with the repo but never appears in branch history:

  refs/etude/runs/<run-id>       — run manifests + stage artifacts
  refs/etude/evals/<eval-id>     — eval results
  refs/etude/retros/<retro-id>   — retrospective artifacts

Content-addressed artifacts live under:

  artifacts/sha256/<aa>/<full-hash>

where <aa> is the first two hex chars of the SHA-256 digest (fan-out prefix).

Each run ref contains a manifest.json (manifest_version 2 or 3) with:
  - workflow name + version (hash of workflow.yaml)
  - stages array: name, inputs, output artifact hash, producer block
  - producer block per stage: harness, model, skill (id/repo/version), git_sha

## Available Commands

`)

	cmds := deriveCommands(root)
	for _, line := range cmds {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	sb.WriteString(`
## Planned / Not Yet Built

The following are described in design documents but NOT yet registered as CLI
commands. Do not invoke them — they will return "unknown command".

  eval      Standalone eval CLI (eval logic is a library used internally by bench)

## Capture Workflow

A typical capture session uses etude to record the artifacts produced at each
stage of a development workflow. The workflow is declared in .etude/workflow.yaml
(created by 'etude init'):

  name: default
  stages:
    - name: plan
      produces: plan
      inputs: [task]
      skill: dev-planner
      eval:
        method: rubric
        rubric: evals/plan-rubric.md
    - name: implement
      produces: diff
      inputs: [plan, repo-state]
      skill: dev-executor
    - name: review
      produces: review
      inputs: [diff, plan]
      skill: dev-pr-reviewer
      eval:
        method: pairwise

Typical capture flow:

  1. etude init
     # scaffold .etude/ and register refspecs

  2. etude capture plan --run <run-id> --output plan=plan.md --skill-id dev-planner
     # capture the plan stage artifact

  3. etude capture implement --run <run-id> --output diff=diff.patch \
       --input plan=<artifact-ref> --skill-id dev-executor
     # capture the implement stage, referencing the prior plan artifact

  4. etude capture-gate --run <run-id> --gate-file gate.json
     # append a gate reviewer record (reviewer/decision live inside gate.json)

  5. etude run list                    # verify the run was recorded
  6. etude run show <run-id>           # inspect manifest + stage details
  7. etude sync                        # push refs/etude/* to remote

For bulk capture from a YAML spec in one operation use 'etude capture-run'.
For replaying a stage with a new skill version use 'etude replay'.
For benchmarking a cohort use 'etude bench'.
`)

	return sb.String()
}

// skipCommand returns true for commands that should be excluded from the
// derived list: cobra's auto-injected "help" and "completion" commands, and
// any Hidden command.
func skipCommand(c *cobra.Command) bool {
	if c.Hidden {
		return true
	}
	name := c.Name()
	return name == "help" || name == "completion"
}

// deriveCommands walks root's non-hidden subcommands (filtering help/completion)
// and formats each as "  name    Short description". It recurses one level into
// commands that have their own subcommands, so "run list/show" and
// "retro capture/list/show/generate" appear in the output.
func deriveCommands(root *cobra.Command) []string {
	var lines []string

	// Collect top-level commands in sorted order for stability.
	topLevel := make([]*cobra.Command, 0, len(root.Commands()))
	for _, c := range root.Commands() {
		if !skipCommand(c) {
			topLevel = append(topLevel, c)
		}
	}
	sort.Slice(topLevel, func(i, j int) bool {
		return topLevel[i].Name() < topLevel[j].Name()
	})

	for _, c := range topLevel {
		lines = append(lines, fmt.Sprintf("  %-18s %s", c.Name(), c.Short))

		// Recurse one level into subcommands (e.g. run list/show, retro capture/…).
		if len(c.Commands()) > 0 {
			subs := make([]*cobra.Command, 0, len(c.Commands()))
			for _, sub := range c.Commands() {
				if !skipCommand(sub) {
					subs = append(subs, sub)
				}
			}
			sort.Slice(subs, func(i, j int) bool {
				return subs[i].Name() < subs[j].Name()
			})
			for _, sub := range subs {
				lines = append(lines, fmt.Sprintf("    %-16s %s", c.Name()+" "+sub.Name(), sub.Short))
			}
		}
	}

	return lines
}
