# Plan — Subtask 4.5.5.4

## Investigation finding

Read the real source of `ContentStore.Create` / `createWithHook` in
engine/catalog/content.go (see architecture-discovery.md for line-level
detail). Confirmed:
- No existence check before writing (no `cat.Get` / `os.Stat` guard).
- `writeContentFile` writes-to-temp-then-renames — atomic replace, no leaked
  fd/inode on a second Create for the same fileID.
- `Catalog.Put` (catalog.go, untouched) is an explicitly-documented safe
  upsert (delete-old-slot-then-reinsert, no history kept, no leaked slots).

Conclusion: current behavior is a SAFE, ATOMIC overwrite. Not corrupting,
not leaking state, but previously undocumented and untested.

## Chosen path: (a) Document as intentional legal overwrite

Rationale: the underlying primitives (temp+rename, Catalog.Put upsert) are
already safe by construction; adding a new already-exists guard would be an
unrequested behavior change (could break any future caller relying on
idempotent re-Create, e.g. retry-after-partial-failure paths) not justified
by any evidence of corruption or the low risk rating in the prior
verification finding. Path (b) would only be warranted if double-Create were
shown to leak resources or produce inconsistent state, which it does not.

## Ordered changes

1. Extend the doc comment on `ContentStore.Create` (engine/catalog/content.go)
   to explicitly state: calling Create twice for the same fileID is legal and
   performs a full overwrite of both the content file and catalog record
   (last-write-wins), with a one-line pointer to writeContentFile's
   atomic-rename mechanism and Catalog.Put's upsert semantics as the reason
   this is safe. No logic change.
2. Add `TestContentCreateDuplicateFileID` (engine/catalog/content_test.go):
   Create fileID once with data A + rec A, then Create again with data B +
   rec B (different SizeBytes/LastModified). Assert:
   - `cs.Read(fileID)` returns B's bytes exactly (not A, not concatenated).
   - `cat.Get(fileID)` returns rec B exactly (via reflect.DeepEqual, matching
     existing TestContentCreate style).
   - Exactly one file exists at `cs.ContentPath(fileID)` (no leaked/orphaned
     temp or `.v1.md` variant files in the content dir) via `os.ReadDir`.
3. Add `TestContentCreateEmptyAndLargeContent`
   (engine/catalog/content_test.go): two fileIDs in one test —
   - Empty content: Create with `[]byte{}` (or nil), assert Read returns
     zero-length (not error), and `cat.Get(...).SizeBytes == 0`.
   - Very large content: Create with a several-MB buffer (e.g. 8 MiB, well
     over defaultSplitThresholdBytes, deterministic non-zero fill pattern),
     assert Read returns byte-for-byte identical content and SizeBytes
     matches.
4. Run `go test ./engine/catalog/... -race` (build + full package pass,
   scope-limited per instructions).
5. Stage exactly `engine/catalog/content.go engine/catalog/content_test.go`
   plus this run's `.cdr/runs/2026-07-11/078-implementation/` directory; one
   commit, Problem/Solution/Impact message.
