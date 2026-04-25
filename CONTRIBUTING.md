# Contributing to koios

Thanks for contributing to koios.

## Before you start

- Read the [Code of Conduct](CODE_OF_CONDUCT.md).
- Check existing issues before starting new work.
- For security-sensitive findings, use the private reporting guidance in [SECURITY.md](SECURITY.md) instead of opening a public issue.

## Development setup

- Install Go 1.26.1 or later.
- Build the daemon with `go build ./...` or `scripts/release.sh`.
- Run the test suite with `go test ./...`.
- Run additional checks with `go vet ./...` and `go test -race -count=1 ./...`.
- Format changes with `go fmt ./...` before you open a pull request.

## Pull requests

- Keep pull requests focused and easy to review.
- Update documentation when behavior, setup, or operations change.
- Add or update tests when code paths change.
- For user-visible changes, add an entry under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md).
- Do not bump the root `VERSION` file unless the release process for that change explicitly requires it.

## Commit and review expectations

- Use clear commit messages that describe the change.
- Include validation details in the pull request description.
- Expect maintainers to ask for follow-up changes before merge when needed.

## Release notes

koios keeps release history in [CHANGELOG.md](CHANGELOG.md). `scripts/release.sh` validates that the current `VERSION` already has a matching changelog section before producing a release build.
