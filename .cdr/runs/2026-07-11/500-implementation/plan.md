# Implementation Plan — Subtask 4.5.18.2 (Issue #57)

## Requirement
`docs/LLD/btree.md` cites a `.cdr/index/regression.jsonl` entry for the 4.5.12.3
NextLeaf self-healing finding that doesn't actually exist there. Per issue #57
subtask 4.5.18.2 (LOW), correct the citation to point at the finding's actual
location, OR append the finding to `regression.jsonl` per project convention if
it belongs there, whichever keeps the doc's claim accurate and verifiable.

## Architecture discovery
- The finding in question is `docs/LLD/btree.md` lines 92-98 ("Known asymmetric
  self-healing property" of `Lookup`'s leaf-level `NextLeaf` move-right
  recovery), which cites "`.cdr/index/regression.jsonl`'s 4.5.12.3 finding".
- Confirmed via `.cdr/runs/2026-07-11/114-verification/verification.json`: the
  actual finding is `non_blocking_findings[0]` (id `F1`, severity low,
  `test_coverage_nuance`) from that run's verification of subtask 4.5.12.3
  (issue #50, commit `d747925`).
- Confirmed via grep of `.cdr/index/regression.jsonl` (line 142, run
  `166-verification`, subtask 4.5.12.7): a prior verification pass for issue
  #50 already independently discovered and documented this exact
  citation-precision defect, explicitly stating "regression.jsonl has zero
  entries for any of issue #50's 4.5.12.x subtasks" and recommending
  "Optionally file a tiny follow-up to append the 4.5.12.x subtasks'
  verification outcomes to regression.jsonl retroactively so the LLD's
  citation resolves to something real."
- Checked `regression.jsonl`'s established schema (e.g. line 142, entries with
  `date`/`run`/`issue`/`subtask`/`module`/`risk`/`verdict`/`summary`/
  `recommendation`/`tests`) to match conventions.

## Impact analysis
- Files touched: `docs/LLD/btree.md` (doc-only), `.cdr/index/regression.jsonl`
  (index append-only).
- No production code (Go engine, Python agents) touched. Zero build/test
  impact. Safe to run in parallel with any other in-flight subtask.

## Decision
Chosen route: **append the finding to `regression.jsonl`** (per the explicit
recommendation left by run `166-verification`, and per project convention that
`regression.jsonl` is the shared, cross-run index for verification findings),
then tighten the doc's citation to name the originating run explicitly so it
remains resolvable even if `regression.jsonl` is later reorganized.

## Validation matrix
| Check | Method | Result |
|---|---|---|
| Cited finding now exists in regression.jsonl under subtask 4.5.12.3 | `grep '"subtask": "4.5.12.3"' .cdr/index/regression.jsonl` | 1 match (new entry) |
| New entry is valid JSON matching existing schema | `python3 -m json.tool` on the appended line | passes |
| New entry's content is a faithful transcription of 114-verification's F1 finding | manual diff of summary/recommendation text | faithful (paraphrased, same claims) |
| Doc citation still renders correctly / no markdown breakage | manual read of `docs/LLD/btree.md` lines 92-100 | clean |
| No production code touched | `git status` / `git diff --stat` | only `docs/LLD/btree.md` + `.cdr/index/regression.jsonl` |

## Self-consistency (internal only — not verification)
- Re-read the edited `docs/LLD/btree.md` section and the appended
  `regression.jsonl` line to confirm they cross-reference consistently
  (run id `114-verification`, subtask `4.5.12.3`).
- Confirmed `git diff --stat` shows exactly the two intended files.
- This is NOT verification; `/cdr:verify --subtask 4.5.18.2` must still run.
