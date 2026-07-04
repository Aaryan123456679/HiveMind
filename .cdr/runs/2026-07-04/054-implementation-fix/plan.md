# Fix plan

1. Add `sync` import to `engine/catalog/content.go`.
2. Add `stripes [numStripes]sync.Mutex` field to `ContentStore`, documented as independent
   from `Catalog.stripes` (reentrancy hazard via `cs.cat.Put`).
3. In `Append`, acquire `cs.stripes[stripeFor(fileID)]` at the top (before `cs.cat.Get`),
   `defer` unlock, so the entire read-modify-write-and-commit sequence for a given fileID is
   serialized against other `Append` calls for the same fileID. Different fileIDs still hash to
   (mostly) different stripes and proceed concurrently.
4. Document why `Create`/`Read` do not need the same lock (see architecture-discovery.md).
5. Add `TestContentAppendConcurrentSameFileID` to `content_test.go`: Create a fileID, fire 50
   concurrent 1-byte `Append` calls (matching the verification agent's exact repro), `wg.Wait()`,
   then assert `Read` returns length 50 and `cat.Get`'s `SizeBytes` is 50.
6. Sanity-check the test actually catches the bug: temporarily stash the content.go fix and
   confirm the new test fails with the exact "length 1, want 50" signature reported by
   verification, then restore the fix.
7. `go build ./... && go vet ./... && go test ./catalog/... -race -v -count=1`; re-run the new
   test alone with `-count=5` for flakiness.
8. One commit, update task.jsonl, write handoff.
