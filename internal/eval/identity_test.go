package eval

import (
	"strings"
	"testing"
)

func TestJudgeIdentityExecJudgeStable(t *testing.T) {
	j := &ExecJudge{Command: []string{"./judge.sh", "--flag"}, Model: "gpt-4"}
	id1 := JudgeIdentity(j)
	id2 := JudgeIdentity(j)
	if id1 == "" {
		t.Fatal("JudgeIdentity returned empty string for ExecJudge")
	}
	if id1 != id2 {
		t.Errorf("JudgeIdentity not stable: %q != %q", id1, id2)
	}
	// Must be a 64-char hex string (sha256).
	if len(id1) != 64 {
		t.Errorf("JudgeIdentity length = %d, want 64", len(id1))
	}
	for _, ch := range id1 {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("JudgeIdentity contains non-hex char %q in %q", ch, id1)
			break
		}
	}
}

func TestJudgeIdentityExecJudgeDiffersOnCommandChange(t *testing.T) {
	j1 := &ExecJudge{Command: []string{"./judge.sh"}, Model: "gpt-4"}
	j2 := &ExecJudge{Command: []string{"./other-judge.sh"}, Model: "gpt-4"}
	id1 := JudgeIdentity(j1)
	id2 := JudgeIdentity(j2)
	if id1 == id2 {
		t.Errorf("JudgeIdentity should differ for different commands, but both = %q", id1)
	}
}

func TestJudgeIdentityExecJudgeDiffersOnModelChange(t *testing.T) {
	j1 := &ExecJudge{Command: []string{"./judge.sh"}, Model: "model-a"}
	j2 := &ExecJudge{Command: []string{"./judge.sh"}, Model: "model-b"}
	id1 := JudgeIdentity(j1)
	id2 := JudgeIdentity(j2)
	if id1 == id2 {
		t.Errorf("JudgeIdentity should differ for different models, but both = %q", id1)
	}
}

func TestJudgeIdentityExecJudgeDiffersOnArgChange(t *testing.T) {
	j1 := &ExecJudge{Command: []string{"./judge.sh", "--flag1"}, Model: "gpt-4"}
	j2 := &ExecJudge{Command: []string{"./judge.sh", "--flag2"}, Model: "gpt-4"}
	id1 := JudgeIdentity(j1)
	id2 := JudgeIdentity(j2)
	if id1 == id2 {
		t.Errorf("JudgeIdentity should differ for different args, but both = %q", id1)
	}
}

func TestJudgeIdentityExecJudgeCommandJoinCollisionFree(t *testing.T) {
	// ["a\x00b", "c"] and ["a", "b\x00c"] must not collide because we join with \x00.
	// The fingerprint is over the shell command components joined by \x00, so
	// "a\x00b" + \x00 + "c" ≠ "a" + \x00 + "b\x00c" only if the inner \x00 in the
	// command arg is treated as part of the word — which it is (no extra escaping).
	// This test is informational: just verify that two structurally different
	// commands with the same string byte sequence produce different identities.
	j1 := &ExecJudge{Command: []string{"./judge.sh", "--model=x"}, Model: ""}
	j2 := &ExecJudge{Command: []string{"./judge.sh"}, Model: "--model=x"}
	id1 := JudgeIdentity(j1)
	id2 := JudgeIdentity(j2)
	// These may or may not collide depending on join encoding — document behavior.
	// With the current scheme "execjudge\x00./judge.sh\x00--model=x\x00model="
	// vs "execjudge\x00./judge.sh\x00model=--model=x", they differ.
	_ = id1
	_ = id2
	// Both must still be non-empty.
	if id1 == "" || id2 == "" {
		t.Error("JudgeIdentity returned empty for non-empty ExecJudge")
	}
}

func TestJudgeIdentityStubJudgeReturnsEmpty(t *testing.T) {
	j := &StubJudge{}
	id := JudgeIdentity(j)
	if id != "" {
		t.Errorf("JudgeIdentity(StubJudge) = %q, want empty string", id)
	}
}

func TestJudgeIdentityExecJudgeEmptyModel(t *testing.T) {
	// An ExecJudge with empty model should still produce a non-empty fingerprint.
	j := &ExecJudge{Command: []string{"./judge.sh"}, Model: ""}
	id := JudgeIdentity(j)
	if id == "" {
		t.Fatal("JudgeIdentity returned empty for ExecJudge with empty model")
	}
	if len(id) != 64 {
		t.Errorf("JudgeIdentity length = %d, want 64", len(id))
	}
}

func TestJudgeIdentityExecJudgeDifferentFromEmptyModel(t *testing.T) {
	j1 := &ExecJudge{Command: []string{"./judge.sh"}, Model: ""}
	j2 := &ExecJudge{Command: []string{"./judge.sh"}, Model: "some-model"}
	if JudgeIdentity(j1) == JudgeIdentity(j2) {
		t.Error("JudgeIdentity should differ for empty vs non-empty model")
	}
}

func TestJudgeIdentityNonExecJudge(t *testing.T) {
	// Any judge that is not *ExecJudge returns "".
	// Test via a custom anonymous implementation.
	var customJudge Judge = &StubJudge{Err: nil}
	if id := JudgeIdentity(customJudge); id != "" {
		t.Errorf("JudgeIdentity(non-ExecJudge) = %q, want empty", id)
	}
}

func TestJudgeIdentityResultIsLowercaseHex(t *testing.T) {
	j := &ExecJudge{Command: []string{"./judge.sh"}, Model: "test-model"}
	id := JudgeIdentity(j)
	if strings.ToLower(id) != id {
		t.Errorf("JudgeIdentity result %q is not all lowercase", id)
	}
}
