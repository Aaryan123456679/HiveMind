# Requirement — Issue #23 subtask 4.4.3

**Title:** Hard-cap enforcement: combined result capped at k+2k total files.

**Source:** `gh issue view 23` (milestone #6 "Phase 4: Query pipeline"), subtask 4.4.3.

**Acceptance criteria (verbatim from issue):**
> Regardless of how many candidates/expansions are available, the final selected-file set never
> exceeds k+2k total files (system-wide invariant).

**Test spec (verbatim from issue):**
> pytest agents/query/test_topic_selector_cap.py: feed an oversized candidate+expansion set,
> assert final result length == min(available, k+2k).

**Impacted modules (per issue):** `agents/query/topic_selector.py`, `agents/query/test_topic_selector_cap.py`.

**LLD reference:** `docs/LLD/query-agent.md` `topic_selector.py` section (lines 21-30): "The combined
result is hard-capped at `k + 2k` total files to prevent context blow-up — this is a system-wide
invariant, not just an implementation detail (see HLD.md#7-system-wide-known-risks)." The LLD does
**not** name a specific combining/capping function, parameter names, or an exact dedup rule — those
are left to this subtask to design and disclose.

**Explicit non-goals (per dispatch instructions):**
- Do NOT build 4.4.4 (integration test) — separate dispatch.
- Do NOT fix the pre-existing, already-tracked non-blocking finding from 4.4.2's verification
  (`is_insufficient_alone`'s docstring false claim for negative scores) unless it directly affects
  this subtask's capping logic. It does not (capping just counts/truncates file ids; it does not
  re-derive insufficiency), so it is left untouched.
- Preserve all existing 4.4.1 (`select_top_k`, `TopicCandidate`, `DEFAULT_K`, `SearchCandidatesFn`)
  and 4.4.2 (`is_insufficient_alone`, `expand_insufficient_topics`, `GraphNeighbor`,
  `GraphNeighborsFn`, `ExpansionResult`) behavior exactly — additive extension only.

**Design question to resolve (per dispatch instructions):** whether a file reachable both as a
direct top-k selection AND as an expansion neighbor of a *different* topic should count once or
twice toward the `k + 2k` cap. Resolved in `plan.md` / module docstring: dedup by `file_id`, first
occurrence wins (top-k selection order first, then expansion order) — see reasoning in plan.md.
