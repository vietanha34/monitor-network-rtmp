# Build stage: compile a static Linux binary from source.
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X main.version=${VERSION}" \
    -o /out/monitor-network-rtmp .

# Runtime stage: minimal image. Uses host network so the exporter can
# see the host's connections and conntrack table.
FROM alpine:3.20
RUN apk add --no-cache conntrack-tools iproute2 ca-certificates && \
    modprobe nf_conntrack 2>/dev/null || true
COPY --from=builder /out/monitor-network-rtmp /usr/local/bin/monitor-network-rtmp
EXPOSE 9101
# host network is required: ss + conntrack inspect the host's network namespace.
# Cap NET_ADMIN + NET_RAW are needed to read the conntrack table.
USER root
ENTRYPOINT ["/usr/local/bin/monitor-network-rtmp"]
CMD ["--target-port", "1935", "--listen-address", ":9101"]
