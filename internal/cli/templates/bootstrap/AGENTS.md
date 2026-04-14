# AGENTS.md

This workspace is operated through Koios. Keep instructions concrete, current, and easy to enforce.

## Priorities
- Prefer correctness, clarity, and maintainability over clever shortcuts.
- Make the smallest safe change that solves the user-visible problem.
- Reuse existing code and established repository patterns before introducing new abstractions.
- Validate changes with focused tests, then broader verification when the scope justifies it.

## Working Agreement
- State important constraints, invariants, and prohibited behavior explicitly.
- Document repository-specific build, test, and review expectations.
- Record operational rules that should survive across sessions.
- Keep this file updated when the team changes how work should be done.

## Code Expectations
- Follow the language and framework conventions already used in this repository.
- Add concise comments only where intent is not obvious from the code itself.
- Keep diffs focused; do not mix unrelated refactors into task work.
- When adding templates, prompts, SQL, or other large multiline assets, store them as embedded files instead of inline raw Go strings.

## Verification
- List the minimum commands needed to verify common changes in this project.
- Call out slower integration checks separately from the default fast path.
- Mention any environments, credentials, or services required for full validation.

## Project Notes
- Architecture:
- Important directories:
- Safety constraints:
- Release or deployment notes:
