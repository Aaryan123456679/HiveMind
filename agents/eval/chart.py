"""Corpus-growth-checkpoint degradation chart renderer (issue #28, subtask 5.3.4).

Renders `run_benchmark.py`'s output data file (a `BenchmarkReport.to_json()`-shaped dict) into a
degradation chart of recall/precision over corpus growth, one line per arm -- the acceptance
criteria's "the key novelty result of the project."

Charting approach -- zero new dependency, disclosed
--------------------------------------------------------
`agents/pyproject.toml` (checked before writing this module) declares no plotting dependency
(only `fastapi`/`uvicorn`/`grpcio`/`grpcio-tools`/`protobuf`/`pydantic`/`httpx`/`pymupdf`).
Introducing `matplotlib` here purely to draw one degradation chart would be scope creep for a
subtask whose impacted-module list is `run_benchmark.py` + `chart.py` only, per this subtask's
own instruction ("prefer zero-new-dependency if a reasonable option exists"). This module
therefore renders a stdlib-only text/data table: one row per checkpoint (ascending ingestion
percentage), one column-pair (recall, precision) per arm -- a "line per arm" reading top-to-bottom
down each arm's own recall/precision column as corpus percentage increases, exactly the
degradation-over-growth shape the acceptance criteria asks for, just rendered as text instead of
a pixel plot. No new dependency is added to `agents/pyproject.toml`.
"""

from __future__ import annotations

from pathlib import Path

#: Metrics rendered per arm, in this fixed order.
_METRICS = ("recall", "precision")


def _rows_by_checkpoint(rows: list[dict]) -> dict[str, dict]:
    """Group flat `rows` (as in `BenchmarkReport.to_json()['rows']`) by
    `(checkpoint_pct, checkpoint_label)`, each mapping `arm -> row`."""
    grouped: dict[tuple[int, str], dict[str, dict]] = {}
    for row in rows:
        key = (row["checkpoint_pct"], row["checkpoint_label"])
        grouped.setdefault(key, {})[row["arm"]] = row
    return grouped


def render_degradation_table(rows: list[dict], *, arms: list[str] | None = None) -> str:
    """Render `rows` (a `BenchmarkReport.to_json()['rows']`-shaped list of per-checkpoint-per-arm
    dicts, each with `checkpoint_label`/`checkpoint_pct`/`arm`/`mean_recall`/`mean_precision`)
    into a stdlib-only text degradation table.

    Args:
        rows: Flat list of row dicts, as produced by `run_benchmark.CheckpointArmResult.to_json`.
        arms: Explicit arm ordering for columns. Defaults to first-seen order across `rows`.

    Returns:
        A multi-line string: one header line naming each arm's recall/precision columns, then
        one line per checkpoint (ascending `checkpoint_pct`), each showing every arm's
        `mean_recall`/`mean_precision` at that checkpoint -- reading down one arm's own column
        pair shows that arm's degradation (or improvement) across corpus growth.

    Raises:
        ValueError: If `rows` is empty.
    """
    if not rows:
        raise ValueError("render_degradation_table: rows must be non-empty")

    if arms is None:
        seen: list[str] = []
        for row in rows:
            if row["arm"] not in seen:
                seen.append(row["arm"])
        arms = seen

    grouped = _rows_by_checkpoint(rows)
    checkpoint_keys = sorted(grouped.keys(), key=lambda pair: pair[0])

    col_width = 18
    header_cells = ["checkpoint".ljust(12)]
    for arm in arms:
        header_cells.append(f"{arm} (recall/prec)".ljust(col_width))
    lines = ["  ".join(header_cells), "-" * (12 + (col_width + 2) * len(arms))]

    for pct, label in checkpoint_keys:
        cells = [label.ljust(12)]
        arm_rows = grouped[(pct, label)]
        for arm in arms:
            row = arm_rows.get(arm)
            if row is None:
                cells.append("(missing)".ljust(col_width))
                continue
            cell = f"{row['mean_recall']:.3f} / {row['mean_precision']:.3f}"
            cells.append(cell.ljust(col_width))
        lines.append("  ".join(cells))

    return "\n".join(lines) + "\n"


def write_chart(rows: list[dict], path: str | Path, *, arms: list[str] | None = None) -> None:
    """Render `rows` via `render_degradation_table` and write the result to `path` (creating
    parent directories as needed)."""
    text = render_degradation_table(rows, arms=arms)
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def render_degradation_table_from_file(path: str | Path, *, arms: list[str] | None = None) -> str:
    """Load a `run_benchmark.write_benchmark_results`-written data file at `path` and render its
    `"rows"` via `render_degradation_table`."""
    import json

    data = json.loads(Path(path).read_text(encoding="utf-8"))
    return render_degradation_table(data["rows"], arms=arms)


def main(argv: list[str] | None = None) -> None:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("data_file", help="Path to a run_benchmark.py data JSON file.")
    parser.add_argument("--out", default=None, help="Optional output .txt path (default: stdout).")
    args = parser.parse_args(argv)

    text = render_degradation_table_from_file(args.data_file)
    if args.out:
        Path(args.out).parent.mkdir(parents=True, exist_ok=True)
        Path(args.out).write_text(text, encoding="utf-8")
    else:
        print(text)


if __name__ == "__main__":
    main()
