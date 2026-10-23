# syntax=docker/dockerfile:1
# Builds one service of the workspace:
#   docker build --build-arg SERVICE=apps/notes/gateway -f deploy/docker/Dockerfile .
ARG GO_IMAGE=golang:1.27
ARG RUNTIME_IMAGE=debian:trixie-slim

FROM ${GO_IMAGE} AS build
ARG SERVICE
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test -n "${SERVICE}" && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./${SERVICE}/cmd/server

FROM ${RUNTIME_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --no-create-home app
COPY --from=build /out/server /app/server
USER app
ENTRYPOINT ["/app/server"]
