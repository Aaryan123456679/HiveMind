# Architecture discovery

## Token-order followed
1. `.cdr/index/task.jsonl` (task-3.2.2, task-3.2.3, task-3.2.5, engine-edge-entity-rpc
   entries) -- established ProposeSplit server-side is Python-scope, issue #18, and the
   Go integration test's `codes.Unimplemented` assertion is permanent/intentional.
2. `.cdr/memory/pending.md` / `.cdr/index/regression.jsonl` -- found open finding F1
   (segment.py rejects markdown-fenced JSON) to defensively avoid reproducing.
3. `docs/LLD/rpc.md`, `docs/LLD/ingestion-agent.md`, `docs/LLD/split.md` -- confirmed
   Python-side ProposeSplit semantics and byte-offset SectionRange contract.
4. `proto/hivemind.proto` (text) + generated `agents/hivemind_pb2.pyi` (exact field
   names/types) -- `ProposeSplitRequest{file_content: bytes}`,
   `ProposeSplitResponse{files: [SplitFileProposal], redirect_summary: str}`,
   `SplitFileProposal{new_path: str, section_ranges: [SectionRange]}`,
   `SectionRange{start: int64, end: int64}` (half-open byte offsets).
5. Touched-adjacent source: `engine/rpc/server.go` (doc comment only, confirmed
   Unimplemented is intentional/permanent), `engine/rpc/integration_test.go` (confirmed
   `ProposeSplit_Unimplemented` subtest exercises the Go server only, by design).
6. Ingredient modules: `agents/llm/client.py` (`LLMClient.complete(prompt, ...) -> str`,
   already names `propose_split.py` as a known future consumer in its own docstring),
   `agents/ingestion/segment.py` (sibling module's JSON-prompt/parse/dataclass/exception
   pattern, reused for consistency), `agents/ingestion/shortlist.py` (lazy-grpc-import
   convention for any future real gRPC wrapper -- not needed here since 3.4.5 has no RPC
   client responsibility, only the callee-side business logic).

## Key finding: ProposeSplit's server-side lives in Python, not Go

See `requirement.md`'s "Architectural correction" section for full evidence chain.
Summary: `engine/rpc/server.go` intentionally embeds
`hivemindv1.UnimplementedHiveMindServer` and does NOT implement `ProposeSplit` -- by
design, forever -- because the Go engine is the gRPC *client* for this one RPC
(`engine/split/proposer_grpc.go`, already implemented+verified in task-3.2.3) while the
Python `agents/ingestion/` package is the real callee. This subtask therefore only adds
Python-side business logic; `engine/rpc/server.go` and
`engine/rpc/integration_test.go` are correctly left untouched.

## Design: deterministic partition guarantee, LLM only for placement/paths

`docs/LLD/ingestion-agent.md`'s "Segmentation agent" section documents the same
"LLM topic-boundary nondeterminism" risk that applies to `segment.py` (3.4.3) also
applying here (`ProposeSplit` cross-referenced under the same "Known risks" entry). The
issue's test spec requires the returned `SectionRange`s to partition the input content
(no gaps, no overlaps) -- an invariant that must hold even if the LLM's output is
imperfect. Rather than trusting LLM-reported byte offsets directly (which LLMs cannot
reliably compute), the LLM is asked only for the *ordered list of split-point marker
strings* (verbatim substrings of the document, one per section boundary after the
first) plus each section's target topic path and a redirect summary. This module then
locates each marker deterministically via `str.find` (monotonically, from the previous
boundary forward) and constructs `SectionRange`s by construction:
`start_0 = 0`, `end_last = len(content)`, `end_i = start_{i+1}` for all others -- so the
partition invariant holds by construction regardless of what the LLM actually said,
and any marker that cannot be located (or would produce a zero/negative-length or
out-of-order section) is a hard `ProposeSplitParseError`, not a silently-wrong offset.

This mirrors `segment.py`'s existing "the code enforces structural correctness, the LLM
only supplies content/judgment" split of responsibility.

## Disclosed simplification vs. the proto's full generality

`SplitFileProposal.section_ranges` is `repeated` in the proto, technically allowing one
output file to be assembled from multiple non-contiguous ranges of the original content.
This implementation always produces exactly one `SectionRange` per proposed file
(contiguous chunks only) -- the simplest reading consistent with "topic-coherent
split" and sufficient to satisfy the acceptance test (a global partition, order
preserved). Scattered/interleaved multi-range files are not attempted; flagged forward
in `handoff.json` as a disclosed scope reduction, not a bug.
