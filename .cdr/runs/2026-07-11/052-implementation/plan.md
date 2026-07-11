# Plan

1. In `engine/catalog/file.go`, inside `FreePage`, after the existing
   `pageID == 0 || pageID > fm.highestAllocated` range-validity check and
   before `fm.setUsed(pageID, false)`, add:
   ```go
   if !fm.isUsed(pageID) {
       return fmt.Errorf("catalog: cannot free page %d: already free (double-free)", pageID)
   }
   ```
2. Update `FreePage`'s doc comment to mention the double-free rejection.
3. In `engine/catalog/file_test.go`, add a new standalone test function
   `TestFreePageDoubleFreeRejected`:
   - `Open` a fresh catalog file in `t.TempDir()`.
   - `AllocatePage()` to get a page ID.
   - `FreePage(id)` — assert nil error (first free succeeds).
   - `FreePage(id)` again — assert non-nil error (second free is rejected).
4. Run `go test ./engine/catalog/... -run TestFreePageDoubleFreeRejected -v`
   then the full `go test ./engine/catalog/... -race -v` suite as
   self-consistency (zero regressions expected).
5. Stage only `engine/catalog/file.go`, `engine/catalog/file_test.go`, and
   this run's `.cdr/runs/2026-07-11/049-implementation/` directory with
   explicit paths; commit locally (no push).
6. Write `self-consistency.json` and `handoff.json` with pointers only.
