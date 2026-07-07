# Architecture discovery — subtask 2b.3.5

## (a) Append-only "repoint" semantics — interpretation

`engine/graph`'s edge log (`edge_append.go`, 2b.3.4) is a pure append-only
`wal.Writer`-backed log: there is no mutate/delete/rewrite API, only
`AppendEdge` and `ReadAll`. So "repoint inbound edges to redirect stub"
**cannot** mean literally rewriting existing `Edge` records in place — that
primitive does not exist and 2b.3.4's own doc comment is explicit that CSR
storage/compaction/mutation is deferred to Epic 3.

The decisive fact, already established by 2b.3.2/2b.3.3 and re-confirmed by
reading `execute.go`/`execute_test.go` this run: **`ExecuteSplitRedirectStub`
reuses `originalFileID` for the redirect stub — no new fileID is ever
allocated for the old path.** The old path's fileID identity never changes;
only its *content* (now a stub) and its catalog `Status`/`RedirectTargetIDs`
change. 2b.3.3's B+Tree repoint step is, by its own doc comment, a
"guaranteed-safe, single-field-write no-op" for exactly this reason.

The same reasoning applies here, one level up: **any pre-existing graph edge
whose `Target == originalFileID` already points at the redirect stub, with
zero graph mutation required**, because `originalFileID`'s identity is
unchanged. There is nothing to "rewrite" and nothing this subtask needs to
append to make old inbound edges resolve to the stub — that guarantee is
free, inherited from 2b.3.2's fileID-reuse decision, exactly as issue #12's
acceptance criteria implies ("...rather than rewriting the full inbound-edge
list" — i.e., don't rewrite anything; the identity-preserving redirect
design was chosen precisely so nothing has to be rewritten).

What this subtask *does* need to append is the other half of the "redirect"
relationship: a way for a reader who has landed on `originalFileID` (the
stub) to discover **where the content actually moved to**. That is exactly
what 2b.3.4's `EdgeRedirect` type's doc comment already says it represents:
"an edge FROM a redirect stub... TO one of its redirect targets." So this
subtask appends, for each new fileID, one `Edge{Source: originalFileID,
Target: newFileID, Type: EdgeRedirect}`. Combined with the "inbound edges to
old path already point at the stub for free" fact above, this gives a
complete, two-hop discovery path: existing inbound edge -> originalFileID
(now the stub) -> (new) `EdgeRedirect` edge -> each new fileID's real
content. No existing edge record is ever touched, matching the append-only
design.

## (b) SPLIT_SIBLING edge topology

Issue text: "All pairs of newly split-off files gain a SPLIT_SIBLING edge."
Read literally ("all pairs", not "a path" or "a star"), this calls for a
complete graph over the N new fileIDs. Given `Edge` is directed
(`Source`/`Target`) and "sibling" is inherently a symmetric relationship (no
natural direction), and a future Epic-3 traversal reader could plausibly
start from either sibling and expect to discover the other without needing
directionality-aware logic, this subtask appends **both directions for every
unordered pair** — i.e. for N new files, N*(N-1) directed
`EdgeSplitSibling` edges (a complete directed graph). For the common small-N
case (a split typically produces a handful of new files) this is cheap and
keeps `ReadAll`-based "siblings of X" queries a trivial `Source == X` filter
without needing undirected-edge-aware traversal logic in Epic 3. A
star-from-first-file topology was considered and rejected: it would silently
depend on `newFileIDs`'s ordering to define which file is the "hub", which
is exactly the kind of unpinned map-iteration-order fragility already flagged
in `.cdr/memory/pending.md`'s "canonical newFileIDs ordering" item; the
complete graph makes this topology decision moot (order does not matter for
correctness, only for on-disk append order, which is still made
deterministic below by sorting `newFileIDs` before iterating).

## (c) Crash-recovery gap — resolution decision

`.cdr/memory/pending.md`'s tracked item (from 2b.3.4) flags that edge-append
records are durable at the byte level (fsynced) but not integrated into any
`wal.Replay`-based crash-recovery path the way catalog/btree records are, and
explicitly says this must be resolved by 2b.3.5 or 2b.3.6, not silently
dropped.

Decision: **this subtask does NOT build any new replay/recovery integration
for graph edges; it defers full resolution to 2b.3.6**, for two reasons
confirmed by re-reading issue #12's 2b.3.6 text this run:

1. 2b.3.6's own acceptance criteria explicitly lists "graph edge writes" as
   one of the things that must "commit atomically under one WAL-covered
   transaction, fsynced before the split becomes visible" — i.e. 2b.3.6
   already owns wrapping graph edge writes into the transactional boundary
   that would also address recovery/replay integration holistically, across
   all of 2b.3.1-2b.3.5's writes, not just this one.
2. Building a bespoke, one-off replay path for just the edges this subtask
   appends would (a) be scope creep beyond "add SPLIT_SIBLING edges /
   redirect edges", (b) very likely be redone or discarded once 2b.3.6 wires
   the real cross-step atomic transaction, and (c) risks producing two
   divergent recovery mechanisms for the same log (this subtask's ad hoc one,
   and 2b.3.6's real one).

This subtask therefore calls `EdgeAppender.AppendEdge` directly/naively
(each call individually fsyncs, so no edge write is silently lost at the
byte level even without this call), and **explicitly documents in code** (doc
comment on the new function) that cross-step atomicity and crash-recovery
replay integration for these appends remains 2b.3.6's job — continuing to
surface the gap rather than silently resolving or dropping it, per this
run's explicit instruction. The `.cdr/memory/pending.md` item is left as-is
(not resolved) since 2b.3.6 is still expected to close it; if 2b.3.6 turns
out not to touch graph edges after all, that would need to be caught then,
not invented here speculatively.

## API surface used (engine/graph, unmodified)

- `graph.OpenEdgeAppender(dir) (*graph.EdgeAppender, error)`
- `(*graph.EdgeAppender).AppendEdge(graph.Edge) error`
- `graph.Edge{Source, Target uint64; Type graph.EdgeType}`
- `graph.EdgeSplitSibling`, `graph.EdgeRedirect`
- `graph.ReadAll(dir) ([]graph.Edge, error)` — test-only, per 2b.3.4's doc
  comment ("not a general query API").

No changes to `engine/graph/edge_append.go` were made or needed; its existing
API is sufficient for this subtask's needs.
