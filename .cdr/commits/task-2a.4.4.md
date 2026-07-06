# task-2a.4.4 — Lock-free optimistic version-counter read

## Summary

Fourth of 5 subtasks under task-2a.4 (B-tree latch-crabbing concurrency, GitHub issue #9). Adds `Tree.Lookup`, a genuinely lock-free, version-bracketed optimistic read path to `engine/btree`, additive alongside the untouched Phase-1 free `Lookup`. Prior to this subtask, `Tree.Insert`/`Tree.Delete` (2a.4.2/2a.4.3) already gave concurrent writers a deadlock-free, latch-crabbing path, but the only available read (`Lookup`) performed zero synchronization and had no correctness guarantee against structural mutation landing mid-read. `Tree.Lookup` closes that gap without introducing any reader/writer lock contention: every node visited during descent (including siblings peeked while chasing `NextSibling`/`NextLeaf` move-right recovery) reads that node's version counter before and after reading its content via `NodeStore.Version`/`ReadNode`; a mismatch on any node discards the whole attempt and restarts descent from the tree's current root, mirroring the `errRestartFromRoot` "always safe redo" discipline and reusing the same jittered no-retry-cap backoff convention established by `crabInsert`/`crabDelete`.

Shipped in one round, no fix cycle. Verification (`010-verification`) independently confirmed the core risk of this subtask — the torn-read/data-race concern — to be sound: no shared mutable Go-level state crosses the reader/writer boundary, `ReadNode`/`WriteNode` are single fixed-size syscalls, and the residual OS/hardware-level same-page atomicity gap is honestly documented as open rather than hand-waved (not closeable without either reintroducing locking into the read path, which defeats the subtask's purpose, or a full odd/even seqlock scheme). One non-blocking finding (F1) surfaced: the retry loop's call to `t.Root()` briefly acquires `rootMu` (shared with `Tree.Insert`/`Tree.Delete`'s root-bootstrap/root-split), which narrowly contradicts the doc comments' literal claim of "never calls Lock/TryLock anywhere in the read path." See Impact and Verification below.

## Features

- `Tree.Lookup`: new, additive lock-free optimistic read entry point on `engine/btree.Tree`. The untouched Phase-1 free `Lookup` function remains available for existing single-threaded callers.
- Version-bracketed read discipline: every visited node (including siblings peeked for Blink-tree move-right recovery) has its version counter read before and after content read via `NodeStore.Version`/`ReadNode`; any mismatch discards the attempt.
- Restart-from-root retry on version mismatch, reusing the same `errRestartFromRoot`-style "always safe redo" discipline and jittered no-retry-cap backoff convention as `crabInsert`/`crabDelete`.
- Zero per-node `Lock`/`TryLock` calls anywhere in the read path (confirmed via `grep -n '\.Lock(\|\.TryLock(\|\.Unlock('` returning zero matches in `lookup.go`'s new code), enabling true multi-reader/multi-writer concurrency: readers never block writers or each other.

## Impact

- `engine/btree` gains a genuinely lock-free concurrent read path, complementing 2a.4.2/2a.4.3's latch-crabbing writes; readers and writers no longer contend on per-node latches at all.
- Establishes the version-bracketed optimistic-read pattern (read version, read content, read version again, retry-from-root on mismatch) as the reusable template for any future lock-free read added to `engine/btree`.
- `task-2a.4` (parent) remains `planned` — subtask 2a.4.5 (mixed insert/delete/lookup workload) is still pending and is the natural place to stress this new read path against live concurrent writers end-to-end.
- One non-blocking, tracked follow-up (F1): `Tree.Lookup`'s retry loop calls `t.Root()`, which briefly acquires `rootMu` — the same mutex used by `Tree.Insert`/`Tree.Delete` for root-bootstrap/root-split. This narrowly contradicts the doc comments' literal claim that the read path "never calls Lock/TryLock anywhere." Practical impact is narrow (`rootMu` is only contended during the rare root-change instant), but the doc comments should be scoped to the per-node-latch guarantee that actually matters (or `rootMu` should be replaced with an atomic root pointer so the literal claim holds). Recorded in `.cdr/memory/pending.md` for a future revisit.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `010-verification`
- All dimensions PASS except `architecture_conformance` and `maintainability`, both PASS_WITH_COMMENTS for the F1 finding above.
- Torn-read/data-race safety argument (the highest-priority dimension for this subtask) independently verified sound: no shared mutable Go-level state crosses the reader/writer boundary; `ReadNode`/`WriteNode` are single fixed-size syscalls; the residual OS/hardware-level same-page atomicity gap is honestly documented as open, not closeable without reintroducing read-path locking or a full seqlock scheme.
- `go build`/`go vet`/`gofmt` clean; `engine/btree` suite green under `-race` (including a 25x repeat of the targeted optimistic-read test); whole-module `go test ./... -race` clean; a temporary, non-committed adversarial stress test (64 readers / 16 writers, tight key overlap, ~200s under `-race`) also ran clean.
- Confidence: HIGH.

## Release Notes

`engine/btree` now supports true multi-reader/multi-writer concurrency: `Tree.Lookup` is a new, additive lock-free optimistic read that never blocks on, or is blocked by, per-node latches held by concurrent `Tree.Insert`/`Tree.Delete`. The pre-existing single-threaded free `Lookup` function is unchanged and remains available. No breaking API change. One non-blocking follow-up is tracked: the read path's doc comments should be scoped to reflect that `t.Root()` briefly acquires a tree-level `rootMu` (unrelated to per-node latch-freedom, but a literal correction to the current wording), or `rootMu` should be replaced with an atomic root pointer.
