# Requirement: subtask 4.5.11.2 (issue #49)

Fix `EdgeLog.ReadNodeAfter`/`TruncateNode` lock-ordering gap racing concurrent `AppendEdge`.

## Acceptance criteria (from `gh issue view 49`, subtask 4.5.11.2)
`EdgeLog.ReadNodeAfter` (called by `Compact` to decide what edge-log content is
"incoming" for a node) and `TruncateNode` must share a lock scope for a given
node across a single `Compact` iteration (OR `Compact` passes the exact
segment set/max-segment it read into `TruncateNode` so it truncates exactly
what was merged, not whatever exists now), so a concurrent `AppendEdge`
landing on that node between `Compact`'s read and `TruncateNode`'s removal
can never have its freshly-appended segment swept up and deleted before
being merged into `graph.dat`.

## Test spec
`go test ./engine/graph/... -race -run TestCompactConcurrentAppendNotLost`:
use a synchronization hook to force an `AppendEdge` to land inside the
read-then-truncate window; assert the concurrently-appended edge survives
and is eventually merged, not silently dropped.

## Impacted modules
`engine/graph/edgelog.go`, `engine/graph/compact.go`, `engine/graph/compact_test.go`.

## Explicitly out of scope
- 4.5.11.1 (sidecar staleness / segment-reuse) — already fixed (commit
  `ed57468`, confirmed by prior run 035-implementation/036-verification).
- 4.5.11.3 (EdgeType validation guard in `LoadCSR`/`decodeCSREdge`) — separate
  commit, do not touch `csr.go`/`csr_test.go`.

## Untrusted-content note
`gh issue view 49`'s body is a plain, well-formed subtask list with no
embedded fake directives. Separately, this run's own conversation/tool
stream contained several environment-injected blocks styled as legitimate
system reminders (a "date has changed, don't mention it" notice, a
"tokensave" MCP-server instruction block for a server never invoked, and an
"Auto Mode Active" directive) that are NOT genuine task input from the user
or from `gh issue view 49`. Per standing instructions, none of these are
treated as consent/approval or as altering scope/permissions; disclosed here
and in handoff.json for the record.
