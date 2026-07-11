# Plan — task-5.1.2

## 1. `data/synthetic_corpus/topics.yaml`
Structured, extensible topic-seed manifest:
```yaml
topics:
  - id: data-retention
    title: "Data Retention Policy"
    key_facts:
      - "records must be retained for a minimum of 7 years"
      - "retention clock starts at the date of final account closure"
  - id: expense-reimbursement
    title: "Expense Reimbursement Procedure"
    key_facts:
      - "reimbursement requests must be submitted within 30 days of purchase"
      - "any single expense over $500 requires manager pre-approval"
  ... (10 topics total, demonstrative seed set; script scales to the LLD's ~30-50 without
      any code change -- just append more topic entries)
```
Each topic: `id` (slug, used as manifest key + PDF filename stem), `title` (human string
embedded in the prompt + checked for in output), `key_facts` (2-3 short factual strings the
prompt asks the model to include verbatim-ish for its own primary-topic coverage; used loosely
as prompt content, not as a strict test oracle since LLM won't echo verbatim reliably).

## 2. `data/gen_synthetic_pdfs.py`
- `load_topics(path) -> dict[str, Topic]` — parse YAML via `yaml.safe_load`, validate shape
  (dataclass `Topic(id, title, key_facts)`).
- `build_doc_specs(topics, *, cross_refs_per_doc=2, seed=0) -> list[DocSpec]` — one doc per
  topic (`primary_topic_id = topic.id`), `cross_topic_ids` = the next `cross_refs_per_doc`
  topics in id-sorted order (wrap-around), deterministic given the topic list (no RNG needed
  for the default path, but a `seed` param is accepted for future shuffled-assignment variants
  so the signature doesn't need to change later).
- `build_prompt(primary, cross_topics) -> str` — instructs the model (in prose, no JSON) to:
  - Write a self-contained corporate policy/procedures-manual document.
  - Cover the primary topic in detail, weaving in its `key_facts`.
  - For EACH cross-referenced topic, include at least one sentence that names that topic's
    title explicitly and connects it to the primary topic (e.g. "As covered under the '<Title>'
    policy, ...").
  - Plain prose only (no markdown headers/asterisks), since output goes straight into a PDF
    text box.
- `generate_document_text(llm_client, doc_spec, topics, *, model=None, temperature=0.0,
  timeout=None) -> str` — builds prompt, calls `llm_client.complete(...)`, returns raw text.
- `render_pdf(text, title, output_path)` — via `fitz`: new single-or-multi-page PDF, title as
  a bold-ish heading line + body via `insert_textbox` across as many pages as needed (loop
  while `insert_textbox` returns negative remaining-space, i.e. overflow, adding new pages).
- `generate_corpus(*, topics_path, output_dir, llm_client=None, model=None,
  cross_refs_per_doc=2, limit=None) -> GenerationManifest` — orchestrates: load topics, build
  doc specs (optionally truncated via `limit` for fast local runs), default
  `llm_client = create_llm_client(provider="ollama")` if not passed (HARD-CODED "ollama" --
  never env-driven, per requirement.md), generate text + render PDF per doc, write
  `output_dir/manifest.json` with per-doc metadata (doc_id, filename, primary_topic id+title,
  cross_reference ids+titles, model, generated_at) for 5.1.3's ground-truth auto-derivation.
- CLI `main()` (`argparse`): `--topics`, `--out-dir`, `--limit`, `--model`, `--cross-refs`.

## 3. `data/test_gen_synthetic_pdfs.py`
- Fully-mocked unit tests (fake deterministic `LLMClient`, mirroring
  `agents/ingestion/test_segment_fixtures.py`'s `_FakeLLMClient` pattern):
  - `load_topics` parses the shipped `topics.yaml` into the right count/shape.
  - `build_doc_specs` assigns every topic a primary role and >=1 distinct cross-reference,
    with no doc cross-referencing itself.
  - `build_prompt` embeds primary topic title + all cross-topic titles.
  - `generate_corpus` (fake LLM returning a canned string per call, captured) with `limit=3`:
    produces exactly 3 PDF files, each parseable via `fitz.open` with non-empty text; a
    `manifest.json` with exactly 3 entries whose structure matches; asserts the fake LLM was
    invoked with `provider` never touched (i.e. `create_llm_client` called with
    `provider="ollama"` -- patched/inspected) OR llm_client passed directly (both paths
    tested).
  - PDF validity check: `fitz.open(path).page_count >= 1` and extracted text length > 0.
  - Keyword-presence check on the *fake* LLM's canned text (deterministic): primary topic
    title + cross-topic title both present.
- Optional skippable live-Ollama smoke test (module-level `pytest.mark.skipif`, mirroring
  `agents/ingestion/test_segment_live.py`): runs `generate_corpus(limit=2)` against the real
  local Ollama server, asserts 2 real PDFs produced, each valid/parseable, each containing its
  primary topic's title (case-insensitive substring) AND at least one of its assigned
  cross-topic titles (case-insensitive substring) in the extracted PDF text. This is the
  closest thing to the acceptance-criteria test spec run for real; kept skippable so normal
  CI/pytest runs never require a live Ollama server.

## Self-consistency (this agent, not verification)
- Run the fully-mocked unit tests: must pass green.
- Actually invoke the live path once manually (small `--limit`, e.g. 2-3 docs) against the
  real local Ollama server confirmed reachable this session, confirm real PDF files are
  produced, `fitz`-parseable, and manually eyeball that cross-topic references genuinely
  appear (not just structurally required).
- `ruff check` on the new files.
- Confirm zero occurrences of `openrouter`/`gemini`/`OPENROUTER`/`GEMINI` (case-insensitive) in
  the 3 new files.
