# Requirement — Subtask 4.5.19.3 (issue #58)

**Title:** (LOW) Fix CDR run-numbering race (replace "ls and pick next" with atomic reservation).

## Problem

The CDR workflow allocates run directories under `.cdr/runs/<date>/<NNN>-{implementation,verification,...}/`
by having each agent list existing directories and pick the next unused ordinal. This is a classic
TOCTOU (time-of-check-to-time-of-use) race: under concurrent agent dispatch, two agents can both
observe the same "highest existing number" and both write to the same `<NNN>-<agent>` directory,
silently overwriting one another's artifacts.

Concrete evidence of collisions in this session (2026-07-11), all confirmed by reading the actual
run metadata/verification files under `.cdr/runs/2026-07-11/`:

- `132-verification` was overwritten: contains artifacts that don't reconcile cleanly between issue
  #47 and issue #52 4.5.14.1 (two different subtasks' verification runs landed on the same ordinal).
- `138-verification` was overwritten: 4.5.14.2 vs 4.5.14.3 verification runs collided on the same
  ordinal, evidenced by identical `started_at` timestamps in the surviving metadata.
- `065-implementation`/`087-verification` show a duplicate-verification split for the same commit
  `75203e0` (the same commit got verified twice under two different run directories because the
  allocator picked two different numbers non-deterministically, or two agents disagreed on "the
  next number" mid-race).
- `140-verification/verification.json` documents a first-hand reconstruction of one such collision
  (`orchestrator_note` records that the verifier had to reconstruct context because a concurrent
  writer had already touched the same ordinal).

## Root cause

Grep across the actual CDR skill/runtime source (`/Users/aaryanmahajan/Main/cdr-runtime/agents/cdr/*.md`,
`/Users/aaryanmahajan/Main/cdr-runtime/commands/cdr/*.md`) shows the run-directory allocation rule is
**purely informal convention described in markdown**, repeated verbatim across
`implementation.md`, `verification.md`, `planner.md`, `compaction.md`, `commit.md`:

> "Create a run dir: `.cdr/runs/$(date +%F)/<NNN>-<agent>/` where NNN is the next zero-padded ordinal
> for today."

There is no dedicated script or program anywhere in `cdr-runtime/` or in this repo's `.cdr/` /
`scripts/` / `tools/` that performs this allocation atomically — each agent instance independently
runs the equivalent of `ls .cdr/runs/<date> | sort | tail -1`, computes `NNN+1`, and `mkdir`s it.
When two agent instances run this sequence concurrently, both can compute the same `NNN` before
either has created the directory, and one `mkdir` (or subsequent file write) silently clobbers the
other.

## Acceptance criteria (from macro)

1. Run-directory allocation no longer relies on "ls then pick next number".
2. Implement an atomic reservation mechanism (e.g. `mkdir`-based lock per candidate number retried
   on `EEXIST`, since `mkdir` is atomic on POSIX filesystems; or an `O_EXCL`-guarded monotonic
   counter file) usable by both the implementation and verification dispatch paths (and, ideally,
   all other CDR agents that allocate run dirs: planner, compaction, commit, documentation, push).
3. Since the current mechanism is informal markdown convention with no dedicated code, implement it
   as a small reusable script (shell, since the CDR tooling here is polyglot/agent-orchestration
   glue rather than part of the Go engine or Python agents codebase) that both `/cdr:implement` and
   `/cdr:verify` (and the other CDR agents) invocations should call. Update the relevant
   skill/documentation to reference it.
4. Test spec: a script/test that spawns N (>=20) concurrent simulated run-directory allocation
   calls and asserts zero collisions (each gets a unique number) across all N runs.

## Scope boundary

The actual skill markdown files that describe the 9-step workflow live in a separate, global
installation directory (`/Users/aaryanmahajan/Main/cdr-runtime`, a distinct git repository installed
to `~/.claude` and shared across all projects), not inside this repo. This subtask's deliverable
inside the HiveMind repo is the reusable allocator script + its test, committed here since `.cdr/`
is tracked in this repo and the allocator is naturally project-scoped (it operates on `.cdr/runs/`
of whichever project invokes it). The cdr-runtime skill markdown files are updated as a
courtesy/pointer update in the same change where possible, but are not part of the HiveMind git
history (separate repo) and are not gated by this repo's commit.
