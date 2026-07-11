# Validation matrix (doc-only subtask — manual review against source)

| Doc claim | Source of truth | Verified |
| --- | --- | --- |
| Status: implemented, lists file.go/idalloc.go/catalog.go/content.go | `engine/catalog/*.go` all non-stub, non-trivial (~1580 total LOC across the four) | yes |
| Front-matter `last_synced_commit` = current HEAD | `git rev-parse HEAD` = `78a18180bf6e611b212a9ba4cba29af0904c1f5f` | yes |
| `ContentPath` = `<root>/content/<fileID>.v1.md` | `content.go` `contentDirName`, `contentVersionSuffix`, `ContentPath` | yes |
| `Create` WAL-before-apply via `wal.AppendAndApply` | `content.go` `createWithHook` | yes |
| `Create` duplicate-fileID = legal last-write-wins overwrite (4.5.5.4) | `content.go` `Create` doc comment (lines 226-241) | yes |
| `Read` resolves via `cs.cat.Get` first, wrapped `ErrNotFound` | `content.go` `Read` | yes |
| `Append` read-modify-write under `cs.stripes[stripeFor(fileID)]`, threshold-crossing signal, header-cache invalidation | `content.go` `Append` | yes |
| `ContentStore.stripes` is independent from `Catalog.stripes` (deadlock avoidance: `cat.Put` takes Catalog's own stripe) | `content.go` `ContentStore` doc comment (lines 44-61) | yes |
| `headerCacheMu` independent single mutex, not striped | `content.go` `ContentStore` doc comment (lines 86-97) | yes |
| `FileManager.mu` narrowly scoped to `highestAllocated`/`bitmap`, not page I/O; fix replaces prior caller-side `fmMu` | `file.go` `FileManager` doc comment (lines 42-65); `catalog.go` doc comment lines 74-92 (compressed view, confirmed via grep/read) | yes |
| `FreePage` double-free guard (4.5.5.1) | `file.go` `FreePage` (lines 194-212) | yes |
| `IDAllocator` sidecar file + rationale | `idalloc.go` `idAllocSuffix` doc comment (lines 16-40) | yes |
| `NewIDAllocator` cross-check against `maxFileIDInCatalog` (4.5.5.2) | `idalloc.go` `NewIDAllocator` (lines 73-132), `maxFileIDInCatalog` (lines 134-184) | yes |
| Three-lock model in `Catalog`: `stripes`, `pageStripes`, `indexMu` | `catalog.go` `Catalog` doc comment + struct fields (lines 55-137) | yes |
| `activeMu`/`activePageID` — single, unstriped, real residual serialization point | `catalog.go` `insert` (lines 391-440), struct field doc comment (lines 130-136) | yes |
| `numStripes = 256`, `numPageStripes = 256` | `catalog.go` lines 13, 30 | yes |
| Cross-references (mvcc.md, split.md, wal.md, btree.md, HLD.md) still accurate | unchanged from prior doc revision, links exist | yes |

No automated test exists for this subtask per its test spec (doc-only, manual
review). Self-consistency here = "every specific claim above traces to a
line/comment actually present in the current source", checked above.
