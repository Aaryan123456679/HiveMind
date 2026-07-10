# Architecture discovery

## F4 investigation

- `engine/catalog/record.go`'s `CatalogRecord.PathHash` (uint64) is the only
  path-shaped field in the on-disk catalog record.
- `engine/rpc/server.go`'s `PutSegment` CREATE branch builds a bare
  `catalog.CatalogRecord{FileID, CurrentVersion, SizeBytes, Status}` -- never touches
  `PathHash`, and never calls `btree.Insert` either.
- Root cause is NOT a missed field assignment: `proto/hivemind.proto`'s
  `PutSegmentRequest` message (`message PutSegmentRequest { uint64 file_id = 1; bytes
  content = 2; }`) carries no `path` field at all. There is nothing for `PutSegment` to
  hash even if the field-population code were added. `server.go`'s own doc comment
  (lines ~55-60) already discloses this precisely: "PutSegmentRequest ... carries only
  file_id + content, no path, so there is no path for PutSegment to index."
- `agents/ingestion/wiring.py`'s module docstring independently confirms the same root
  cause from the client side ("`new_topic_path` cannot be registered anywhere queryable
  today").
- Conclusion: fixing F4 for real requires adding a `path` field to `PutSegmentRequest`
  (a proto/wire-contract change), regenerating stubs, updating `wiring.py`'s
  `GrpcPutSegmentClient.put_segment` call site to pass it, AND adding the
  `PathHash`-population + `btree.Insert` call in `server.go`'s CREATE branch. That is a
  multi-file, cross-cutting change spanning proto/Go/Python, explicitly the kind of
  "requires a proto/wire-contract change" case this run's instructions said to STOP on
  rather than force. Not fixed in this run; see handoff.json.

## Real end-to-end harness options considered

- `engine/rpc/integration_test.go` already establishes the "real *grpc.Server bound to
  a real net.Listener" pattern, but only from within a Go test (in-process, same
  process as the test). Issue #19's own scope is a Python-driven pipeline
  (`agents/ingestion/`), so a Go-test-only harness could not exercise `data/`'s loaders,
  `shortlist.py`, `segment.py`, or `wiring.py` for real.
- No existing standalone runnable engine binary exists (`api/main.go` is a stub with an
  empty `main()`; grepping the whole repo for `grpc.NewServer` outside test files
  returned nothing).
- Decision: add `engine/cmd/smokeserver`, a new minimal standalone binary mirroring
  `integration_test.go`'s real-storage-backed fixture setup (catalog/content/btree/
  graph, no seeded fixture content), printing its listen address to stdout so a Python
  parent process can launch it as a real subprocess, dial it over a real gRPC channel,
  and drive the real pipeline against it end-to-end. Terminated via SIGTERM at test
  teardown.

## Real dataset sourcing

- Bitext: already real (task-3.5.1's `data/fixtures/bitext_sample.json`, a genuine
  30-row Hugging Face sample) -- reused as-is, not re-sourced.
- Enron: searched Hugging Face's `datasets-server` for Enron datasets; the ones found
  (e.g. `LLM-PBE/enron-email`) are body-only (headers already stripped), which does not
  match `normalize_email.py`'s expected raw maildir-format input (same gap 3.5.1's
  loader docstring already documented). Verified network reachability to the official
  CMU/FERC-hosted `enron_mail_20150507.tar.gz` (`https://www.cs.cmu.edu/~enron/`,
  `Accept-Ranges: bytes` present). Streamed the gzip'd tar directly via
  `urllib.request.urlopen` + `tarfile.open(fileobj=..., mode="r|gz")`, scanning archive
  members in order and stopping after finding 6 suitable real messages (header-ful,
  400-3000 bytes) under `maildir/blair-l/sent_items/` -- only ~1,733 of ~500k members
  were ever read off the wire; no intermediate file larger than one ~2KB message was
  ever written to disk. See `data/fixtures/enron_real_sample/PROVENANCE.md` for full
  detail.

## New finding surfaced during this run (forwarded, not fixed)

Running the real local `llama3.1:8b` Ollama model (temperature=0.0) against these 11
real documents reproducibly (deterministically) produced malformed-JSON completions for
a majority of them: raw, unescaped newlines/control characters embedded directly inside
a JSON string value, and in some responses a stray `"""` triple-quote artifact. This is
distinct from the already-known/fixed F1 (markdown code-fence wrapping) --
`strip_code_fences` already runs correctly here; the malformed completions still fail
JSON parsing after fence-stripping. `agents/ingestion/segment.py`'s `_parse_segment_json`
was explicitly out of scope to modify for this run (per its own instructions), so this
is disclosed as a new forwarded finding rather than fixed. See handoff.json.
