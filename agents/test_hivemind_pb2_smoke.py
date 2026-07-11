"""Lightweight smoke check for the generated gRPC stubs (issue #46).

`hivemind_pb2.py` / `hivemind_pb2_grpc.py` are generated code checked into git (see
`proto/README.md`) and embed a hard `ValidateProtobufRuntimeVersion` gencode/runtime check
at import time. If the installed `protobuf` runtime ever falls behind the gencode baked into
these stubs (e.g. after a `grpcio-tools` bump regenerates them against a newer `protoc`, or a
dependency resolution picks an old transitive `protobuf`), importing them raises
`google.protobuf.runtime_version.VersionError` and silently blocks pytest *collection* of
every test module that imports the real gRPC stubs (this bit `agents/ingestion/
test_e2e_smoke.py` and `agents/ingestion/test_shortlist.py` repeatedly — see
`.cdr/index/regression.jsonl` entries for issues #20, #23, #24, #25).

This test exists purely to turn that failure mode into a fast, clearly-labeled, always-collected
failure instead of a confusing collection error buried in a full-suite run. It is intentionally
independent of any fixture/mocking so it fails even if every other test file in `agents/` is
skipped or filtered out.
"""

import importlib


def test_hivemind_pb2_imports_without_version_mismatch():
    """Import the generated gRPC stubs and fail loudly with a clear message on drift.

    If this fails with `google.protobuf.runtime_version.VersionError`, the installed
    `protobuf` runtime has fallen behind the gencode baked into `hivemind_pb2.py` /
    `hivemind_pb2_grpc.py`. Fix by either:
      1. Bumping the `protobuf` pin in `agents/pyproject.toml` to satisfy the gencode
         floor reported in the error, and reinstalling; or
      2. Regenerating the stubs from `proto/hivemind.proto` against the currently
         pinned `grpcio-tools` (see `proto/README.md` for the exact command) so gencode
         and runtime are back in lockstep.
    """
    try:
        hivemind_pb2 = importlib.import_module("hivemind_pb2")
        hivemind_pb2_grpc = importlib.import_module("hivemind_pb2_grpc")
    except Exception as exc:  # noqa: BLE001 - intentionally broad, re-raised with context
        raise AssertionError(
            "Failed to import generated gRPC stubs (hivemind_pb2 / hivemind_pb2_grpc). "
            "This is very likely a protobuf gencode/runtime version mismatch (issue #46) "
            "-- see this test's docstring for the fix. Original error: "
            f"{exc!r}"
        ) from exc

    # Basic sanity: the module actually defines what we expect, not just "imported without
    # raising" (e.g. a stale .pyc masking a real problem).
    assert hasattr(hivemind_pb2, "DESCRIPTOR")
    assert hasattr(hivemind_pb2_grpc, "HiveMindStub")
    assert hasattr(hivemind_pb2_grpc, "HiveMindServicer")
