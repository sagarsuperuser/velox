# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /velox ./cmd/velox

# Runtime stage — distroless has no shell, no package manager, minimal CVE surface
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/sagarsuperuser/velox"
LABEL org.opencontainers.image.description="Velox — usage-based billing engine"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=builder /velox /usr/local/bin/velox
COPY --from=builder /app/internal/invoice/fonts/ /usr/local/share/velox/fonts/
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

EXPOSE 8080

ENTRYPOINT ["velox"]
