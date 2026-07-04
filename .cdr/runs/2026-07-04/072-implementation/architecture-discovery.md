# Architecture Discovery — 2a.1.3

Index-first, then targeted reads (no source read before this):

- `.cdr/index/task.jsonl` — confirmed 2a.1.1/2a.1.2 both state "verified".
- `docs/LLD/mvcc.md` "Read path" section (authoritative contract):
  > "Readers snapshot the current version pointer at the start of the request and read that specific
  > version to completion, regardless of concurrent writers advancing the pointer afterward."
  This is the exact contract 2a.1.3 must implement. GC section confirms old versions are reclaimed
  only once "no in-flight reader still holds a snapshot referencing them" via refcounted epochs — but
  that reference-counting/epoch-GC machinery is explicitly a SEPARATE subtask (2a.2, out of scope here
  per the task brief). No epoch-GC exists yet anywhere in the repo (`grep -r epoch engine/` — nothing),
  so nothing today ever deletes a version file once written.

- `engine/mvcc/write.go` (2a.1.1/2a.1.2, already verified):
  - `VersionWriter.VersionPath(fileID, version) string` — deterministic path
    `<dir>/<fileID>.v<version>.md`.
  - `VersionWriter.WriteVersion` — writes a version file via write-temp-then-rename (atomic on same
    filesystem); once a version N's file exists, no code path in `write.go` ever reopens or rewrites
    it (`WriteVersion` always computes a NEW, never-before-used N for its own write; `CommitVersion`'s
    retry loop calls `WriteVersion` again for a fresh N, never touches an old N's file).
  - `Catalog.CompareAndSwapCurrentVersion` (engine/catalog/catalog.go) — CAS keyed on the caller's
    observed `CurrentVersion`; write-side durability order is: version file written to disk FIRST,
    THEN CAS publishes the pointer. This is what 2a.1.2 already guarantees.

- `engine/catalog/catalog.go`'s `Catalog.Get(fileID) (CatalogRecord, error)` — the only way to read
  `CurrentVersion` "as of now". This is the snapshot-capture primitive: calling `cat.Get(fileID)` at
  the start of a read and reading `rec.CurrentVersion` IS "capturing the pointer at request start".

- `engine/catalog/content.go`'s `ContentStore.Read` — Phase 1 (1.4.2), explicitly single-version only
  (always reads the fixed `v1.md` file regardless of `CurrentVersion`; see its own doc comment noting
  version-aware read is deferred to "whichever later MVCC subtask"). That subtask is this one. This
  subtask does NOT modify `content.go` (out of impacted-modules scope for 2a.1.3 — impacted modules are
  `engine/mvcc/read.go, engine/mvcc/read_test.go` only); it adds mvcc's own version-aware read path
  independent of `ContentStore`, consistent with how `write.go` (2a.1.1) added its own `VersionWriter`
  independent of `ContentStore.Create`/`Append` rather than modifying `content.go`.

## Key design confirmation (correctness reasoning, per task brief step 4)

Because (a) version files are immutable and never rewritten once written (2a.1.1's guarantee, i.e.
`WriteVersion` always assigns a fresh N and never reopens an old N's path), and (b) nothing in the
codebase today deletes old version files (epoch-GC is explicitly future/out-of-scope subtask 2a.2), a
version N's on-disk file, once observed to exist via a snapshot taken at time T, is guaranteed to still
exist, at that exact byte content, at any later time T' > T — including throughout the entire duration
of a read that started at T. Therefore "read to completion against a stale-but-consistent version" is
achievable with NO additional locking on the read side: capture `CurrentVersion` via `cat.Get` at the
start (this is the snapshot moment), then `os.ReadFile` the specific versioned path
(`VersionWriter.VersionPath(fileID, snapshottedVersion)`) — even if some other goroutine concurrently
calls `CommitVersion` and CASes `CurrentVersion` forward in between, that only ever writes and publishes
a NEW, different-numbered file; it can never touch the already-snapshotted N's file. This reasoning is
identical in spirit to `ContentStore.Read`'s existing doc comment ("writeContentFile's write-to-temp-
then-rename technique makes a single Read always observe either fully-old or fully-new content, never a
torn/partial one") but stronger here: MVCC versioning means the reader doesn't even need atomicity of a
single file swap — it reads a specific immutable file that no writer will ever touch again.

## Style precedent for the interleaving test

- `engine/wal/record_test.go`'s `TestFsyncBeforeApply` and `engine/catalog/content_test.go`'s
  `TestContentCreate` (via `content.go`'s `createWithHook` test-only seam, `afterWALBeforeApply func()`)
  establish this repo's convention for proving ordering: an internal, package-visible hook parameter
  that the exported API wires as `nil`, and the test file (same package) calls the unexported "...WithHook"
  variant directly, injecting a hook function at the precise interleaving point to be proven. 2a.1.3's
  test follows the same convention: `Snapshot` exposes an unexported hook point invoked after the
  version-pointer snapshot is captured but before the version file's bytes are actually read from disk,
  letting the test pause there, run a concurrent `CommitVersion` to completion, then resume the read and
  assert it still returns the pre-commit content.
