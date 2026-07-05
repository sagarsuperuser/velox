# Build stage
# Pin the exact patch (not the floating 1.25 tag) so the shipped binary always
# carries the patched stdlib — the build cache otherwise reuses an older 1.25.x
# layer and the image ships CVEs the Grype gate (critical) then rejects.
FROM golang:1.25.11-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# VERSION is stamped by the CI release job (git tag) or defaults to "dev".
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/sagarsuperuser/velox/internal/version.Version=${VERSION}" \
    -o /velox ./cmd/velox

# Runtime stage — distroless has no shell, no package manager, minimal CVE surface
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/sagarsuperuser/velox"
LABEL org.opencontainers.image.description="Velox — usage-based billing engine"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=builder /velox /usr/local/bin/velox
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

EXPOSE 8080

ENTRYPOINT ["velox"]
