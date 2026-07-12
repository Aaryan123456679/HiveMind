# Architecture Discovery — Subtask 6.1.1

## Order followed
index/ (`.cdr/index/file.jsonl`, 69 lines, no `ui/` entries) -> memory/handoffs (none found for
`ui/`) -> docs/HLD.md -> docs/LLD/*.md (none dedicated to `ui/`; HLD is the only doc mentioning
it) -> touched-path check (`ls ui/`) -> source (`api/routes/query.go`, `api/main.go`) for the
gateway contract the UI mirrors.

## Does `ui/` already exist?

Checked directly (did not assume): `ui/` exists but contains only a placeholder
`ui/README.md` (no `package.json`, no `src/`, no build tooling). README text:

> React dashboard for HiveMind: document upload, query with a `k` (topic count) slider,
> knowledge-graph visualization, and benchmark result charts. Scaffolding (Vite/React) to be
> added in the demo/deployment build phase.

docs/HLD.md's repo-layout section (line 83) also marks `ui/` as "# React dashboard (not yet
built)". So this is genuinely greenfield: no framework, no package.json, no existing
conventions to preserve. The README's own wording ("Scaffolding (Vite/React) to be added")
is a strong repo-authored hint toward Vite, which this run adopts.

## Frontend tooling decision inputs

- No existing `package.json` anywhere in `ui/` to constrain framework choice.
- `ui/README.md` explicitly names "Vite/React" as the intended scaffolding tool, authored
  before this subtask — treated as the repo's stated intent rather than an open choice.
- Environment has `node v22.22.3` / `npm 10.9.8` available.
- Issue #30's later subtasks (6.1.2-6.1.4) specify component tests "e.g. React Testing
  Library" and 6.1.5 specifies e2e "e.g. Playwright/Cypress" — both are React-ecosystem-first
  suggestions, consistent with Vite + React + TypeScript.
- Decision: **Vite + React + TypeScript**, with **react-router-dom** for the 5-route shell
  (react-router is the de facto standard router for this stack and is what "a router test"
  in the acceptance criteria implies). Vitest + @testing-library/react for the router test
  (Vitest integrates natively with Vite, avoiding a second test-runner config).

## What does `api/` expose today, and does it matter for 6.1.1?

`api/routes/query.go` defines the `/query` HTTP route and documents a disclosed gap (see its
doc comment, also referenced in `api/main.go`): proto/hivemind.proto's `service HiveMind` has
no RPC for invoking the Python query pipeline yet, so `/query` is "structurally reachable"
but not wired to a live end-to-end answer today (tracked as a separate, already-disclosed gap
from a prior subtask, issue #56 subtask 4.6.3.2). `api/main.go` wires only the routes package's
mux; grep confirms no `/ingest /graph /files /admin` Go handlers exist yet either (only
`api/routes/query.go` + `api/routes/query_test.go` are present under `api/routes/`).

This confirms the task instruction: this subtask's UI must **mock** API responses in its
tests rather than hit a live backend — there is no live, fully-wired backend to hit yet for
any of the 5 routes, and even where there is (`/query`), it's a stand-in. The UI shell itself
(6.1.1) makes no HTTP calls at all; that arrives with 6.1.2.

## Existing conventions elsewhere in the repo to mirror

- Go modules (`engine/`, `api/`) use doc-comment-heavy files that disclose gaps/decisions
  inline (e.g. api/routes/query.go's "Real-wiring gap -- disclosed choice" section) and
  reference the originating GitHub issue/subtask number. This run follows the same
  convention in the new TS/TSX files' doc comments.
- Commit style (from `git log --oneline -20`): single-line `type: summary` subject
  (`feat: ...`, `fix: ...`, `chore: ...`, `docs(cdr): ...`), body organized as
  Problem/Solution/Impact paragraphs in prior commits with that format (per task instructions).
