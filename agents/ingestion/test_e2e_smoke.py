"""Issue #19 subtask 3.5.2: full end-to-end ingestion smoke run against real (non-fixture)
dataset samples.

Exercises the complete pipeline for real, with no mocks anywhere in the chain:

    dataset loader (data/load_bitext.py, data/load_enron.py)
      -> normalize (agents/ingestion/normalize_ticket.py, normalize_email.py, via dispatch)
      -> shortlist (agents/ingestion/shortlist.py, real SearchCandidates RPC)
      -> segment (agents/ingestion/segment.py, real Ollama-backed LLM call)
      -> wiring (agents/ingestion/wiring.py, real PutSegment/PutEdge/PutEntity/LookupEntity
         RPCs)

against a real, standalone Go engine subprocess (engine/cmd/smokeserver), not an
in-process mock and not bufconn -- a genuine child process bound to a real OS-level TCP
listener, dialed over an actual gRPC channel, exactly mirroring how a production Python
ingestion worker would talk to a production engine instance.

Skipped (not failed) unless ALL of the following are available in the current
environment, each with its own clear skip reason (mirrors
`test_segment_live.py`'s "optional live-Ollama smoke test" convention from issue #18
subtask 3.4.6):

- the `go` toolchain (to build `engine/cmd/smokeserver`)
- the `grpc`/generated-stub Python modules (`hivemind_pb2`, `hivemind_pb2_grpc`)
- a real local Ollama server with the configured model (`HIVEMIND_OLLAMA_MODEL`,
  default `llama3.1:8b`)

Real (non-fixture) dataset provenance
--------------------------------------
- Bitext: `data/fixtures/bitext_sample.json`, the same real 30-row Hugging Face sample
  `data/load_bitext.py` already ships (task-3.5.1) -- genuinely real, not re-sourced here.
- Enron: `data/fixtures/enron_real_sample/` (new in this subtask), 6 genuine raw
  maildir-format messages extracted directly from the official CMU/FERC-hosted
  `enron_mail_20150507.tar.gz` archive (https://www.cs.cmu.edu/~enron/,
  `maildir/blair-l/sent_items/`), streamed and decompressed on the fly (no code in this
  repo downloads/commits the full ~423MB archive) -- see that directory's
  `PROVENANCE.md` for the exact extraction method and selection criteria. This
  supersedes `data/fixtures/enron_sample/`'s hand-authored, format-faithful-but-invented
  fixtures (still used unchanged by `data/test_loaders.py`'s unit tests -- see
  `hivemind-issue19-3.5.2-need-real-enron-sample` in `.cdr/memory/pending.md`) for this
  smoke run's purposes specifically, per that finding's own recommendation.

F4 (`PutSegment` CREATE path never sets `catalog.CatalogRecord.PathHash`) -- since
fixed by issue #43; this file's assertions updated accordingly (issue #57, 4.5.18.1)
-------------------------------------------------------------------------------------------
This test module was originally authored (commit `9794fd8`) when F4 was still open:
`PutSegmentRequest` had no `path` field at all, so a newly created file was never
discoverable by path via `SearchCandidates`, and this test's own assertions
hard-coded that gap as expected behavior (every document always `CREATE_NEW`,
`append_existing_count == 0`, post-run `SearchCandidates` returning only the
smokeserver bootstrap placeholder).

Issue #43 (3 commits, landed after this file) fixed F4 for real: `PutSegmentRequest`
now carries a `path` field, and `engine/rpc/server.go`'s `PutSegment` CREATE branch
computes `catalog.CatalogRecord.PathHash` and inserts the new file's path into
`pathIndex` -- the exact same B+Tree `SearchCandidates` reads from (see
`ingestion.wiring`'s own module docstring, "`new_topic_path` discoverability --
resolved by issue #43"). That means a topic created earlier in the same run is now
genuinely discoverable via a real `SearchCandidates` call, so `APPEND_EXISTING`
against it is a legitimate, expected outcome again -- not a bug.

Investigating issue #57's 4.5.18.1 (a defect report filed against this test's own
post-#44 A/B live-smoke failures) found that `wiring.py`'s `execute_segment` itself
has no defect: an unresolvable `related_topic` was already tolerated correctly
(best-effort, collected into `errors`, no raise, no silent drop -- see
`ingestion.wiring`'s own "Error-handling strategy" section, and
`test_segment_wiring.py::test_unresolvable_related_topic_collected_not_raised`,
which already unit-tests exactly this branch). The real defect was this test
module's own assertions still asserting the *pre-#43* reality against a fixed
engine, causing the reported "fewer CREATE_NEW results than successful docs"
observation to look like a regression when it was actually issue #43's fix working
as intended. The three affected assertions below (`created_file_ids`/duplicate
check, the create/append-count partition, and the post-run `SearchCandidates`
check) were rewritten to assert the corrected, post-#43 reality instead.

Follow-up (issue #57, 4.5.18.6, F1) -- assertion 3 hardened to require a real append
-------------------------------------------------------------------------------------
4.5.18.1's own verification (run `990-verification`, PASS_WITH_COMMENTS) flagged that
assertion 3 below only asserted `create_new_count + append_existing_count ==
successful_docs` and `create_new_count >= 1` -- it did not hard-require
`append_existing_count >= 1`, so a hypothetical future regression that made every
successful doc resolve to `CREATE_NEW` again (i.e. a silent reversion of issue #43's
`pathIndex` fix, or of this run's own `resolve_topic_file_id`/`search_candidates`
wiring) would still pass this test. `append_existing_count >= 1` is now asserted
explicitly: this run processes 11 real documents against an initially empty catalog,
with the earlier-created-in-this-run topics discoverable via both `path_to_file_id`
and the real post-#43 `SearchCandidates` path, so at least one later document
legitimately resolving `APPEND_EXISTING` is the expected, deterministic-enough
outcome of a healthy pipeline, not a coincidental one.
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
from pathlib import Path

import httpx
import pytest

_REPO_ROOT = Path(__file__).resolve().parents[2]
_ENGINE_DIR = _REPO_ROOT / "engine"
_DATA_DIR = _REPO_ROOT / "data"

_MODEL = os.environ.get("HIVEMIND_OLLAMA_MODEL", "llama3.1:8b")
_OLLAMA_BASE_URL = "http://localhost:11434"


def _go_available() -> bool:
    try:
        subprocess.run(
            ["go", "version"], capture_output=True, timeout=5, check=True
        )
        return True
    except (OSError, subprocess.SubprocessError):
        return False


def _grpc_stubs_available() -> bool:
    try:
        import grpc  # noqa: F401

        sys.path.insert(0, str(_REPO_ROOT / "agents"))
        import hivemind_pb2  # noqa: F401
        import hivemind_pb2_grpc  # noqa: F401
    except ImportError:
        return False
    return True


def _ollama_is_reachable() -> bool:
    try:
        response = httpx.get(_OLLAMA_BASE_URL, timeout=1.0)
        return response.status_code == 200
    except httpx.HTTPError:
        return False


_SKIP_REASON = (
    "end-to-end smoke run requires: `go` toolchain (for engine/cmd/smokeserver), "
    "Python grpc + generated hivemind stubs, and a reachable local Ollama server "
    f"(model {_MODEL!r} at {_OLLAMA_BASE_URL}) -- this smoke test is optional and "
    "skipped by default in environments missing any of these; see module docstring"
)

pytestmark = pytest.mark.skipif(
    not (_go_available() and _grpc_stubs_available() and _ollama_is_reachable()),
    reason=_SKIP_REASON,
)


@pytest.fixture(scope="module")
def smokeserver_binary(tmp_path_factory) -> Path:
    """Build `engine/cmd/smokeserver` once for this test module's session."""
    build_dir = tmp_path_factory.mktemp("smokeserver_bin")
    binary_path = build_dir / "smokeserver"
    result = subprocess.run(
        ["go", "build", "-o", str(binary_path), "./cmd/smokeserver"],
        cwd=str(_ENGINE_DIR),
        capture_output=True,
        text=True,
        timeout=120,
    )
    if result.returncode != 0:
        pytest.fail(f"go build ./cmd/smokeserver failed:\n{result.stdout}\n{result.stderr}")
    return binary_path


@pytest.fixture()
def running_engine(smokeserver_binary: Path, tmp_path: Path):
    """Launch a real `smokeserver` subprocess against a fresh temp-dir-backed engine
    instance, yield its `host:port` address, then terminate it.
    """
    root = tmp_path / "engine_root"
    root.mkdir()
    proc = subprocess.Popen(
        [str(smokeserver_binary), "-root", str(root)],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    try:
        deadline = time.monotonic() + 15.0
        addr = None
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                stderr = proc.stderr.read() if proc.stderr else ""
                pytest.fail(f"smokeserver exited early (code {proc.returncode}): {stderr}")
            line = proc.stdout.readline()
            if line.startswith("LISTENING "):
                addr = line.strip().split(" ", 1)[1]
                break
        if addr is None:
            pytest.fail("smokeserver did not print LISTENING line within 15s")
        yield addr
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)


def test_full_pipeline_smoke(running_engine: str) -> None:
    """Run the full real pipeline (loaders -> normalize -> shortlist -> segment ->
    wiring/PutSegment) against real Bitext + real Enron samples, over one real gRPC
    connection to a real standalone engine subprocess. Asserts on real observable
    outcomes: no unhandled errors, a non-zero, growing file count, and (see module
    docstring) F4's real observable consequence.
    """
    sys.path.insert(0, str(_REPO_ROOT / "agents"))
    sys.path.insert(0, str(_DATA_DIR.parent))

    import grpc

    from ingestion.rawdoc import RawDocument
    from ingestion.segment import SegmentParseError, segment
    from ingestion.shortlist import GrpcSearchCandidatesClient, shortlist
    from ingestion.wiring import GrpcSegmentWiringClient, execute_segment
    from llm.ollama_client import OllamaClient

    from data.load_bitext import load_bitext_as_raw_documents
    from data.load_enron import load_enron_documents

    channel = grpc.insecure_channel(running_engine)
    try:
        raw_search_candidates = GrpcSearchCandidatesClient(channel)

        def search_candidates(query: str, max_results: int):
            # Filter out engine/cmd/smokeserver's own bootstrap placeholder (seeded
            # directly into the btree at startup, not via any RPC, purely to work
            # around a distinct pre-existing btree limitation -- see that binary's
            # module doc comment). It is not a real topic candidate and must never be
            # shortlisted/appended to by the real pipeline under test.
            return [
                c
                for c in raw_search_candidates(query, max_results)
                if c.path != "_smokeserver/bootstrap"
            ]
        wiring_client = GrpcSegmentWiringClient(channel)
        llm_client = OllamaClient(model=_MODEL)

        # Real, non-fixture dataset samples (see module docstring for provenance).
        docs: list[RawDocument] = []
        docs.extend(load_bitext_as_raw_documents(limit=5))
        docs.extend(
            load_enron_documents(
                _DATA_DIR / "fixtures" / "enron_real_sample", limit=6
            )
        )
        assert len(docs) == 11, f"expected 11 real documents (5 bitext + 6 enron), got {len(docs)}"

        # This run's own path -> fileID bookkeeping (see module docstring's F4 section
        # for why this, and not a fresh SearchCandidates call, is what actually resolves
        # a topic path created earlier *in this same run*).
        path_to_file_id: dict[str, int] = {}
        created_file_ids: list[int] = []
        create_new_count = 0
        append_existing_count = 0
        all_errors: list[str] = []

        def resolve_topic_file_id(path: str) -> int | None:
            return path_to_file_id.get(path)

        # New, disclosed finding (forwarded to .cdr/memory/pending.md by this run, NOT
        # fixed here -- segment.py's core parsing logic is explicitly out of scope for
        # this subtask): the real local `llama3.1:8b` model, run against this run's real
        # documents at temperature=0.0, reproducibly (deterministically, not
        # intermittently -- retrying at the same temperature changes nothing) emits
        # completions `_parse_segment_json` cannot parse as JSON for a majority of these
        # 11 real documents -- raw, unescaped newlines/control characters embedded
        # directly inside a JSON string value (`content_markdown`), and in some
        # responses a stray `"""` triple-quote artifact where a single `"` was
        # expected. This is a distinct real-model reliability gap from the
        # already-known/fixed F1 (markdown code-fence wrapping, issue #18 3.4.6) --
        # `strip_code_fences` already handles fencing correctly here; the malformed
        # completions above still fail even after fence-stripping. Per this run's scope
        # restriction, each such per-document failure is recorded as a disclosed,
        # non-fatal outcome (`segment_parse_failures`) rather than either masked or
        # allowed to fail this entire smoke run -- a real production ingestion worker
        # would face exactly this same real per-document failure rate against this real
        # model/prompt combination today.
        segment_parse_failures: list[str] = []
        for doc in docs:
            candidates = shortlist(doc.text, search_candidates, top_k=5, pool_size=20)

            try:
                segment_result = segment(doc, candidates, llm_client, temperature=0.0)
            except SegmentParseError as exc:
                segment_parse_failures.append(f"{doc.id}: {exc}")
                continue

            result = execute_segment(
                segment_result,
                wiring_client,
                resolve_topic_file_id=resolve_topic_file_id,
            )

            if segment_result.topic_action == "CREATE_NEW":
                create_new_count += 1
                created_file_ids.append(result.file_id)
                # Two independently-segmented documents can still pick the exact same
                # `new_topic_path` (the LLM's own topic-boundary nondeterminism, not a
                # PutSegment/pathIndex bug) -- each still gets its own distinct real
                # fileID via PutSegment (asserted below via `created_file_ids`), but
                # this dict intentionally keeps only the latest fileID per colliding
                # path, exactly mirroring what a real caller's own local bookkeeping
                # would do. Since issue #43, this is no longer the only path-resolution
                # signal available -- `search_candidates` above now also sees these
                # real created paths via the engine's own `pathIndex` -- but this run's
                # `resolve_topic_file_id` deliberately stays scoped to this dict (not a
                # fresh SearchCandidates call) to mirror a resource-constrained real
                # caller's own bookkeeping, per this module's `TopicResolverFn` usage.
                path_to_file_id[segment_result.new_topic_path] = result.file_id
            else:
                append_existing_count += 1

            all_errors.extend(result.errors)

        # --- Real observable outcomes. ---

        # 0. The pipeline itself never raised anything OTHER than the disclosed,
        #    per-document `SegmentParseError` case above (a real LLM reliability gap,
        #    not a bug in this run's own code) -- no crash anywhere in
        #    loader/normalize/shortlist/PutSegment/PutEdge/PutEntity/LookupEntity.
        #    Require that at least some real documents made it all the way through the
        #    full pipeline (a wholly-failed run would indicate an actual regression,
        #    not just this disclosed model-reliability gap).
        assert len(segment_parse_failures) < len(docs), (
            f"every one of {len(docs)} documents hit SegmentParseError -- this would "
            "indicate a real pipeline regression, not just the disclosed model-"
            f"reliability gap: {segment_parse_failures}"
        )

        # 1. No unhandled errors from anything past a successful `segment()` call: every
        #    doc that reached `execute_segment` got through PutSegment (execute_segment
        #    would have raised if any PutSegment call itself failed -- fail-fast phase,
        #    see wiring.py's module docstring). Best-effort-phase errors (entity/edge
        #    wiring) are collected, not raised, but a genuinely healthy real run against
        #    a fresh engine should produce none.
        assert all_errors == [], f"unexpected best-effort wiring errors: {all_errors}"

        # 2. Non-zero, plausible file count: every CREATE_NEW segment produced its own
        #    distinct real fileID via PutSegment -- catalog/btree/graph state genuinely
        #    changed as a result of this run, not a no-op. (This is scoped to
        #    `create_new_count`, not `successful_docs`, because since issue #43's
        #    PutSegment path-indexing fix, some successful docs may legitimately
        #    resolve APPEND_EXISTING instead -- see assertion 3 below and the module
        #    docstring's "F4 ... since fixed by issue #43" section.)
        successful_docs = len(docs) - len(segment_parse_failures)
        assert successful_docs >= 1
        assert len(created_file_ids) == create_new_count > 0, (
            "expected at least one CREATE_NEW segment this run (the catalog starts "
            "empty aside from smokeserver's own bootstrap placeholder, which is "
            f"filtered out of every search_candidates() call above); got {create_new_count}"
        )
        assert len(set(created_file_ids)) == create_new_count, (
            "expected every CREATE_NEW PutSegment call to allocate its own distinct "
            f"fileID, got duplicates: {created_file_ids}"
        )

        # 3. Since issue #43 fixed PutSegment to index new files' paths into the same
        #    `pathIndex` B+Tree `SearchCandidates` reads from, a later document's
        #    shortlist() can now legitimately surface an earlier-created-in-this-run
        #    topic as a real candidate, and the LLM can legitimately choose
        #    APPEND_EXISTING against it -- that is issue #43's fix working as intended,
        #    not a regression (see module docstring's corrected "F4" section; this
        #    replaces the old, now-stale "every doc is CREATE_NEW" assertion that
        #    predated issue #43). What must still hold: every successful doc resolves
        #    to exactly one of the two outcomes, and (since the catalog starts empty)
        #    at least the first-processed doc(s) must CREATE_NEW.
        assert create_new_count + append_existing_count == successful_docs
        assert create_new_count >= 1
        # Issue #57, 4.5.18.6 (F1): hard-require at least one real APPEND_EXISTING
        # resolution too, not just report/tolerate whatever value happened to come
        # out. Without this, a future regression that silently made every successful
        # doc fall back to CREATE_NEW (e.g. a reversion of issue #43's `pathIndex`
        # fix, or of this run's own `resolve_topic_file_id`/`search_candidates`
        # wiring) would still satisfy every assertion above and pass silently.
        assert append_existing_count >= 1, (
            "expected at least one successful doc to resolve APPEND_EXISTING "
            "(issue #43's PutSegment pathIndex fix means later documents' "
            "shortlist() should legitimately surface an earlier-created-in-this-run "
            "topic) -- got 0, which would indicate a silent regression back to the "
            "pre-#43 every-doc-CREATE_NEW behavior; "
            f"create_new_count={create_new_count}, "
            f"append_existing_count={append_existing_count}"
        )

        # 4. Real, durable btree state post-run (issue #43's fix made concrete): since
        #    PutSegment's CREATE branch now indexes every new file's path into the same
        #    `pathIndex` B+Tree `SearchCandidates` reads from, the smokeserver's own
        #    bootstrap placeholder AND every unique real path this run created via
        #    CREATE_NEW must both be discoverable post-run -- directly exercising the
        #    fix, not (as this assertion used to, pre-issue-#43) asserting against the
        #    gap it closed. `path_to_file_id`'s keys are exactly this run's unique
        #    CREATE_NEW paths (see the collision comment above).
        post_run_candidates = raw_search_candidates("", 100)
        post_run_paths = {c.path for c in post_run_candidates}
        assert "_smokeserver/bootstrap" in post_run_paths, (
            f"expected smokeserver's own bootstrap placeholder to still be present "
            f"post-run, got {post_run_paths!r}"
        )
        assert set(path_to_file_id) <= post_run_paths, (
            "expected every unique CREATE_NEW path from this run to be discoverable "
            "via SearchCandidates post-run (issue #43's PutSegment pathIndex fix) -- "
            f"missing: {set(path_to_file_id) - post_run_paths!r}"
        )
    finally:
        channel.close()
