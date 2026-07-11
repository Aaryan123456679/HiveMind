#!/usr/bin/env bash
# reserve-run-dir.sh — atomically reserve the next CDR run directory.
#
# Replaces the old, racy "ls .cdr/runs/<date> | sort | pick next number" convention
# with an atomic mkdir-based reservation loop. mkdir(2) is atomic on POSIX filesystems:
# when two processes race to create the same path, exactly one succeeds and the other
# gets EEXIST. This script uses that guarantee directly, so it is safe to call
# concurrently from multiple independent agent processes with no external locking.
#
# Usage:
#   reserve-run-dir.sh <agent-suffix> [base-dir] [start-hint] [max-attempts]
#
#   agent-suffix   Required. e.g. implementation, verification, planner, compaction,
#                  commit, documentation, push. Directory created is
#                  "<NNN>-<agent-suffix>" inside <base-dir>/<today>/.
#   base-dir       Optional. Defaults to ".cdr/runs". The date subdirectory
#                  (YYYY-MM-DD, UTC) is appended automatically.
#   start-hint     Optional. Candidate number to start probing from. Defaults to
#                  (highest existing NNN across all agent-suffixes for today) + 1,
#                  or 1 if none exist. This is ONLY a performance hint to avoid
#                  retrying from 1 every time — it does not affect correctness,
#                  because the actual winner for any given NNN is decided by mkdir,
#                  not by this scan.
#   max-attempts   Optional. Defaults to 10000. Safety bound on retries.
#
# On success: prints the reserved directory path (relative to cwd, matching
# base-dir's form) to stdout and exits 0. The directory is guaranteed to exist
# and to have been created by THIS invocation (not a pre-existing directory).
# On failure: prints a diagnostic to stderr and exits 1.
#
# Example:
#   run_dir="$(.cdr/tools/reserve-run-dir.sh implementation)"
#   # run_dir == .cdr/runs/2026-07-11/6301-implementation (first free slot)

set -u

agent_suffix="${1:-}"
base_dir="${2:-.cdr/runs}"
start_hint="${3:-}"
max_attempts="${4:-10000}"

if [ -z "$agent_suffix" ]; then
  echo "usage: reserve-run-dir.sh <agent-suffix> [base-dir] [start-hint] [max-attempts]" >&2
  exit 1
fi

today="$(date -u +%Y-%m-%d)"
date_dir="${base_dir%/}/${today}"

mkdir -p "$date_dir" 2>/dev/null

# Determine a starting candidate. This is a hint only: correctness comes from the
# mkdir-and-retry loop below, not from this scan, so a stale/racy read here cannot
# cause a collision — at worst it costs a few extra EEXIST retries.
if [ -z "$start_hint" ]; then
  highest=0
  if [ -d "$date_dir" ]; then
    for entry in "$date_dir"/*-*; do
      [ -e "$entry" ] || continue
      base="$(basename "$entry")"
      num="${base%%-*}"
      case "$num" in
        ''|*[!0-9]*) continue ;;
      esac
      # strip leading zeros for numeric comparison
      num_stripped=$((10#$num))
      if [ "$num_stripped" -gt "$highest" ]; then
        highest=$num_stripped
      fi
    done
  fi
  candidate=$((highest + 1))
else
  case "$start_hint" in
    ''|*[!0-9]*)
      echo "start-hint must be a non-negative integer, got: $start_hint" >&2
      exit 1
      ;;
  esac
  candidate=$((10#$start_hint))
fi

attempts=0
while [ "$attempts" -lt "$max_attempts" ]; do
  # Zero-pad to at least 3 digits; numbers >= 1000 are used as-is (matches existing
  # convention in this repo's .cdr/runs/ history, e.g. 1300-implementation).
  padded=$(printf '%03d' "$candidate")
  dir="${date_dir}/${padded}-${agent_suffix}"

  if mkdir "$dir" 2>/dev/null; then
    echo "$dir"
    exit 0
  fi

  # EEXIST (or any other mkdir failure for this specific path): another process won
  # this candidate, or the path is otherwise unusable. Try the next number.
  candidate=$((candidate + 1))
  attempts=$((attempts + 1))
done

echo "reserve-run-dir.sh: exhausted $max_attempts attempts starting from candidate without reserving a slot under $date_dir" >&2
exit 1
