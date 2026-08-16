package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/spf13/cobra"
)

const doctorRemoteOutputLimit = 8 << 20

var errDoctorFailed = errors.New("etude doctor found failures")

type doctorStatus string

const (
	doctorOK    doctorStatus = "OK"
	doctorWarn  doctorStatus = "WARN"
	doctorFail  doctorStatus = "FAIL"
	doctorProxy doctorStatus = "PROXY"
)

type doctorFinding struct {
	status      doctorStatus
	check       string
	message     string
	remediation string
}

type doctorRunner struct {
	stdout io.Writer
	git    doctorGit
}

type doctorGit struct {
	root string
}

type doctorConfigEntry struct {
	scope  string
	origin string
	value  string
}

type doctorRemoteState struct {
	name           string
	endpoints      []doctorRemoteEndpoint
	atRiskRefs     map[string]bool
	configBefore   string
	configCaptured bool
	syncUnsafe     bool
	readErr        error
}

type doctorRemoteEndpoint struct {
	label string
	url   string
	refs  map[string]string
}

type doctorRefspec struct {
	raw      string
	force    bool
	negative bool
	src      string
	dst      string
	hasDst   bool
	matching bool
}

type doctorCommandResolution struct {
	program          string
	resolved         string
	cwd              string
	env              map[string]string
	inHarness        bool
	adapter          string
	adapterErr       string
	opaqueWrapper    bool
	indeterminate    string
	missingPath      string
	humanInstruction string
	err              string
}

func newDoctorCommand(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "doctor",
		Short:         "Check whether this repository's etude setup is safe and working",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := doctorRunner{stdout: out}
			return r.run(cmd.Context())
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	return cmd
}

func (r doctorRunner) run(ctx context.Context) error {
	root, err := doctorRepoRoot(ctx)
	if err != nil {
		return err
	}
	r.git.root = root

	findings := make([]doctorFinding, 0, 32)
	wf, reg := r.checkConfig(root, &findings)
	remotes, err := r.git.remotes(ctx)
	if err != nil {
		findings = append(findings, doctorFinding{doctorFail, "remotes", err.Error(), doctorHuman("repair Git repository access, then rerun etude doctor")})
	}
	if len(remotes) == 0 && err == nil {
		findings = append(findings, doctorFinding{doctorFail, "remotes", "no Git remote is configured, so run refs cannot be checked or synchronized", doctorHuman("add the intended Git remote URL, then run etude init --remote with that remote name")})
	}

	remoteStates := make([]doctorRemoteState, 0, len(remotes))
	for _, remote := range remotes {
		state := doctorRemoteState{name: remote}
		state.configBefore, err = r.git.remoteConfigSnapshot(ctx, remote)
		if err != nil {
			state.readErr = err
			findings = append(findings, doctorFinding{doctorFail, "run-refs[" + remote + "]", fmt.Sprintf("cannot read remote %q configuration: %v", remote, err), doctorHuman("repair Git configuration access, then rerun etude doctor")})
			remoteStates = append(remoteStates, state)
			continue
		}
		state.configCaptured = true
		urlEntries, urlConfigErr := r.git.configEntries(ctx, "remote."+remote+".url")
		if urlConfigErr != nil {
			state.syncUnsafe = true
			findings = append(findings, doctorFinding{doctorFail, "remote-config[" + remote + "]", urlConfigErr.Error(), doctorHuman("repair Git configuration access, then rerun etude doctor")})
		} else if len(urlEntries) == 0 {
			state.syncUnsafe = true
			findings = append(findings, doctorFinding{doctorFail, "remote-config[" + remote + "]", "no fetch URL is configured, so etude sync cannot refresh the local mirror", doctorHuman("add the intended remote URL; doctor cannot derive its endpoint")})
		} else {
			for _, entry := range urlEntries {
				if entry.value == "" || doctorContainsControl(entry.value) || !utf8.ValidString(entry.value) {
					state.syncUnsafe = true
					findings = append(findings, doctorFinding{doctorFail, "remote-config[" + remote + "]", "configured fetch URL is empty, non-UTF-8, or contains a control character", doctorHuman("replace the invalid URL with the intended literal endpoint")})
					break
				}
			}
			if len(urlEntries) > 1 {
				state.syncUnsafe = true
				findings = append(findings, doctorFinding{doctorProxy, "push-endpoint[" + remote + "]", "NOT CHECKED: multiple remote URL entries may be used as push destinations, but one fetched mirror cannot prove every endpoint is non-fast-forward safe", doctorHuman("compare every configured push destination with the local and fetched mirror refs before synchronizing")})
			}
		}
		pushURLEntries, pushURLErr := r.git.configEntries(ctx, "remote."+remote+".pushurl")
		if pushURLErr != nil {
			state.syncUnsafe = true
			findings = append(findings, doctorFinding{doctorFail, "remote-config[" + remote + "]", pushURLErr.Error(), doctorHuman("repair Git configuration access, then rerun etude doctor")})
		} else if len(pushURLEntries) > 0 {
			state.syncUnsafe = true
			invalidPushURL := false
			for _, entry := range pushURLEntries {
				if entry.value == "" || doctorContainsControl(entry.value) || !utf8.ValidString(entry.value) {
					invalidPushURL = true
					findings = append(findings, doctorFinding{doctorFail, "remote-config[" + remote + "]", "configured pushurl is empty, non-UTF-8, or contains a control character", doctorHuman("replace the invalid pushurl with the intended literal endpoint")})
				}
			}
			if !invalidPushURL {
				findings = append(findings, doctorFinding{doctorProxy, "push-endpoint[" + remote + "]", "NOT CHECKED: configured pushurl endpoints are not represented by the fetch mirror, so doctor cannot prove etude sync will be non-fast-forward safe", doctorHuman("compare the configured push endpoint with the local and fetched mirror refs before synchronizing")})
			}
		}
		if mirrorEntries, mirrorErr := r.git.configEntries(ctx, "remote."+remote+".mirror"); mirrorErr == nil && len(mirrorEntries) > 0 {
			mirrorUnsafe, boolErr := r.git.configBool(ctx, "remote."+remote+".mirror")
			if boolErr != nil {
				// An indeterminate mirror mode must never produce advice that can
				// delete remote-only refs.
				mirrorUnsafe = true
			}
			state.syncUnsafe = state.syncUnsafe || mirrorUnsafe
		}
		mirrorRefs, mirrorErr := r.git.mirrorEtudeRefs(ctx, remote)
		if mirrorErr != nil {
			state.readErr = mirrorErr
			findings = append(findings, doctorFinding{doctorFail, "run-refs[" + remote + "]", mirrorErr.Error(), doctorHuman("repair local Git ref access, then rerun etude doctor")})
		} else {
			state.endpoints = append(state.endpoints, doctorRemoteEndpoint{label: "local mirror (last update time NOT RECORDED)", refs: mirrorRefs})
			findings = append(findings, doctorFinding{doctorProxy, "remote-reachability[" + remote + "]", "NOT CHECKED: doctor performs no network or credential operation; run-ref comparison uses the local refs/etude-mirror snapshot, whose last fetch time Git does not record", ""})
		}
		remoteStates = append(remoteStates, state)
	}

	// Mirror-first ordering is deliberate: a local ref created while doctor is
	// running can produce a conservative warning, but cannot be missed as mirrored.
	localRefs, localErr := r.git.localEtudeRefs(ctx)
	if localErr != nil {
		findings = append(findings, doctorFinding{doctorFail, "run-refs", localErr.Error(), doctorHuman("repair local Git ref access, then rerun etude doctor")})
		localRefs = map[string]string{}
	}
	for i := range remoteStates {
		state := &remoteStates[i]
		if state.readErr == nil && localErr == nil {
			state.atRiskRefs = r.checkUnpushedRefs(ctx, state.name, localRefs, state.endpoints, state.syncUnsafe, &findings)
		}
		after, snapshotErr := r.git.remoteConfigSnapshot(ctx, state.name)
		configChanged := state.configCaptured && (snapshotErr != nil || after != state.configBefore)
		if configChanged {
			findings = append(findings, doctorFinding{doctorWarn, "concurrency[" + state.name + "]", "repository remote configuration changed during doctor; this diagnosis is a non-transactional snapshot", "etude doctor"})
			for j := range findings {
				if findings[j].check == "run-refs["+state.name+"]" && findings[j].remediation != "" {
					findings[j].remediation = "etude doctor"
				}
			}
		}
	}
	findings = append(findings, doctorFinding{doctorProxy, "snapshot", "ordered read-only observations are conservative, not transactional; concurrent ABA or later changes require a rerun", ""})

	for _, state := range remoteStates {
		r.checkRemoteRefspecs(ctx, state, &findings)
	}

	if wf != nil || reg != nil {
		r.checkReferencedPathsAndCommands(root, wf, reg, &findings)
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].check != findings[j].check {
			return findings[i].check < findings[j].check
		}
		if findings[i].status != findings[j].status {
			return findings[i].status < findings[j].status
		}
		return findings[i].message < findings[j].message
	})

	failures := 0
	for _, f := range findings {
		fmt.Fprintf(r.stdout, "%s %-24s %s\n", f.status, doctorTerminalSafe(f.check), doctorTerminalSafe(f.message))
		if f.remediation != "" {
			fmt.Fprintf(r.stdout, "  remedy: %s\n", doctorTerminalSafe(f.remediation))
		}
		if f.status == doctorFail {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("%w: %d", errDoctorFailed, failures)
	}
	return nil
}

func (r doctorRunner) checkConfig(root string, findings *[]doctorFinding) (*workflow.Workflow, *registry.Registry) {
	wfPath := filepath.Join(root, ".etude", "workflow.yaml")
	regPath := filepath.Join(root, ".etude", "registry.yaml")
	wfBytes, wfErr := os.ReadFile(wfPath)
	regBytes, regErr := os.ReadFile(regPath)
	if errors.Is(wfErr, os.ErrNotExist) && errors.Is(regErr, os.ErrNotExist) {
		*findings = append(*findings, doctorFinding{doctorFail, "config", ".etude/workflow.yaml and .etude/registry.yaml are missing", "etude init"})
		return nil, nil
	}

	var wf *workflow.Workflow
	if wfErr != nil {
		*findings = append(*findings, doctorFinding{doctorFail, "config", fmt.Sprintf("workflow.yaml cannot be read: %v", wfErr), doctorHuman("restore the authored .etude/workflow.yaml without overwriting registry.yaml")})
	} else if parsed, parseErr := workflow.ParseYAML(wfBytes); parseErr != nil {
		*findings = append(*findings, doctorFinding{doctorFail, "config", fmt.Sprintf("workflow.yaml does not parse: %v", parseErr), doctorHuman("edit .etude/workflow.yaml so it satisfies the workflow schema")})
	} else {
		wf = &parsed
	}

	var reg *registry.Registry
	if regErr != nil {
		*findings = append(*findings, doctorFinding{doctorFail, "config", fmt.Sprintf("registry.yaml cannot be read: %v", regErr), doctorHuman("restore the authored .etude/registry.yaml without overwriting workflow.yaml")})
	} else if parsed, parseErr := registry.ParseYAML(regBytes); parseErr != nil {
		*findings = append(*findings, doctorFinding{doctorFail, "config", fmt.Sprintf("registry.yaml does not parse: %v", parseErr), doctorHuman("edit .etude/registry.yaml so it satisfies the registry schema")})
	} else {
		reg = &parsed
	}
	if wf == nil || reg == nil {
		return wf, reg
	}

	broken := 0
	checkRunner := func(where string, runner *workflow.Runner) {
		if runner == nil || runner.Name == "" {
			return
		}
		if _, ok := reg.Seats[runner.Name]; !ok {
			broken++
			*findings = append(*findings, doctorFinding{doctorFail, "config", fmt.Sprintf("%s references undefined registry seat %q", where, runner.Name), doctorHuman("define that seat in .etude/registry.yaml or change the runner reference")})
		}
	}
	checkRunner("default_runner", wf.DefaultRunner)
	for _, stage := range wf.Stages {
		checkRunner("stage "+stage.Name+" runner", stage.Runner)
		if stage.Gate == nil {
			continue
		}
		if stage.Gate.Tier != "" {
			if _, ok := reg.Tiers[stage.Gate.Tier]; !ok {
				broken++
				*findings = append(*findings, doctorFinding{doctorFail, "config", fmt.Sprintf("stage %q gate references undefined tier %q", stage.Name, stage.Gate.Tier), doctorHuman("define that tier in .etude/registry.yaml or change the stage gate tier")})
			}
		}
		for _, seat := range stage.Gate.Seats {
			if _, ok := reg.Seats[seat]; !ok {
				broken++
				*findings = append(*findings, doctorFinding{doctorFail, "config", fmt.Sprintf("stage %q gate references undefined seat %q", stage.Name, seat), doctorHuman("define that seat in .etude/registry.yaml or change the inline gate seat")})
			}
		}
		for i := range stage.Gate.Checks {
			checkRunner(fmt.Sprintf("stage %s gate check %d", stage.Name, i+1), &stage.Gate.Checks[i])
		}
	}
	if broken == 0 {
		*findings = append(*findings, doctorFinding{doctorOK, "config", "workflow.yaml and registry.yaml parse and cross-resolve", ""})
	}
	return wf, reg
}

func (r doctorRunner) checkRemoteRefspecs(ctx context.Context, state doctorRemoteState, findings *[]doctorFinding) {
	remote := state.name
	fetchKey := "remote." + remote + ".fetch"
	pushKey := "remote." + remote + ".push"
	fetchEntries, fetchErr := r.git.configEntries(ctx, fetchKey)
	pushEntries, pushErr := r.git.configEntries(ctx, pushKey)
	if fetchErr != nil {
		*findings = append(*findings, doctorFinding{doctorFail, "fetch-refspec[" + remote + "]", fetchErr.Error(), doctorHuman("repair Git config access and rerun etude doctor")})
		return
	}
	if pushErr != nil {
		*findings = append(*findings, doctorFinding{doctorFail, "push-refspec[" + remote + "]", pushErr.Error(), doctorHuman("repair Git config access and rerun etude doctor")})
		return
	}

	dangerous := 0
	broad := 0
	validFetch := make([]doctorRefspec, 0, len(fetchEntries))
	validFetchEntries := make([]doctorConfigEntry, 0, len(fetchEntries))
	for _, entry := range fetchEntries {
		rs, err := r.git.parseRefspec(ctx, entry.value, true)
		if err != nil {
			*findings = append(*findings, doctorFinding{doctorFail, "fetch-refspec[" + remote + "]", fmt.Sprintf("invalid fetch refspec %q: %v", entry.value, err), doctorConfigUnsetRemediation(r.git.root, fetchKey, entry)})
			continue
		}
		validFetch = append(validFetch, rs)
		validFetchEntries = append(validFetchEntries, entry)
	}
	for i, rs := range validFetch {
		entry := validFetchEntries[i]
		if rs.negative || !rs.hasDst {
			continue
		}
		if strings.HasPrefix(rs.dst, "refs/etude/remotes/") {
			dangerous++
			*findings = append(*findings, doctorFinding{doctorFail, "fetch-refspec[" + remote + "]", fmt.Sprintf("%q uses the rejected nested refs/etude/remotes mirror namespace; fetched mirrors belong under refs/etude-mirror", entry.value), doctorConfigUnsetRemediation(r.git.root, fetchKey, entry)})
			continue
		}
		intersects := false
		for _, kind := range refstore.Kinds {
			if doctorPatternIntersectsPrefix(rs.dst, "refs/etude/"+kind+"/") {
				intersects = true
				break
			}
		}
		if !intersects {
			continue
		}
		if doctorFetchMappingFullyExcluded(rs, validFetch, refstore.Kinds) {
			continue
		}
		if doctorPatternEtudeScoped(rs.dst) {
			dangerous++
			*findings = append(*findings, doctorFinding{doctorFail, "fetch-refspec[" + remote + "]", fmt.Sprintf("%q writes fetched refs into the authoritative refs/etude namespace and makes them prunable", entry.value), doctorConfigUnsetRemediation(r.git.root, fetchKey, entry)})
		} else {
			broad++
			*findings = append(*findings, doctorFinding{doctorFail, "fetch-refspec[" + remote + "]", fmt.Sprintf("user-owned broad fetch refspec %q intersects refs/etude; it is not safe to delete automatically", entry.value), doctorHuman("narrow this fetch mapping so its destination cannot match refs/etude/{runs,retros,evals}/*")})
		}
	}
	if dangerous == 0 && broad == 0 {
		*findings = append(*findings, doctorFinding{doctorOK, "fetch-refspec[" + remote + "]", "no fetch refspec can prune authoritative refs/etude refs", ""})
	}

	validPush := make([]doctorRefspec, 0, len(pushEntries))
	for _, entry := range pushEntries {
		rs, err := r.git.parseRefspec(ctx, entry.value, false)
		if err != nil {
			*findings = append(*findings, doctorFinding{doctorFail, "push-refspec[" + remote + "]", fmt.Sprintf("invalid push refspec %q: %v", entry.value, err), doctorConfigUnsetRemediation(r.git.root, pushKey, entry)})
			continue
		}
		validPush = append(validPush, rs)
	}
	mirrorPush := false
	if entries, err := r.git.configEntries(ctx, "remote."+remote+".mirror"); err == nil && len(entries) > 0 {
		last := entries[len(entries)-1]
		mirrorPush, err = r.git.configBool(ctx, "remote."+remote+".mirror")
		if err != nil {
			*findings = append(*findings, doctorFinding{doctorFail, "push-refspec[" + remote + "]", fmt.Sprintf("remote.%s.mirror has invalid boolean value %q", remote, last.value), doctorConfigUnsetRemediation(r.git.root, "remote."+remote+".mirror", last)})
		}
	}
	missingKinds := make([]string, 0, len(refstore.Kinds))
	for _, kind := range refstore.Kinds {
		if mirrorPush {
			continue
		}
		prefix := "refs/etude/" + kind + "/"
		covered := false
		for _, rs := range validPush {
			if doctorRefspecMapsPrefix(rs, prefix, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			missingKinds = append(missingKinds, kind)
		}
	}
	if len(missingKinds) > 0 {
		shapes := doctorMisleadingPushShapes(validPush)
		message := "no name-preserving push coverage for refs/etude/" + strings.Join(missingKinds, ", refs/etude/")
		if len(shapes) > 0 {
			message += "; observed " + strings.Join(shapes, ", ")
		}
		*findings = append(*findings, doctorFinding{doctorFail, "push-refspec[" + remote + "]", message, "etude init --remote " + doctorShellQuote(remote)})
	} else if mirrorPush {
		*findings = append(*findings, doctorFinding{doctorWarn, "push-refspec[" + remote + "]", "remote mirror-push semantics cover every refs/etude kind but can delete remote-only refs", doctorHuman("replace mirror-push semantics with explicit name-preserving refs/etude push mappings if remote-only refs must be preserved")})
	} else {
		*findings = append(*findings, doctorFinding{doctorOK, "push-refspec[" + remote + "]", "name-preserving push mappings cover every refs/etude kind", ""})
	}

	missingMirrors := make([]string, 0, len(refstore.Kinds))
	negativeMirrors := make([]string, 0, len(refstore.Kinds))
	for _, kind := range refstore.Kinds {
		sourcePrefix := "refs/etude/" + kind + "/"
		covered := false
		excluded := false
		for _, rs := range validFetch {
			if rs.negative && doctorPatternIntersectsPrefix(rs.src, sourcePrefix) {
				excluded = true
			}
			if doctorRefspecMapsPrefix(rs, sourcePrefix, refstore.MirrorPrefix(remote, kind)) {
				covered = true
			}
		}
		if excluded {
			negativeMirrors = append(negativeMirrors, kind)
		}
		if !covered || excluded {
			missingMirrors = append(missingMirrors, kind)
		}
	}
	if len(missingMirrors) > 0 {
		remedy := "etude init --remote " + doctorShellQuote(remote)
		message := "safe fetch mirroring is missing for " + strings.Join(missingMirrors, ", ")
		if len(negativeMirrors) > 0 {
			message += "; negative fetch entries exclude " + strings.Join(negativeMirrors, ", ")
			remedy = doctorHuman("remove or narrow the negative fetch entries only if mirroring those etude kinds is intended")
		}
		*findings = append(*findings, doctorFinding{doctorWarn, "fetch-mirror[" + remote + "]", message, remedy})
	} else {
		*findings = append(*findings, doctorFinding{doctorOK, "fetch-mirror[" + remote + "]", "safe per-kind mirror fetch mappings are configured", ""})
	}
}

func (r doctorRunner) checkUnpushedRefs(ctx context.Context, remote string, local map[string]string, endpoints []doctorRemoteEndpoint, syncUnsafe bool, findings *[]doctorFinding) map[string]bool {
	refs := make([]string, 0, len(local))
	for ref := range local {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	if len(refs) == 0 {
		*findings = append(*findings, doctorFinding{doctorOK, "run-refs[" + remote + "]", "no local refs/etude run records need synchronization", ""})
		return map[string]bool{}
	}
	atRisk := make(map[string]bool)
	shallow := r.git.isShallow()
	for _, ref := range refs {
		localOID := local[ref]
		needsPush := false
		unsafePush := false
		localBehindOnly := false
		details := make([]string, 0, len(endpoints))
		for endpointIndex, endpoint := range endpoints {
			remoteOID, ok := endpoint.refs[ref]
			label := endpoint.label
			if label == "" {
				label = fmt.Sprintf("endpoint %d", endpointIndex+1)
			}
			if !ok {
				needsPush = true
				atRisk[ref] = true
				details = append(details, label+" absent")
				continue
			}
			if localOID == remoteOID {
				details = append(details, label+" matches")
				continue
			}
			if shallow || !r.git.objectExists(ctx, remoteOID) {
				unsafePush = true
				atRisk[ref] = true
				details = append(details, label+" direction UNKNOWN")
				continue
			}
			localBehind, errA := r.git.isAncestor(ctx, localOID, remoteOID)
			remoteBehind, errB := r.git.isAncestor(ctx, remoteOID, localOID)
			switch {
			case errA != nil || errB != nil || (!localBehind && !remoteBehind):
				unsafePush = true
				atRisk[ref] = true
				details = append(details, label+" direction UNKNOWN")
			case localBehind:
				unsafePush = true
				localBehindOnly = true
				details = append(details, label+" local is behind")
			case remoteBehind:
				needsPush = true
				atRisk[ref] = true
				details = append(details, label+" local is ahead")
			}
		}
		switch {
		case unsafePush:
			message := fmt.Sprintf("%s differs from the last fetched mirror for %q (%s); mirror staleness means the current remote may differ", ref, remote, strings.Join(details, ", "))
			instruction := "refresh the remote mirror and compare again before changing or synchronizing this ref"
			if localBehindOnly && !atRisk[ref] {
				instruction = "refresh this remote's etude mirror and compare before changing the local ref"
			}
			*findings = append(*findings, doctorFinding{doctorWarn, "run-refs[" + remote + "]", message, doctorHuman(instruction)})
		case needsPush:
			remedy := "etude sync --remote " + doctorShellQuote(remote)
			if syncUnsafe {
				remedy = doctorHuman("resolve the reported remote configuration uncertainty and compare the actual push endpoint before synchronizing")
			}
			*findings = append(*findings, doctorFinding{doctorWarn, "run-refs[" + remote + "]", fmt.Sprintf("%s is absent or behind in the last fetched mirror for %q (%s); the current remote may already match or be newer", ref, remote, strings.Join(details, ", ")), remedy})
		}
	}
	if len(atRisk) == 0 {
		matched := true
		for _, ref := range refs {
			for _, endpoint := range endpoints {
				if endpoint.refs[ref] != local[ref] {
					matched = false
					break
				}
			}
		}
		if matched {
			*findings = append(*findings, doctorFinding{doctorProxy, "run-refs[" + remote + "]", "all local refs/etude records match the last fetched mirror; the current remote may have changed since that fetch", ""})
		}
	}
	return atRisk
}

func (r doctorRunner) checkReferencedPathsAndCommands(root string, wf *workflow.Workflow, reg *registry.Registry, findings *[]doctorFinding) {
	pathFailures := 0
	etudeRoot := filepath.Join(root, ".etude")
	if wf != nil {
		for _, stage := range wf.Stages {
			if stage.Eval == nil || stage.Eval.Method != "rubric" {
				continue
			}
			path := filepath.Join(etudeRoot, filepath.FromSlash(stage.Eval.Rubric))
			if err := doctorReadableRegularWithin(etudeRoot, path); err != nil {
				pathFailures++
				*findings = append(*findings, doctorFinding{doctorFail, "path[rubric]", fmt.Sprintf("stage %q rubric %q resolves from .etude/ to %q: %v", stage.Name, stage.Eval.Rubric, path, err), doctorHuman("create a readable regular rubric file containing the evaluation criteria for stage " + doctorShellQuote(stage.Name))})
			}
		}
	}
	if wf != nil && pathFailures == 0 {
		*findings = append(*findings, doctorFinding{doctorOK, "paths", "all configured rubric files are readable regular files under .etude", ""})
	}

	type commandSpec struct{ where, invoke string }
	commands := make([]commandSpec, 0)
	if reg != nil {
		for name, seat := range reg.Seats {
			commands = append(commands, commandSpec{"seat " + name + " primary", seat.Invoke})
			for i, fb := range seat.InvocationFallbacks {
				commands = append(commands, commandSpec{fmt.Sprintf("seat %s fallback %d", name, i+1), fb.Invoke})
			}
		}
	}
	appendRunner := func(where string, runner *workflow.Runner) {
		if runner != nil && runner.Command != "" {
			commands = append(commands, commandSpec{where, runner.Command})
		}
	}
	if wf != nil {
		appendRunner("workflow default runner", wf.DefaultRunner)
		for _, stage := range wf.Stages {
			appendRunner("stage "+stage.Name+" runner", stage.Runner)
			if stage.Gate != nil {
				for i := range stage.Gate.Checks {
					appendRunner(fmt.Sprintf("stage %s gate check %d", stage.Name, i+1), &stage.Gate.Checks[i])
				}
			}
		}
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].where < commands[j].where })
	for _, spec := range commands {
		res := doctorResolveCommand(spec.invoke, root, doctorBaseEnv())
		check := "command[" + spec.where + "]"
		switch {
		case res.inHarness:
			*findings = append(*findings, doctorFinding{doctorProxy, check, "host-provided in-harness invocation; PATH/authentication not probed", ""})
		case res.indeterminate != "":
			*findings = append(*findings, doctorFinding{doctorProxy, check, res.indeterminate, ""})
		case res.err != "":
			status := doctorFail
			instruction := "install executable " + doctorShellQuote(res.program) + " and make it reachable under this invocation's effective PATH/cwd"
			if res.humanInstruction != "" {
				instruction = res.humanInstruction
			}
			if res.missingPath != "" {
				status = doctorFail
				if res.humanInstruction == "" {
					instruction = "provide a regular executable runner script at " + doctorShellQuote(res.missingPath)
				}
			}
			*findings = append(*findings, doctorFinding{status, check, res.err, doctorHuman(instruction)})
		case res.adapterErr != "":
			*findings = append(*findings, doctorFinding{doctorFail, check, res.adapterErr, doctorHuman("restore a regular executable seat adapter at the resolved path")})
		default:
			msg := fmt.Sprintf("binary present at %s; this does not prove authentication, quota, or tier eligibility", res.resolved)
			if res.opaqueWrapper {
				msg = fmt.Sprintf("wrapper binary %s is present; the wrapped reviewer is NOT CHECKED because doctor does not execute or interpret arbitrary wrapper programs", res.resolved)
				if res.adapter != "" {
					msg = fmt.Sprintf("adapter %s and wrapper binary %s are present; the wrapped reviewer is NOT CHECKED because doctor does not execute or interpret arbitrary wrapper programs", res.adapter, res.resolved)
				}
			} else if res.adapter != "" {
				msg = fmt.Sprintf("adapter %s and reviewer binary %s are present; this does not prove authentication, quota, or tier eligibility", res.adapter, res.resolved)
			}
			*findings = append(*findings, doctorFinding{doctorProxy, check, msg, ""})
		}
	}
}

func (g doctorGit) remotes(ctx context.Context) ([]string, error) {
	const pattern = `^remote\..*\..*$`
	out, err := g.runExitOneOK(ctx, g.root, "config", "--null", "--name-only", "--get-regexp", pattern)
	if err != nil {
		return nil, fmt.Errorf("list Git remote configuration: %w", err)
	}
	seen := make(map[string]bool)
	for _, part := range bytes.Split(out, []byte{0}) {
		key := string(part)
		if key == "" {
			continue
		}
		nameAndKey := strings.TrimPrefix(key, "remote.")
		separator := strings.LastIndex(nameAndKey, ".")
		if separator <= 0 || separator == len(nameAndKey)-1 {
			return nil, fmt.Errorf("Git returned an invalid remote configuration key %q", key)
		}
		name := nameAndKey[:separator]
		if doctorContainsControl(name) || !utf8.ValidString(name) {
			return nil, fmt.Errorf("Git returned an invalid remote name %q", name)
		}
		seen[name] = true
	}
	lines := make([]string, 0, len(seen))
	for name := range seen {
		lines = append(lines, name)
	}
	sort.Strings(lines)
	return lines, nil
}

func (g doctorGit) remoteConfigSnapshot(ctx context.Context, remote string) (string, error) {
	keys := []string{
		"remote." + remote + ".url",
		"remote." + remote + ".pushurl",
		"remote." + remote + ".fetch",
		"remote." + remote + ".push",
		"remote." + remote + ".mirror",
	}
	var snapshot strings.Builder
	for _, key := range keys {
		entries, err := g.configEntries(ctx, key)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			fmt.Fprintf(&snapshot, "%s\x00%s\x00%s\x00%s\x00", key, entry.scope, entry.origin, entry.value)
		}
	}
	return snapshot.String(), nil
}

func (g doctorGit) configEntries(ctx context.Context, key string) ([]doctorConfigEntry, error) {
	out, err := g.runExitOneOK(ctx, g.root, "config", "--null", "--show-origin", "--show-scope", "--get-all", key)
	if err != nil {
		return nil, fmt.Errorf("git config --get-all %s: %w", key, err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	parts := bytes.Split(out, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts)%3 != 0 {
		return nil, fmt.Errorf("unexpected git config origin/scope output for %s", key)
	}
	entries := make([]doctorConfigEntry, 0, len(parts)/3)
	for i := 0; i < len(parts); i += 3 {
		entries = append(entries, doctorConfigEntry{string(parts[i]), string(parts[i+1]), string(parts[i+2])})
	}
	return entries, nil
}

func (g doctorGit) configBool(ctx context.Context, key string) (bool, error) {
	out, err := g.run(ctx, g.root, "config", "--type=bool", "--get", key)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected Git boolean output %q", out)
	}
}

func (g doctorGit) localEtudeRefs(ctx context.Context) (map[string]string, error) {
	args := []string{"for-each-ref", "--format=%(objectname) %(refname)"}
	for _, kind := range refstore.Kinds {
		args = append(args, "refs/etude/"+kind+"/")
	}
	out, err := g.run(ctx, g.root, args...)
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref refs/etude: %w", err)
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected ref output line %q", line)
		}
		refs[fields[1]] = fields[0]
	}
	return refs, nil
}

func (g doctorGit) mirrorEtudeRefs(ctx context.Context, remote string) (map[string]string, error) {
	args := []string{"for-each-ref", "--format=%(objectname) %(refname)"}
	for _, kind := range refstore.Kinds {
		args = append(args, refstore.MirrorPrefix(remote, kind))
	}
	out, err := g.run(ctx, g.root, args...)
	if err != nil {
		return nil, fmt.Errorf("read local mirror refs for %q: %w", remote, err)
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected mirror ref output line %q", line)
		}
		mapped := ""
		for _, kind := range refstore.Kinds {
			prefix := refstore.MirrorPrefix(remote, kind)
			if strings.HasPrefix(fields[1], prefix) {
				mapped = "refs/etude/" + kind + "/" + strings.TrimPrefix(fields[1], prefix)
				break
			}
		}
		if mapped == "" {
			return nil, fmt.Errorf("mirror ref %q is outside the expected namespace", fields[1])
		}
		refs[mapped] = fields[0]
	}
	return refs, nil
}

func (g doctorGit) objectExists(ctx context.Context, oid string) bool {
	_, err := g.run(ctx, g.root, "cat-file", "-e", oid+"^{commit}")
	return err == nil
}

func (g doctorGit) isAncestor(ctx context.Context, older, newer string) (bool, error) {
	_, err := g.run(ctx, g.root, "merge-base", "--is-ancestor", older, newer)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (g doctorGit) isShallow() bool {
	common, err := filepath.EvalSymlinks(filepath.Join(g.root, ".git"))
	if err == nil {
		if info, statErr := os.Stat(common); statErr == nil && info.IsDir() {
			_, err = os.Stat(filepath.Join(common, "shallow"))
			return err == nil
		}
	}
	cmd := exec.Command("git", "-C", g.root, "rev-parse", "--git-common-dir")
	cmd.Env = doctorGitEnv()
	var stdout doctorLimitedBuffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	if err != nil {
		return false
	}
	if stdout.truncated {
		return false
	}
	path := doctorTrimGitLine(stdout.String())
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.root, path)
	}
	_, err = os.Stat(filepath.Join(path, "shallow"))
	return err == nil
}

func (g doctorGit) parseRefspec(ctx context.Context, value string, fetch bool) (doctorRefspec, error) {
	rs := doctorRefspec{raw: value}
	s := value
	if strings.HasPrefix(s, "+") {
		rs.force = true
		s = strings.TrimPrefix(s, "+")
	}
	if strings.HasPrefix(s, "^") {
		if !fetch {
			return rs, fmt.Errorf("negative refspec is only valid for fetch")
		}
		rs.negative = true
		s = strings.TrimPrefix(s, "^")
		if s == "" || strings.Contains(s, ":") {
			return rs, fmt.Errorf("negative fetch refspec requires one source and no destination")
		}
		rs.src = s
		if err := g.validateRefspecSide(ctx, s); err != nil {
			return rs, err
		}
		return rs, nil
	}
	if strings.Count(s, ":") > 1 {
		return rs, fmt.Errorf("more than one ':' separator")
	}
	src, dst, hasDst := strings.Cut(s, ":")
	rs.src, rs.dst, rs.hasDst = src, dst, hasDst
	if !fetch && hasDst && src == "" && dst == "" {
		rs.matching = true
		return rs, nil
	}
	if src == "" {
		if fetch {
			return rs, fmt.Errorf("fetch refspec source is empty")
		}
		if !hasDst || dst == "" {
			return rs, fmt.Errorf("empty-source push refspec requires a destination")
		}
	} else {
		var err error
		if !fetch && !strings.Contains(src, "*") {
			err = g.validatePushSource(ctx, src)
		} else if fetch && !strings.Contains(src, "*") {
			err = g.validateFetchSource(ctx, src)
		} else {
			err = g.validateRefspecSide(ctx, src)
		}
		if err != nil {
			return rs, fmt.Errorf("source: %w", err)
		}
	}
	if hasDst {
		if dst == "" {
			return rs, fmt.Errorf("destination is empty")
		}
		if err := g.validateRefspecSide(ctx, dst); err != nil {
			return rs, fmt.Errorf("destination: %w", err)
		}
	}
	if hasDst && (strings.Count(src, "*") != strings.Count(dst, "*")) {
		return rs, fmt.Errorf("source and destination wildcard counts differ")
	}
	return rs, nil
}

func (g doctorGit) validateFetchSource(ctx context.Context, source string) error {
	if err := g.validateRefspecSide(ctx, source); err == nil {
		return nil
	}
	if strings.HasPrefix(source, "-") || strings.ContainsAny(source, "\x00\r\n\t ") {
		return fmt.Errorf("invalid fetch source %q", source)
	}
	if _, err := g.run(ctx, g.root, "check-ref-format", "--allow-onelevel", source); err != nil {
		return fmt.Errorf("invalid fetch source %q", source)
	}
	return nil
}

func (g doctorGit) validatePushSource(ctx context.Context, source string) error {
	if strings.HasPrefix(source, "-") || strings.ContainsAny(source, "\x00\r\n\t ") {
		return fmt.Errorf("invalid push source %q", source)
	}
	if _, err := g.run(ctx, g.root, "check-ref-format", "--allow-onelevel", source); err == nil {
		return nil
	}
	if _, err := g.run(ctx, g.root, "rev-parse", "--verify", "--quiet", source+"^{}"); err == nil {
		return nil
	}
	return fmt.Errorf("invalid or unresolved push source %q", source)
}

func (g doctorGit) validateRefspecSide(ctx context.Context, side string) error {
	if strings.HasPrefix(side, "-") {
		return fmt.Errorf("option-shaped ref pattern %q", side)
	}
	if strings.Count(side, "*") > 1 {
		return fmt.Errorf("more than one wildcard")
	}
	args := []string{"check-ref-format"}
	if strings.Contains(side, "*") {
		args = append(args, "--refspec-pattern")
	}
	args = append(args, side)
	if _, err := g.run(ctx, g.root, args...); err != nil {
		return fmt.Errorf("invalid ref pattern %q", side)
	}
	return nil
}

func (g doctorGit) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if err := doctorValidateGitArgs(args); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = doctorGitEnv()
	var stdout doctorLimitedBuffer
	var stderr doctorLimitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.truncated {
		return nil, fmt.Errorf("git %s output exceeded %d bytes", args[0], doctorRemoteOutputLimit)
	}
	if message := strings.TrimSpace(stderr.String()); message != "" && (err != nil || args[0] == "for-each-ref") {
		if err == nil {
			return nil, fmt.Errorf("git %s reported stderr despite success: %s", args[0], message)
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return []byte(stdout.String()), err
}

func (g doctorGit) runExitOneOK(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := g.run(ctx, dir, args...)
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, nil
	}
	return nil, err
}

func doctorValidateGitArgs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("doctor Git call has no subcommand")
	}
	switch args[0] {
	case "config":
		if len(args) == 5 && args[1] == "--null" && args[2] == "--name-only" && args[3] == "--get-regexp" && args[4] == `^remote\..*\..*$` {
			return nil
		}
		if len(args) == 6 && args[1] == "--null" && args[2] == "--show-origin" && args[3] == "--show-scope" && args[4] == "--get-all" {
			return nil
		}
		if len(args) == 4 && args[1] == "--type=bool" && args[2] == "--get" {
			return nil
		}
	case "for-each-ref":
		if len(args) >= 3 && strings.HasPrefix(args[1], "--format=") {
			return nil
		}
	case "cat-file":
		if len(args) == 3 && args[1] == "-e" {
			return nil
		}
	case "merge-base":
		if len(args) == 4 && args[1] == "--is-ancestor" {
			return nil
		}
	case "check-ref-format":
		if len(args) == 2 || (len(args) == 3 && (args[1] == "--refspec-pattern" || args[1] == "--allow-onelevel")) {
			return nil
		}
	case "rev-parse":
		if len(args) == 4 && args[1] == "--verify" && args[2] == "--quiet" {
			return nil
		}
	}
	return fmt.Errorf("doctor refused write-capable or unexpected Git argv: %q", args)
}

func doctorPatternIntersectsPrefix(pattern, prefix string) bool {
	if !strings.Contains(pattern, "*") {
		return strings.HasPrefix(pattern, prefix)
	}
	pre, _, _ := strings.Cut(pattern, "*")
	return strings.HasPrefix(pre, prefix) || strings.HasPrefix(prefix, pre)
}

func doctorPatternEtudeScoped(pattern string) bool {
	pre, _, _ := strings.Cut(pattern, "*")
	return strings.HasPrefix(pre, "refs/etude/")
}

func doctorAnyPatternMatch(pattern string, refs map[string]bool) bool {
	for ref := range refs {
		if doctorPatternMatches(pattern, ref) {
			return true
		}
	}
	return false
}

func doctorPatternMatches(pattern, ref string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == ref
	}
	pre, post, _ := strings.Cut(pattern, "*")
	return strings.HasPrefix(ref, pre) && strings.HasSuffix(ref, post) && len(ref) >= len(pre)+len(post)
}

func doctorMisleadingPushShapes(refspecs []doctorRefspec) []string {
	shapes := make(map[string]bool)
	for _, rs := range refspecs {
		switch {
		case rs.src == "" && strings.HasPrefix(rs.dst, "refs/etude/"):
			shapes["empty-source delete mapping"] = true
		case !strings.Contains(rs.src, "*") && strings.HasPrefix(rs.src, "refs/etude/"):
			shapes["single-ref mapping"] = true
		case strings.Contains(rs.src, "*") && doctorPatternIntersectsPrefix(rs.src, "refs/etude/"):
			probe := "refs/etude/runs/__doctor_probe__"
			if dst, ok := doctorMapRefspec(rs, probe); ok && dst != probe {
				shapes["name-changing mapping"] = true
			}
		}
	}
	result := make([]string, 0, len(shapes))
	for shape := range shapes {
		result = append(result, shape)
	}
	sort.Strings(result)
	return result
}

func doctorMapRefspec(rs doctorRefspec, ref string) (string, bool) {
	if rs.negative || rs.src == "" {
		return "", false
	}
	dstPattern := rs.dst
	if !rs.hasDst {
		dstPattern = rs.src
	}
	if !strings.Contains(rs.src, "*") {
		if rs.src != ref {
			return "", false
		}
		return dstPattern, true
	}
	pre, post, _ := strings.Cut(rs.src, "*")
	if !strings.HasPrefix(ref, pre) || !strings.HasSuffix(ref, post) || len(ref) < len(pre)+len(post) {
		return "", false
	}
	capture := ref[len(pre) : len(ref)-len(post)]
	return strings.Replace(dstPattern, "*", capture, 1), true
}

// doctorRefspecMapsPrefix proves that every ref below sourcePrefix maps to the
// same suffix below destinationPrefix. A single sentinel cannot establish this:
// an exact refspec for that sentinel would otherwise look like namespace-wide
// coverage.
func doctorRefspecMapsPrefix(rs doctorRefspec, sourcePrefix, destinationPrefix string) bool {
	if rs.negative || rs.src == "" || !strings.Contains(rs.src, "*") {
		return false
	}
	destination := rs.dst
	if !rs.hasDst {
		destination = rs.src
	}
	if !strings.Contains(destination, "*") {
		return false
	}
	sourcePre, sourcePost, _ := strings.Cut(rs.src, "*")
	destPre, destPost, _ := strings.Cut(destination, "*")
	if sourcePost != "" || destPost != "" || !strings.HasPrefix(sourcePrefix, sourcePre) {
		return false
	}
	return destPre+strings.TrimPrefix(sourcePrefix, sourcePre) == destinationPrefix
}

func doctorFetchMappingFullyExcluded(mapping doctorRefspec, all []doctorRefspec, kinds []string) bool {
	for _, candidate := range all {
		if candidate.negative && candidate.src == mapping.src {
			return true
		}
	}
	affected := false
	for _, kind := range kinds {
		destinationPrefix := "refs/etude/" + kind + "/"
		if !doctorPatternIntersectsPrefix(mapping.dst, destinationPrefix) {
			continue
		}
		affected = true
		sourcePrefix, exactSource, ok := doctorSourceForDestinationPrefix(mapping, destinationPrefix)
		if !ok {
			return false
		}
		excluded := false
		for _, candidate := range all {
			if !candidate.negative {
				continue
			}
			if exactSource {
				excluded = doctorPatternMatches(candidate.src, sourcePrefix)
			} else {
				excluded = doctorPatternCoversPrefix(candidate.src, sourcePrefix)
			}
			if excluded {
				break
			}
		}
		if !excluded {
			return false
		}
	}
	return affected
}

func doctorSourceForDestinationPrefix(mapping doctorRefspec, destinationPrefix string) (string, bool, bool) {
	if mapping.src == "" || mapping.negative {
		return "", false, false
	}
	if !strings.Contains(mapping.dst, "*") {
		if strings.HasPrefix(mapping.dst, destinationPrefix) && !strings.Contains(mapping.src, "*") {
			return mapping.src, true, true
		}
		return "", false, false
	}
	sourcePre, sourcePost, _ := strings.Cut(mapping.src, "*")
	destPre, destPost, _ := strings.Cut(mapping.dst, "*")
	if sourcePost != "" || destPost != "" {
		return "", false, false
	}
	switch {
	case strings.HasPrefix(destinationPrefix, destPre):
		return sourcePre + strings.TrimPrefix(destinationPrefix, destPre), false, true
	case strings.HasPrefix(destPre, destinationPrefix):
		return sourcePre, false, true
	default:
		return "", false, false
	}
}

func doctorPatternCoversPrefix(pattern, prefix string) bool {
	if !strings.Contains(pattern, "*") {
		return false
	}
	pre, post, _ := strings.Cut(pattern, "*")
	return post == "" && strings.HasPrefix(prefix, pre)
}

func doctorConfigUnsetRemediation(repoRoot, key string, entry doctorConfigEntry) string {
	if doctorContainsControl(entry.value) || doctorContainsControl(entry.origin) || !utf8.ValidString(entry.value) || !utf8.ValidString(entry.origin) {
		return doctorHuman("remove the exact control-character-bearing value from " + key + " in its reported config origin; no terminal-safe runnable command can represent it faithfully")
	}
	if runtime.GOOS == "windows" {
		return doctorHuman("remove this exact value from " + key + " in its reported config origin; doctor cannot infer whether the operator uses cmd.exe or PowerShell, whose quoting rules differ")
	}
	origin := strings.TrimPrefix(entry.origin, "file:")
	if origin == "" || entry.origin == "command line:" || entry.origin == "standard input:" {
		return doctorHuman("remove this exact value from its reported config origin " + entry.origin)
	}
	if !filepath.IsAbs(origin) {
		origin = filepath.Join(repoRoot, origin)
	}
	pattern := "^" + regexp.QuoteMeta(entry.value) + "$"
	return "git config --file " + doctorShellQuote(origin) + " --unset-all " + doctorShellQuote(key) + " " + doctorShellQuote(pattern)
}

func doctorHuman(instruction string) string {
	return "HUMAN AUTHORSHIP REQUIRED: " + instruction
}

type doctorLimitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *doctorLimitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := doctorRemoteOutputLimit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			b.truncated = true
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	} else if len(p) > 0 {
		b.truncated = true
	}
	return original, nil
}

func (b *doctorLimitedBuffer) String() string { return b.buf.String() }

func doctorRepoRoot(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	cmd.Env = doctorGitEnv()
	var stdout doctorLimitedBuffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	if err != nil {
		return "", fmt.Errorf("not a git repository (or any parent up to root %s)", cwd)
	}
	if stdout.truncated {
		return "", fmt.Errorf("repository root output exceeded %d bytes", doctorRemoteOutputLimit)
	}
	return doctorTrimGitLine(stdout.String()), nil
}

func doctorTrimGitLine(value string) string {
	return strings.TrimSuffix(value, "\n")
}

func doctorReadableRegularWithin(base, path string) error {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	baseReal, err := filepath.EvalSymlinks(baseAbs)
	if err != nil {
		return err
	}
	pathReal, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(baseReal, pathReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes %s", base)
	}
	info, err := os.Stat(pathReal)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	f, err := os.Open(pathReal)
	if err != nil {
		return fmt.Errorf("not readable: %w", err)
	}
	return f.Close()
}

func doctorBaseEnv() map[string]string {
	env := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func doctorResolveCommand(invoke, root string, baseEnv map[string]string) doctorCommandResolution {
	fields, splitErr := doctorSplitCommand(invoke)
	if splitErr != nil {
		res := doctorCommandResolution{cwd: root, env: cloneDoctorEnv(baseEnv)}
		res.err = "invocation cannot be parsed: " + splitErr.Error()
		return res
	}
	return doctorResolveCommandFields(fields, root, baseEnv)
}

func doctorResolveCommandFields(fields []string, root string, baseEnv map[string]string) doctorCommandResolution {
	res := doctorCommandResolution{cwd: root, env: cloneDoctorEnv(baseEnv)}
	if len(fields) == 0 {
		res.err = "invocation is empty"
		return res
	}
	if strings.HasPrefix(fields[0], "in-harness:") {
		res.inHarness = true
		return res
	}
	idx := 0
	ignoredEnvironment := false
	if doctorIsEnvProgram(fields[0]) {
		envExecutable, err := doctorLookPath(fields[0], res.cwd, res.env)
		if err != nil {
			res.program = fields[0]
			if doctorHasPathSeparator(fields[0]) {
				res.missingPath = fields[0]
				if !filepath.IsAbs(res.missingPath) {
					res.missingPath = filepath.Clean(filepath.Join(res.cwd, res.missingPath))
				}
			}
			res.err = fmt.Sprintf("env executable %q is not reachable", fields[0])
			return res
		}
		if err := doctorRegularExecutable(envExecutable); err != nil {
			res.program = fields[0]
			res.err = fmt.Sprintf("env executable %q is unusable: %v", envExecutable, err)
			return res
		}
		idx = 1
		for idx < len(fields) {
			token := fields[idx]
			switch {
			case token == "--":
				idx++
				goto command
			case token == "-i" || token == "--ignore-environment":
				res.env = make(map[string]string)
				ignoredEnvironment = true
				idx++
			case token == "-u" && idx+1 < len(fields):
				doctorEnvDelete(res.env, fields[idx+1])
				idx += 2
			case strings.HasPrefix(token, "--unset="):
				doctorEnvDelete(res.env, strings.TrimPrefix(token, "--unset="))
				idx++
			case (token == "-C" || token == "--chdir") && idx+1 < len(fields):
				res.indeterminate = "NOT CHECKED: env chdir options are implementation-specific, so doctor cannot reproduce this invocation without executing the installed env utility"
				return res
			case strings.HasPrefix(token, "--chdir="):
				res.indeterminate = "NOT CHECKED: env chdir options are implementation-specific, so doctor cannot reproduce this invocation without executing the installed env utility"
				return res
			case doctorEnvAssignment(token):
				key, value, _ := strings.Cut(token, "=")
				doctorEnvSet(res.env, key, value)
				idx++
			case strings.HasPrefix(token, "-"):
				res.err = fmt.Sprintf("unsupported env option %q prevents faithful reachability probing", token)
				return res
			default:
				goto command
			}
		}
	}

command:
	if idx >= len(fields) {
		res.err = "invocation has no executable after env prefix"
		return res
	}
	if doctorIsEnvProgram(fields[0]) {
		if _, ok := doctorEnvLookup(res.env, "PATH"); !ok {
			if runtime.GOOS == "windows" && ignoredEnvironment {
				res.indeterminate = "NOT CHECKED: env -i on Windows delegates default executable search to the installed env runtime, which doctor cannot reproduce without executing it"
				return res
			}
			doctorEnvSet(res.env, "PATH", doctorDefaultExecPath(res.env))
		}
	}
	program := fields[idx]
	resolved, err := doctorLookPath(program, res.cwd, res.env)
	if err != nil {
		res.program = program
		if doctorHasPathSeparator(program) {
			res.missingPath = program
			if !filepath.IsAbs(res.missingPath) {
				res.missingPath = filepath.Clean(filepath.Join(res.cwd, res.missingPath))
			}
		}
		res.err = fmt.Sprintf("real executable %q is not reachable under the invocation's effective PATH/cwd", program)
		return res
	}
	if doctorIsRepoSeatAdapter(resolved, root) {
		if err := doctorRegularExecutable(resolved); err != nil {
			res.adapterErr = fmt.Sprintf("seat adapter %q is unusable: %v", resolved, err)
			return res
		}
		res.adapter = resolved
		if idx+2 >= len(fields) {
			res.program = "reviewer executable"
			res.humanInstruction = "edit the seat-adapter invocation so a real reviewer executable follows the adapter seat name"
			res.err = "seat adapter invocation has no reviewer executable after its seat name"
			return res
		}
		nested := doctorResolveCommandFields(fields[idx+2:], res.cwd, res.env)
		nested.adapter = res.adapter
		return nested
	}
	if err := doctorRegularExecutable(resolved); err != nil {
		res.err = fmt.Sprintf("resolved executable %q is unusable: %v", resolved, err)
		return res
	}
	res.program = program
	res.resolved = resolved
	res.opaqueWrapper = doctorOpaqueWrapper(filepath.Base(resolved))
	return res
}

func doctorResolveCWD(current, next string) string {
	if filepath.IsAbs(next) {
		return filepath.Clean(next)
	}
	return filepath.Join(current, next)
}

func doctorDefaultExecPath(env map[string]string) string {
	if runtime.GOOS == "windows" {
		root, _ := doctorEnvLookup(env, "SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32") + string(os.PathListSeparator) + root
	}
	return "/usr/bin:/bin"
}

func doctorEnvAssignment(token string) bool {
	key, _, ok := strings.Cut(token, "=")
	if !ok || key == "" {
		return false
	}
	for i, r := range key {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func doctorLookPath(program, cwd string, env map[string]string) (string, error) {
	programs := []string{program}
	if runtime.GOOS == "windows" && filepath.Ext(program) == "" {
		extensions, _ := doctorEnvLookup(env, "PATHEXT")
		if extensions == "" {
			extensions = ".COM;.EXE;.BAT;.CMD"
		}
		programs = programs[:0]
		for _, extension := range strings.Split(extensions, ";") {
			programs = append(programs, program+strings.ToLower(extension), program+strings.ToUpper(extension))
		}
	}
	if doctorHasPathSeparator(program) {
		for _, candidate := range programs {
			path := candidate
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			if err := doctorRegularExecutable(path); err == nil {
				return filepath.Clean(path), nil
			}
		}
		return "", exec.ErrNotFound
	}
	pathValue, ok := doctorEnvLookup(env, "PATH")
	if !ok || pathValue == "" {
		return "", exec.ErrNotFound
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = cwd
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		for _, executable := range programs {
			candidate := filepath.Join(dir, executable)
			if doctorRegularExecutable(candidate) == nil {
				return candidate, nil
			}
		}
	}
	return "", exec.ErrNotFound
}

func doctorRegularExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("not executable")
	}
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("not executable by the current user: %w", err)
	}
	return nil
}

func doctorSplitCommand(input string) ([]string, error) {
	return doctorSplitPlatformCommand(input)
}

func doctorSplitPOSIX(input string) ([]string, error) {
	var fields []string
	var field strings.Builder
	quote := rune(0)
	escaped := false
	started := false
	flush := func() {
		if started {
			fields = append(fields, field.String())
			field.Reset()
			started = false
		}
	}
	runes := []rune(input)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		if escaped {
			if char == '\n' {
				escaped = false
				continue
			}
			field.WriteRune(char)
			started = true
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			if quote == '"' && index+1 < len(runes) && !strings.ContainsRune("$`\"\\\n", runes[index+1]) {
				field.WriteRune(char)
				started = true
				continue
			}
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				field.WriteRune(char)
			}
			started = true
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
			started = true
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			field.WriteRune(char)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	flush()
	return fields, nil
}

func doctorTerminalSafe(value string) string {
	var safe strings.Builder
	for _, char := range value {
		if unicode.IsControl(char) {
			switch {
			case char <= 0xff:
				fmt.Fprintf(&safe, "\\x%02x", char)
			case char <= 0xffff:
				fmt.Fprintf(&safe, "\\u%04x", char)
			default:
				fmt.Fprintf(&safe, "\\U%08x", char)
			}
			continue
		}
		safe.WriteRune(char)
	}
	return safe.String()
}

func doctorContainsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func doctorDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	return doctorCanSearch(path)
}

func cloneDoctorEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func doctorEnvLookup(env map[string]string, wanted string) (string, bool) {
	if runtime.GOOS != "windows" {
		value, ok := env[wanted]
		return value, ok
	}
	for key, value := range env {
		if strings.EqualFold(key, wanted) {
			return value, true
		}
	}
	return "", false
}

func doctorEnvDelete(env map[string]string, wanted string) {
	for key := range env {
		if key == wanted || runtime.GOOS == "windows" && strings.EqualFold(key, wanted) {
			delete(env, key)
		}
	}
}

func doctorEnvSet(env map[string]string, key, value string) {
	doctorEnvDelete(env, key)
	env[key] = value
}

func doctorHasPathSeparator(path string) bool {
	if strings.ContainsRune(path, filepath.Separator) {
		return true
	}
	return runtime.GOOS == "windows" && strings.ContainsRune(path, '/')
}

func doctorOpaqueWrapper(base string) bool {
	base = strings.ToLower(base)
	switch base {
	case "sh", "bash", "dash", "zsh", "ksh", "mksh", "fish", "csh", "tcsh", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe", "python", "python2", "python3", "node", "node.exe", "ruby", "ruby.exe", "perl", "perl.exe":
		return true
	}
	for _, prefix := range []string{"python", "ruby", "perl", "node"} {
		if suffix, ok := strings.CutPrefix(base, prefix); ok && doctorVersionSuffix(suffix) {
			return true
		}
	}
	return false
}

func doctorVersionSuffix(suffix string) bool {
	if suffix == "" || suffix[0] == '.' || suffix[len(suffix)-1] == '.' {
		return false
	}
	previousDot := false
	for _, r := range suffix {
		if r == '.' {
			if previousDot {
				return false
			}
			previousDot = true
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
		previousDot = false
	}
	return true
}

func doctorIsEnvProgram(program string) bool {
	base := filepath.Base(program)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(base, "env") || strings.EqualFold(base, "env.exe")
	}
	return base == "env"
}

func doctorIsRepoSeatAdapter(resolved, root string) bool {
	expected := filepath.Join(root, "scripts", "seat-adapter.sh")
	resolvedReal, resolvedErr := filepath.EvalSymlinks(resolved)
	expectedReal, expectedErr := filepath.EvalSymlinks(expected)
	if resolvedErr == nil && expectedErr == nil {
		return filepath.Clean(resolvedReal) == filepath.Clean(expectedReal)
	}
	return filepath.Clean(resolved) == filepath.Clean(expected)
}

func doctorGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if doctorGitEnvironmentKey(key) && !doctorGitConfigEnvironmentKey(key) {
			continue
		}
		env = append(env, item)
	}
	return append(env,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func doctorGitConfigEnvironmentKey(key string) bool {
	if runtime.GOOS == "windows" {
		key = strings.ToUpper(key)
	}
	switch key {
	case "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_PARAMETERS":
		return true
	default:
		return strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_")
	}
}

func doctorGitEnvironmentKey(key string) bool {
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(strings.ToUpper(key), "GIT_")
	}
	return strings.HasPrefix(key, "GIT_")
}
