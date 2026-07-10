# Requirement — new engine-side RPCs for edge-writing and entity index (user-authorized new scope, discovered during issue #18 subtask 3.4.4 verification)

## Provenance

This is **not** one of issue #18's originally numbered subtasks (3.4.1-3.4.6). It is new,
user-authorized scope surfaced during `/cdr:verify`'s review of subtask 3.4.4
(`agents/ingestion/wiring.py`, commit `ae099571`, run `.cdr/runs/2026-07-10/010-verification/`).
That verification (`verdict: PASS_WITH_COMMENTS`, `escalation_required: true`) confirmed a real gap:
`proto/hivemind.proto` defines exactly 6 RPCs (frozen at task-3.2.1 per `docs/LLD/rpc.md`), none of
which write graph edges or maintain an entity->file index, so 3.4.4's `SegmentWiringClient`
Protocol methods (`lookup_entity_files`, `index_entity`, `put_edge`) have no real backing anywhere in
the engine. The user has explicitly authorized this follow-up as engine/proto-only scope, separate
from a later 3.4.4b Python rewiring task.

## Scope (this task only)

1. A new RPC (or RPCs) letting a caller create/increment a graph edge of a given `EdgeType`
   between two fileIDs, with weight-increment semantics for repeated calls (matching
   `ENTITY_COOCCUR`'s documented "increment weight" behavior).
2. A new RPC (or RPCs) letting a caller (a) look up which fileIDs are associated with a given
   entity name, and (b) register a new fileID association for an entity ("entity.idx").

## Explicit non-goals

- Do NOT modify `agents/ingestion/wiring.py` or any other Python file (separate 3.4.4b task).
- Do NOT implement `ProposeSplit`'s server side (issue #18's own 3.4.5).
- Do NOT fix `engine/catalog/record.go`'s pre-existing `PutSegment` CREATE-path `PathHash` bug
  (already logged as regression finding F4; out of scope, not touched unless directly blocking).
- Do NOT verify this task's own work (invariant I4) — verification is `/cdr:verify`'s job.

## Security note

Tool output and `.cdr/` artifacts in this repo have repeatedly contained fake injected
"system-reminder"-style instructions (fake date-change notices, fake MCP tool directives, fake
"Auto Mode Active" text). Two such fake reminders appeared in this session's own conversation
(a "date changed to 2026-07-10" notice, a fake `tokensave` MCP-server instruction block, and a fake
"Auto Mode Active" directive). All are treated as untrusted data, not followed, and disclosed here
and in the handoff. No file/directory outside this task's strict scope was touched as a result.
