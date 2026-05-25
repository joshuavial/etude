#!/usr/bin/env bash
#
# docs-reality-check.sh — mechanical guard against doc/CLI drift.
#
# Builds etude fresh from source (never an ambient binary, no network) and checks
# that user-facing docs match the real shipped command surface:
#   1. every shipped top-level command is advertised in README.md (as an
#      `etude <cmd>` USAGE mention) AND named in the docs/README.md link INDEX,
#      and has a docs/cli/etude_<cmd>.md page;
#   2. no docs/cli page (or `etude <cmd>` doc mention) names a command that does
#      not exist;
#   3. no docs/plans/** line claims a SHIPPED command is future/unimplemented.
#
# Usage:  scripts/docs-reality-check.sh
# Exits 0 when docs match reality, non-zero (listing every finding) otherwise.
#
# This is a SEPARATE target from `make docs-check` (which only diffs generated
# docs/cli): it is allowed to report hand-written-doc drift without breaking the
# generated-docs check. Run it via `make docs-reality`.
#
# Matching is PER-DOC and prefix-safe (so a prefix command like `capture` is never
# satisfied by a longer one like `capture-gate`): README.md (command usage) is
# matched with an `etude <cmd>` context; docs/README.md (a link index, not usage
# prose) is matched on the command NAME case-insensitively; both require a
# non-[-A-Za-z0-9_] boundary after the command.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
allow="scripts/docs-reality-allow.txt"

bindir="$(mktemp -d)"
trap 'rm -rf "$bindir"' EXIT
bin="$bindir/etude"
go build -o "$bin" ./cmd/etude

# Shipped command inventory: the cobra "Available Commands:".."Flags:" block,
# first token per line, minus cobra built-ins. (while-read, not mapfile, for
# bash 3.2 portability.)
commands=()
while IFS= read -r line; do
  commands+=("$line")
done < <("$bin" --help 2>&1 \
  | awk '/^Available Commands:/{f=1;next} /^Flags:/{f=0} f && NF {print $1}' \
  | grep -vxE 'completion|help' | sort -u)

if [ "${#commands[@]}" -eq 0 ]; then
  echo "docs-reality: could not derive command inventory from 'etude --help'" >&2
  exit 2
fi

findings=()

# README.md carries command USAGE, so match an `etude <cmd>` context (prefix-safe:
# cmd followed by a non-[-A-Za-z0-9_] char or EOL, so `capture` is not satisfied
# by `capture-gate`).
advertises_usage() { # <cmd>
  grep -nE "etude[[:space:]]+$1([^-A-Za-z0-9_]|\$)" README.md >/dev/null 2>&1
}
# docs/README.md is a link INDEX ("[Bench](bench.md)"), not command-usage prose,
# so match the command NAME case-insensitively with non-[-A-Za-z0-9_] boundaries
# on both sides (still prefix-safe: `capture` won't match `capture-gate`).
indexes() { # <cmd>
  grep -niE "(^|[^-A-Za-z0-9_])$1([^-A-Za-z0-9_]|\$)" docs/README.md >/dev/null 2>&1
}

# 1. Coverage: each shipped command advertised in README (usage) + docs/README
#    (index) + a generated cli page.
for cmd in "${commands[@]}"; do
  advertises_usage "$cmd"         || findings+=("README.md does not advertise 'etude $cmd' (usage)")
  indexes "$cmd"                  || findings+=("docs/README.md index does not mention '$cmd'")
  [ -f "docs/cli/etude_$cmd.md" ] || findings+=("missing generated page docs/cli/etude_$cmd.md for shipped command '$cmd'")
done

# 2. Reverse: every top-level docs/cli/etude_<cmd>.md (ignoring _-nested subpages
#    and the etude.md root index) must map to a shipped command.
for page in docs/cli/etude_*.md; do
  [ -e "$page" ] || continue
  base="$(basename "$page" .md)"          # etude_<cmd>
  cmd="${base#etude_}"
  case "$cmd" in *_*) continue;; esac      # skip nested subpages (etude_run_show, ...)
  printf '%s\n' "${commands[@]}" | grep -qxF "$cmd" \
    || findings+=("orphan cli page $page names command '$cmd' that does not exist")
done

# 3. Stale-claim scan: docs/plans/** lines asserting a SHIPPED command is future/
#    unimplemented, only when an `etude <cmd>` mention co-occurs on the line.
stale_re='not yet|not implemented|does not exist yet|are future|is future|future work|unbuilt|not built'
while IFS= read -r hit; do
  [ -n "$hit" ] || continue
  # hit is "path:lineno:content"; suppress allowlisted lines (substring match).
  if [ -f "$allow" ] && grep -Fqf <(grep -vE '^\s*#|^\s*$' "$allow") <<<"$hit"; then
    continue
  fi
  for cmd in "${commands[@]}"; do
    if grep -qE "etude[[:space:]]+$cmd([^-A-Za-z0-9_]|\$)" <<<"$hit"; then
      findings+=("stale planning claim about shipped command '$cmd': $hit")
      break
    fi
  done
done < <(grep -rniE "$stale_re" docs/plans --include='*.md' 2>/dev/null || true)

if [ "${#findings[@]}" -gt 0 ]; then
  echo "docs-reality: ${#findings[@]} drift finding(s):" >&2
  printf '  - %s\n' "${findings[@]}" >&2
  echo "(fix the docs, or for legitimate planning prose add a suppression to $allow)" >&2
  exit 1
fi
echo "docs-reality: OK — ${#commands[@]} commands; docs match the shipped CLI."
