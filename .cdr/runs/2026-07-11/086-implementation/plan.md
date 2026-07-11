# Plan

1. Update `docs/LLD/catalog.md` front-matter `last_synced_commit` to current
   HEAD (`78a18180bf6e611b212a9ba4cba29af0904c1f5f`).
2. Change `Status: scaffold only` to `Status: implemented (engine/catalog/*.go: ...)`.
3. Add/expand sections, integrated into the doc's existing structure (not a
   changelog append):
   - Storage layout: mention `FileManager`, `IDAllocator` sidecar, `ContentStore`
     content/ subdirectory layout.
   - New "CRUD API (catalog.go)" section covering `Put`/`Get`/`Delete`/CAS and
     the three-lock model (stripes, pageStripes, indexMu), replacing the old
     one-line "Concurrency" bullet.
   - New "activeMu and the residual insert-path serialization caveat"
     subsection, explicit about it being a real (not just theoretical)
     contention point.
   - New "FileManager and the striped-mutex scoping fix" section: narrow
     per-page/per-stripe locking replacing full-body serialization, plus the
     FreePage double-free guard (4.5.5.1).
   - New "ID allocation (idalloc.go)" section: sidecar rationale + the
     cross-check against catalog.dat's max FileID (4.5.5.2).
   - New "ContentStore (content I/O)" section: Create/Read/Append contract,
     duplicate-fileID last-write-wins semantics (4.5.5.4), ContentStore's own
     independent striped locking (distinct array from Catalog.stripes, why it
     must be independent to avoid deadlock).
   - New "WAL-before-apply" section, cross-referencing wal.md's invariant and
     spelling out that this guarantee lives in ContentStore, not Catalog itself.
   - Expanded "Concurrency" section summarizing all locks package-wide.
   - Update "Known risks" to keep section-index staleness, add the activeMu
     residual-serialization risk and the no-index-rebuild-on-load known gap
     (previously buried in catalog.go's doc comment, now surfaced in the LLD).
4. Keep "Cross-references" section as-is (still accurate).
5. Manually re-read the updated doc against the four source files for
   accuracy (self-consistency step) before committing.
6. Stage `docs/LLD/catalog.md` explicitly (not `-A`), plus this run's own
   artifacts, and commit with Problem/Solution/Impact message.
