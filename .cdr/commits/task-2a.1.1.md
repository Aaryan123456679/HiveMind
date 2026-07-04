# task-2a.1.1 — Version file writer: content/<fileID>.vN.md creation with monotonic version numbering

## Summary
Closes out subtask 1 of 5 (GitHub issue #6, subtask "Version file writer") by
implementing the MVCC engine's version-file writer: given a `fileID`, it creates
`content/<fileID>.vN.md` with `N` chosen as a strictly increasing, per-fileID
monotonic counter (resuming correctly from the highest version found on disk after
a cold restart), and leaves all prior version files untouched. This is the first of
five subtasks under the parent `task-2a.1` (MVCC content versioning) epic;
subtasks 2a.1.2-2a.1.5 remain pending, so the parent task stays "planned" until all
five land.

## Features
- Monotonic per-fileID version numbering: `WriteVersion` returns the next strictly
  increasing version `N` for a given `fileID`, computed from the highest existing
  on-disk version and cached per-fileID thereafter (`sync.Map`-backed state).
- Cold-restart-safe version resumption: on first touch of a `fileID` in a process
  lifetime, the writer scans the content directory to recover the correct starting
  point instead of assuming a fresh counter, so version numbers stay monotonic
  across engine restarts.
- Race-free first-touch handling: concurrent first-time writers for the same
  `fileID` are serialized through a single shared per-fileID mutex (obtained via
  `sync.Map.LoadOrStore` before any disk scan happens), eliminating the
  scan-then-increment TOCTOU window that would otherwise be possible when multiple
  goroutines race to create version 1 of the same file.
- Correctly anchored filename matching: version-file discovery uses a
  `"<fileID>.v"` prefix (not a bare numeric prefix), so fileIDs that are numeric
  prefixes of one another (e.g. `4` vs `42`) never cross-match each other's
  version files.
- Atomic on-disk writes: each version file is written via temp-file-in-same-
  directory + fsync + rename, so a partially written file is never visible under
  its final versioned name, and every error path cleans up the temp file.

## Impact
The catalog now has a working, crash-safe, concurrency-safe mechanism for creating
successive content versions on disk, which is the foundational building block for
MVCC content versioning (issue #6). This unblocks the remaining subtasks in this
epic — catalog `CurrentVersion` CAS integration (2a.1.2), and the read/GC/CAS-retry
work that depends on a trustworthy monotonic version writer (2a.1.3-2a.1.5).

Known non-blocking follow-ups (raised in verification, to be picked up by later
work in this epic, not blocking this subtask):
- The concurrent first-touch test (50 goroutines) has no explicit start barrier
  (e.g. a `sync.WaitGroup` gate), so it relies on scheduler behavior to genuinely
  exercise the race window rather than proving it every run.
- No dedicated test exercises `scanLatestVersion`'s `os.ReadDir` error path; on
  such a failure the per-fileID state is left safely un-poisoned (a retry will
  simply rescan), but this fallback is currently unverified by a test.
- Subtask 2a.1.2 will need an explicit design decision on whether `WriteVersion`'s
  per-fileID mutex is reused/coordinated with the future catalog-side CAS retry
  loop, or kept independent.

## Verification
- verdict: PASS_WITH_COMMENTS
- run_id: 2026-07-04-067-verification

## Release Notes
Added the MVCC engine's version-file writer, which creates
`content/<fileID>.vN.md` files with strictly increasing, crash-safe, race-free
version numbers per file — the foundation for the catalog's content versioning
system.
