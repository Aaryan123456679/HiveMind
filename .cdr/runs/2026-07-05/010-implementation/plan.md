# Plan — task-2a.3.1

Add `TestStripedConcurrencyStress` to `engine/catalog/catalog_test.go`.

## Oracle design
Put/Get/Delete on DIFFERENT fileIDs are commutative (no shared state across fileIDs in
the Catalog's observable API), so the only ordering that matters for correctness is the
ordering of operations WITHIN a single fileID's sequence. Design:

1. Pick `numFileIDs = 2000` (>> `numStripes` = 256, so many fileIDs collide per stripe —
   average ~7.8 fileIDs/stripe — actually stressing cross-fileID stripe contention rather
   than spreading everything into distinct stripes).
2. Assign each fileID one of 5 fixed, deterministic CRUD patterns via `fileID % 5`:
   - pattern 0: Put(v1) -> final: present, v1
   - pattern 1: Put(v1), Delete -> final: absent
   - pattern 2: Put(v1), Put(v2) -> final: present, v2
   - pattern 3: Put(v1), Delete, Put(v2) -> final: present, v2
   - pattern 4: Put(v1), Put(v2), Delete -> final: absent
   Each Put uses a `recordForVersion(fileID, version)` helper (like `testRecord` but with
   `CurrentVersion` and `SizeBytes` varied by version) so final-state assertions check full
   record equality, not just presence.
3. The "serial-execution oracle" is computed statically/deterministically per fileID from
   the pattern definition above (expected presence + expected CatalogRecord) — this is
   equivalent to running each fileID's fixed sequence in a single serial goroutine, since
   the expected outcome only depends on that fileID's own ops in order.
4. Concurrency: spawn ONE goroutine per fileID (2000 goroutines), each executing its
   pattern's Put/Delete calls against the SAME shared `*Catalog` in program order (ops
   within one fileID's goroutine execute sequentially in that goroutine, guaranteeing
   within-fileID ordering; different fileIDs' goroutines race against each other
   unordered, which is fine since they're commutative). `wg.Wait()` for all to finish.
5. After all goroutines finish, iterate over every fileID (single-threaded, no goroutines)
   and assert: if expected-absent, `Get` returns `ErrNotFound`; if expected-present, `Get`
   succeeds and `reflect.DeepEqual`s the expected final record.
6. Errors during Put/Delete inside the per-fileID goroutines are reported via `t.Errorf`
   (safe: goroutines only ever call `t.Errorf`, never `t.Fatalf`, per Go testing rules for
   concurrent subtests/goroutines).

No changes to `catalog.go`. No new helper files.
