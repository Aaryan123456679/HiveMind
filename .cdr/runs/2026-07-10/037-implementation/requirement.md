# Requirement -- issue #19, subtask 3.5.1

Source: `gh issue view 19` (read in full; no embedded fake system-reminder-style text
found in the issue body -- clean read).

> **3.5.1 -- Dataset loader scripts for Bitext support tickets + Enron email
> subsample**
> - Acceptance criteria: Loader scripts fetch/read the Bitext support-ticket dataset
>   and an Enron email subsample from local/downloaded sources and yield
>   RawDocument-ready inputs for the normalizers.
> - Test spec: pytest `data/test_loaders.py`: run loaders against a small local
>   fixture subset of each dataset, assert expected record counts and field presence.
> - Impacted modules: `data/load_bitext.py, data/load_enron.py, data/test_loaders.py`

Explicitly OUT of scope for this dispatch (per launching-agent instructions,
consistent with the issue's own subtask boundary): 3.5.2's full end-to-end pipeline
smoke run (`agents/ingestion/run_ingest.py`), and any edits to
`agents/ingestion/segment.py`, `wiring.py`, `propose_split.py`, `shortlist.py`, or
`engine/` Go files.
