# Requirement — task-5.1.1

**Source:** GitHub issue #26, "[5] Dataset + ground-truth preparation (data/, agents/eval/)",
subtask 5.1.1. First subtask of milestone #7 (Phase 5: Benchmark suite), unblocked after
milestone #10 (Phase 4.5) closed.

**Title:** Support-ticket + Enron dataset loaders wired into agents/eval/'s dataset interface

**Acceptance criteria:** `agents/eval/` can load the Bitext and Enron datasets (via task-3.5.1's
loaders) through a common dataset-loader interface used by all three benchmark arms.

**Test spec:** `pytest agents/eval/test_dataset_interface.py`: load both datasets through the
common interface, assert consistent record shape.

**Impacted modules (declared):** `agents/eval/datasets.py`, `agents/eval/test_dataset_interface.py`

**Explicit constraint from launcher:** task-3.5.1's Bitext/Enron loaders already exist somewhere
in the repo (Phase 3) — locate via `.cdr/index/file.jsonl` + git history, do not reimplement
loading logic; only wire existing loaders behind a common interface.
