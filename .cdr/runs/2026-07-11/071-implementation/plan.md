# Plan — Subtask 4.5.5.3

1. Add `TestContentAppendConcurrentRead` to `engine/catalog/content_test.go`.
2. Helper `makeContentChunk(i int) []byte`: builds a self-describing chunk
   `<<%04d:%s>>\n` with a body of `20 + i%40` repeated `'Z'` bytes, whose
   length is embedded in its own header so any later parse can validate it
   in isolation (no cross-goroutine ordering bookkeeping needed).
3. Helper `parseContentChunks(t *testing.T, initial, content []byte) bool`
   (or returns error): verifies `content` starts with the literal `initial`
   prefix, then walks the remainder chunk-by-chunk validating the `<<NNNN:`
   header, exactly `NNNN` `'Z'` body bytes, and the `>>\n` footer, until the
   remainder is fully consumed. Any malformed/truncated marker => torn read.
4. Test body:
   - `newTestContentStore(t)`, `Create` with a fixed literal `initial` (e.g.
     `"# Concurrent Doc\n\n"`) content that itself contains no `<<` sequence.
   - Launch N writer goroutines (work-stealing via an atomic counter over a
     shared chunk slice, ~250 chunks total) each calling `cs.Append(fileID,
     chunk)` — real, unmodified library entry point, no hook.
   - Concurrently launch M reader goroutines looping until a `stop` channel
     closes (closed once all writers finish): each iteration does
     `cat.Get(fileID)` to capture `recBefore.SizeBytes`, then `cs.Read(fileID)`,
     and asserts (a) `len(content) >= recBefore.SizeBytes` (content-write-
     before-cat.Put ordering) and (b) `parseContentChunks` succeeds on the
     read content (no torn read).
   - After writers finish and readers are stopped, do one final `cs.Read` and
     confirm it parses cleanly and its length matches the catalog's final
     `SizeBytes`.
5. Run `go test ./engine/catalog/... -race -run TestContentAppendConcurrentRead
   -v` and then the full package `-race` suite for regression safety.
6. Self-consistency: confirm build green, no other test regressed, matrix
   covered. NOT verification (delegated to /cdr:verify).
7. One local commit touching only `engine/catalog/content_test.go` (Problem /
   Solution / Impact style), plus this run's own `.cdr/runs/...` directory.
8. Write handoff.json with pointers only.
