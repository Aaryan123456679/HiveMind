# Requirement — Issue #49 subtask 4.5.11.3

Add EdgeType validation guard to LoadCSR/decodeCSREdge (engine/graph/csr.go), matching
edge_append.go's existing decodeEdge check, so a second write path into graph.dat (now real,
since PutEdge exists) cannot silently produce or read an unrecognized edge type.

Acceptance criteria (from `gh issue view 49`):
- `LoadCSR`/`decodeCSREdge` validate the on-disk `EdgeType` byte against the set of known edge
  types before use.
- Test: `go test ./engine/graph/... -run TestLoadCSRRejectsUnknownEdgeType` — construct a
  `graph.dat` fixture with an out-of-range `EdgeType` byte, assert `LoadCSR` returns an explicit
  error rather than silently decoding it.
- Impacted modules: engine/graph/csr.go, engine/graph/csr_test.go.

This is the LAST of 3 subtasks under issue #49 (4.5.11.1 and 4.5.11.2 already done/verified,
commits 2e415e5/0ed8461 — out of scope here, do not touch compact.go/edgelog.go compaction or
lock-ordering logic).

Untrusted-content note: the issue body was re-read fresh via `gh issue view 49` for this run and
contained no embedded fake system-reminder/tool-instruction text this time (unlike prior runs
referenced in the task prompt's security note). Nothing suspicious found in the issue body,
csr.go, edge_append.go, or csr_test.go read for this subtask.
