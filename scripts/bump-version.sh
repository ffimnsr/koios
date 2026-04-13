#!/usr/bin/env bash
# bump-version.sh — increment the SemVer stored in ./VERSION
#
# Usage:
#   scripts/bump-version.sh patch   # 0.1.0 → 0.1.1
#   scripts/bump-version.sh minor   # 0.1.0 → 0.2.0
#   scripts/bump-version.sh major   # 0.1.0 → 1.0.0
#
# The script must be run from the repository root (where VERSION lives).
set -euo pipefail

PART="${1:-patch}"
VERSION_FILE="$(dirname "$0")/../VERSION"

if [[ ! -f "$VERSION_FILE" ]]; then
  echo "error: VERSION file not found at $VERSION_FILE" >&2
  exit 1
fi

CURRENT="$(tr -d '[:space:]' < "$VERSION_FILE")"

if [[ ! "$CURRENT" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "error: VERSION contains '$CURRENT' which is not a valid SemVer (X.Y.Z)" >&2
  exit 1
fi

MAJOR="${BASH_REMATCH[1]}"
MINOR="${BASH_REMATCH[2]}"
PATCH="${BASH_REMATCH[3]}"

case "$PART" in
  major)
    MAJOR=$((MAJOR + 1))
    MINOR=0
    PATCH=0
    ;;
  minor)
    MINOR=$((MINOR + 1))
    PATCH=0
    ;;
  patch)
    PATCH=$((PATCH + 1))
    ;;
  *)
    echo "error: unknown bump type '$PART'. Use major, minor, or patch." >&2
    exit 1
    ;;
esac

NEW="${MAJOR}.${MINOR}.${PATCH}"
printf '%s\n' "$NEW" > "$VERSION_FILE"
echo "bumped: $CURRENT → $NEW"
