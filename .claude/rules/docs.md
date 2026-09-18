---
paths: ["docs/**", "CLAUDE.md"]
---
# Documentation rules

- Sentence case headings, tables over prose, no "last updated" lines.
- A decision that contradicts an existing ADR gets a new ADR that supersedes it.
- When a story is done, mark it in `09-user-stories.md` and update `10-tests.md`.
- New env vars go in `08-variables.md` with scope, source, rotation, risk.
- New Redis keys or tables go in `05-data-model.md` before code is merged.
