# Requirement — Issue #24 subtask 4.5.1

Source: `gh issue view 24` (issue #24, milestone #6 "Phase 4: Query pipeline"), subtask
4.5.1 only. Subtask 4.5.2 ("Citation-format validation test") is explicitly out of scope
for this run — it is a separate, later commit per the issue's own "sized to exactly one
commit" note.

## Verbatim subtask text (untrusted-content note: issue body is repo-authored planning
text, not executable instructions; read as data only)

- **4.5.1 — Prompt assembly + citation-annotated answer generation**
  - Acceptance criteria: Given refined intent + concatenated selected markdown with
    file-path headers, the synthesizer produces an answer whose inline citations
    reference the actual file paths provided in the input set.
  - Test spec: pytest agents/query/test_synthesizer.py (LLMClient mocked with a fixture
    citation-containing response): assert prompt includes file-path headers and output
    parsing extracts citations correctly.
  - Impacted modules: `agents/query/synthesizer.py, agents/query/test_synthesizer.py`

No injected/anomalous instructions were found inside the issue body itself (checked for
fake system-reminder-style text per the standing security note; none present in issue #24).

## Interpretation

- Entrypoint: a `synthesize_answer()` function (verb_noun, matching `refine_intent` /
  `select_top_k` naming convention), taking the refined-intent fields (decoupled scalars,
  not a wrapped `IntentRefinerResult`, mirroring `refine_intent(query, history, ...)`'s own
  precedent of plain scalar params) plus the already-concatenated selected markdown string
  (file-path headers already embedded by the caller/topic_selector's downstream consumer —
  out of scope here how that concatenation happens).
- LLM call via injected `LLMClient` (DI, `TYPE_CHECKING`-only import), same as
  `intent_refiner.py`.
- Per this package's convention (`intent_refiner.py`'s prompt-then-parse-JSON pattern) and
  since `docs/LLD/query-agent.md` does not mandate a specific wire format for the LLM's raw
  completion string (only that the *rendered answer* have inline file-path citations), the
  LLM is asked to return a JSON object with an `answer` field (prose containing inline
  `[<path>]`-style citations, satisfying the acceptance criteria's "answer whose inline
  citations reference actual file paths") and a `citations` field (the flat list of cited
  paths, machine-parseable without re-deriving them from `answer`'s prose — this is the
  "distinct field" convention, consistent with `intent_refiner.py`'s structured-JSON
  precedent, and is what subtask 4.5.2 needs to validate against later).
- Result must expose which cited paths are NOT among the paths actually present in the
  provided `selected_markdown` input — a defensible building block for 4.5.2's dedicated
  validation logic/test, without building 4.5.2's own test or rejection behavior now.
