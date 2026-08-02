#!/usr/bin/env bash
# Release helper — validates and bumps local version files.
# Does NOT commit, push, tag, or publish. After running, inspect the diff,
# commit, tag vX.Y.Z, and push the tag to trigger the GoReleaser workflow.
# Usage: ./scripts/release.sh [patch|minor|major]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

BUMP="${1:-validate}"
if [[ ! "$BUMP" =~ ^(validate|major|minor|patch)$ ]]; then
  echo "ERROR: argument must be validate, patch, minor, or major (got: $BUMP)" >&2
  exit 1
fi

# Read current version from version.go (single source of truth)
CURRENT=$(grep -oP 'var VERSION = "\K[^"]+' pkg/common/version.go)
if [ -z "$CURRENT" ]; then
  echo "ERROR: could not read VERSION from pkg/common/version.go" >&2
  exit 1
fi

if [ "$BUMP" = "validate" ]; then
  NEW_VERSION="$CURRENT"
else
  IFS='.' read -r MAJOR MINOR PATCH_NUM <<< "$CURRENT"

  case "$BUMP" in
    major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH_NUM=0 ;;
    minor) MINOR=$((MINOR + 1)); PATCH_NUM=0 ;;
    patch) PATCH_NUM=$((PATCH_NUM + 1)) ;;
  esac

  NEW_VERSION="${MAJOR}.${MINOR}.${PATCH_NUM}"
  echo "Bumping: $CURRENT -> $NEW_VERSION ($BUMP)"

  # Update version files
  sed -i -E "s/(var SUMMARY = \"v)[^\"]+/\\1${NEW_VERSION}/" pkg/common/version.go
  sed -i -E "s/(var VERSION = \"?)[^\"]+/\\1${NEW_VERSION}/" pkg/common/version.go

  if [ -f web/package.json ]; then
    sed -i -E "s/(\"version\": \"?)[^\"]+/\\1${NEW_VERSION}/" web/package.json
  fi

  if [ -f web/package-lock.json ] && command -v node >/dev/null 2>&1; then
    node -e '
      const fs = require("fs");
      const p = "web/package-lock.json";
      const lock = JSON.parse(fs.readFileSync(p, "utf8"));
      lock.version = process.argv[1];
      if (lock.packages && lock.packages[""]) lock.packages[""].version = process.argv[1];
      fs.writeFileSync(p, JSON.stringify(lock, null, 2) + "\n");
    ' "$NEW_VERSION"
  elif [ -f web/package-lock.json ]; then
    echo "WARN: node not found; skipping web/package-lock.json sync (reconcile manually)" >&2
  fi

  echo "Updated local version files to $NEW_VERSION."
fi

# Verification
ERRORS=0

GO_VER=$(grep -oP 'var VERSION = "\K[^"]+' pkg/common/version.go)
if [ "$GO_VER" != "$NEW_VERSION" ]; then
  echo "  FAIL: pkg/common/version.go has $GO_VER (expected $NEW_VERSION)" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  OK: pkg/common/version.go = $NEW_VERSION"
fi

if [ -f web/package.json ]; then
  PKG_VER=$(grep -oP '"version": "\K[^"]+' web/package.json)
  if [ "$PKG_VER" != "$NEW_VERSION" ]; then
    echo "  FAIL: web/package.json has $PKG_VER (expected $NEW_VERSION)" >&2
    ERRORS=$((ERRORS + 1))
  else
    echo "  OK: web/package.json = $NEW_VERSION"
  fi
fi

if [ -f web/package-lock.json ]; then
  LOCK_VER=$(node -e 'console.log(require("./web/package-lock.json").version)' 2>/dev/null || true)
  if [ "$LOCK_VER" != "$NEW_VERSION" ]; then
    echo "  FAIL: web/package-lock.json root version still $LOCK_VER (expected $NEW_VERSION)" >&2
    ERRORS=$((ERRORS + 1))
  else
    echo "  OK: web/package-lock.json root version = $NEW_VERSION"
  fi
fi

if [ "$ERRORS" -gt 0 ]; then
  echo "ABORT: $ERRORS file(s) failed verification." >&2
  exit 1
fi

echo "Version files are consistent at $NEW_VERSION."
echo "Next: review the diff, commit, run 'git tag -a v$NEW_VERSION -m \"Release v$NEW_VERSION\"', and push the tag."
