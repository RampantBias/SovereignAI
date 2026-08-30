# Syntax directive enables modern BuildKit caching standard features
# syntax=docker/dockerfile:1.7

FROM golang:1.26.2-alpine3.23 AS base
WORKDIR /src
ENV CGO_ENABLED=0

FROM base AS deps
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    --mount=type=bind,source=go.mod,target=go.mod \
    go mod download

FROM deps AS build-base
# Bind source code directly to prevent unnecessary image layer writes
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    true

# Parallel target builders
FROM build-base AS build-controller
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/controller ./cmd/controller

FROM build-base AS build-api
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/api ./cmd/api

FROM build-base AS build-cli
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/cli ./cmd/cli

FROM build-base AS build-agent-wrapper
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/agent-wrapper ./cmd/agent-wrapper

FROM build-base AS build-reference-agent
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/reference-agent ./cmd/reference-agent

FROM build-base AS build-utility-runner
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/utility-runner ./cmd/utility-runner

FROM build-base AS build-smoke-agent
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/smoke-agent ./cmd/smoke-agent

FROM build-base AS build-mcp-server
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/mcp-server ./cmd/mcp-server

FROM build-base AS build-artifact-collector
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/artifact-collector ./cmd/artifact-collector

FROM build-base AS build-artifact-bootstrap
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/artifact-bootstrap ./cmd/artifact-bootstrap

# Final runtime stages
FROM gcr.io/distroless/static-debian12:nonroot AS controller
COPY --from=build-controller /out/controller /controller
ENTRYPOINT ["/controller"]

FROM gcr.io/distroless/static-debian12:nonroot AS api
COPY --from=build-api /out/api /api
ENTRYPOINT ["/api"]

FROM gcr.io/distroless/static-debian12:nonroot AS cli
COPY --from=build-cli /out/cli /cli
ENTRYPOINT ["/cli"]

FROM gcr.io/distroless/static-debian12:nonroot AS agent-wrapper
COPY --from=build-agent-wrapper /out/agent-wrapper /agent-wrapper
ENTRYPOINT ["/agent-wrapper"]

FROM gcr.io/distroless/static-debian12:nonroot AS reference-agent
COPY --from=build-agent-wrapper /out/agent-wrapper /agent-wrapper
COPY --from=build-reference-agent /out/reference-agent /reference-agent
ENTRYPOINT ["/agent-wrapper"]

FROM alpine:3.23 AS utility-runner
RUN --mount=type=cache,target=/var/cache/apk \
    apk add ca-certificates git
COPY --from=build-utility-runner /out/utility-runner /utility-runner
ENTRYPOINT ["/utility-runner"]

FROM golang:1.26.2-alpine3.23 AS test-runner
RUN --mount=type=cache,target=/var/cache/apk \
    apk add ca-certificates git
WORKDIR /workspace

FROM moby/buildkit:v0.24.0-rootless AS build-runner
USER root
RUN --mount=type=cache,target=/var/cache/apk \
    apk add ca-certificates git
USER 1000:1000
WORKDIR /workspace

FROM gcr.io/distroless/static-debian12:nonroot AS smoke-agent
COPY --from=build-agent-wrapper /out/agent-wrapper /agent-wrapper
COPY --from=build-smoke-agent /out/smoke-agent /smoke-agent
ENTRYPOINT ["/agent-wrapper"]

FROM gcr.io/distroless/static-debian12:nonroot AS mcp-server
COPY --from=build-mcp-server /out/mcp-server /mcp-server
ENTRYPOINT ["/mcp-server"]

FROM gcr.io/distroless/static-debian12:nonroot AS artifact-collector
COPY --from=build-artifact-collector /out/artifact-collector /artifact-collector
ENTRYPOINT ["/artifact-collector"]

FROM gcr.io/distroless/static-debian12:nonroot AS artifact-bootstrap
COPY --from=build-artifact-bootstrap /out/artifact-bootstrap /artifact-bootstrap
ENTRYPOINT ["/artifact-bootstrap"]