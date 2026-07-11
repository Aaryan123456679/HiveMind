# Architecture Discovery — 4.5.19.3

## Index-first pass

- `docs/HLD.md` and `docs/LLD/*.md` describe the HiveMind database engine (btree, wal, mvcc, graph,
  catalog, split, rpc, ingestion-agent, query-agent, llm-provider, eval). None of these documents
  describe the CDR meta-workflow tooling — the run-directory allocator is not part of the product
  architecture, it is CI/agent-orchestration infra local to `.cdr/`. No HLD/LLD update is required
  for this subtask.
- `.cdr/index/{decision,feature,file,regression,task}.jsonl` — checked for prior entries about
  run-directory allocation or CDR tooling; none found (this is the first dedicated fix for the
  allocator itself). `.cdr/cdr.config.json` has no allocator-related config keys today
  (`{"schema_version":"1.0","runtime":"1.x","macros":{},"retention":{"runs_days":30},"github":{...},"overrides":[]}`).
- No `handoffs/` or prior memory entries reference run-dir allocation.

## Targeted LLD / skill source

- `/Users/aaryanmahajan/Main/cdr-runtime/agents/cdr/implementation.md` (lines 12-15) and
  `/Users/aaryanmahajan/Main/cdr-runtime/agents/cdr/verification.md` (lines 14-17), plus
  `planner.md`, `compaction.md`, `commit.md`, `documentation.md` — all contain the identical
  informal instruction: "Create a run dir ... where NNN is the next zero-padded ordinal for today."
  There is no code backing this; it's prose in a markdown skill definition consumed by the agent's
  own reasoning at each invocation (i.e., each agent instance re-derives "the next number" from
  scratch via `ls`, with no shared lock).
- `commands/cdr/implement.md`, `commands/cdr/verify.md`, `commands/cdr/plan.md`, `commands/cdr/doc.md`,
  `commands/cdr/push.md` all reference `.cdr/runs/<date>/<NNN>-<agent>/...` output paths but do not
  specify *how* NNN is obtained beyond the agent-file prose above.
- No `scripts/`, `tools/`, or `bin/` directory exists in either `cdr-runtime` or this repo
  (`HiveMind`) prior to this change — confirmed via `find`/`ls`.

## Touched files (this change)

- New: `.cdr/tools/reserve-run-dir.sh` — atomic reservation helper (mkdir+EEXIST retry loop).
- New: `.cdr/tools/test-reserve-run-dir.sh` — concurrency test spawning N parallel reservations,
  asserting uniqueness.
- Doc update (informational, not part of HiveMind commit): pointer added to
  `cdr-runtime/agents/cdr/implementation.md` and `verification.md` (separate repo, applied
  out-of-band — see handoff for details) referencing the new script.

## Source read

- Read `.cdr/runs/2026-07-11/140-verification/verification.json` in full (see requirement.md for
  the extracted evidence) — confirms `orchestrator_note` documents a collision reconstruction.
- Confirmed via `git status`/`git log -- .cdr` that `.cdr/` is tracked in this repo (not
  gitignored), so the new helper script under `.cdr/tools/` will be committed normally.
