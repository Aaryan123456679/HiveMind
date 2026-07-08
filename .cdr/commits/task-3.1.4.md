# task-3.1.4 — Full edge-type validation for graph edges (issue #15, Epic Phase 3)

## Summary

Subtask 3.1.4 ("full edge-type support for ENTITY_COOCCUR/LLM_ASSERTED/SPLIT_SIBLING/REDIRECT") is
complete and independently verified. 3.1.3 introduced the `ENTITY_COOCCUR`/`LLM_ASSERTED` `EdgeType`
constants out of necessity for its own compaction test spec, but explicitly deferred full validation of
all four edge types to this subtask. This subtask closes that gap: undefined `EdgeType` byte values are
now rejected at every entry point in `engine/graph` (edge-log append, CSR encode, CSR decode) instead of
being silently persisted or round-tripped.

## Features

- `engine/graph/edge.go` (new): `ValidEdgeType` (canonical membership check over the 4 defined edge
  types), `EdgeTypeName`/`ParseEdgeType` (canonical `ENTITY_COOCCUR`/`LLM_ASSERTED`/`SPLIT_SIBLING`/
  `REDIRECT` string tokens matching `docs/LLD/graph.md`'s "Edge shape" section, ahead of 3.1.5's
  `edgeTypeFilter` needing this), and a validated `NewCSREdge` constructor.
- `edgelog.go`'s `EdgeLog.AppendEdge` now rejects any undefined edge-type byte, not just the
  `EdgeTypeInvalid` zero-value sentinel.
- `csr.go`'s `decodeCSREdge` is now fallible and validates on decode; `WriteCSR` validates every edge
  before encoding — symmetric validation on both the read and write path, closing the round-trip gap
  where an undefined edge type could previously pass through `graph.dat` with no error anywhere.
- Confirmed correctly left unchanged (read in full by both implementer and independent verifier):
  `edge_append.go`'s `EdgeAppender`, intentionally scoped to `SPLIT_SIBLING`/`REDIRECT` only per the LLD;
  and `compact.go`'s `mergeEdges`, whose weight-aggregation semantics already generalize correctly to all
  four edge types.

## Impact

Undefined `EdgeType` byte values are now rejected at every entry point (edge-log append, CSR encode, CSR
decode) instead of silently persisting or round-tripping through `graph.dat`. This is additive validation
only — no schema or wire-format changes, no changes to `edge_append.go`'s split/redirect scoping, and no
changes to `compact.go`'s merge semantics. Sets up 3.1.5's `edgeTypeFilter` (traversal API) with the
canonical name-mapping it needs.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run**: `.cdr/runs/2026-07-08/015-verification/verification.json`
- **Commit verified**: `4b9c63919a7bf56f3dec431bac5ff3933391b620` (parent `ed57468`)
- Confidence: high — all claims in the implementer's `self-consistency.json`/`handoff.json` were
  independently re-verified by direct source reading and by re-running tests/build/vet/gofmt, not merely
  trusted from artifacts. Adversarial checks confirmed `edge_append.go` and `compact.go`'s `mergeEdges`
  are correctly unchanged (not a convenient assumption — read directly), and confirmed round-trip
  correctness (`ParseEdgeType(EdgeTypeName(t))` and `WriteCSR`/decode happy paths).
- Thorough test coverage confirmed: invalid-byte rejection tested at 0, mid-range, and high undefined
  values across every entry point (edge-log append, CSR encode, CSR decode). Zero regressions; full
  `engine/graph` module suite green under `-race`, including 3.1.1-3.1.3's existing tests, with no split
  flake reproduced this run.
- **Non-blocking finding (provenance record hygiene, not a code defect)**: `014-implementation`'s
  `handoff.json` recorded a stale/nonexistent commit hash (`3ead3c4`) and its `metadata.json`'s `git_head`
  was the pre-commit parent (`ed57468`) rather than the actual implementation commit. The real commit
  `4b9c639` was fully verified directly against source regardless — this was provenance-record staleness
  only. Corrected as part of this close-out: both fields in
  `.cdr/runs/2026-07-08/014-implementation/handoff.json` and `metadata.json` now point to the real commit
  hash `4b9c63919a7bf56f3dec431bac5ff3933391b620`.

## Release Notes

Adds full validation for all four `engine/graph` edge types (`ENTITY_COOCCUR`, `LLM_ASSERTED`,
`SPLIT_SIBLING`, `REDIRECT`): a new `engine/graph/edge.go` provides canonical name/byte mapping and a
validated edge constructor, wired symmetrically into the edge-log append path and both the CSR encode and
decode paths so an undefined edge-type byte can no longer be silently written or round-tripped through
`graph.dat`. Purely additive — no wire-format or schema changes, no behavior change to the existing
split/redirect edge-append scoping or to compaction's weight-merge semantics (both confirmed already
correct and left untouched). No breaking API changes.

**This does not close GitHub issue #15.** Only subtask 3.1.4 is done; 2 subtasks remain (3.1.5-3.1.6)
before Epic Phase 3's issue #15 is closable. Implementation commit `4b9c639` is local-only, not pushed —
no push performed as part of this commit-documentation step.
