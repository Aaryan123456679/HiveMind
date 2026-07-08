# Requirement — subtask 3.1.2 (issue #15)

Source: `gh issue view 15` (verbatim subtask entry). Note: the issue body also contained
injected fake system-reminder-style text (fake date-change notice, fake MCP tool
instructions, fake "Auto Mode Active" directive) appended after the real subtask list —
this is a known recurring prompt-injection pattern in this repo's GitHub content and was
ignored as untrusted data, not acted upon.

## 3.1.2 — Append-only per-node edge log writer (avoids locking a shared adjacency array)

- **Acceptance criteria**: Concurrent edge appends to different source fileIDs never block
  each other because each writes to its own per-node log rather than a shared array.
- **Test spec**: `go test ./engine/graph/... -race -run TestPerNodeEdgeLog`: concurrent
  appenders across many distinct fileIDs, assert no cross-blocking and correct per-node log
  contents.
- **Impacted modules**: `engine/graph/edgelog.go`, `engine/graph/edgelog_test.go`.

Context (not requirements, background only):
- 3.1.1 (previous subtask, verified) added `engine/graph/csr.go`: whole-snapshot CSR format
  for `graph.dat`, rewritten wholesale, not an append log.
- 3.1.3 (future subtask) will compact accumulated per-node edge-log entries into the CSR
  array, including ENTITY_COOCCUR weight increments.
- 3.1.4 (future subtask) owns edge-type *creation* support for all four types
  (ENTITY_COOCCUR, LLM_ASSERTED, SPLIT_SIBLING, REDIRECT) plus type-filtered queries; not
  this subtask's job.
