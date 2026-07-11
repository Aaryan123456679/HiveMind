# Architecture discovery

## Current state of engine/graph/csr.go (read in full)

`decodeCSREdge` (csr.go:101-112) **already** validates the decoded `Type` byte:

```go
func decodeCSREdge(data []byte) (CSREdge, error) {
    e := CSREdge{ ... Type: EdgeType(data[offCSREdgeType]), ... }
    if !ValidEdgeType(e.Type) {
        return CSREdge{}, fmt.Errorf("graph: decoded CSR edge has invalid type %d (target=%d)", data[offCSREdgeType], e.Target)
    }
    return e, nil
}
```

`LoadCSR` (csr.go:277-285) calls `decodeCSREdge` per edge and propagates its error, wrapped with
file path + edge index context. `WriteCSR` (csr.go:174-179) also validates every edge's Type
before writing, refusing to persist an invalid one in the first place.

This guard was added by subtask 3.1.4 (`edge.go`'s `ValidEdgeType`, see edge.go:1-34 package
doc), which predates issue #49's `PutEdge` write path. So the acceptance criteria's *code* is
already satisfied — `ValidEdgeType` covers `EdgeSplitSibling`, `EdgeRedirect`,
`EdgeEntityCooccur`, `EdgeLLMAsserted`; anything else (including `EdgeTypeInvalid`, the zero
value, and any undefined byte 5-255) is rejected.

## engine/graph/edge_append.go's decodeEdge convention (the pattern to match)

`decodeEdge` (edge_append.go, EdgeAppender's narrower SPLIT_SIBLING/REDIRECT-only scope) does:

```go
switch e.Type {
case EdgeSplitSibling, EdgeRedirect:
    // valid
default:
    return Edge{}, fmt.Errorf("graph: decoded edge invalid type %d", data[offEdgeType])
}
```

csr.go's `decodeCSREdge` already follows the same convention (explicit error, no panic, includes
the raw byte value in the message) but delegates the switch to the shared `ValidEdgeType` helper
(edge.go) since CSR/EdgeLog need the full 4-type set, not EdgeAppender's narrower 2-type set.
edge.go's own package doc (lines 14-18) explicitly documents this split-scope relationship and
states edge_append.go's decodeEdge/AppendEdge are intentionally left unchanged. No mismatch to
fix.

## Gap: missing test

`engine/graph/csr_test.go` (read in full, 5 existing tests: TestCSRFormat, TestCSREmptyGraph,
TestCSRCorruptedPayloadDetected, TestCSRTruncatedHeaderRejected, TestCSRLargeAdjacency) has no
test named `TestLoadCSRRejectsUnknownEdgeType` and no test that exercises decodeCSREdge's
type-validation branch specifically (as opposed to the CRC32/truncation checks already covered).
`WriteCSR` itself refuses to write an invalid type, so the only way to produce the fixture the
issue's test spec calls for is to build the file bytes directly (as
`TestCSRCorruptedPayloadDetected` already does for payload corruption): write a normal graph via
`WriteCSR`, then patch the on-disk edge-type byte to an out-of-range value and recompute the
CRC32 so the failure is attributable to the type-validation guard, not the (already-tested) CRC
check.

## Conclusion

No production-code change needed in csr.go — the guard already exists and already matches
edge_append.go's convention. This subtask's actual deliverable is exclusively the missing
regression test in csr_test.go, per its own "Impacted modules" list (csr.go, csr_test.go) — csr.go
is listed as potentially touched but requires no functional change; only its comments could be
extended to reference the new test if useful (optional, not required by the acceptance
criteria).
