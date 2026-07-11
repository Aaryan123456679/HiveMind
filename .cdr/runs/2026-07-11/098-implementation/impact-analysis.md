# Impact analysis -- subtask 4.5.9.1

## Files touched (this dispatch, doc-only)

- `docs/LLD/btree.md` -- add a "Known risks" bullet recording the `PrefixScan` literal-prefix
  limitation and a short pointer to the decision (full rationale lives in query-agent.md to
  avoid duplicating it across two docs).
- `docs/LLD/query-agent.md` -- add the full decision (option chosen, rationale, residual
  limitation, deferred-implementation pointer) under "Known risks".

## Files explicitly NOT touched (out of scope for 4.5.9.1)

- `engine/rpc/search_candidates.go`, `engine/rpc/search_candidates_test.go`,
  `engine/btree/scan.go` -- these are 4.5.9.2's impacted modules (actual code change), deferred.
- `docs/LLD/rpc.md` -- not named by the issue as a target doc; `SearchCandidates`'s one-line
  description there remains accurate as-is and is cross-referenced by both edited docs.
- `agents/query/topic_selector.py`, `agents/query/wiring.py`, `api/main.go` -- issue #56's
  concurrent scope; confirmed no file overlap.

## Downstream/consumer impact

- No behavior change: this dispatch is documentation only, `git diff --stat` should show only
  the two `docs/LLD/*.md` files.
- Consumers of these docs (future 4.5.9.2 implementer, future #56 wiring implementer, future
  verification agent) now have a recorded, justified decision to implement/verify against,
  instead of an open three-way choice.
- Regression/pending trackers (`.cdr/index/regression.jsonl`, `.cdr/memory/pending.md`) are not
  edited by this subtask -- their existing entries remain accurate historical record of when/how
  the limitation was first surfaced; this subtask's docs cross-reference them rather than
  duplicating or superseding them.

## Risk of this change

- Very low: additive documentation only, no executable code paths affected, no test surface
  changed. Main risk is the decision itself being wrong/unjustified -- mitigated by grounding it
  in a direct reading of `topic_selector.py` and `search_candidates.go` (see
  architecture-discovery.md) rather than assumption.
