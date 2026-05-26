//go:build !unix

package cli

// nofollowFlag is 0 on platforms without O_NOFOLLOW (e.g. Windows): the atomic
// no-follow-open guard is unavailable there, so symlink rejection falls back to
// the caller's Lstat / f.Stat regular-file check (non-atomic). etude's threat
// model targets unix; this keeps the package compiling on all platforms.
const nofollowFlag = 0
