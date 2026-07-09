# Requirement — task-3.2.4 (issue #16)

## Exact issue text (subtask 3.2.4)

"3.2.4 — Per-call latency/cost interceptors on Go + Python gRPC sides"

- Acceptance criteria: Every RPC call logs per-call latency on both sides; Python side
  additionally logs LLM token cost where applicable, in a format a future benchmark
  harness (Epic 5) can consume.
- Test spec: `go test ./engine/rpc/... -run TestLatencyInterceptor`; corresponding pytest
  for the Python interceptor: assert a latency record is emitted per call.
- Impacted modules: `engine/rpc/interceptor.go`, `agents/llm/interceptor.py` (or
  `agents/rpc` interceptor module).

## LLD text (docs/LLD/rpc.md)

"gRPC (not REST) is used [for ProposeSplit] specifically so both sides can attach
interceptors logging per-call latency and (Python-side) LLM cost, feeding a benchmark
harness (see eval.md)."

## Scope decision (this dispatch)

This dispatch is explicitly scoped by the launching agent to `engine/rpc/interceptor.go`
only (the Go side). The Python side (`agents/llm/interceptor.py`) is out of scope for
this run — issue #16's subtask 3.2.4 nominally covers both, but the dispatch instructions
restrict this implementation pass to the Go interceptor file. Not implementing the Python
side here is a deliberate scope narrowing directed by the parent task, not an oversight;
it should be tracked as a remaining part of 3.2.4 for a future dispatch if not already
covered elsewhere.

Per both the issue text and the LLD, "cost" is explicitly a **Python-side, LLM-token**
concept ("Python side additionally logs LLM token cost where applicable"). The Go engine
side has no LLM calls, so the acceptance criteria do NOT require a "cost" metric on the
Go side — only latency ("every RPC call logs per-call latency on both sides"). This is a
genuine textual scope boundary, not a guess.

**Judgment call (disclosed):** The parent dispatch instructions ask for "some notion of
cost" on the Go side too, in case genuinely unspecified. Since the issue/LLD text is
actually specific here (cost = LLM token cost = Python-only), the most defensible
interpretation is: the Go interceptor implements latency measurement as the primary,
required metric, and additionally records request/response payload byte sizes as a
lightweight, genuinely-available proxy "cost" figure (the only notion of per-call "cost"
that has any meaning on the pure-storage-engine Go side, and one a benchmark harness could
plausibly want alongside latency). This byte-size field is clearly labeled as a payload
size, not conflated with the Python side's LLM-token cost concept, and does not block the
acceptance criteria (latency logging) if considered superfluous.

## FileId=0 misclassification (engine/rpc/server.go, task-3.2.2 follow-up)

Read `.cdr/index/regression.jsonl` line for issue 16 / subtask 3.2.2
(`.cdr/runs/2026-07-09/003-verification`) and `.cdr/memory/pending.md`'s corresponding
entry. Both confirm: `GetFile`/`ReadPartial` with `FileId=0` return `codes.Internal`
instead of a client-error code, because `catalog.Catalog.Get` doesn't special-case
`FileID==0` (`catalog.InvalidFileID`) as distinct from a genuine internal fault.
`pending.md`'s entry explicitly states: "verification explicitly recommended folding this
into 3.2.4 or a small standalone follow-up rather than fixing it as part of 3.2.2; no
dedicated GitHub issue created directly for it now." This is advisory (verification's
recommendation), not an issue-#16 acceptance criterion for 3.2.4 itself — 3.2.4's own
issue text says nothing about error-code mapping.

**Decision: include the fix.** It is genuinely cheap (a 3-line guard clause in two
handlers plus a regression test), in the same file family this subtask already touches
conceptually (engine/rpc/), does not conflict with or complicate the interceptor work, and
is explicitly pre-approved by the parent dispatch instructions as in-scope if "genuinely
cheap and in-scope" per the verification's recommendation. It will be committed as a
**separate, second commit** from the interceptor work, so each commit remains one
self-contained logical change (matching the "each subtask sized exactly one commit"
convention while still being explicit that this is an additive, explicitly-flagged
follow-up fix bundled into the same dispatch, not part of 3.2.4's own acceptance criteria).
