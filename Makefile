.PHONY: build run test lint migrate dev clean

# Build the velox binary
build:
	go build -o bin/velox ./cmd/velox

# Run the server locally
run: build
	./bin/velox

# Run with live reload (requires air: go install github.com/air-verse/air@latest)
dev:
	DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
	RUN_MIGRATIONS_ON_BOOT=true \
	air -c .air.toml 2>/dev/null || \
	DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
	RUN_MIGRATIONS_ON_BOOT=true \
	go run ./cmd/velox

# Run all tests
test:
	go test ./... -v -count=1

# Run tests with short flag (unit only)
test-unit:
	go test ./... -v -short -count=1

# Run linter
lint:
	golangci-lint run ./...

# Tidy dependencies
tidy:
	go mod tidy

# Clean build artifacts
clean:
	rm -rf bin/

# Docker compose for local dev
up:
	docker compose up -d

down:
	docker compose down
