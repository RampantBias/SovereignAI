FROM golang:1.26.2-alpine3.23 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/calculator ./src

FROM gcr.io/distroless/static-debian12:nonroot AS calculator
COPY --from=builder /out/calculator /calculator
ENTRYPOINT ["/calculator"]