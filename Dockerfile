# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /velox ./cmd/velox

# Runtime stage
FROM alpine:3.21

LABEL org.opencontainers.image.source="https://github.com/sagarsuperuser/velox"
LABEL org.opencontainers.image.description="Velox — usage-based billing engine"
LABEL org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata curl \
    && adduser -D -u 1000 velox

COPY --from=builder /velox /usr/local/bin/velox
COPY --from=builder /app/internal/invoice/fonts/ /usr/local/share/velox/fonts/

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8080/health/ready || exit 1

USER velox
EXPOSE 8080

ENTRYPOINT ["velox"]
