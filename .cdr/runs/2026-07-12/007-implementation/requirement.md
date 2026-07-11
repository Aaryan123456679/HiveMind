# Requirement -- subtask 5.3.4 (issue #28)

"Corpus-growth-checkpoint degradation chart (20%/50%/100% ingested)"

## Acceptance criteria
Running all three arms at 20%, 50%, and 100% of the corpus ingested produces a degradation
chart of recall/precision over corpus growth -- the key novelty result of the project.

## Test spec
A scripted benchmark run (`agents/eval/run_benchmark.py --checkpoints 20,50,100`) executed
manually/CI-gated, producing a chart/data file; assert the output data file contains exactly
three checkpoints with well-formed metrics for all three arms.

## Impacted modules
`agents/eval/run_benchmark.py`, `agents/eval/chart.py`.

## Binding scoping constraint (this pass only)
CODE-ONLY. Local Ollama / mocked/fixture data only. No real live run against real
OpenRouter/Gemini APIs; no consumption of the user's OpenRouter/Gemini budget caps. The harness
must be built and fixture-tested, demonstrably correct, ready for a real run later -- but this
pass does not execute that real run. That is deferred to a separate orchestrator-run step once
a concrete cost estimate is computed.
