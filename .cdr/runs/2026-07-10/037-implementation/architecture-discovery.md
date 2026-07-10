# Architecture discovery -- issue #19, subtask 3.5.1

Token order followed: `docs/LLD/ingestion-agent.md` -> `data/README.md` ->
`agents/ingestion/rawdoc.py`, `dispatch.py`, `normalize_ticket.py`,
`normalize_email.py` -> `agents/ingestion/test_dispatch.py` (existing fixture/test
conventions) -> `agents/pyproject.toml` (package layout).

Key findings:
- `agents/ingestion/rawdoc.py::RawDocument` is the stable hand-off shape: `id`,
  `source_type` (`"pdf"|"email"|"ticket"`), `text`, `structured_fields`, `timestamp`.
- `agents/ingestion/dispatch.py::dispatch_ticket_json(doc_id, data, timestamp=None)`
  and `dispatch_email(doc_id, path, timestamp=None)` are the two normalizer entry
  points relevant here (PDF is out of scope for both datasets in 3.5.1).
- `normalize_ticket.py::normalize_ticket_json` expects a dict with keys `ticket_id`,
  `subject`, `description`, `status`, `priority`, `category`, `requester`,
  `assignee`, `created_at`, `comments` (list of `{author, body}`).
- `normalize_email.py::normalize_email(path)` expects a path to one raw
  RFC-2822-ish message file (headers, blank line, body) -- the maildir single-message
  format, same as `agents/ingestion/testdata/enron_sample_*.txt`.
- `agents/pyproject.toml` installs `ingestion` (and `query`, `llm`, `eval`) as
  top-level packages rooted at `agents/`, editable-installed into `agents/.venv`.
  Confirmed via `python -c "import ingestion; print(ingestion.__file__)"` under that
  venv: resolves regardless of cwd. This means `data/` (a sibling top-level directory,
  not part of the `agents` package) can `import ingestion...` as long as
  `agents/.venv` is the active interpreter -- no path hacking needed.
- `data/README.md` (pre-existing) already scopes `data/` as "dataset preparation
  scripts for the benchmark corpus: a public support-ticket dataset, an Enron email
  subsample, ...".
- No existing `data/*.py` files; `data/` contained only `README.md` before this run.
- Regression index (`.cdr/index/regression.jsonl`) F3/F4 (issue #18, subtask 3.4.4):
  `engine/rpc/server.go` PutSegment has an open, non-blocking `PathHash` bug (F4,
  pre-existing since task 3.2.2) relevant to 3.5.2 (real pipeline run creates new
  topics), not to this loader-only subtask.
