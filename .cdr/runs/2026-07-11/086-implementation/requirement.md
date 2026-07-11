# Subtask 4.5.5.5 (issue #42, milestone #10)

Title: docs/LLD/catalog.md LLD-sync pass

Acceptance criteria (verbatim from `gh issue view 42`):
> `docs/LLD/catalog.md` is updated from scaffold-level to document `ContentStore`'s
> create/write/append/read contract, its WAL-before-apply wiring, the striped-mutex
> `FileManager` scoping fix (narrow per-page/per-stripe locking, not full-body
> serialization), and the `activeMu`/insert-path residual serialization caveat.

Test spec (verbatim): doc-only change; manual review against `engine/catalog/*.go` for
accuracy, no automated test.

Impacted modules (per issue): `docs/LLD/catalog.md` only.

Context: final subtask of issue #42 ("[4.5] engine/catalog: low-risk hardening follow-ups").
Subtasks 4.5.5.1-4.5.5.4 (FreePage double-free guard, IDAllocator cross-check,
Append-vs-Read -race test, ContentStore.Create duplicate-fileID semantics) are already
implemented and committed.
