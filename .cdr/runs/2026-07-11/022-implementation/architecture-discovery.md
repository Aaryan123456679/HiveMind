# Architecture discovery — 4.4.4

## Real signatures (read directly from `agents/query/topic_selector.py`, not guessed)

```python
DEFAULT_K = 3
DEFAULT_INSUFFICIENCY_RATIO = 0.5
DEFAULT_EXPANSION_HOPS = 2

@dataclass(frozen=True)
class TopicCandidate:
    file_id: int
    path: str
    score: float

@dataclass(frozen=True)
class GraphNeighbor:
    file_id: int
    edge_type: str
    weight: int
    hop: int

GraphNeighborsFn = Callable[[int, int], Sequence[GraphNeighbor]]

@dataclass(frozen=True)
class ExpansionResult:
    topic: TopicCandidate
    neighbors: list[GraphNeighbor]

def select_top_k(candidates: Sequence[TopicCandidate], *, k: int = DEFAULT_K) -> list[TopicCandidate]
def is_insufficient_alone(topic: TopicCandidate, top_score: float, *, ratio: float = DEFAULT_INSUFFICIENCY_RATIO) -> bool
def expand_insufficient_topics(selected: Sequence[TopicCandidate], graph_neighbors: GraphNeighborsFn, *, hops: int = DEFAULT_EXPANSION_HOPS, ratio: float = DEFAULT_INSUFFICIENCY_RATIO) -> list[ExpansionResult]
def combine_and_cap(selected: Sequence[TopicCandidate], expansions: Sequence[ExpansionResult], *, k: int = DEFAULT_K) -> list[int]
```

Key behavioral facts confirmed by reading the source (not the docstring alone):

- `expand_insufficient_topics` computes `top_score = max(t.score for t in selected)`
  internally — callers do not pass `top_score`; it is derived from `selected` each call.
  Insufficiency is *relative to the current selection's own top score*, not absolute.
- A topic is flagged insufficient iff `topic.score < ratio * top_score` (strict `<`).
  With default `ratio=0.5`, a topic scoring below half of the selection's top score
  triggers expansion. The top topic itself (`score == top_score`) is never flagged for
  `ratio <= 1`.
- `graph_neighbors(file_id, hops)` is called once per flagged topic, in `selected`'s
  order, only for flagged topics — confirmed by `expand_insufficient_topics`'s loop
  (`for topic in selected: if is_insufficient_alone(...): neighbors = graph_neighbors(...)`).
- `combine_and_cap` walks `selected` first (dedup by `file_id`, first-seen wins), then
  each `ExpansionResult.neighbors` in order, then truncates to `k + 2*k`.
- `combine_and_cap` takes `expansions: Sequence[ExpansionResult]` — the *output* of
  `expand_insufficient_topics`, not a raw neighbor list — so the pipeline composition is
  exactly `select_top_k(...)` -> `expand_insufficient_topics(selected, mock_graph_neighbors)`
  -> `combine_and_cap(selected, expansions, k=k)`.

## Existing test file conventions (read all three in full)

- `test_topic_selector.py`: module docstring cites issue #23/subtask + test spec verbatim;
  fixtures are module-level constants with a comment explaining *why* they're shaped that
  way (deliberately unsorted, etc.); `from __future__ import annotations`; plain `assert`,
  `pytest.mark.parametrize` for boundary sweeps.
- `test_topic_selector_expansion.py`: defines a local `_RecordingGraphNeighbors` callable
  class implementing `GraphNeighborsFn` that records `(file_id, hops)` call tuples and
  returns a canned per-file_id neighbor list from a dict — this is the established mock
  shape for `GraphNeighborsFn` in this package; reused verbatim rather than reinvented.
- `test_topic_selector_cap.py`: small `_topic()`/`_neighbor()` helper factories with
  sensible defaults (`score=1.0`, `edge_type="references"`, `hop=1`); each test docstring
  section separated by `# ---` banner comments describing the behavior class under test;
  cites the specific `.cdr/runs/.../plan.md` that disclosed the dedup/priority behavior
  it covers, in addition to the issue's test spec.
- All three use `from query.topic_selector import ...` (package-relative import, since
  `agents/` is pytest's rootdir per `pyproject.toml`'s `testpaths = ["."]`, and `query` is
  a listed package in `[tool.setuptools] packages`).

## Reused mock

Following `test_topic_selector_expansion.py`'s established `_RecordingGraphNeighbors`
shape exactly (same field names/behavior) so the integration test's mock boundary matches
the same convention already verified for 4.4.2, rather than introducing a second,
divergent mock shape for the same `GraphNeighborsFn` contract in the same package.

## No LLD/topic_selector.py changes

`docs/LLD/query-agent.md`'s `topic_selector.py` section was already the basis for
4.4.1–4.4.3's implementations (per their own module-docstring citations read above); this
subtask adds no new behavior, so no LLD delta is needed. Confirmed `topic_selector.py` is
not touched by this dispatch (test-only, per explicit instruction).
