FROM golang:1.26.2-alpine3.23 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/controller ./cmd/controller
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -o /out/cli ./cmd/cli
RUN CGO_ENABLED=0 go build -trimpath -o /out/agent-wrapper ./cmd/agent-wrapper
RUN CGO_ENABLED=0 go build -trimpath -o /out/reference-agent ./cmd/reference-agent
RUN CGO_ENABLED=0 go build -trimpath -o /out/utility-runner ./cmd/utility-runner
RUN CGO_ENABLED=0 go build -trimpath -o /out/smoke-agent ./cmd/smoke-agent
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp-server ./cmd/mcp-server
RUN CGO_ENABLED=0 go build -trimpath -o /out/artifact-collector ./cmd/artifact-collector
RUN CGO_ENABLED=0 go build -trimpath -o /out/artifact-bootstrap ./cmd/artifact-bootstrap

FROM gcr.io/distroless/static-debian12:nonroot AS controller
COPY --from=builder /out/controller /controller
ENTRYPOINT ["/controller"]

FROM gcr.io/distroless/static-debian12:nonroot AS api
COPY --from=builder /out/api /api
ENTRYPOINT ["/api"]

FROM gcr.io/distroless/static-debian12:nonroot AS cli
COPY --from=builder /out/cli /cli
ENTRYPOINT ["/cli"]

FROM gcr.io/distroless/static-debian12:nonroot AS agent-wrapper
COPY --from=builder /out/agent-wrapper /agent-wrapper
ENTRYPOINT ["/agent-wrapper"]

FROM gcr.io/distroless/static-debian12:nonroot AS reference-agent
COPY --from=builder /out/agent-wrapper /agent-wrapper
COPY --from=builder /out/reference-agent /reference-agent
ENTRYPOINT ["/agent-wrapper"]

FROM alpine:3.23 AS utility-runner
RUN apk add --no-cache ca-certificates git
COPY --from=builder /out/utility-runner /utility-runner
ENTRYPOINT ["/utility-runner"]

FROM gcr.io/distroless/static-debian12:nonroot AS smoke-agent
COPY --from=builder /out/agent-wrapper /agent-wrapper
COPY --from=builder /out/smoke-agent /smoke-agent
ENTRYPOINT ["/agent-wrapper"]

FROM gcr.io/distroless/static-debian12:nonroot AS mcp-server
COPY --from=builder /out/mcp-server /mcp-server
ENTRYPOINT ["/mcp-server"]

FROM gcr.io/distroless/static-debian12:nonroot AS artifact-collector
COPY --from=builder /out/artifact-collector /artifact-collector
ENTRYPOINT ["/artifact-collector"]

FROM gcr.io/distroless/static-debian12:nonroot AS artifact-bootstrap
COPY --from=builder /out/artifact-bootstrap /artifact-bootstrap
ENTRYPOINT ["/artifact-bootstrap"]
