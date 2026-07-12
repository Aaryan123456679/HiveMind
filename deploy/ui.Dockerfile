# deploy/ui.Dockerfile -- GitHub issue #31 subtask 6.2.2.
#
# Containerizes ui/ (the Vite/React dashboard, see ui/package.json) as a multi-stage build:
# stage 1 compiles the static bundle with Node, stage 2 serves it (plus reverse-proxies API
# calls to the `api` service, see deploy/nginx.conf) via nginx -- no Node runtime needed in
# the final image, matching the other three services' small-image precedent
# (deploy/engine.Dockerfile, deploy/api.Dockerfile, deploy/agents.Dockerfile all use minimal
# alpine-based final stages).
#
# Build context MUST be repo root (consistent with the other three Dockerfiles), e.g.:
#   docker build -f deploy/ui.Dockerfile -t hivemind-ui .

FROM node:20-alpine AS builder
WORKDIR /src
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
# `npm run build` runs `tsc -b && vite build` (ui/package.json) -- type-checks the whole
# ui/src tree (ui/tsconfig.json's `include: ["src"]`) before producing the static bundle
# under ui/dist.
RUN npm run build

FROM nginx:1.27-alpine
COPY --from=builder /src/dist /usr/share/nginx/html
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
# nginx:alpine ships wget (busybox) but not curl -- same dependency-free HEALTHCHECK
# convention as deploy/api.Dockerfile's wget-based check.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:80/ >/dev/null || exit 1
