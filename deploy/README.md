# deploy/

Dockerfiles/compose for the Go API+engine and Python agent service, plus CI
config. Target: a single small cloud VM/container service (single-node
scope matches the storage engine's concurrency design — no multi-node
orchestration needed).
