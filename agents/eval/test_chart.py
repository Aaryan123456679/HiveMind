"""Tests for `agents/eval/chart.py` (issue #28, subtask 5.3.4).

Pure-function tests over fixed row data -- no LLM/network involvement at all in this module or
its tests.
"""

from __future__ import annotations

import json

import pytest

from eval.chart import render_degradation_table, render_degradation_table_from_file, write_chart

_ROWS = [
    {"checkpoint_label": "20pct", "checkpoint_pct": 20, "arm": "hivemind", "mean_recall": 0.5, "mean_precision": 0.4},
    {"checkpoint_label": "20pct", "checkpoint_pct": 20, "arm": "vector_rag", "mean_recall": 0.3, "mean_precision": 0.2},
    {"checkpoint_label": "20pct", "checkpoint_pct": 20, "arm": "graphrag_lite", "mean_recall": 0.6, "mean_precision": 0.5},
    {"checkpoint_label": "50pct", "checkpoint_pct": 50, "arm": "hivemind", "mean_recall": 0.7, "mean_precision": 0.6},
    {"checkpoint_label": "50pct", "checkpoint_pct": 50, "arm": "vector_rag", "mean_recall": 0.5, "mean_precision": 0.4},
    {"checkpoint_label": "50pct", "checkpoint_pct": 50, "arm": "graphrag_lite", "mean_recall": 0.65, "mean_precision": 0.55},
    {"checkpoint_label": "100pct", "checkpoint_pct": 100, "arm": "hivemind", "mean_recall": 0.9, "mean_precision": 0.85},
    {"checkpoint_label": "100pct", "checkpoint_pct": 100, "arm": "vector_rag", "mean_recall": 0.8, "mean_precision": 0.7},
    {"checkpoint_label": "100pct", "checkpoint_pct": 100, "arm": "graphrag_lite", "mean_recall": 0.7, "mean_precision": 0.6},
]


def test_render_degradation_table_contains_all_checkpoints_and_arms():
    table = render_degradation_table(_ROWS)

    for label in ("20pct", "50pct", "100pct"):
        assert label in table
    for arm in ("hivemind", "vector_rag", "graphrag_lite"):
        assert arm in table

    # ascending checkpoint order
    idx_20 = table.index("20pct")
    idx_50 = table.index("50pct")
    idx_100 = table.index("100pct")
    assert idx_20 < idx_50 < idx_100

    # spot-check a formatted recall/precision cell
    assert "0.900 / 0.850" in table


def test_render_degradation_table_rejects_empty_rows():
    with pytest.raises(ValueError):
        render_degradation_table([])


def test_render_degradation_table_missing_arm_at_checkpoint_shows_placeholder():
    rows = [r for r in _ROWS if not (r["checkpoint_label"] == "20pct" and r["arm"] == "vector_rag")]
    table = render_degradation_table(rows)
    assert "(missing)" in table


def test_write_chart_and_render_from_file_round_trip(tmp_path):
    data_file = tmp_path / "benchmark_results.json"
    data_file.write_text(json.dumps({"rows": _ROWS}), encoding="utf-8")

    from_file = render_degradation_table_from_file(data_file)
    direct = render_degradation_table(_ROWS)
    assert from_file == direct

    out_path = tmp_path / "chart.txt"
    write_chart(_ROWS, out_path)
    assert out_path.read_text(encoding="utf-8") == direct
