# deploy/api.Dockerfile -- GitHub issue #31 subtask 6.2.1.
#
# Containerizes api/ (the HTTP gateway: auth/routing to the engine's gRPC server and the
# agents/ query service; see api/main.go).
#
# Build context MUST be the repo root, e.g.:
#   docker build -f deploy/api.Dockerfile -t hivemind-api .
# api/ and engine/ are SEPARATE Go modules (api/go.mod, engine/go.mod) joined only via
# go.work at the repo root -- api/main.go imports
# github.com/Aaryan123456679/HiveMind/engine/rpc/gen, so both trees plus go.work/go.work.sum
# must be present in the builder stage for `go build` to resolve the workspace locally
# instead of trying to fetch engine/ as a remote module (see architecture-discovery.md).

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.work go.work.sum ./
COPY api/ ./api/
COPY engine/ ./engine/
RUN go build -o /out/api ./api

FROM alpine:3.20
COPY --from=builder /out/api /usr/local/bin/api
ENV PORT=8080
EXPOSE 8080
# alpine's busybox ships `wget` by default -- used only for this HEALTHCHECK, avoiding an
# extra curl package install just for the container-internal check (routes.HealthHandler,
# api/routes/query.go, added by this same subtask, registered at GET /health).
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["api"]
