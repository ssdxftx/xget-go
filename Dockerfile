# Xget-Go — multi-stage Docker build, works directly from a fresh clone.
#
# Build:  docker build -t xget-go .
# Run:    docker run -d -p 127.0.0.1:8080:8080 \
#           -v xget-cache:/var/cache/xget \
#           --restart unless-stopped --name xget xget-go
# Note: no built-in auth (same as the official Xget) — bind to loopback and
# put a reverse proxy with auth in front, or firewall the port.
#
# Stage 1 compiles the static binary inside golang:1.23-alpine (zero
# third-party dependencies, so no module downloads are needed); stage 2
# produces a minimal runtime image with CA certificates for upstream HTTPS.
# Buildx/QEMU handle cross-arch automatically: Go inside the container
# targets the container platform natively.

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
COPY internal ./internal
ARG VERSION=v3.0.7
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/xget-go .

FROM alpine:3.20
# CA certificates are mandatory: every upstream connection is HTTPS.
RUN apk add --no-cache ca-certificates wget \
    && adduser -D -H -u 1000 xget \
    && mkdir -p /var/cache/xget \
    && chown -R xget:root /var/cache/xget
COPY --from=build /out/xget-go /usr/local/bin/xget-go
RUN chmod 0755 /usr/local/bin/xget-go

ENV PORT=8080
ENV CACHE_DIR=/var/cache/xget
ENV CACHE_MAX_MB=1024
ENV CACHE_MAX_FILE_MB=512
USER xget
EXPOSE 8080
VOLUME ["/var/cache/xget"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${PORT}/healthz" | grep -q ok || exit 1

ENTRYPOINT ["xget-go"]
