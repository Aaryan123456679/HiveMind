# Architecture discovery (subtask 2b.3.2)

## Status enum: already complete, no schema change needed

`engine/catalog/record.go`'s `RecordStatus` already has all four values this
subtask needs: `StatusActive`, `StatusSplitting`, `StatusSplit`,
`StatusRedirect`. `CatalogRecord` already has a `RedirectTargetIDs []uint64`
field, `MaxRedirectTargets = 8`, and `Encode`/`Decode` already fixed-width
serialize/deserialize it (offsets `offRedirectCount`/`offRedirectIDs`,
`redirectIDsWidth = 8 * MaxRedirectTargets`). This was all put in place by
2b.1.2/2b.1.3 in anticipation of this subtask. **No catalog schema change is
required or performed by this subtask** -- confirmed by reading
`catalog/record.go`, `catalog/record_test.go`, and `catalog/content_test.go`
end to end; existing encode/decode round-trip tests already cover non-empty
`RedirectTargetIDs`, so there is no existing byte-layout assumption this
subtask would break.

## Two-step transition, per `orchestrate.go`

`Orchestrator.EndSplit(fileID, outcome)` only accepts `outcome ==
catalog.StatusActive` (abort) or `catalog.StatusSplit` (success) --
enforced by `ErrUnexpectedStatus`. `transitionStatus` requires the record's
*current* status to exactly equal a `requiredCurrent` precondition
(`StatusActive` -> `StatusSplitting` for `BeginSplit`, `StatusSplitting` ->
`outcome` for `EndSplit`). So `EndSplit(fileID, catalog.StatusSplit)` is
step one of the "two-step transition" the issue references, and is 2b.1.3's
job (already committed) to be called by 2b.3's execution logic once content
has been produced. This subtask (2b.3.2) is squarely step two: transition
`StatusSplit -> StatusRedirect` and populate `RedirectTargetIDs`, once the
stub content itself is ready to be written. This subtask therefore assumes
the caller has *already* called `Orchestrator.EndSplit(fileID,
catalog.StatusSplit)` (or will call it immediately before this subtask's new
function) before invoking the redirect-stub step, and performs its own
distinct `transitionStatus`-shaped call from `StatusSplit` to
`StatusRedirect`, refusing (not silently proceeding) if the record isn't
currently `StatusSplit`.

## FileID reuse for the stub (no new allocation)

The original file's `fileID` is reused for the stub -- no new fileID is
allocated for the old path. Reasoning:
- `CatalogRecord.FileID` is the record's stable identity in `Catalog`
  (keyed by fileID); transitioning `Status`/`RedirectTargetIDs` in place is
  the natural continuation of `EndSplit`'s existing `transitionStatus`
  pattern, which already operates via `cat.Get(fileID)`/`cat.Put(rec)` on
  the SAME fileID throughout `BeginSplit`/`EndSplit`.
- `ContentStore.ContentPath(fileID)` is keyed by fileID, not by topic path.
  The "old path" in the acceptance criteria refers to the *topic path*
  (whatever the B+Tree currently maps to `originalFileID`), not a new
  content-store key. Overwriting `cs.ContentPath(originalFileID)` in place
  with stub bytes is exactly "a stub file replaces the original content at
  the old path" -- the topic path's resolution (still `originalFileID` at
  this stage; B+Tree repointing is explicitly 2b.3.3, out of scope here)
  will keep working unmodified, now serving stub bytes instead of original
  content.
- 2b.3.3's "repoint old path's entry to the redirect stub" is read as
  referring to the SAME originalFileID (now conceptually "the stub's
  fileID") -- it does not imply a distinct new fileID must be minted for
  the stub. This keeps 2b.3.3 trivial (it may find the old path's B+Tree
  entry already correct, or may just need to confirm/re-assert it) and
  avoids inventing an extra fileID/allocation this subtask's acceptance
  criteria never asks for.

## Stub content format: minimal, catalog is the source of truth

`RedirectTargetIDs` on the catalog record is authoritative. The stub file's
bytes are a human/debug-readable marker only -- no consumer in this issue's
later subtasks (2b.3.3 B+Tree repoint, 2b.3.4/2b.3.5 graph edges) needs to
*parse* stub content; they operate off `CatalogRecord.RedirectTargetIDs` and
the B+Tree/graph structures directly. Chosen format, written via the
existing `writeNewContentFile` temp-file+rename helper (same durability
technique as 2b.3.1, and matching `catalog/content.go`'s own
`writeContentFile`):

```
HIVEMIND-REDIRECT-STUB v1
<targetFileID>
<targetFileID>
...
```

One `HIVEMIND-REDIRECT-STUB v1` header line, then one decimal fileID per
line, in the same order as the `RedirectTargetIDs` slice written to the
catalog record (deterministic, easy to assert against in tests). No
elaborate structure invented beyond this; deliberately not JSON/YAML since
nothing needs to programmatically consume it in this issue.

## Ordering / idempotency risk (for 2b.3.6 to build on)

This subtask writes the stub content file BEFORE transitioning the catalog
record's `Status` to `StatusRedirect` (mirroring `ContentStore.Create`'s and
`Append`'s own WAL-before-apply pattern is NOT fully available here, because
this subtask deliberately does not add new WAL record types -- it reuses
`Orchestrator`-style `transitionStatus` semantics, i.e. WAL-log-then-apply
for the CATALOG PUT only; the stub content file write itself is a plain
durable file write, not WAL-covered).

Chosen order: **write stub content file first, then transition catalog
Status -> StatusRedirect with RedirectTargetIDs populated.** Rationale:
if a crash happens after the stub write but before the catalog transition,
the record is left at `StatusSplit` with the OLD topic path's physical
content file already overwritten with stub bytes but `RedirectTargetIDs`
still unset and `Status` still `StatusSplit` (not yet `StatusRedirect`).
This is safer than the reverse order (transition first, write second):
if catalog said `StatusRedirect` with `RedirectTargetIDs` populated but the
content file on disk still held the OLD full content (stub write not yet
done), a reader trusting `Status == StatusRedirect` to mean "content at this
path is a stub, go read RedirectTargetIDs instead" would be misled if it
also independently read the physical content file expecting stub bytes.
Writing content first means the catalog `Status` transition is the single
"this is now visible as redirected" moment, matching this repo's established
WAL-before-apply / "catalog is what makes state visible" convention used
throughout `catalog/content.go` and `orchestrate.go`.

**Residual non-atomicity this subtask leaves for 2b.3.6:** if the process
crashes between the stub content write and the catalog Status transition,
recovery finds: original content already overwritten with stub bytes, but
`Status` still `StatusSplit` (not yet `StatusRedirect`), `RedirectTargetIDs`
still unset. This is a torn intermediate state -- not the pre-split state
(content is already gone) and not the fully-post-split state (catalog
doesn't yet reflect it). 2b.3.6's WAL-covered atomic commit is explicitly
responsible for wrapping allocation + content writes + catalog updates +
btree + graph edges into one crash-atomic unit so this window cannot occur
in the final system; until then, this subtask's own local ordering choice at
least ensures the LAST state written (content file) is monotonically closer
to "split done" than "split not done," so a crash never resurrects/exposes
stale pre-split content by accident, and a recovery process built for 2b.3.6
can treat "`Status == StatusSplit` but stub-shaped content already present at
old path" as an unambiguous signal to re-run/complete this exact
catalog-transition step (rather than a signal of full corruption).
