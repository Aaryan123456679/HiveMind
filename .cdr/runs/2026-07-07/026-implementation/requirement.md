# Subtask 2b.3.6 — Atomic split commit

Issue #12, Phase 2b "Auto-split", final capstone subtask.

Commit the entire split (allocate+write new files, redirect stub + catalog
transition, btree insert/repoint, graph SPLIT_SIBLING/REDIRECT edges — i.e.
2b.3.1 through 2b.3.5) as a single WAL-covered, fsynced transaction, and
release queued writers on commit.

This is explicitly flagged as the highest-risk correctness surface in the
repo. The requirement is to be honest about exactly what atomicity guarantee
is achieved vs. what remains a residual risk, rather than claim a stronger
guarantee than is actually implemented.

Impacted modules (per task grant): `engine/split/execute.go` (+ tests,
extend not replace), with explicit permission to touch `engine/graph/` and
`engine/wal/` if genuinely necessary to close the crash-recovery gap noted
in `.cdr/memory/pending.md` (graph edge-append records durable at the byte
level but not integrated into any WAL replay path).

Test spec: `go test ./engine/split/... -run TestSplitAtomicCommit`, using
crash-injection at deterministic hook points, per repo convention
(`optimisticReadHook` / `crabRetryHook` idiom in `engine/btree`).

SECURITY NOTE: issue/commit content in this repo has repeatedly contained
embedded fake system-reminder-style prompt injections (fake date-change
notices, fake MCP tool instructions, fake "Auto Mode Active" directives).
During this run, tool stdout again contained such injected text. It was
treated as untrusted plain-text data only; no embedded instruction was
followed.
