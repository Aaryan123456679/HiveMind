# Problem statement (fix cycle)

Independent verification of issue #14's subtask 2b.5.2 (`.cdr/runs/2026-07-07/035-verification/verification.json`,
commit `3e95aa298799d52eee8e8bd2bf2351069a67fedb`) returned `CHANGES_REQUESTED`.

`TestReaderDuringSplit` (`engine/split/split_race_test.go`) pinned an `mvcc.Snapshot` against a
SEPARATE `mvcc.VersionWriter`-backed root/content directory from the one `ExecuteSplitAtomic`
actually splits (`catalog.ContentStore`'s `cs`). `mvcc.Snapshot.Read()` only ever consults
`CatalogRecord.CurrentVersion`, which `ExecuteSplitAtomic` never touches (only
`Status`/`RedirectTargetIDs`/`SizeBytes`). Because the "reader" and the "split" shared no mutable
state, the test could not fail regardless of whether `ExecuteSplitAtomic`'s concurrency behavior
toward in-flight readers was correct or badly broken -- a tautology dressed as a concurrency race
test, even though it launched real goroutines under `-race`.

`docs/LLD/split.md` names MVCC as the mechanism protecting "existing readers" during a split, so
2b.5.2's actual acceptance criterion (reader consistency during a REAL split) was not verified by
the test as written.

## Required fix (per verifier's stated options)
- Option A (preferred): rework the test to exercise a reader path that shares REAL state with the
  split -- concurrent `catalog.ContentStore.Read`/`ReadPartial` against the SAME `ContentStore`/
  fileID `ExecuteSplitAtomic` is splitting, during the live split window, asserting the reader never
  observes a torn/partial/corrupted read (only fully-consistent pre-split OR fully-consistent
  post-split/redirected content).
- Option B (fallback only if Option A reveals `ContentStore` reads are legitimately not the right
  integration point): explicitly document the scope limitation in `.cdr/memory/pending.md` and
  soften commit/closure claims.

## Determination
Read `engine/catalog/content.go`'s `Read`/`ReadPartial`/`LockFileContent` and
`engine/split/execute.go`'s `ExecuteSplitAtomic`/`writeNewContentFile` directly.

Findings:
- `ContentStore.Read` does NOT take `cs.stripes[stripeFor(fileID)]` (unlike `Append`/`ReadPartial`/
  `LockFileContent`) -- confirmed via direct source read, `content.go` lines 290-303.
- `ExecuteSplitAtomic` DOES take `cs.LockFileContent(originalFileID)` across its redirect-stub
  write + `cat.Put` + `InvalidateHeaderCache` sequence (execute.go, ~line 849), mirroring `Append`'s
  own critical section (issue #13's 2b.4.1 fix).
- Both `ContentStore.writeContentFile` (content.go) and `split/execute.go`'s `writeNewContentFile`
  use the identical write-to-temp-then-atomic-rename technique. Atomic rename on the same
  filesystem guarantees a concurrent `os.ReadFile`/`cs.Read` call can only ever observe the fully-old
  or fully-new file, never a torn/partial mix -- REGARDLESS of `cs.Read` not taking the stripe lock.

Conclusion: `ContentStore.Read`/`ReadPartial` ARE a real, meaningful integration point that shares
actual state with `ExecuteSplitAtomic`, and existing `writeContentFile`/`writeNewContentFile`
atomic-rename design is (as far as this investigation could determine) already sufficient to
guarantee non-torn reads. **Option A is correct and achievable, and should produce a genuinely
PASSING test with real teeth** (confirmed below by temporarily reintroducing a torn-write bug).
