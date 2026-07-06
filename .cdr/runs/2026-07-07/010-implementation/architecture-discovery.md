# Architecture discovery — subtask 2b.2.1

## Files read (read-only dependencies, not modified)

- `engine/split/orchestrate.go` (+ its own architecture-discovery.md at
  `.cdr/runs/2026-07-07/007-implementation/architecture-discovery.md`, 2b.1.3) — documents
  the scope boundary vs issue #12 explicitly: `Orchestrator` (BeginSplit/EndSplit/
  AbortSplit/AdmitWrite) owns only the catalog `Status` state machine
  (Active -> Splitting -> Active/Split) plus the write-admission gate. It deliberately does
  **not** own "allocating redirect targets, writing new content/stub files, B+Tree
  repointing, graph edges" — all issue #12's ("Atomic split-transaction execution") job.
  `EndSplit(fileID, catalog.StatusSplit)` is the primitive issue #12's execution logic is
  expected to call once it has actually finished producing data; `Orchestrator` does not
  populate `CatalogRecord.RedirectTargetIDs` itself.
- `engine/split/guard.go` (2b.1.2) — per-file CAS guard, unrelated to proposer shape;
  confirms no gRPC/network dependency anywhere in the package today.
- `engine/split/trigger.go` (2b.1.1) — stateless size-threshold detector, unrelated to
  proposer shape; confirms same (no gRPC deps).
- `engine/catalog/content.go` — `ContentStore.Create`/`Append`/`Read` all operate on
  `[]byte` for file content (not `string`/`io.Reader`). Establishes the idiom: split-related
  code in this repo treats file content as `[]byte`.
- `engine/catalog/record.go` — `CatalogRecord.RedirectTargetIDs []uint64` already exists on
  the on-disk model (added in an earlier subtask), confirming issue #12 will populate a
  `[]uint64`-shaped redirect target list, not something `SplitPlan` needs to duplicate.

## Design conclusion — shape of `SplitProposer` and `SplitPlan`

Given:
1. The interface signature mandated by the issue: `ProposeSplit(fileContent) -> plan`.
2. Sibling subtask 2b.2.2's test spec explicitly says the mock returns fixed
   `[{newPath, sectionRanges}]` + a "redirect-summary" fixture — i.e. the plan's shape is
   already dictated by 2b.2.2's acceptance criteria, not invented here.
3. `orchestrate.go`'s documented boundary: actual redirect-target *allocation* (assigning
   real fileIDs), content/stub file writes, B+Tree repointing, and graph edges are ALL
   issue #12's job, not this subtask's or 2b.2.2's. So `SplitPlan` must describe a
   *proposed* split (which byte ranges of the original content go into which new logical
   path, plus a human/redirect-facing summary) without allocating any real fileIDs or
   writing anything — that's a pure proposal artifact for issue #12 to later execute against.
4. Content is `[]byte` throughout `engine/catalog`, so `ProposeSplit` takes `[]byte` and
   `SectionRanges` are byte offsets into that same `[]byte`, consistent with the codebase's
   existing convention (avoids inventing a parallel line-oriented or rune-oriented model).

Resulting minimal types (all in new `engine/split/proposer.go`):

- `SectionRange{Start, End int}` — half-open byte-offset range `[Start, End)` into the
  original `fileContent` slice, matching Go slice-indexing convention used elsewhere in the
  repo (e.g. `content.go`'s own slicing idioms).
- `SplitFileProposal{NewPath string; SectionRanges []SectionRange}` — one proposed new
  logical file: `NewPath` is a proposal-time human-readable/relative logical path (NOT a
  real fileID — issue #12 owns actual fileID allocation), `SectionRanges` says which parts
  of the original content belong to it (supports non-contiguous content, e.g. multiple
  header + body ranges assembled into one new file, matching 2b.2.2's "sectionRanges"
  plural test-spec wording).
- `SplitPlan{Files []SplitFileProposal; RedirectSummary string}` — `Files` is the full
  proposed split; `RedirectSummary` is a short human-readable description of the intended
  redirect (matching 2b.2.2's "redirect-summary fixtures" wording) that issue #12's
  execution logic can use e.g. as a stub-file body or log message — deliberately just a
  `string`, not a structured redirect model, since actual `RedirectTargetIDs []uint64`
  population is issue #12's job against `catalog.CatalogRecord`, not this proposal type's.
- `SplitProposer` interface: `ProposeSplit(fileContent []byte) (SplitPlan, error)` — the
  `error` return follows this repo's universal convention (every fallible call in
  `engine/split`, `engine/catalog`, `engine/mvcc` returns `(T, error)`); a real gRPC-backed
  implementation will need to report transport failures once it exists (later epic), so the
  interface must allow that from day one even though no such implementation exists yet.

## Confirmed absence of gRPC/agent dependency

`engine/split/` currently imports only `errors`, `fmt`, `github.com/Aaryan123456679/
HiveMind/engine/catalog`, `github.com/Aaryan123456679/HiveMind/engine/wal` (all internal,
no network/gRPC). Top-level `proto/` and `agents/` directories do exist in this repo, but
`proto/` contains only a `README.md` (no `.proto` files or generated Go stubs yet) and
`agents/` is an unrelated Python package (`agents/ingestion`, `agents/llm`, etc., with its
own `pyproject.toml`/venv) — there is no Go-importable gRPC client package anywhere in the
module for `engine/split` to depend on, consistent with the issue's statement that "real
gRPC wiring happens in a later epic once proto/ and agents/ingestion exist" (i.e. exist in
Go-consumable form). `proposer.go` introduces zero new imports beyond possibly `errors`
(for the trivial fake's own error path in the test file) — no gRPC client package exists in
the module to accidentally import.
