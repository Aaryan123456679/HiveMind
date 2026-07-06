# Plan — task-2a.4.4

## New API

`func (t *Tree) Lookup(path string) (fileID uint64, found bool, err error)`

Entry point mirroring `Tree.Insert`/`Tree.Delete`: uses `t.Root()` (safe under
concurrent root swaps) and retries the whole lookup from the (possibly new)
root whenever a version mismatch is detected anywhere during descent.

```
func (t *Tree) Lookup(path string) (uint64, bool, error) {
    for attempt := 0; ; attempt++ {
        if attempt > 0 { crabRetryBackoff(attempt) }
        root := t.Root()
        if root == reservedNodeID { return 0, false, nil }
        fileID, found, err := t.lookupOnce(root, path)
        if err == errOptimisticRetry { continue }
        return fileID, found, err
    }
}
```

## Per-node optimistic read primitive

```
func readNodeOptimistic(store *NodeStore, nodeID uint64) (isLeaf bool, leaf LeafNode, internal InternalNode, ok bool, err error) {
    v1 := store.Version(nodeID)
    isLeaf, leaf, internal, err = store.ReadNode(nodeID)
    if err != nil { return }
    v2 := store.Version(nodeID)
    ok = v1 == v2
    return
}
```

Never calls `Lock`/`TryLock` — only `Version` (atomic load) and `ReadNode`
(plain `ReadAt` + decode, no synchronization at all). This is the load-bearing
guarantee: the read path can NEVER block a writer, and a writer's `Lock` call
never has to wait for or coordinate with a reader.

## Descent protocol (`lookupOnce`)

Mirrors `crabInsertOnce`'s shape exactly, with two substitutions: (1) no
`Lock`/`TryLock`/`Unlock` at all — `readNodeOptimistic` replaces every
`store.Lock` + `store.ReadNode` pair; (2) a version mismatch anywhere aborts
the ENTIRE lookup (return `errOptimisticRetry` up to `Tree.Lookup`, which
restarts from the CURRENT root, not just re-reads the one node) rather than
locally retrying — mirrors `errRestartFromRoot`'s "always safe to redo, since
descent so far performed no mutation" discipline, restart-from-root is what
matches read data path so descent can't act on stale results
picked up before the current node's mismatch.

1. Read current node optimistically. If mismatch -> `errOptimisticRetry`.
2. If leaf: chase `NextLeaf` while it exists, peeking each sibling's first
   key against `path` exactly as `crabInsertOnce` does (stop once
   `len(nextLeaf.Keys) > 0 && path < nextLeaf.Keys[0]`), each hop itself
   version-checked via `readNodeOptimistic`. Then binary-search the settled
   leaf for `path`.
3. If internal: chase `NextSibling` the same way, peeking each sibling's
   `LowKey`, each hop version-checked. Then `sort.Search` for the child index
   and descend.

Every single-node read in every hop (initial node AND every sibling peeked
during move-right) is independently version-bracketed; a mismatch on ANY of
them aborts the whole lookup rather than just that hop, since we hold no
lock across hops and cannot otherwise be sure the hops we've already
committed to (`currentID = nextID`) are still valid once one hop turns out
torn.

## Torn-read safety argument (the crux of this subtask)

`NodeStore.ReadNode` does exactly one `s.f.ReadAt(buf, offset)` call for a
buffer of exactly `NodeSize` (4096) bytes; `NodeStore.WriteNode` does exactly
one `s.f.WriteAt(encoded, offset)` call, also exactly `NodeSize` bytes,
followed by `version.Add(1)`. Both are single syscalls against the same
`*os.File`, at the same node-ID-aligned offset, for a payload exactly one
memory-page size (4096 bytes is the common VM page size on the platforms this
project targets: Linux/ext4/xfs, macOS/APFS). This matters for two reasons:

1. **Common-case correctness (version-bump-after-write is what makes the
   check meaningful at all):** `WriteNode` bumps the version strictly AFTER
   its `WriteAt` call returns, never before and never during. So if a
   reader's `v1` (read before `ReadNode`) and `v2` (read after `ReadNode`)
   are equal, no `WriteNode` call for this node COMPLETED its version bump
   within that window — which, in the ordinary case where the OS/filesystem
   read and write calls don't interleave byte-by-byte, guarantees the content
   read is either entirely pre-write or entirely post-write, never a mix.
2. **Residual risk this subtask does NOT eliminate (explicitly surfaced, not
   hand-waved away):** POSIX does not guarantee that a `ReadAt` for N bytes
   and a concurrent `WriteAt` for the same N bytes on the same file, from
   different goroutines/threads, are atomic with respect to each other for
   arbitrary N. In principle, a reader's `ReadAt` could observe some bytes
   from the pre-write content and some bytes from the in-flight write (a
   genuine torn read at the OS/page-cache level), and if the writer's
   `version.Add(1)` (which happens strictly after `WriteAt` returns, but with
   NO synchronization tying it to the reader's second `Version` load) has not
   yet executed by the time the reader takes its `v2` snapshot, `v1 == v2`
   would hold despite the content being torn. This is exactly the risk the
   task description calls out.
   - Mitigating factors specific to this codebase, not a hard guarantee:
     `NodeSize` == 4096 == one VM page on the platforms in scope; on Linux
     and macOS, page-cache-backed regular-file writes of exactly one page are
     effectively page-atomic in practice with respect to concurrent reads of
     the same page (the kernel does not interleave sub-page byte writes with
     a concurrent read of that same page in normal operation), even though
     POSIX itself does not codify this as a portable requirement.
   - This is NOT fixed by adding a `Lock`/`TryLock` call to the read path
     (which would defeat this subtask's entire acceptance criterion: readers
     must never block writers or be blocked by them) and NOT fixed by
     changing `WriteNode`'s version-bump protocol to a full seqlock
     (odd-during-write / even-when-stable) scheme, which was an EXPLICIT,
     already-shipped design decision in 2a.4.1 ("single-increment-after-
     mutation scheme, NOT a seqlock-style pre/post pair" — latch.go's own
     doc comment) that 2a.4.2/2a.4.3 already depend on for their own
     `WriteNode` call sites; changing it now is out of this subtask's scope
     and would risk regressing two already-verified subtasks.
   - Disposition: documented, deferred, non-blocking residual risk, in the
     same spirit as this task's own explicit "no retry cap" and prior
     subtasks' "no latch-registry eviction" deferred items — NOT silently
     ignored. Surfaced again in `handoff.json` for 2a.4.5 and any future
     hardening pass (e.g. a real seqlock odd/even version scheme would close
     this fully, at the cost of revisiting 2a.4.1's already-verified design).

## Test plan (`TestOptimisticRead`, lookup_test.go)

1. `NoConcurrency`: build a tree via `insertN`, run `Tree.Lookup` for every
   inserted key and a few absent keys, assert exact match against Phase-1
   `Lookup` results (sanity baseline, also exercises Blink-tree move-right
   descent since `insertN` produces multi-level trees).
2. `InterleavedWithInsertDelete`: mirrors
   `testCrabbingDeleteInterleavedWithInsert`'s oracle style — concurrent
   `Tree.Lookup` goroutines running continuously alongside concurrent
   `Tree.Insert`/`Tree.Delete` goroutines. Every `Tree.Lookup` result is
   checked against a definition of "correct": for `found=true`, the returned
   fileID must equal SOME still-valid mapping (original insert or the
   concurrent re-insert); for `found=false` on an originally-present key, it
   must be one of the keys concurrently being deleted (never an untouched
   key). No `Tree.Lookup` call may return an error. Run under `-race`.
3. `ForcedRetryDeterministic`: build a small tree, install
   `optimisticRetryHook` to detect the retry, then run `Tree.Lookup` in a
   goroutine that is paused (via a lookup-side test hook invoked right after
   the FIRST version read, before `ReadNode`/second version read) while a
   concurrent goroutine performs a real `Tree.Insert`/`WriteNode` on that
   exact node, bumping its version, before releasing the paused lookup to
   continue -- deterministically forcing a genuine version mismatch and
   proving the retry path is taken (not just probabilistically reached).

## Test hook design

Add `optimisticReadHook func(nodeID uint64, phase string)` (or two hooks) OR
reuse a single `beforeVersionRecheckHook func(nodeID uint64)` invoked in
`readNodeOptimistic` right after the content `ReadNode` call, before the
second `Version` load -- this is the natural place to pause deterministically
and let a concurrent writer land its `WriteNode` in the window, mirroring
`crabRetryHook`'s "test-only synchronous hook, nil no-op in production" shape.
