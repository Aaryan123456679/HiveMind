# Plan — task-2b.1.1

## Signal shape design (and reasoning)

Chose a **pure function + small value-type struct**, not a callback/channel:

```go
// Signal describes a single split-eligibility signal: the specific append that
// caused fileID's content size to cross the configured threshold.
type Signal struct {
    FileID          uint64
    OldSizeBytes    uint64
    NewSizeBytes    uint64
    ThresholdBytes  uint64
}

// Trigger holds one configured (tunable) threshold and evaluates append
// before/after sizes against it.
type Trigger struct {
    thresholdBytes uint64
}

func NewTrigger(thresholdBytes uint64) (*Trigger, error)
func DefaultTrigger() *Trigger  // thresholdBytes = DefaultThresholdBytes (8*1024)
func (t *Trigger) ThresholdBytes() uint64
func (t *Trigger) Detect(fileID, oldSizeBytes, newSizeBytes uint64) (Signal, bool)

// CrossesThreshold is the underlying pure boolean predicate, exported standalone
// so callers/tests can reason about the crossing condition without constructing
// a Trigger or a fileID.
func CrossesThreshold(oldSizeBytes, newSizeBytes, thresholdBytes uint64) bool
```

**Why this shape over alternatives:**
- *Callback/event-bus*: rejected — would force this pure-logic package to own a
  registration/dispatch mechanism it doesn't need yet (no consumer exists this subtask); the
  existing call site this will eventually integrate with (`ContentStore.Append`) already has a
  simple `(bool, error)` return-value calling convention (see task-1.4.3's inline stub) — a
  return-value-based hook is the most natural fit and the lowest-risk to wire in later.
- *Channel*: rejected — would introduce concurrency machinery (buffering, blocking sends,
  goroutine lifecycle) into a package whose correctness bar is already the highest in the repo;
  unnecessary complexity for what is fundamentally a synchronous, in-line decision made inside
  an existing critical section (the caller already holds a per-fileID stripe lock when it would
  call this).
- *Bare bool return*: `CrossesThreshold` bool-only predicate is included for the simplest case,
  but a richer `Signal` struct is also provided because a real split-initiation consumer
  (2b.1.2's CAS guard) will want `FileID` (which file), and having `OldSizeBytes`/`NewSizeBytes`/
  `ThresholdBytes` on the signal itself gives log/debug/test observability "for free" without
  the consumer needing to separately thread that context through.
- **Stateless, no internal per-file bookkeeping**: `Trigger` does NOT track per-fileID state
  (e.g. "have I already signaled for this file"). The exactly-once-per-crossing property is
  achieved structurally, not via memory: `Detect` is given both `oldSizeBytes` (before this
  specific append) and `newSizeBytes` (after this specific append) on every call, and only
  returns `true` when the crossing condition holds strictly between those two specific values.
  This mirrors task-1.4.3's existing catalog-side stub exactly and avoids introducing a *second*
  place a "have I signaled" flag could drift out of sync with the source of truth
  (`CatalogRecord.SizeBytes`, owned by `engine/catalog`) — a real risk given AGENT.md's framing
  of this whole epic. Stateless-by-construction is deliberately the simplest design that cannot
  duplicate-signal or miss-signal as long as the caller passes true before/after sizes for each
  individual append (which `ContentStore.Append` already does/will do).

## Boundary semantics (matches existing engine/catalog/content.go precedent exactly)

```
crossed := oldSizeBytes <= thresholdBytes && newSizeBytes > thresholdBytes
```

- Landing exactly ON the threshold (`newSizeBytes == thresholdBytes`) does NOT count as crossed
  — must be *strictly* over.
- A file already over threshold before this append (`oldSizeBytes > thresholdBytes`) never
  re-signals, regardless of `newSizeBytes`, satisfying "already over -> no re-signal" and
  "starts already over at hook install time -> no retroactive signal".
- Defensive guard: if `newSizeBytes < oldSizeBytes` (not a valid append-growth observation),
  `Detect`/`CrossesThreshold` return `false` — an append can only grow size, so a caller passing
  a shrinking pair is either misusing the hook or describing a non-append mutation; the safe
  default is "no signal" rather than guessing.
- Zero-byte append (`oldSizeBytes == newSizeBytes`): can never satisfy
  `old <= T < new` when `old == new`, so it structurally never signals, regardless of where the
  (equal) sizes sit relative to the threshold. No special-case code needed; falls out of the
  formula.

## Threshold validation

`NewTrigger(thresholdBytes uint64) (*Trigger, error)` rejects `thresholdBytes == 0` with an
error (`"split: NewTrigger: thresholdBytes must be positive, got 0"`), matching this repo's
error-return convention (e.g. `catalog.OpenContentStore`'s nil-arg checks) rather than a panic.
Since `thresholdBytes` is `uint64`, negative values are not representable at the type level —
documented explicitly in the doc comment so a future caller doesn't wonder why there's no
negative-value test. `DefaultTrigger()` uses `DefaultThresholdBytes = 8 * 1024` (matches
`docs/LLD/split.md`'s "~8KB / ~2000 tokens" and `engine/catalog/content.go`'s
`defaultSplitThresholdBytes`), constructed via `NewTrigger` internally and `must`-panics only on
an internal invariant violation (the default itself is always valid, so this path is
unreachable in practice, not a user-facing panic).

## Test plan (`TestThresholdDetection`, table-driven, plus focused sub-tests)

1. **Stays under**: e.g. threshold 100, old=10, new=50 -> no signal.
2. **Exactly reaches boundary**: old=90, new=100 (== threshold) -> no signal (not strictly over).
3. **Crosses boundary by 1**: old=99, new=101 -> signal, `Signal.FileID`/sizes/threshold
   populated correctly.
4. **Already over, appends again**: old=150, new=200 (both > threshold) -> no signal.
5. **Cumulative small appends crossing on a specific one**: simulate a sequence of appends
   (e.g. sizes 0->30->60->90->110->140 against threshold 100) by calling `Detect` once per
   step with that step's (old,new) pair; assert signal fires on exactly the 90->110 step and no
   other step in the sequence.
6. **Zero-byte append**: old=new=50 (under) and old=new=150 (over) -> no signal either case.
7. **Threshold validation**: `NewTrigger(0)` returns non-nil error and nil Trigger;
   `NewTrigger(1)` succeeds.
8. **Defensive shrink guard**: old=200, new=50 (invalid "shrinking append") -> no signal.
9. **DefaultTrigger** sanity: `DefaultTrigger().ThresholdBytes() == DefaultThresholdBytes`, and
   an old/new pair that crosses 8*1024 fires via the default instance too.

Every acceptance-criteria scenario from the subtask brief ((a)-(d) in the task description) maps
onto matrix rows above: (a)=row1, (b)=rows2-3, (c)=row4, (d)=row5.

## Non-goals for this subtask (explicitly deferred, recorded so nothing is silently lost)
- Wiring `Trigger` into `engine/catalog/content.go`'s `ContentStore.Append`.
- The CAS guard consuming `Signal` (2b.1.2, `engine/split/guard.go`).
- Any catalog status transition (2b.1.3, `engine/split/orchestrate.go`).
