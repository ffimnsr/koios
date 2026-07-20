#!/usr/bin/env bash
# release.sh — build a production binary or prepare a tagged release.
#
# Build mode (default):
#   scripts/release.sh [output]
#
# Release mode:
#   scripts/release.sh --patch [--skip-push]
#   scripts/release.sh --minor [--skip-push]
#   scripts/release.sh --major [--skip-push]
#   scripts/release.sh [--skip-push] <version>
#
# In build mode, the optional output path defaults to ./koios.
# In release mode, the script bumps VERSION, refreshes CHANGELOG.md,
# runs release quality gates, creates a release commit, creates a v-prefixed
# git tag, and optionally pushes the commit and tag.
set -euo pipefail

readonly REMOTE_NAME="origin"
readonly REPO_URL="https://github.com/ffimnsr/koios"
readonly SIGN_OFF="Edward Fitz Abucay <29743013+ffimnsr@users.noreply.github.com>"

REPO_ROOT=""
VERSION_FILE=""
CHANGELOG_FILE=""

usage() {
  cat <<'EOF'
Usage:
  scripts/release.sh [output]
  scripts/release.sh [options] [<version>]

Build mode:
  With no release flags, build a production binary embedding version, git hash,
  and build time. The optional output path defaults to ./koios.

Release mode:
  Bump VERSION, refresh CHANGELOG.md, run release quality gates, create a
  release commit, create a v-prefixed tag, and optionally push commit/tag.

Options:
  --major      Increment major version and reset minor/patch to zero.
  --minor      Increment minor version and reset patch to zero.
  --patch      Increment patch version.
  --skip-push  Skip pushing the release commit and tag to origin.
  -h, --help   Show this help message.

Examples:
  scripts/release.sh
  scripts/release.sh dist/koios
  scripts/release.sh --patch
  scripts/release.sh --minor --skip-push
  scripts/release.sh 0.3.0
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

setup_repo_paths() {
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || die "must be run inside a git repository"
  cd "$REPO_ROOT"
  VERSION_FILE="$REPO_ROOT/VERSION"
  CHANGELOG_FILE="$REPO_ROOT/CHANGELOG.md"
}

ensure_version_file() {
  [[ -f "$VERSION_FILE" ]] || die "VERSION file not found at $VERSION_FILE"
}

ensure_changelog_file() {
  [[ -f "$CHANGELOG_FILE" ]] || die "CHANGELOG file not found at $CHANGELOG_FILE"
}

current_version() {
  ensure_version_file
  tr -d '[:space:]' < "$VERSION_FILE"
}

require_semver() {
  local version="$1"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must match x.y.z"
}

ensure_clean_worktree() {
  git diff --quiet --exit-code || die "working tree has unstaged changes"
  git diff --cached --quiet --exit-code || die "index has staged but uncommitted changes"
}

ensure_remote_exists() {
  git remote get-url "$REMOTE_NAME" >/dev/null 2>&1 || die "git remote '$REMOTE_NAME' is not configured"
}

ensure_tag_absent() {
  local tag_name="$1"

  git rev-parse --verify "refs/tags/$tag_name" >/dev/null 2>&1 &&
    die "tag '$tag_name' already exists locally"

  if git remote get-url "$REMOTE_NAME" >/dev/null 2>&1; then
    if git ls-remote --exit-code --tags "$REMOTE_NAME" "refs/tags/$tag_name" >/dev/null 2>&1; then
      die "tag '$tag_name' already exists on '$REMOTE_NAME'"
    fi
  fi
}

increment_version() {
  local current="$1"
  local bump_kind="$2"
  local major minor patch

  IFS='.' read -r major minor patch <<<"$current"

  case "$bump_kind" in
    major)
      ((major += 1))
      minor=0
      patch=0
      ;;
    minor)
      ((minor += 1))
      patch=0
      ;;
    patch)
      ((patch += 1))
      ;;
    *)
      die "unsupported bump kind: $bump_kind"
      ;;
  esac

  printf '%s.%s.%s\n' "$major" "$minor" "$patch"
}

previous_release_tag() {
  git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true
}

trim_blank_lines() {
  awk '
    NF { start = 1 }
    start { lines[++count] = $0 }
    END {
      while (count > 0 && lines[count] == "") {
        count--
      }
      for (i = 1; i <= count; i++) {
        print lines[i]
      }
    }
  '
}

extract_unreleased_body() {
  ensure_changelog_file

  awk '
    /^## \[Unreleased\]$/ { capture = 1; next }
    capture && /^## \[/ { exit }
    capture { print }
  ' "$CHANGELOG_FILE" | trim_blank_lines
}

append_section_entry() {
  local section_name="$1"
  local entry="$2"

  printf -v "$section_name" '%s- %s\n' "${!section_name}" "$entry"
}

render_changelog_group() {
  local title="$1"
  local content="$2"

  [[ -n "$content" ]] || return 0

  printf '### %s\n\n' "$title"
  printf '%s\n' "$content"
  printf '\n'
}

generate_release_body_from_commits() {
  local previous_tag="$1"
  local log_range
  local added=""
  local fixed=""
  local docs=""
  local tests=""
  local maintenance=""
  local other=""
  local entry_count=0
  local conventional_commit_regex='^([[:alnum:]_-]+)(\([^)]+\))?(!)?:[[:space:]]*(.+)$'

  if [[ -n "$previous_tag" ]]; then
    log_range="${previous_tag}..HEAD"
  else
    log_range="HEAD"
  fi

  while IFS=$'\t' read -r commit_sha subject; do
    local short_sha category message commit_type
    [[ -n "$commit_sha" ]] || continue

    short_sha="$(git rev-parse --short "$commit_sha")"
    category="other"
    message="$subject"

    if [[ "$subject" =~ $conventional_commit_regex ]]; then
      commit_type="${BASH_REMATCH[1]}"
      message="${BASH_REMATCH[4]}"

      case "$commit_type" in
        feat)
          category="added"
          ;;
        fix)
          category="fixed"
          ;;
        docs)
          category="docs"
          ;;
        test)
          category="tests"
          ;;
        build|chore|ci|perf|refactor|style)
          category="maintenance"
          ;;
      esac
    fi

    case "$category" in
      added)
        append_section_entry added "${message} (\`${short_sha}\`)"
        ;;
      fixed)
        append_section_entry fixed "${message} (\`${short_sha}\`)"
        ;;
      docs)
        append_section_entry docs "${message} (\`${short_sha}\`)"
        ;;
      tests)
        append_section_entry tests "${message} (\`${short_sha}\`)"
        ;;
      maintenance)
        append_section_entry maintenance "${message} (\`${short_sha}\`)"
        ;;
      *)
        append_section_entry other "${message} (\`${short_sha}\`)"
        ;;
    esac

    ((entry_count += 1))
  done < <(git log --reverse --format='%H%x09%s' "$log_range")

  ((entry_count > 0)) || die "no commits found for changelog range: ${log_range}"

  {
    render_changelog_group "Added" "$added"
    render_changelog_group "Fixed" "$fixed"
    render_changelog_group "Documentation" "$docs"
    render_changelog_group "Tests" "$tests"
    render_changelog_group "Maintenance" "$maintenance"
    render_changelog_group "Other Changes" "$other"
  } | trim_blank_lines
}

release_body() {
  local previous_tag="$1"
  local unreleased

  unreleased="$(extract_unreleased_body)"
  if [[ -n "${unreleased//[[:space:]]/}" ]]; then
    printf '%s\n' "$unreleased"
    return 0
  fi

  generate_release_body_from_commits "$previous_tag"
}

extract_existing_release_sections() {
  ensure_changelog_file

  awk '
    /^## \[[0-9]+\.[0-9]+\.[0-9]+\] - / { capture = 1 }
    /^\[[^]]+\]: / { capture = 0 }
    capture { print }
  ' "$CHANGELOG_FILE" | trim_blank_lines
}

extract_release_links() {
  ensure_changelog_file

  awk '/^\[[^]]+\]: / { print }' "$CHANGELOG_FILE"
}

upsert_release_links() {
  local version="$1"
  local previous_tag="$2"
  local existing_links

  existing_links="$(extract_release_links || true)"

  {
    if [[ -n "$previous_tag" ]]; then
      printf '[Unreleased]: %s/compare/%s...HEAD\n' "$REPO_URL" "v$version"
    else
      printf '[Unreleased]: %s/releases/tag/v%s\n' "$REPO_URL" "$version"
    fi
    printf '[%s]: %s/releases/tag/v%s\n' "$version" "$REPO_URL" "$version"
    if [[ -n "$existing_links" ]]; then
      printf '%s\n' "$existing_links" | awk -v version="$version" '
        $0 !~ "^\\[Unreleased\\]: " && $0 !~ "^\\[" version "\\]: "
      '
    fi
  } | trim_blank_lines
}

update_changelog() {
  local version="$1"
  local release_date="$2"
  local previous_tag="$3"
  local body existing_sections link_block tmp escaped_version

  ensure_changelog_file
  escaped_version="${version//./\\.}"

  if grep -Eq "^## \[$escaped_version\] - " "$CHANGELOG_FILE"; then
    die "CHANGELOG.md already contains an entry for version $version"
  fi

  body="$(release_body "$previous_tag")"
  existing_sections="$(extract_existing_release_sections || true)"
  link_block="$(upsert_release_links "$version" "$previous_tag")"
  tmp="$(mktemp)"

  {
    printf '# Changelog\n\n'
    printf 'All notable changes to this project will be documented in this file.\n\n'
    printf 'The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),\n'
    printf 'and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).\n\n'
    printf 'The canonical release version lives in [`VERSION`](VERSION). Build mode in\n'
    printf '[`scripts/release.sh`](scripts/release.sh) validates a matching changelog\n'
    printf 'entry for the current version, and release mode updates `VERSION` and this\n'
    printf 'changelog together.\n\n'
    printf '## [Unreleased]\n\n'
    printf '## [%s] - %s\n\n' "$version" "$release_date"
    printf '%s\n\n' "$body"
    if [[ -n "$existing_sections" ]]; then
      printf '%s\n\n' "$existing_sections"
    fi
    printf '%s\n' "$link_block"
  } > "$tmp"

  mv "$tmp" "$CHANGELOG_FILE"
}

write_version() {
  local version="$1"
  printf '%s\n' "$version" > "$VERSION_FILE"
}

ensure_release_entry_exists() {
  local version="$1"
  local escaped_version

  ensure_changelog_file
  escaped_version="${version//./\\.}"

  if ! grep -Eq "^## \[$escaped_version\]( - .+)?$" "$CHANGELOG_FILE"; then
    die "CHANGELOG.md does not contain a release entry for version $version"
  fi
}

build_release_binary() {
  local output="$1"
  local version git_hash build_time
  local -a ldflags

  version="$(current_version)"
  require_semver "$version"
  ensure_release_entry_exists "$version"

  git_hash="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")"
  build_time="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

  ldflags=(
    "-X main.version=${version}"
    "-X main.gitHash=${git_hash}"
    "-X main.buildTime=${build_time}"
    "-s -w"
  )

  printf 'building koios %s (%s) at %s...\n' "$version" "$git_hash" "$build_time"
  go build -ldflags "${ldflags[*]}" -o "$output" "$REPO_ROOT"
  printf 'output: %s\n' "$output"
}

ensure_gofmt_clean() {
  local -a go_files
  local unformatted

  mapfile -t go_files < <(git ls-files '*.go')
  ((${#go_files[@]} > 0)) || return 0

  unformatted="$(gofmt -l "${go_files[@]}")"
  if [[ -n "$unformatted" ]]; then
    printf 'error: gofmt required for:\n%s\n' "$unformatted" >&2
    die "run gofmt -w on the files above before releasing"
  fi
}

run_release_quality_gates() {
  ensure_gofmt_clean
  go vet ./...
  go test ./...
  go test -race ./...
}

commit_release() {
  local version="$1"
  local tag_name="$2"

  git add VERSION CHANGELOG.md
  git commit \
    -m "release: $tag_name" \
    -m "- bump VERSION to ${version}
- update CHANGELOG.md for ${version}

Signed-off-by: ${SIGN_OFF}"
}

release_mode() {
  local version="$1"
  local run_push="$2"
  local old_version tag_name previous_tag release_date

  old_version="$(current_version)"
  require_semver "$old_version"
  require_semver "$version"
  [[ "$old_version" != "$version" ]] || die "version is already $version"

  if (( run_push )); then
    ensure_remote_exists
  fi

  ensure_clean_worktree

  tag_name="v$version"
  ensure_tag_absent "$tag_name"

  previous_tag="$(previous_release_tag)"
  release_date="$(date +%Y-%m-%d)"

  write_version "$version"
  update_changelog "$version" "$release_date" "$previous_tag"
  run_release_quality_gates
  commit_release "$version" "$tag_name"
  git tag -a "$tag_name" -m "release: $tag_name"

  if (( run_push )); then
    git push "$REMOTE_NAME" HEAD
    git push "$REMOTE_NAME" "$tag_name"
  fi

  printf 'Released %s -> %s (%s)\n' "$old_version" "$version" "$tag_name"
}

main() {
  local mode="build"
  local output=""
  local explicit_version=""
  local bump_kind=""
  local run_push=1

  need_cmd date
  need_cmd git
  need_cmd go
  need_cmd gofmt
  need_cmd mktemp
  setup_repo_paths

  while (($# > 0)); do
    case "$1" in
      --major|--minor|--patch)
        mode="release"
        [[ -z "$bump_kind" ]] || die "only one of --major, --minor, or --patch may be used"
        bump_kind="${1#--}"
        shift
        ;;
      --skip-push)
        mode="release"
        run_push=0
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      -* )
        die "unknown option: $1"
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
          mode="release"
          [[ -z "$explicit_version" ]] || die "version may only be provided once"
          explicit_version="$1"
        else
          [[ "$mode" == "build" ]] || die "unexpected build output argument in release mode: $1"
          [[ -z "$output" ]] || die "output may only be provided once"
          output="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ "$mode" == "build" ]]; then
    output="${output:-$REPO_ROOT/koios}"
    build_release_binary "$output"
    return 0
  fi

  [[ -z "$explicit_version" || -z "$bump_kind" ]] || die "pass either an explicit version or one bump flag"

  if [[ -n "$bump_kind" ]]; then
    explicit_version="$(increment_version "$(current_version)" "$bump_kind")"
  fi

  [[ -n "$explicit_version" ]] || {
    usage
    exit 1
  }

  release_mode "$explicit_version" "$run_push"
}

main "$@"
