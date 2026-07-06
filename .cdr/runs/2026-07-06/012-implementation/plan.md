# Plan — task-2a.4.5

## Oracle / partitioning design

Every key index `i` in `[0, N)` maps to `genKey(i)`, and is statically
assigned to exactly ONE of four disjoint role-ranges (no key index is ever
targeted by two different roles, so the oracle is unambiguous regardless of
scheduling):

- **insertOnly** range `[0, 30000)` — 30,000 keys, 60 goroutines, each
  goroutine inserts its own disjoint modulo-slice of the range once, via
  `tree.Insert(genKey(i), fileID(i,0))`. Final oracle state: present with
  version 0.
- **deleteOnly** range `[30000, 60000)` — 30,000 keys, pre-seeded serially
  (via `insertN`-style real `Insert` calls, single-threaded, BEFORE
  goroutines start) so the tree is non-empty at the start of the concurrent
  phase; 60 goroutines then concurrently `tree.Delete` their own disjoint
  modulo-slice. Final oracle state: absent.
- **mutate** range `[60000, 80000)` — 20,000 keys, 40 goroutines, each owning
  a contiguous block of 500 key indices. Each goroutine runs three
  sequential passes over its OWN block: (1) insert all with version 0
  (forces splits), (2) delete all (forces merges), (3) re-insert all with
  version 1 (forces splits again) — genuinely forcing repeated
  split-then-merge-then-split structural churn in the SAME region while
  other goroutines (including lookups) are concurrently touching
  nearby/overlapping nodes. Final oracle state: present with version 1.
- **lookup** — 40 goroutines, no owned key range; each loops continuously
  over the ENTIRE keyspace `[0, 80000)` (sequential scan with a per-
  goroutine phase offset, wrapping) calling `tree.Lookup`, until a `done`
  channel (closed after all insert/delete/mutate goroutines finish) signals
  stop. Every single lookup result is checked inline (never deferred):
  error must be nil; if found, `fileID` must be a member of that key's own
  precomputed valid-fileID set (see below) — otherwise it is corruption
  (wrong key's value, or a value never legitimately assigned to this key)
  and the goroutine reports a hard failure via an error channel.

### Collision-proof fileID encoding

`fileID(i, v) = uint64(i)*10 + uint64(v)`, with `v` in `{0, 1}` (only
`mutate` ever uses `v=1`). Since role-ranges are disjoint on `i`, and the
`*10` spacing leaves room for the small number of versions used, fileID
values are globally unique per (key, version) pair. This makes any
cross-key corruption or stale/impossible-value corruption automatically
detectable: a lookup that returns a `fileID` not in `validFileIDs[genKey(i)]`
is corruption by construction, without needing any runtime coordination
between goroutines to detect it.

`validFileIDs[key]` (precomputed once, before starting goroutines, from the
static role assignment):
- insertOnly key i: `{fileID(i,0)}`
- deleteOnly key i: `{fileID(i,0)}` (its one and only ever-assigned value;
  post-delete lookups legitimately return not-found, which is NOT checked
  against a specific answer per the acceptance guidance — only "found implies
  member of valid set" is asserted)
- mutate key i: `{fileID(i,0), fileID(i,1)}`

## Scale

Total keys: 80,000. Total non-lookup goroutines: 60+60+40 = 160. Plus 40
continuous lookup goroutines = 200 total concurrent goroutines. This mirrors
2a.4.2's `VeryDeepOverlappingSubtree` (160g/80k) scale for the write side
while adding a lookup population on top, exceeding both 2a.4.2's and
2a.4.4's `InterleavedWithInsertDelete` (4000 keys, 32 goroutines) scale by a
wide margin — appropriate for the final capstone.

## Post-workload validation (after `wg.Wait()` on all non-lookup goroutines,
then close `done` and `wg2.Wait()` on lookup goroutines)

1. `finalRoot := tree.Root()`.
2. Build `wantPresent map[string]uint64` (insertOnly @v0, mutate @v1) and
   `wantAbsent []string` (deleteOnly) from the static partition.
3. `assertAllLookupable(t, store, finalRoot, wantPresent)` — via the
   Phase-1 free `Lookup` function (as insert_test.go's helper already does).
4. Cross-check every `wantPresent` key ALSO via `tree.Lookup` directly
   (independent-implementation cross-check, this capstone's specific
   extra correctness signal).
5. `assertAbsent(t, store, finalRoot, wantAbsent)`.
6. `assertStructuralInvariants(t, store, finalRoot, len(wantPresent))`.
7. `assertNoOrphanedPointers(t, store, finalRoot)`.
8. Fail on any error reported by the in-flight lookup goroutines' error
   channel (corruption / unexpected error), collected via a buffered error
   channel drained after both wait groups complete.

## Deterministic companion test (design guidance item 5)

Add `TestConcurrentMixedWorkloadForcedLookupDuringDelete`: a small,
fast, deterministic test mirroring `TestOptimisticRead/ForcedRetryDeterministic`'s
hook-based pattern, but with the concurrent mutator being a real `Tree.Delete`
(not `Tree.Insert`) that triggers a leaf merge touching the exact node a
paused `Tree.Lookup` has already read optimistically -- targeting "a lookup
optimistically reading a node exactly as a concurrent delete's merge/splice
touches it" per the design guidance. Uses `optimisticReadHook` to pause the
lookup right after it reads the root leaf's content, then a concurrent
`Delete` of a different key in the same leaf runs to completion (bumping the
node's version through the same `WriteNode` choke point), then releases the
lookup and asserts: no error, retry observed via `optimisticRetryHook`, and
the final answer is consistent with the post-delete state (the looked-up key
was not the one deleted, so it must still be found with its original
fileID).

## Timeouts

Every `go test` invocation during self-consistency uses an explicit
`-timeout`, generous given scale (10m for the full existing-suite run, 20m
for the 5x repeat of the exact test-spec invocation), per this package's
binding convention after the 43-minute hang experienced during 2a.4.2.
