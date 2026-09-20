# ViTTS Gateway — Documentation Set

ViTTS Gateway is a multi-tenant, usage-metered Vietnamese text-to-speech API built on
[ZeroTTS](https://github.com/zeroweight-ai/ZeroTTS). This folder is the single source of
intent for the project. Code must match these docs; when they disagree, fix one of them
in the same PR.

## Reading order

| # | File | Purpose | Owner |
|---|------|---------|-------|
| 01 | `01-PRD.md` | Why we build it, for whom, what "done" means | Product |
| 02 | `02-architecture.md` | System overview, C4 levels, trust boundaries, tech stack | Engineering |
| 03 | `adr/` | Every load-bearing decision and its trade-offs | Engineering |
| 04 | `api/openapi.yaml`, `api/worker.proto` | Public REST contract and internal gRPC contract | Engineering |
| 05 | `05-data-model.md` | PostgreSQL schema, Redis keys, S3 layout | Engineering |
| 06 | `06-flows.md` | Load-bearing runtime flows with authz and side effects | Engineering |
| 07 | `07-permissions.md` | Roles, scopes, resource × operation matrix | Security |
| 08 | `08-variables.md` | Config and secrets mapped to risk | Ops |
| 09 | `09-user-stories.md` | Backlog: epics, stories, acceptance criteria | Product |
| 10 | `10-tests.md` | Verification map: rule → test → status | QA |
| 11 | `11-pre-mortem.md` | Tigers, paper tigers, elephants | Product |
| 12 | `12-implementation-plan.md` | Milestones and task order for Claude Code | Engineering |
| 13 | `13-runbook.md` | Operating the system: deploy, alerts, incidents | Ops |
| 14 | `14-engineering-standards.md` | Coding, testing, review, and git standards | Engineering |
| 15 | `15-manual-test-guide.md` | Exercising every feature by hand, with pass criteria | Engineering, QA |
| — | `plans/` | Per-task implementation plans written before the code | Engineering |
| — | `reports/` | Measured results and milestone close-outs | Engineering |

Agent operating instructions live in `/CLAUDE.md` and `/.claude/rules/*.md` (repo root),
derived from these docs. They are not system documentation.

## Conventions

- Markdown, sentence case headings, tables over prose.
- No "last updated" lines; git history is the source of truth.
- Every ADR has a status: Proposed, Accepted, Superseded.
- Requirements use IDs (`F-xx`, `NFR-xx`); stories reference them; tests reference stories.
