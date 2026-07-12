# Plan — docs/WRITEUP.md outline

1. Title + one-paragraph framing (what HiveMind is, what this doc covers).
2. Section: System architecture
   - High-level diagram/flow (React UI -> Go API gateway -> gRPC -> Go storage engine;
     -> gRPC -> Python agent service -> LLM providers).
   - Go storage engine module table (catalog/btree/mvcc/split/graph/wal/rpc/loadtest) and
     what each is responsible for.
   - Python agent layer (ingestion/query/llm/eval) and what each is responsible for.
   - The core storage-model distinction: MVCC + auto-splitting graph-linked `.md` file
     store vs. vector-chunk indexing.
3. Section: Novelty vs. prior art (GraphRAG / RAPTOR / MemGPT)
   - One paragraph/bullet per system, taken directly from HLD.md section 6 (no invented
     claims).
   - HiveMind's stated contribution (storage-engine-enforced topical coherence + MVCC/
     auto-split concurrency correctness + agentic graph-aware retrieval, evaluated against
     corpus-growth degradation).
4. Section: Benchmark results
   - Run description (run-001: 32-doc corpus, 3 checkpoints x 3 arms x 32 queries,
     OpenRouter gpt-4o-mini, real judge scoring, $0.1677 actual spend).
   - Results table (recall/precision/judge/cost per 1k) reproduced from
     results/run-001/live_benchmark_results.json (cross-checked against README's table).
   - Honest interpretation: vector_rag leads recall/judge at all 3 checkpoints on this small
     corpus; hivemind's precision improves fastest with corpus growth (0.19->0.38->0.72);
     cost/1k queries flat/comparable across arms; single-run/small-corpus caveat.
5. Section: Lessons learned / known limitations
   - fsync-bound WAL append scaling insight from `engine/loadtest/split_race_scale_test.go`
     (wall clock dominated by total append COUNT, not concurrency/worker count).
   - Small-corpus/single-run benchmark caveat (already established in section 4, restated
     as a lesson about eval design).
   - deploy/oci/ is scripted (provision.sh) but never executed against a live OCI account —
     unverified end-to-end.
   - Catalog free-list/bitmap page-count ceiling (~32.7k pages / ~128MB catalog.dat), a known
     accepted limitation pending a future extensible free-list design.
   - Query-agent prefix-only candidate search residual gap (queries whose terms are all
     stopwords not present as path-segment prefixes return an empty candidate pool).
6. Footer: links back to docs/HLD.md, docs/LLD/, README.md benchmark/load-testing sections,
   results/run-001/ as sources of truth for anything summarized here.
