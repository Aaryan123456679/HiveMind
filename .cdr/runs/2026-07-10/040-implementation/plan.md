# Plan

1. Source a genuine, real (non-fixture) small Enron sample; document provenance.
2. Investigate F4 (`PutSegment` CREATE path never sets `PathHash`) precisely enough to
   decide fix vs. block; if it requires a wire-contract change, stop and document as a
   blocker rather than forcing an incomplete fix.
3. Build a real (non-mocked) standalone Go engine subprocess (`engine/cmd/smokeserver`)
   that a Python test can launch and dial over a real gRPC channel.
4. Build `agents/ingestion/test_e2e_smoke.py`: drive Bitext (5 docs) + the new real
   Enron sample (6 docs) through load -> normalize -> shortlist -> segment (real local
   Ollama) -> wiring.execute_segment (real PutSegment/PutEdge over the real subprocess),
   with a skipif guard for missing Go toolchain / grpc stubs / unreachable Ollama.
5. Do not modify `segment.py`'s core parsing logic, `propose_split.py`, or
   `shortlist.py`'s core logic; tolerate (record + skip) any `SegmentParseError` a real
   LLM completion produces rather than forcing 100% success.
6. Assert real, observable end-to-end outcomes: no unhandled errors, every successfully
   segmented document gets a real distinct fileID via PutSegment, entity/edge creation
   runs without error, and a final `SearchCandidates("")` sanity check documents F4's
   real observable consequence (only the bootstrap placeholder is discoverable; newly
   created files are not, because PathHash/btree-insert never ran for them).
7. Run full regression: `agents/` pytest suite, `go test ./... -race`, `go vet ./...`,
   `ruff check`.
8. Write all CDR artifacts; forward the new LLM-JSON-malformation finding to
   `.cdr/memory/pending.md`; write handoff.json with the F4-blocked reasoning, the
   issue-#19-final-subtask stop-and-ask directive, and the milestone #5 closability
   note.
9. Create exactly one local git commit (Problem/Solution/Impact format), no push.
10. Stop after self-consistency / handoff -- do not run or invoke `/cdr:verify` myself.

## Commit-splitting decision

One commit, not two. There is no separate F4 *code fix* to split out -- F4 was
investigated and found to require a proto/wire-contract change, so it is documented as
a blocker only (no code change for it exists). The Enron sample, `smokeserver`, and the
smoke test are all one cohesive "build the E2E harness" change with no independently
useful/revertable boundary between them, so splitting would not add clarity.
