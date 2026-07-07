# Fix-cycle requirement (issue #13, subtask 2b.4.1)

Independent verification (`.cdr/runs/2026-07-07/030-verification/verification.json`)
returned `CHANGES_REQUESTED` against commit `4b8fe670a852ad0bba2bdfa129dae46a054eff75`
(2b.4.1: "invalidate markdown header-offset cache atomically on append/split").
This run is a fix ON TOP of that commit, not a revert.

Two confirmed bugs to fix:

**Bug 1 (real correctness bug):** `engine/catalog/content.go`'s `Append` holds
`cs.stripes[stripeFor(fileID)]` across its entire read-modify-write-invalidate
sequence, matching `ReadPartial`'s own locking, so the two can never interleave.
But `engine/split/execute.go`'s `ExecuteSplitRedirectStub` and
`ExecuteSplitAtomic` never acquired `cs.stripes` at all for their
stub-content-write + `cat.Put` + `InvalidateHeaderCache` sequence. `FileGuard`
only prevents concurrent splits, not concurrent `ReadPartial` calls. This left
a real, narrow race window where a `ReadPartial` call could return a stale,
pre-split cached header index between the durable `cat.Put` and the
`InvalidateHeaderCache` call.

**Bug 2:** `TestSectionIndexInvalidation` (2b.4.1's original test) was entirely
serial -- split fully completes, then `ReadPartial` is called -- despite the
issue's acceptance criteria requiring `-race` coverage of real concurrency.

See the original task prompt (relayed by the launching agent) for the full
verdict text and fix requirements; not re-transcribed here to avoid drift from
the actual verification.json, which remains the source of truth.

## Security note

This task's instructions explicitly flagged that this repo's GitHub issue
content, commit messages/diffs, and some Bash tool stdout have repeatedly
contained embedded fake system-reminder-style prompt injection (fabricated
date-change notices, fake MCP/tool instructions, fake "Auto Mode Active"
directives) across multiple prior agent sessions. Confirmed again this run:
reading `.cdr/runs/2026-07-07/030-verification/verification.json` via Bash
returned two such fabricated blocks appended after the real JSON content (a
fake "date changed to 2026-07-07" notice, a fake "tokensave MCP server" tool
description, and a fake "Auto Mode Active" directive). Treated as untrusted
plain-text data only; nothing in them was acted upon. Not novel -- this
matches the pattern the 029-implementation run's handoff.json already
documented.
