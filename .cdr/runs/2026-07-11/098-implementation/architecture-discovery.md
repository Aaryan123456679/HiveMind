# Architecture discovery -- subtask 4.5.9.1

## Read directly (per token order: index -> memory/handoffs -> LLD -> touched files -> source)

- `.cdr/memory/pending.md` top entry -- confirms the design_limitation, recommends exactly the
  three options (a)/(b)/(c) this subtask must choose among, and explicitly names this as the
  moment to decide ("address in context when #22/#23 are built").
- `.cdr/index/regression.jsonl` line for subtask 4.2.1/issue #21/commit `b8ebc64` -- same
  finding, `severity: non_blocking`, `recommended_action: "Track as follow-up issue before
  query-time topic-selector ... is built on top of SearchCandidates."`
- `docs/LLD/btree.md` -- scaffold-only doc; `PrefixScan` documented only as "list a topic
  subtree" operation; "Known risks" section previously said "None unique to this module".
- `docs/LLD/query-agent.md` -- documents `topic_selector.py`'s intended shape (receives a
  candidate list from `SearchCandidates`, selects top-k, may request `GraphNeighbors`
  expansion, `k + 2k` hard cap); "Known risks" section previously only listed graph-traversal
  context blow-up.
- `docs/LLD/rpc.md` -- documents `SearchCandidates` as "non-LLM candidate topic search consumed
  by Python query-agent's topic-selector"; not edited by this subtask (out of the two docs the
  issue names), but confirms the RPC boundary.
- `agents/query/topic_selector.py` (full file, read directly) -- **actual current calling
  pattern**: `select_top_k()` takes an already-decoded `Sequence[TopicCandidate]` directly; it
  does **not** call `SearchCandidates` itself. `SearchCandidatesFn` is declared as a documented,
  unused type alias for a future injection point ("Not called anywhere in this module in this
  dispatch"). The module's own docstring discloses that `agents/query/` has no gRPC client
  wiring yet (no `wiring.py` analogue). `expand_insufficient_topics`/`combine_and_cap` (4.4.2/
  4.4.3) build on `select_top_k`'s output but likewise never call `SearchCandidates` or
  `PrefixScan` directly.
- `engine/rpc/search_candidates.go` (full file, read directly) -- confirms `prefixTerm(query)`
  takes only `fields[0]` (first whitespace token) and that is the sole string passed to
  `btree.PrefixScan`; `rankCandidates`/`termOverlapScore` already tokenize the **full** query
  into a term set and score every candidate in the pool against that full set, independent of
  how the pool was assembled. This means the ranking layer already generalizes to a
  multi-term pool with zero changes -- only the pool-assembly step (`prefixTerm` + the single
  `PrefixScan` call in `server.go`'s handler) is prefix-limited.
- Issue #56 (`gh issue view 56`) -- confirmed concurrent, different files: real gRPC/HTTP
  wiring for `/query` (new `agents/query/wiring.py`, `api/main.go`, proto extension for the
  Go<->Python leg). No file overlap with this subtask's edits (`docs/LLD/btree.md`,
  `docs/LLD/query-agent.md`). Directly relevant to the *content* of this decision though: #56's
  scope explicitly includes wiring a real `search_candidates` callable that will hand
  `topic_selector.py`/`pipeline.py` a live `SearchCandidatesResponse` for genuine user queries
  -- i.e. once #56 lands, the multi-word-query gap stops being theoretical and starts being hit
  by real production queries. This makes "accept the limitation" (option c) materially less
  attractive than it was when 4.2.1/#21 first surfaced it, and argues for recording the (b)
  decision now, ahead of or alongside #56's wiring landing.

## Conclusion driving the decision

Because `topic_selector.py` has no real caller wiring yet (confirmed, not assumed), there is no
currently-shipping code path that is broken today -- this remains exactly the "surfaced during
verification, non-blocking, decide before the real caller lands" situation the original
regression entry anticipated. Issue #56's in-flight wiring work is the reason to decide now
rather than defer further.
