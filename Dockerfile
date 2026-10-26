# Xget-Go — Docker image build.
# Requires the prebuilt binaries from build.sh to be present in the build
# context (xget-linux-amd64 / xget-linux-arm64). The correct one is picked
# automatically via the TARGETARCH build arg supplied by BuildKit.
#
# Build:  docker build -t xget-go .
# Run:    docker run -d -p 8080:8080 \
#           -e ACCESS_TOKEN=$(openssl rand -hex 24) \
#           --restart unless-stopped --name xget xget-go

FROM alpine:3.20

# CA certificates are mandatory: every upstream connection is HTTPS.
RUN apk add --no-cache ca-certificates wget \
    && adduser -D -H -u 1000 xget

ARG TARGETARCH=amd64
COPY xget-linux-${TARGETARCH} /usr/local/bin/xget-go
RUN chmod 0755 /usr/local/bin/xget-go

ENV PORT=8080
USER xget
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${PORT}/healthz" | grep -q ok || exit 1

ENTRYPOINT ["xget-go"]
