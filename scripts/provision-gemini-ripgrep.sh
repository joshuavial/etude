#!/usr/bin/env bash
#
# provision-gemini-ripgrep.sh — provision a ripgrep symlink for the gemini-cli
# bundle so the gemini reviewer seat uses RipGrepTool instead of the bleeding
# GrepTool fallback.
#
# Background:
#   gemini-cli's getRipgrepPath() looks ONLY for a bundled binary at
#   <bundle>/vendor/ripgrep/rg-<platform>-<arch>[.exe] — it never consults
#   system rg on PATH.  When the vendor path is absent, ensureRgPath() throws
#   and gemini falls back to GrepTool, which bleeds matches across files and
#   has reproducibly misattributed string literals from planning docs to test
#   files, producing confident BLOCKs on phantom assertions.
#
#   This script creates that bundled path as a symlink to the system rg so
#   gemini finds and registers RipGrepTool on startup.
#
# Usage:
#   scripts/provision-gemini-ripgrep.sh
#
# Run once per machine.  Re-run after any gemini-cli reinstall/upgrade (which
# wipes the symlink).  Re-running is always safe — the script is idempotent.
#
# The script NEVER overwrites a regular file at the target path.  If a future
# gemini-cli ships its own bundled binary there, this script detects that and
# exits successfully without touching it.
#
# Requires: gemini (gemini-cli), node, rg (ripgrep).
set -euo pipefail

# ---------------------------------------------------------------------------
# 1. Resolve gemini-cli bundle dir
# ---------------------------------------------------------------------------
if ! command -v gemini >/dev/null 2>&1; then
  echo "error: gemini not found on PATH; install gemini-cli first" >&2
  exit 1
fi

gemini_bin="$(command -v gemini)"
# Follow the symlink to the real file (the gemini bin is usually a symlink into
# the npm .bin wrapper, which in turn points at the real bundle entry point).
gemini_real="$(realpath "$gemini_bin")"
# The real gemini binary is <bundle>/gemini.js (or similar); the bundle dir is
# its containing directory.
bundle_dir="$(dirname "$gemini_real")"

echo "gemini binary : $gemini_bin"
echo "resolved to   : $gemini_real"
echo "bundle dir    : $bundle_dir"

# ---------------------------------------------------------------------------
# 2. Derive binName the same way gemini-cli does (via node)
# ---------------------------------------------------------------------------
if ! command -v node >/dev/null 2>&1; then
  echo "error: node not found on PATH; node is required to derive the ripgrep binary name" >&2
  exit 1
fi

platform="$(node -e 'process.stdout.write(require("os").platform())')"
arch="$(node -e 'process.stdout.write(require("os").arch())')"
binName="rg-${platform}-${arch}"
if [ "$platform" = "win32" ]; then
  binName="${binName}.exe"
fi

echo "platform      : $platform"
echo "arch          : $arch"
echo "binName       : $binName"

# ---------------------------------------------------------------------------
# 3. Find and verify system rg
# ---------------------------------------------------------------------------
rg_path="$(command -v rg || true)"
if [ -z "$rg_path" ]; then
  echo "error: rg (ripgrep) not found on PATH" >&2
  echo "       install with: brew install ripgrep" >&2
  exit 1
fi

echo "system rg     : $rg_path"

if ! "$rg_path" --version >/dev/null 2>&1; then
  echo "error: system rg found at $rg_path but '$rg_path --version' failed" >&2
  echo "       the binary is not runnable; reinstall ripgrep" >&2
  exit 1
fi

rg_version="$("$rg_path" --version | head -1)"
echo "rg version    : $rg_version"

# ---------------------------------------------------------------------------
# 4. Decide what to do at the target path
# ---------------------------------------------------------------------------
target="${bundle_dir}/vendor/ripgrep/${binName}"
target_dir="$(dirname "$target")"

echo "target        : $target"

if [ -L "$target" ]; then
  # Target is a symlink (managed by this script or a prior run).
  if "$target" --version >/dev/null 2>&1; then
    echo "status        : already provisioned (symlink runs ok)"
    # Fall through to the final VERIFY step.
  else
    echo "status        : stale/broken symlink — removing and recreating"
    rm "$target"
    ln -s "$rg_path" "$target"
    echo "status        : symlink recreated"
  fi
elif [ -e "$target" ]; then
  # Target exists and is NOT a symlink — gemini ships its own bundled binary.
  echo "status        : regular file at target (gemini ships its own bundled ripgrep)"
  if ! "$target" --version >/dev/null 2>&1; then
    echo "error: gemini's bundled rg at $target is not runnable" >&2
    exit 1
  fi
  echo "status        : gemini already ships a bundled ripgrep; nothing to do"
  exit 0
else
  # Target does not exist — create it.
  echo "status        : target absent — provisioning symlink"
  mkdir -p "$target_dir"
  ln -s "$rg_path" "$target"
  echo "status        : symlink created"
fi

# ---------------------------------------------------------------------------
# 5. Verify: the target now exists and runs
# ---------------------------------------------------------------------------
if ! [ -e "$target" ]; then
  echo "error: verification failed — target does not exist after provisioning: $target" >&2
  exit 1
fi

if ! "$target" --version >/dev/null 2>&1; then
  echo "error: verification failed — target exists but '$target --version' failed" >&2
  exit 1
fi

echo ""
echo "ok: ripgrep provisioned for gemini-cli"
echo "    $target -> $rg_path"
echo "    gemini will now register RipGrepTool instead of the bleeding GrepTool"
echo ""
echo "Note: re-run this script after any gemini-cli reinstall/upgrade"
echo "      (upgrading gemini-cli wipes the vendor/ dir and its symlinks)."
