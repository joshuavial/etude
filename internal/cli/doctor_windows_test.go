//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorWindowsCommandSplitUsesNativeRules(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{`"C:\tools\reviewer.exe" --prompt "say \"hello world\""`, []string{`C:\tools\reviewer.exe`, "--prompt", `say "hello world"`}},
		{`reviewer.exe --dir "C:\work\\"`, []string{"reviewer.exe", "--dir", `C:\work\`}},
	}
	for _, test := range tests {
		got, err := doctorSplitCommand(test.input)
		if err != nil || len(got) != len(test.want) {
			t.Fatalf("split %q = %q, %v", test.input, got, err)
		}
		for i := range got {
			if got[i] != test.want[i] {
				t.Fatalf("split %q field %d = %q, want %q", test.input, i, got[i], test.want[i])
			}
		}
	}
}

func TestDoctorWindowsRemediationUsesNativeArgumentQuoting(t *testing.T) {
	if got := doctorShellQuote("my remote"); got != `"my remote"` {
		t.Fatalf("Windows argument quote = %q", got)
	}
	if got := doctorShellQuote("origin"); got != "origin" {
		t.Fatalf("unnecessary Windows argument quote = %q", got)
	}
}

func TestDoctorWindowsConfigRemovalRequiresKnownShell(t *testing.T) {
	got := doctorConfigUnsetRemediation(`C:\repo`, "remote.origin.fetch", doctorConfigEntry{origin: `file:C:\repo\.git\config`, value: "+refs/x/*:refs/y/*"})
	if !strings.HasPrefix(got, "HUMAN AUTHORSHIP REQUIRED:") || strings.Contains(got, "git config") || !strings.Contains(got, "remote.origin.fetch") {
		t.Fatalf("Windows config remediation = %q", got)
	}
}

func TestDoctorWindowsEnvironmentLookupIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.exe")
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := doctorResolveCommand("reviewer.exe", dir, map[string]string{"Path": dir, "PathExt": ".EXE"})
	if res.err != "" || res.resolved != path {
		t.Fatalf("case-insensitive environment resolution = %+v", res)
	}
}

func TestDoctorWindowsGitEnvironmentFilteringIsCaseInsensitive(t *testing.T) {
	t.Setenv("Git_Dir", `C:\wrong-repository`)
	for _, item := range doctorGitEnv() {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "GIT_DIR") {
			t.Fatalf("mixed-case Git repository redirection survived filtering: %q", item)
		}
	}
}

func TestDoctorWindowsRecognizesForwardSlashExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.exe")
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	forward := strings.ReplaceAll(path, `\`, "/")
	res := doctorResolveCommand(forward, dir, map[string]string{})
	if res.err != "" || res.resolved == "" {
		t.Fatalf("forward-slash explicit path resolution = %+v", res)
	}
}

func TestDoctorWindowsExplicitPathUsesExecutableExtensions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.exe")
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := doctorResolveCommand(filepath.Join(dir, "reviewer"), dir, map[string]string{"PathExt": ".EXE"})
	if res.err != "" || !strings.EqualFold(res.resolved, path) {
		t.Fatalf("extensionless explicit path resolution = %+v", res)
	}
}

func TestDoctorWindowsEnvIgnoreEnvironmentIsNotFabricated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "env.exe"), []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := doctorResolveCommand("env -i true", dir, map[string]string{"Path": dir, "PathExt": ".EXE"})
	if res.err != "" || !strings.Contains(res.indeterminate, "NOT CHECKED") {
		t.Fatalf("Windows env -i resolution = %+v", res)
	}
}

func TestDoctorWindowsRecognizesEnvExePrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "env.exe"), []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := doctorResolveCommand("env.exe -i missing-reviewer", dir, map[string]string{"Path": dir, "PathExt": ".EXE"})
	if res.err != "" || !strings.Contains(res.indeterminate, "NOT CHECKED") {
		t.Fatalf("env.exe prefix resolution = %+v", res)
	}
}
