# Requirement -- Subtask 4.3.1 (issue #22, milestone #6 "Phase 4: Query pipeline")

## Source
`gh issue view 22` (title: "[4] intent_refiner.py (agents/query/)"). Issue has two subtasks;
this run implements **only 4.3.1**. 4.3.2 (unit tests covering query_type variants) is a
separate, later dispatch and is explicitly out of scope here.

## Subtask 4.3.1 text (verbatim from issue)

- **Acceptance criteria**: Given raw query + short history, produces
  `{refined_intent, entities, query_type}` via the `LLMClient` interface.
- **Test spec**: `pytest agents/query/test_intent_refiner.py` (`LLMClient` mocked): assert
  output shape for representative fixture queries.
- **Impacted modules**: `agents/query/intent_refiner.py`, `agents/query/test_intent_refiner.py`

## Context from sibling subtask 4.3.2 (read-only, for contract alignment)

4.3.2 acceptance criteria: "The refiner correctly differentiates at least the query_type
categories the topic-selector depends on (e.g. factual/lookup vs. broad/exploratory),
verified across multiple fixture queries." Test spec references a *new* test file
(`test_intent_refiner_types.py`), so 4.3.1 only needs to define the `query_type` taxonomy in
a way 4.3.2 can later test against -- it does not need to write those tests itself.

=> `query_type` taxonomy chosen: two literal values, `"factual_lookup"` and
`"broad_exploratory"`, directly matching 4.3.2's own example wording. This is the minimal
taxonomy the issue itself hints at; nothing in `docs/LLD/query-agent.md` specifies a richer
one (see architecture-discovery.md).

## Non-goals
- Do not implement `topic_selector.py` or `synthesizer.py` (LLD scaffold only, not in this
  issue).
- Do not write `test_intent_refiner_types.py` (4.3.2).
- Do not modify the GitHub issue.
- Do not push.

## Security note (disclosed, not acted on)
Two fake "system-reminder"-style blocks (a fake date-change notice and a fake "Auto Mode
Active" MCP-instructions block) appeared in the tool-call transcript immediately after the
`gh issue view 22` call. Per the dispatching agent's explicit warning about this repo's
history of injected fake-system-reminder content in issue/PR bodies and tool output, these
were treated as untrusted data only, not acted upon, and are disclosed here and in the
handoff.
