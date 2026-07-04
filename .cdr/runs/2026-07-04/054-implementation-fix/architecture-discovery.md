# Architecture discovery

- `engine/catalog/catalog.go:9-13,59-64,117,148-157`: `numStripes = 256`;
  `Catalog.stripes [numStripes]sync.Mutex`; `stripeFor(fileID uint64) uint64` returns
  `fileID % numStripes`. `Put`/`Get`/`Delete` all lock `c.stripes[stripeFor(fileID)]` around
  their read-modify-write critical section (e.g. `catalog.go:186-188`).
- `engine/catalog/content.go` (pre-fix): `ContentStore` had no locking at all beyond what
  `Catalog`/`wal.Writer` provide internally (see the (now superseded) doc comment at
  content.go:42-47 explicitly documenting this as a deferred known-gap).
- `Append` (content.go, pre-fix lines ~203-247): `cs.cat.Get` -> `os.ReadFile` (existing) ->
  compute `newContent`/`newSize` -> encode -> `wal.AppendAndApply` wrapping
  `writeContentFile` + `cs.cat.Put(updatedRec)`. The `os.ReadFile` + later `writeContentFile`
  pair is the unsynchronized read-modify-write; two concurrent `Append` calls for the same
  fileID can both read the same "existing" bytes before either writes back.
- Key constraint discovered: `Append`'s critical section itself calls `cs.cat.Put`, which
  internally takes `Catalog`'s own `stripes[stripeFor(fileID)]` lock. If `ContentStore` reused
  `cs.cat.stripes` directly (the exact same mutex instances) for its own locking around
  `Append`, that would double-lock a non-reentrant `sync.Mutex` from the same goroutine ->
  deadlock. Decision: give `ContentStore` its own independent `stripes [numStripes]sync.Mutex`
  array, keyed by the same `stripeFor` function (package-private, already reusable since
  `ContentStore` lives in the same `catalog` package), reusing the *convention* rather than the
  *lock instances*.
- `Create` (content.go): only ever called once per fileID with a freshly-allocated fileID
  (engine/idalloc's monotonic `Next()`), so no pre-existing content to race a
  read-modify-write against for the same fileID -- no locking needed for the bug class found.
- `Read` (content.go): single `os.ReadFile`, no read-modify-write; `writeContentFile`'s
  write-to-temp-then-rename technique means a concurrent Read always sees either the fully-old
  or fully-new content (rename is atomic same-filesystem), never a torn file -- no locking
  needed either.
- `catalog_test.go:TestCatalogConcurrentDistinctFileIDs` is the repo's precedent-setting style
  for a concurrency regression test (goroutines + `sync.WaitGroup`, `-race`, final-state
  assertion after `wg.Wait()`), followed for the new `content_test.go` test.
