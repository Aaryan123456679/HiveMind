# Requirement — Subtask 6.2.1 (GitHub issue #31)

Source: `gh issue view 31` (milestone #8 "Phase 6: Demo + deployment + load tests").

## Subtask
**6.2.1 — Dockerfiles for engine (Go), api (Go), and agents (Python) services**

## Acceptance criteria
Each of the three services builds a working container image via its Dockerfile, and the
container starts and responds to a basic health check.

## Test spec
`docker build` succeeds for each Dockerfile; `docker run` + a basic health-check curl/script
confirms each service starts.

## Impacted modules
- `deploy/engine.Dockerfile`
- `deploy/api.Dockerfile`
- `deploy/agents.Dockerfile`

## Out of scope (belongs to later subtasks in the same issue, not this one)
- 6.2.2 — `deploy/docker-compose.yml` wiring all 4 services (engine, api, agents, ui) together.
- 6.2.3 — Deploy to a real target host (blocked on OQ-3, hosting platform undecided).

This subtask is scoped strictly to the three Dockerfiles + whatever minimal health-check
surface each service needs to satisfy "container starts and responds to a basic health
check" for standalone `docker run`, not networked compose wiring.
