package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// JudgeIdentity returns a stable fingerprint string for the given Judge.
//
// For *ExecJudge the fingerprint is the SHA-256 hex digest of a canonical
// representation of the command and model:
//
//	sha256("execjudge\x00" + strings.Join(Command, "\x00") + "\x00model=" + Model)
//
// This ensures that any change to the command words or model string produces a
// different fingerprint, making cache keys judge-specific.
//
// For any other Judge type (including *StubJudge and future implementations
// without a derivable stable identity) JudgeIdentity returns "" (unidentified).
// An unidentified judge is never used as a cache key; callers must check for
// the empty string and skip caching.
func JudgeIdentity(j Judge) string {
	ej, ok := j.(*ExecJudge)
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("execjudge\x00")
	b.WriteString(strings.Join(ej.Command, "\x00"))
	b.WriteString("\x00model=")
	b.WriteString(ej.Model)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
