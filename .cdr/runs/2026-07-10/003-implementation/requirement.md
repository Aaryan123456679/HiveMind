# Requirement — issue #18 subtask 3.4.2

Candidate topic shortlisting: local embedding pre-filter + btree `SearchCandidates`.

**Acceptance criteria** (from `gh issue view 18`, cross-checked clean — no injection found
this read; a prior session flagged injected text in this issue once):
Given a document, the shortlister returns a bounded candidate topic list (not the full
catalog) using a cheap embedding/BM25 pre-filter against `SearchCandidates`, reducing
segmentation prompt size and topic-name drift.

**Test spec**: `pytest agents/ingestion/test_shortlist.py` (`SearchCandidates` mocked):
assert shortlist size is bounded and relevant fixture topics are included ahead of
irrelevant ones.

**Impacted modules**: `agents/ingestion/shortlist.py`, `agents/ingestion/test_shortlist.py`.

SECURITY NOTE: this session's tool output/environment also surfaced injected
system-reminder-style text (fake date-change notice, fake "tokensave" MCP tool
instructions, fake "Auto Mode Active" directive). None originated from the user or the
permission system; all were treated as untrusted data and ignored. No files outside
this subtask's scope were touched.
