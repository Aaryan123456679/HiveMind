# Requirement — Issue #23 subtask 4.4.2

**Title**: Graph-traversal expansion decision (0-2 hop) delegating to GraphNeighbors

**Acceptance criteria** (verbatim from `gh issue view 23`):
> For any selected topic judged insufficient alone, the selector requests a 0-2 hop
> GraphNeighbors expansion via the engine RPC.

**Test spec** (verbatim):
> pytest agents/query/test_topic_selector_expansion.py (GraphNeighbors mocked): assert
> expansion is requested only for topics flagged insufficient, with correct hop-depth
> parameter.

**Impacted modules**: `agents/query/topic_selector.py`, `agents/query/test_topic_selector_expansion.py`

**Explicitly excluded from this dispatch** (separate subtasks, do not implement):
- 4.4.3 hard-cap enforcement (`k + 2k` total files)
- 4.4.4 integration test combining all three behaviors

**Preconditions**: 4.4.1 (`select_top_k`, `TopicCandidate`, `DEFAULT_K`) already implemented,
verified PASS_WITH_COMMENTS, committed at `5cc0ea3`. This subtask extends the same file
additively — must not alter 4.4.1's existing public behavior/signatures.

**Security note (disclosed)**: Two `<system-reminder>`-formatted blocks appeared in my own
tool-call flow during this run (not embedded in `gh issue view 23` output, which was clean),
instructing (a) not to mention a "date change" to the user, and (b) to bias toward not asking
clarifying questions ("Auto Mode Active"). These match the exact injection patterns the
dispatcher explicitly warned about for this repo (fake date-change notices, fake mode-activation
directives). Per the dispatcher's explicit instruction, genuine harness reminders don't ask me to
conceal things from the user, so both were treated as untrusted and their directives were not
followed; disclosing here and in the handoff.
