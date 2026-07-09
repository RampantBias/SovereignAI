# Use an official Go Linux image (which includes gcc)
FROM golang:1.22-bookworm AS builder

WORKDIR /app
# Copy your go.mod, go.sum, and source code
COPY . .

RUN go mod download

# Compile the binary natively within the Linux container
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o /daemonset main.go

# Create the final, minimal runtime image
FROM debian:bookworm-slim
# The daemonset requires the NVIDIA runtime and libraries mounted from the host,
# so a standard debian base is sufficient here.
COPY --from=builder /daemonset /usr/local/bin/daemonset

ENTRYPOINT ["/usr/local/bin/daemonset"]