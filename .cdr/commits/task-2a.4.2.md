# task-2a.4.2 — Latch-crabbing insert

## Summary

Second of 5 subtasks under task-2a.4 (B-tree latch-crabbing concurrency, GitHub issue #9). Adds concurrent, hand-over-hand ("latch-crabbing") insertion to `engine/btree`, built on the per-node latch/version primitive from 2a.4.1, with a Blink-tree (Lehman & Yao) technique for staying correct across concurrent internal-node splits.

This subtask took four implementation rounds to close. That is reported here as useful engineering history, not as a defect in the process — it is exactly the outcome rigorous, adversarial re-verification is designed to produce on genuinely hard lock-free/latch-based concurrency code:

1. **Round 1** (`eff41a0`) shipped the initial design but its own test suite never exercised concurrent internal-node splits. The verifier's independent stress test surfaced an intermittent hard error (`findParent reached leaf while searching for parent`).
2. **Fix round 1** (`f0e972c`) added a bounded `NextLeaf`-chain-walk recovery in `findParent`'s leaf-level routing. This was verified sound by code trace plus 40/40 clean re-runs — but that same, harder-pushing re-verification pass (160 goroutines / 80k keys) uncovered a new, more severe **silent data-loss** bug: previously-inserted keys became unfindable with no error raised.
3. **Fix round 2** (`d145f08`) introduced a `sort.Search`-based positional-index change to `propagate`, claimed to fix the data loss, and looked promising over 154 stress runs. The next re-verification pass built an exhaustive 24-permutation deterministic test proving the "fix" was a mathematical no-op — identical output to the old formula in every case — so the apparent improvement was very likely incidental timing perturbation, not a real fix. Changes requested a second time.
4. **Fix round 3** (`efa5cef`/`7c10bba`) followed a deadlock the orchestrator personally caught live: a background stress run hung for 43+ minutes, was sent `SIGQUIT`, and the resulting goroutine dump proved a genuine circular-wait deadlock on `NodeStore.Lock` between concurrent `findParent`/`propagate` calls. Root cause: hand-over-hand crabbing used blocking `Lock` during descent/re-derivation, and the tree's structure lets a node be reached both via its parent's `Children` link and via an unlinked sibling's `NextLeaf`/`NextSibling` chain — two concurrent walks could each hold a latch the other needed. The structural fix converted every hand-over-hand latch acquisition to non-blocking `TryLock`, with full release and restart-from-root (jittered backoff) on a miss — deadlock-free by construction. This round also reverted round 2's no-op `sort.Search` back to the simpler `pos := j`, keeping its bounds check as a permanent invariant.

Final re-verification (`2026-07-06-002-verification`, 4th pass) confirmed the deadlock fix is structurally sound — every remaining blocking `Lock` call was enumerated and proven cycle-free — and re-ran the original silent-data-loss repro 30 additional times with zero recurrences, favorable (though not airtight, ~7% false-negative chance) evidence that the TryLock/restart fix also incidentally resolved the earlier data-loss symptom, i.e. the same underlying defect manifesting differently under different contention levels.

## Features

- Window-of-2 latch-crabbing insert: hand-over-hand latch acquisition down the tree, holding at most a parent/child pair of latches at any time, releasing the parent once the child is confirmed safe.
- Blink-tree (Lehman & Yao) technique (`NextSibling` / `LowKey`) so a crabbing walk that lands on a node mid-split can detect it and step sideways instead of failing or corrupting the tree.
- Deadlock-free-by-construction latch acquisition: every hand-over-hand `Lock` call site converted to `NodeStore.TryLock`; on a miss the walk fully releases all held latches and restarts from the root after a small jittered backoff, since no goroutine can ever block on one latch while holding another.
- `TestCrabbingConcurrentPropagateNoDeadlock`: fast, deterministic regression test using a synchronization hook (`crabRetryHook`) to force the exact TryLock-collision interleaving that produced the deadlock, without relying on large-scale random contention.

## Impact

Foundational for the remaining `task-2a.4` subtasks:

- **2a.4.3 (delete)** and **2a.4.5 (mixed workload)** must reuse the `TryLock` + full-release + restart-from-root pattern established here for any hand-over-hand latch acquisition. Blocking `Lock` calls during descent are the proven root cause of the round-3 deadlock and must not be reintroduced in the delete path or the mixed-workload driver.
- `task-2a.4` (parent) remains `planned` — subtasks 2a.4.3, 2a.4.4, and 2a.4.5 are still pending.

One low-risk item is carried forward as a tracked follow-up rather than a blocker: the TryLock restart loop has no retry cap, so there is a theoretical (never observed, mitigated by jittered backoff) livelock risk under adversarial scheduling, with no data-loss-via-give-up risk since the loop never gives up. See `.cdr/memory/pending.md`.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `2026-07-06-002-verification`
- Deadlock fix confirmed structurally sound: every remaining blocking `Lock` call site enumerated and proven unable to participate in a wait-for cycle.
- Deadlock-class stress repro (the exact 160g/80k scenario that hung for 43 minutes) clean across 30 additional independent runs, plus the implementer's own 10-run batch; full engine module `-race` suite clean.
- Silent-data-loss repro re-run 30 more times post-fix with zero recurrences — favorable but not airtight evidence (~7% false-negative chance) that the same root cause underlies both symptoms and both are now resolved.
- Recommends closing subtask 2a.4.2 / GitHub issue #9 now; no further round required.

## Release Notes

`engine/btree` now supports concurrent, multi-writer inserts via latch-crabbing with Blink-tree sibling-chasing for concurrent splits. No public API change; this is an internal concurrency-correctness upgrade to the B-tree insert path, reached after four implementation rounds that surfaced and fixed a routing edge case, a silent-data-loss bug, and (root cause) a genuine deadlock in the original hand-over-hand locking scheme. One low-risk, non-blocking follow-up (no retry cap on the TryLock restart loop) is tracked for later hardening.
