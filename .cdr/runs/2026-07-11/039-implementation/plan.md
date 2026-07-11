# Plan

1. Add `TestLoadCSRRejectsUnknownEdgeType` to `engine/graph/csr_test.go`:
   - Build a normal, valid adjacency map via `BuildCSR` and write it with `WriteCSR` (so the
     on-disk layout, header, and CRC are all produced by the real code path, not hand-rolled).
   - Read the file back into raw bytes.
   - Locate the on-disk offset of the first edge's `Type` byte
     (`csrHeaderSize + nodeCount*8 + (nodeCount+1)*8 + offCSREdgeType`) and overwrite it with an
     out-of-range `EdgeType` value (e.g. 99 — not one of the 4 valid types nor the 0
     `EdgeTypeInvalid` sentinel).
   - Recompute CRC32 over the modified payload and patch the header's CRC field, so the
     resulting failure is attributable specifically to the EdgeType validation guard, not the
     already-covered CRC mismatch path (`TestCSRCorruptedPayloadDetected` already covers that).
   - Write the patched bytes back to disk (this is the "second write path" fixture: bytes
     written outside WriteCSR's own validation, simulating a future/buggy writer).
   - Call `LoadCSR` and assert it returns a non-nil, explicit error (not a panic, not a
     silently-decoded invalid type).
2. No changes to csr.go (guard already present and already correct).
3. Run `go vet`/`go build` and the full test suite as self-consistency check (not verification).
4. One local commit; update handoff.json/self-consistency.json; do not push.
