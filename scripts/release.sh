#!/usr/bin/env bash
# release.sh — build a production binary embedding version, git hash, and build time
#
# Usage:
#   scripts/release.sh [output]
#
#   output  Optional path for the compiled binary. Defaults to ./koios
#
# The script must be run from the repository root (where VERSION lives).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION_FILE="$REPO_ROOT/VERSION"
CHANGELOG_FILE="$REPO_ROOT/CHANGELOG.md"
OUTPUT="${1:-$REPO_ROOT/koios}"

if [[ ! -f "$VERSION_FILE" ]]; then
  echo "error: VERSION file not found at $VERSION_FILE" >&2
  exit 1
fi

VERSION="$(tr -d '[:space:]' < "$VERSION_FILE")"

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: VERSION contains '$VERSION' which is not a valid SemVer (X.Y.Z)" >&2
  exit 1
fi

if [[ ! -f "$CHANGELOG_FILE" ]]; then
  echo "error: CHANGELOG file not found at $CHANGELOG_FILE" >&2
  exit 1
fi

if ! grep -Eq "^## \[$VERSION\]( - .+)?$" "$CHANGELOG_FILE"; then
  echo "error: CHANGELOG.md does not contain a release entry for version $VERSION" >&2
  exit 1
fi

GIT_HASH="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")"
BUILD_TIME="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

LDFLAGS=(
  "-X main.version=${VERSION}"
  "-X main.gitHash=${GIT_HASH}"
  "-X main.buildTime=${BUILD_TIME}"
  "-s -w"
)

echo "building koios ${VERSION} (${GIT_HASH}) at ${BUILD_TIME}..."
go build -ldflags "${LDFLAGS[*]}" -o "$OUTPUT" "$REPO_ROOT"
echo "output: $OUTPUT"
