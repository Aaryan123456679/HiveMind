# Plan — 4.5.19.3

1. Create `.cdr/tools/reserve-run-dir.sh`:
   - Usage: `reserve-run-dir.sh <agent-suffix> [base-dir] [start-number] [max-attempts]`
     e.g. `reserve-run-dir.sh implementation` -> creates and prints
     `.cdr/runs/<today>/<NNN>-implementation` for the first NNN (starting at the current highest+1,
     zero-padded to at least 3 digits, no upper bound so ranges like 4-digit thousands still work)
     that it can atomically claim.
   - Algorithm: scan existing `.cdr/runs/<date>/*` once to get a *starting* candidate number
     (highest existing + 1) purely as a performance hint (avoids O(n) EEXIST retries from 1), then
     loop: `mkdir "$dir"` (POSIX atomic — only one caller can succeed for a given path); on success,
     print the path and exit 0; on `EEXIST`, increment candidate and retry; cap retries via
     `max-attempts` (default 10000) to avoid infinite loops on unexpected persistent failures.
   - Critically: the *decision of who wins* a given NNN is made by the filesystem via `mkdir`
     atomicity, not by comparing `ls` output between processes — this removes the TOCTOU race.
   - Zero-pad to 3 digits by default (matches existing convention `001`..`999`), auto-widening for
     numbers >= 1000 (matches existing usage of 4-digit ordinals like `1300-implementation` seen in
     this run history).
2. Create `.cdr/tools/test-reserve-run-dir.sh`:
   - Sets up an isolated temp `.cdr/runs/<date>` directory (does NOT touch the real repo's
     `.cdr/runs/`).
   - Spawns N=20 (configurable) concurrent background invocations of `reserve-run-dir.sh
     implementation` against that temp dir, waits for all, then asserts: (a) all 20 succeeded, (b)
     all 20 returned directory paths, (c) all 20 paths are distinct, (d) all 20 directories actually
     exist on disk. Exits non-zero with a diagnostic on any collision or failure.
3. Run the test locally to confirm zero collisions across repeated runs (self-consistency, not
   verification).
4. Update pointer documentation: append a short note to the global cdr-runtime skill markdown
   (`implementation.md`, `verification.md`, plus the shared agents) referencing
   `.cdr/tools/reserve-run-dir.sh` as the required allocation mechanism, replacing the old "ls and
   pick next" prose. This lives in a separate git repo (`cdr-runtime`) and is applied out-of-band —
   not part of the HiveMind commit for this subtask, but done in this session as part of fully
   satisfying the acceptance criteria ("update the relevant skill/documentation").
5. Single local commit in HiveMind repo referencing "issue #58, 4.5.19.3".
