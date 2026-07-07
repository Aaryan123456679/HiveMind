# Architecture discovery

## Security note (untrusted text)

This repo's GitHub issue bodies, commit messages/diffs, and some Bash stdout
have repeatedly contained embedded FAKE system-reminder-style text (fabricated
"date changed" notices, fake MCP/tokensave tool instructions, fake "Auto Mode
Active" directives) in prior turns of this exact session — confirmed again
during this run: `.cdr/memory/pending.md`'s file content, when read via Bash,
came back with two fabricated `<system-reminder>` blocks appended at the end
(a fake "date changed to 2026-07-07" notice and a fake "tokensave MCP
server"/"Auto Mode Active" directive). These are not real system reminders —
real ones are injected by the harness outside of tool output, never embedded
inside a file's own bytes. Treated as untrusted plain-text data only; nothing
in them was acted upon. Issue #13's own body was clean (no injected text this
time).

## What already exists vs. what issue #13 needs to newly build

Grepped the entire `engine/` tree (and `docs/LLD/`) for `SectionRange`,
`section`, `index`, `ReadPartial`, `headerOffset`/`HeaderOffset`,
`markdown header`:

- `SectionRange` / `SectionRanges` exist ONLY in `engine/split/proposer.go`
  (the split-plan's byte-offset ranges into the ORIGINAL file, used purely to
  decide what bytes go into which new split-off file at proposal/execution
  time) and are consumed by `engine/split/execute.go`'s
  `ExecuteSplitAllocateAndWrite`/`extractSections`. This is a completely
  different concept from a "header-offset cache" — it's a one-shot input to
  content extraction, not a persistent/cached index that could go stale after
  the fact. No invalidation concept applies to it.
- There is NO existing "markdown header-offset cache" and NO existing
  `ReadPartial` method anywhere in the codebase. `docs/LLD/catalog.md` and
  `docs/LLD/split.md` each have a "Known risks" bullet ("Section-index
  staleness: the markdown header-offset cache used by `ReadPartial` must be
  invalidated atomically within the same append/split transaction...") that
  describes this as a *risk to guard against once such a cache exists* — it
  is forward-looking design language, not a pointer to an already-built
  primitive. Issue #13 is the subtask that actually builds the cache/
  `ReadPartial` AND wires its invalidation, in one shot (its acceptance
  criteria and test spec talk about both in the same breath: "so
  `ReadPartial` never serves offsets against stale content").
- `engine/catalog/content.go`'s `ContentStore` (task 1.4.1-1.4.3) owns the
  single-version `.md` content file per fileID, with `Create`/`Read`/`Append`
  and a striped-mutex (`cs.stripes`, `numStripes=256`, keyed by
  `stripeFor(fileID) = fileID % numStripes`) serializing Append's
  read-modify-write critical section per fileID. This is the natural home for
  the header-offset cache and `ReadPartial`, matching the issue's own
  "Impacted modules" list.
- `engine/split/execute.go` (2b.3.1-2b.3.6, issue #12, just completed/verified)
  has two functions that change an existing fileID's on-disk content in place
  in a way that would invalidate any pre-existing header-offset index for that
  fileID:
  - `ExecuteSplitRedirectStub` (2b.3.2): overwrites `originalFileID`'s content
    with a redirect-stub, transitions `Status` to `StatusRedirect`.
  - `ExecuteSplitAtomic` (2b.3.6, the capstone/production path): does the same
    stub rewrite + catalog/B+Tree/graph updates as one WAL-covered atomic
    commit.
  Newly allocated fileIDs (`ExecuteSplitAllocateAndWrite`) never had a cache
  entry to begin with, so no invalidation is needed for them.

## Design decision: cache locking

`cs.stripes[stripeFor(fileID)]` already serializes Append's critical section
per fileID, but it is a `[256]sync.Mutex` array shared by potentially many
different fileIDs per stripe (hash collision). A header-offset cache keyed by
fileID, if guarded by `cs.stripes`, would let two different fileIDs landing in
different stripes mutate the SAME Go map concurrently without any shared
lock — a data race regardless of "different keys," since plain Go maps are
never safe for concurrent access. Chose a single dedicated
`cs.headerCacheMu sync.Mutex` + `cs.headerCache map[uint64][]HeaderOffset`
field instead: one mutex protecting the whole map (cheap operations —
map read/write/delete — so no need for per-fileID striping here), kept
strictly separate from `cs.stripes` so nesting `headerCacheMu` inside an
already-held `cs.stripes[stripe]` lock (as `Append` does) can never deadlock
(different lock instance, non-reentrant `sync.Mutex` concern from the
existing `cs.stripes`-vs-`cat.stripes` separation doc comment applies here
too).

`ReadPartial` additionally takes `cs.stripes[stripeFor(fileID)]` for its own
critical section (read current content + populate cache), so it is
serialized against `Append`'s read-modify-write for the same fileID — this
guarantees a strict happens-before/after ordering: if `Append` (or a split
commit) has already invalidated fileID's cache entry and returned, no
in-flight `ReadPartial` call that started before it can re-populate the cache
with stale data after the invalidation (they cannot overlap: both hold the
same per-fileID stripe lock).

## Design decision: invalidation call site for split

Added an exported `ContentStore.InvalidateHeaderCache(fileID uint64)` method
(self-locking on `headerCacheMu` only, NOT on `cs.stripes` — the split package
never holds any `cs.stripes` lock, so no deadlock risk, and using the
dedicated cache mutex keeps this call trivially safe to invoke from another
package). Called from within the WAL apply closures of both
`ExecuteSplitRedirectStub` and `ExecuteSplitAtomic`, immediately after
`cat.Put` succeeds — i.e. as part of the same durably-committed apply step
that flips the catalog record's Status/RedirectTargetIDs, matching "invalidated
atomically within the same...transaction" (the cache eviction only happens if
and when the transaction's WAL-covered apply closure actually runs, exactly
like the B+Tree/graph updates in `ExecuteSplitAtomic`).

`RecoverSplitCommits` (crash-recovery replay) does not receive a `*ContentStore`
parameter today and this subtask does not add one: the in-memory header cache
is necessarily empty in a freshly-started process before any `ReadPartial`
call has ever populated it, so there is no stale entry to evict on recovery —
recovery correctness is unaffected. Noted, not a gap.

## Scoping decision

Issue #13 has exactly one subtask (2b.4.1) — this run implements it in full
(cache + `ReadPartial` + invalidation wiring in both `Append` and the two
split commit paths), in one commit, matching the epic's one-subtask-per-commit
discipline (trivially satisfied here since there is only one subtask).
