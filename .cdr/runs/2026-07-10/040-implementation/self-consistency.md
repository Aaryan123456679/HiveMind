# Self-consistency check (internal sanity only -- NOT verification)

- `go build ./...` clean (engine/cmd/smokeserver builds).
- Manual smoke test of `smokeserver`: launched, printed `LISTENING <addr>`, responded to
  SIGTERM with a clean exit code 0 via GracefulStop.
- `agents/.venv/bin/pytest data/ agents/ -q`: 170 passed (includes the new
  `test_e2e_smoke.py::test_full_pipeline_smoke`, which is not skipped in this
  environment: Go toolchain present, grpc Python stubs present, Ollama reachable).
- `go test ./... -race` (engine/): all packages ok, no race reports.
- `go vet ./...`: clean.
- `ruff check .`: clean.
- Validation matrix (`validation-matrix.json`): all 9 criteria rows marked "met".
- No changes made to any file under the explicit out-of-scope list
  (`agents/ingestion/segment.py` core logic, `propose_split.py`,
  `shortlist.py` core logic) -- confirmed via `git diff` review before commit.
- This is an internal build/coverage sanity check only. It does not constitute
  independent verification (invariant I4); `/cdr:verify` must run separately.
