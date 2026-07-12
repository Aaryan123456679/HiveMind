# deploy/engine.Dockerfile -- GitHub issue #31 subtask 6.2.1.
#
# Containerizes engine/cmd/smokeserver (the only buildable engine/ binary in the repo:
# real, not faked, engine/catalog + engine/graph + engine/btree + engine/wal storage,
# wired to engine/rpc.NewServer over a real gRPC server -- see .cdr/runs/2026-07-12/
# 021-implementation/architecture-discovery.md for why this is the correct entrypoint).
#
# Build context MUST be the repo root (not deploy/ or engine/ alone), e.g.:
#   docker build -f deploy/engine.Dockerfile -t hivemind-engine .
# engine/ is its own Go module (engine/go.mod), so only engine/ needs to be copied into
# the builder stage -- api/ is intentionally NOT copied here (unlike api.Dockerfile),
# since the engine binary has no dependency on api/.

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY engine/ ./engine/
WORKDIR /src/engine
RUN go build -o /out/smokeserver ./cmd/smokeserver

FROM alpine:3.20
# alpine's busybox ships an `nc` applet by default -- used only by the HEALTHCHECK below,
# no extra package install needed (see architecture-discovery.md's health-check decision:
# engine is gRPC-only, so a dependency-free TCP-connect check stands in for curl).
RUN mkdir -p /data
COPY --from=builder /out/smokeserver /usr/local/bin/smokeserver
EXPOSE 50051
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD nc -z 127.0.0.1 50051 || exit 1
ENTRYPOINT ["smokeserver", "-root", "/data", "-addr", "0.0.0.0:50051"]
