---
id: repo-review
name: Repo Review
version: "1.0.0"
origin: bundled
trust: high
managed: true
commands:
  - name: repo-review
    description: Show the bundled repository review checklist.
    assistant_text: |
      Repository review checklist:
      - verify the requested scope matches the diff
      - look for tests covering changed behavior
      - note migration or config impact
      - call out risky assumptions, secrets, and destructive operations
---
Use this skill when reviewing a focused repository change. Prioritize:
- correctness over style nits
- user-visible behavior changes
- config or migration impact
- missing validation and test coverage
