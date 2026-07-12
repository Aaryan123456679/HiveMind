#!/usr/bin/env bash
# deploy/smoke-test.sh -- GitHub issue #31 subtask 6.2.2's scripted smoke check.
#
# Brings up all four compose services, waits for them to report healthy, curls api/'s
# /health route and ui/'s root page, then always tears down (pass or fail). Matches the
# subtask's test spec verbatim: "docker compose up followed by a scripted smoke check (curl
# api/ health route, load ui/ root page) confirms all services are reachable."
#
# Usage: ./deploy/smoke-test.sh   (run from anywhere; paths below are script-relative)

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"
API_HEALTH_URL="http://localhost:8080/health"
UI_ROOT_URL="http://localhost:8081/"
HEALTH_WAIT_BUDGET_SECS=120
POLL_INTERVAL_SECS=3

FAILED=0

cleanup() {
  echo "== tearing down (docker compose down -v) =="
  docker compose -f "${COMPOSE_FILE}" down -v
}
trap cleanup EXIT

echo "== docker compose up -d --build =="
if ! docker compose -f "${COMPOSE_FILE}" up -d --build; then
  echo "FAIL: docker compose up did not start cleanly"
  docker compose -f "${COMPOSE_FILE}" logs
  exit 1
fi

echo "== waiting for all services to report healthy (budget: ${HEALTH_WAIT_BUDGET_SECS}s) =="
elapsed=0
while true; do
  # Count services whose compose-reported State/Health is not yet "healthy".
  unhealthy=$(docker compose -f "${COMPOSE_FILE}" ps --format '{{.Service}} {{.Health}}' \
    | awk '$2 != "healthy" { print }' | wc -l | tr -d ' ')

  if [ "${unhealthy}" -eq 0 ]; then
    echo "all services healthy after ${elapsed}s"
    break
  fi

  if [ "${elapsed}" -ge "${HEALTH_WAIT_BUDGET_SECS}" ]; then
    echo "FAIL: services did not become healthy within ${HEALTH_WAIT_BUDGET_SECS}s"
    docker compose -f "${COMPOSE_FILE}" ps
    docker compose -f "${COMPOSE_FILE}" logs
    exit 1
  fi

  sleep "${POLL_INTERVAL_SECS}"
  elapsed=$((elapsed + POLL_INTERVAL_SECS))
done

docker compose -f "${COMPOSE_FILE}" ps

echo "== curl api/ health route: ${API_HEALTH_URL} =="
api_body="$(curl -fsS "${API_HEALTH_URL}")" || { echo "FAIL: api /health unreachable"; FAILED=1; }
if [ "${FAILED}" -eq 0 ]; then
  echo "api /health responded: ${api_body}"
fi

echo "== load ui/ root page: ${UI_ROOT_URL} =="
ui_body="$(curl -fsS "${UI_ROOT_URL}")" || { echo "FAIL: ui root page unreachable"; FAILED=1; }
if [ "${FAILED}" -eq 0 ]; then
  if echo "${ui_body}" | grep -q '<div id="root">'; then
    echo "ui root page responded with the built SPA shell (found <div id=\"root\">)"
  else
    echo "FAIL: ui root page did not contain expected SPA markup"
    FAILED=1
  fi
fi

if [ "${FAILED}" -ne 0 ]; then
  echo "== SMOKE TEST FAILED -- dumping logs for diagnosis =="
  docker compose -f "${COMPOSE_FILE}" logs
  exit 1
fi

echo "== SMOKE TEST PASSED: engine, api, agents, ui all reachable =="
exit 0
