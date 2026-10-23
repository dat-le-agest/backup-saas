# syntax=docker/dockerfile:1
# The admin service with its dashboard (React, built here, embedded in the binary):
#   docker build -f deploy/docker/admin.Dockerfile .
ARG NODE_IMAGE=node:24-alpine
ARG GO_IMAGE=golang:1.27
ARG RUNTIME_IMAGE=debian:trixie-slim

FROM ${NODE_IMAGE} AS web
WORKDIR /web
COPY platform/admin/web/package.json platform/admin/web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY platform/admin/web/ ./
RUN npm run build

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY . .
COPY --from=web /web/dist/ platform/admin/web/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./platform/admin/cmd/server

FROM ${RUNTIME_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --no-create-home app
COPY --from=build /out/server /app/server
USER app
ENTRYPOINT ["/app/server"]
