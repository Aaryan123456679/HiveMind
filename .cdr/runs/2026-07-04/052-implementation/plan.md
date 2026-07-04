# Plan — 1.4.3

1. Add `defaultSplitThresholdBytes = 8 * 1024` const (documented override point:
   `ContentStore.splitThresholdBytes` field, defaulted in `OpenContentStore`,
   directly settable by tests in-package for exercising the threshold cheaply
   without needing real 8KB writes).
2. Add `ContentStore.splitThresholdBytes uint64` field; set to
   `defaultSplitThresholdBytes` in `OpenContentStore`.
3. Add `Append(fileID uint64, data []byte) (thresholdCrossed bool, err error)`:
   - Look up current record via `cs.cat.Get(fileID)`; wrap ErrNotFound like Read.
   - Read existing content bytes via `os.ReadFile(cs.ContentPath(fileID))` to
     get oldSize (авoid relying solely on rec.SizeBytes so on-disk content and
     catalog stay the single source of truth for the appended bytes).
   - newContent = append(oldContent, data...); newSize = len(newContent).
   - updatedRec := rec with SizeBytes = uint64(newSize), LastModified bumped
     (use existing rec.LastModified as before; leave as-is, not in scope) —
     actually bump LastModified is out of scope/no clock available; leave
     rec.LastModified unchanged (only SizeBytes changes per acceptance
     criteria) to avoid inventing unrequested behavior.
   - WAL-log updatedRec via wal.NewCatalogPutRecord + wal.AppendAndApply,
     apply fn: writeContentFile(fileID, newContent) then cat.Put(updatedRec).
   - thresholdCrossed = oldSize <= threshold && newSize > threshold.
4. Test `TestContentAppend`: Create small initial file, then append chunks in
   a loop; assert cat.Get(fileID).SizeBytes matches cumulative length after
   each append; assert thresholdCrossed is true exactly once (the append that
   pushes size over splitThresholdBytes) using a small overridden threshold
   for speed (e.g. 64 bytes) to keep the test fast, avoiding an 8KB real
   write loop, while still exercising the exact-once crossing semantics.
5. Build/vet/gofmt, run `go test ./engine/catalog/... -race -v -count=1`.
6. One commit, handoff.json, task.jsonl entry.
