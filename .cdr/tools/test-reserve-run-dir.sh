#!/usr/bin/env bash
# test-reserve-run-dir.sh — concurrency test for reserve-run-dir.sh.
#
# Spawns N concurrent calls to reserve-run-dir.sh against an isolated temp directory
# (never touches the real .cdr/runs/) and asserts that all N calls succeed with
# distinct, actually-existing directories. This is the test spec required by
# subtask 4.5.19.3 / issue #58: "a script/test that spawns N (e.g. 20) concurrent
# simulated run-directory allocation calls and asserts zero collisions".
#
# Usage: test-reserve-run-dir.sh [N]   (default N=20)

set -u

N="${1:-20}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
reserve_script="${script_dir}/reserve-run-dir.sh"

if [ ! -x "$reserve_script" ]; then
  echo "FAIL: $reserve_script not found or not executable" >&2
  exit 1
fi

tmp_base="$(mktemp -d)"
cleanup() { rm -rf "$tmp_base"; }
trap cleanup EXIT

out_dir="$(mktemp -d)"
cleanup_out() { rm -rf "$out_dir"; }
trap 'cleanup; cleanup_out' EXIT

pids=()
for i in $(seq 1 "$N"); do
  ("$reserve_script" implementation "$tmp_base" > "${out_dir}/${i}.out" 2> "${out_dir}/${i}.err") &
  pids+=("$!")
done

fail=0
for pid in "${pids[@]}"; do
  wait "$pid" || fail=1
done

if [ "$fail" -ne 0 ]; then
  echo "FAIL: one or more reserve-run-dir.sh invocations exited non-zero" >&2
  for i in $(seq 1 "$N"); do
    if [ -s "${out_dir}/${i}.err" ]; then
      echo "  attempt $i stderr: $(cat "${out_dir}/${i}.err")" >&2
    fi
  done
  exit 1
fi

paths=()
for i in $(seq 1 "$N"); do
  p="$(cat "${out_dir}/${i}.out")"
  if [ -z "$p" ]; then
    echo "FAIL: attempt $i produced no output path" >&2
    exit 1
  fi
  if [ ! -d "$p" ]; then
    echo "FAIL: attempt $i reported path '$p' but it does not exist as a directory" >&2
    exit 1
  fi
  paths+=("$p")
done

unique_count="$(printf '%s\n' "${paths[@]}" | sort -u | wc -l | tr -d ' ')"

if [ "$unique_count" -ne "$N" ]; then
  echo "FAIL: expected $N unique run directories, got $unique_count. Collisions detected:" >&2
  printf '%s\n' "${paths[@]}" | sort | uniq -d >&2
  exit 1
fi

echo "PASS: $N concurrent reserve-run-dir.sh calls produced $unique_count unique run directories, zero collisions."
exit 0
