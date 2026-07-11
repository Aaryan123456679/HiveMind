# Architecture discovery — task-5.1.2

## Docs read
- `docs/HLD.md` — `eval/` = "Benchmark harness against vector-RAG and GraphRAG baselines"
  (line ~74); `agents/` layout includes `eval/` (line ~81).
- `docs/LLD/eval.md` (`Status: scaffold only`) — "Dataset loaders" section explicitly lists
  "Synthetic policy/manual PDFs, seeded with ~30-50 predefined ground-truth topics and
  deliberate cross-topic references" and "Ground-truth topic/query labels attached for
  dataset recall/precision measurement." This is the authoritative spec for this subtask.
- `.cdr/memory/pending.md` lines 3 (Ollama-only standing preference), 5 (do-not-autonomously-
  run-benchmarks — does not apply here, 5.1.2 is generation, not a benchmark run), 145-149
  (task-5.1.1 precedent: `agents/eval/datasets.py` common loader interface, issue #26 status).

## agents/llm client reuse (per dispatch instruction)
- `agents/llm/client.py` — `LLMClient` ABC, single `complete(prompt, *, model=None,
  temperature=0.0, max_tokens=None, timeout=None) -> str` method. No chat/streaming.
- `agents/llm/factory.py` — `create_llm_client(provider=None, **client_kwargs)`. Resolves
  `provider` explicit-arg first, else `LLM_PROVIDER` env var, else raises `LLMFactoryError`.
  `PROVIDER_OLLAMA = "ollama"` constant.
- Real call sites for the pattern to mirror:
  - `agents/ingestion/segment.py` `segment()`: `if llm_client is None: llm_client =
    create_llm_client()` — but that call relies on `LLM_PROVIDER` env var / no-arg default,
    which is NOT safe for this subtask's hard Ollama-only constraint (a future `.env` could
    set `LLM_PROVIDER=openrouter` for unrelated benchmark work, and this script must never
    silently pick that up). **Deviation, deliberate:** `data/gen_synthetic_pdfs.py` must call
    `create_llm_client(provider="ollama")` explicitly (or construct `OllamaClient()` directly)
    rather than the zero-arg form other call sites use.
  - `agents/query/server.py` — same zero-arg `create_llm_client()` pattern (also relies on env
    var); confirms zero-arg is the general repo convention for *neutral* call sites, but not
    appropriate here given the explicit user constraint.
- `agents/llm/ollama_client.py` — `OllamaClient(base_url=DEFAULT_BASE_URL
  ="http://localhost:11434", model=DEFAULT_MODEL="llama3.1:8b", timeout=120.0,
  transport=None)`. Talks `/api/generate`. Verified reachable this session:
  `curl localhost:11434/api/tags` → `llama3.1:8b`, `mistral:latest`, `llama3.2:latest`
  installed locally.
- Live-Ollama optional-smoke-test convention: `agents/ingestion/test_segment_live.py` —
  `_ollama_is_reachable()` probes `httpx.get(base_url)`, `pytestmark =
  pytest.mark.skipif(not _ollama_is_reachable(), reason=...)`. Mirrored for this subtask's
  test file.

## PDF rendering — deviation from OQ-1's literal example libraries
OQ-1's resolution says "e.g. reportlab or weasyprint." Checked `agents/.venv`: neither
`reportlab`, `weasyprint`, `pypdf`, nor `fpdf` is installed. `pymupdf>=1.24` (import name
`fitz`) IS already a first-class project dependency (`agents/pyproject.toml`), already used
for PDF *parsing* in `agents/ingestion/normalize_pdf.py`. Confirmed live in this session that
`fitz` can also *write* PDFs (`fitz.open()` → `new_page()` → `insert_textbox()` → `.save()`),
producing valid, parseable output (round-tripped via `get_text()`).

**Decision:** use `pymupdf`/`fitz` for PDF generation instead of adding a brand-new dependency
(reportlab/weasyprint). This satisfies "a lightweight library" (it's arguably lighter than
adding a second, unused-elsewhere PDF stack) and avoids a new third-party dependency + its
transitive footprint for a repo that already depends on pymupdf for the inverse operation.
Recorded here as an implementer's-judgment deviation from OQ-1's literal "e.g." list, per the
dispatch's explicit allowance for implementer judgment on module location/tooling specifics.

## Repo conventions checked
- `data/load_bitext.py` / `data/load_enron.py` — module docstring discloses design decisions
  up front (mirrored in the new script's docstring).
- `agents/eval/datasets.py` (task-5.1.1) — delegates to `data/` loaders; this subtask's script
  lives in `data/` (generation, not loading) and issue #26's own impacted-modules note
  explicitly names `data/gen_synthetic_pdfs.py` as the expected path.
- `agents/pyproject.toml` `[tool.setuptools] packages` does not include `data` as a Python
  package (it's script/fixture space, imported by path like `load_bitext.py`/`load_enron.py`
  already are from `agents/eval/datasets.py` via `sys.path` munging) — new script follows the
  same convention, no pyproject changes needed.
- Marker/manifest precedent for machine-readable structured output consumable by a later
  subtask: `agents/ingestion/normalize_pdf.py`'s `[[PAGE n LEN=k]]` framing establishes the
  repo's general comfort with a structured sidecar/marker format alongside prose content —
  informs (but does not literally reuse) this subtask's JSON generation-manifest, which is the
  cleaner mechanism for 5.1.3's OQ-2 "auto-derived from generation config" consumption.

## Files touched (new only)
- `data/gen_synthetic_pdfs.py`
- `data/synthetic_corpus/topics.yaml`
- `data/test_gen_synthetic_pdfs.py`
