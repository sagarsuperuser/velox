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

# Bootstrap: create demo tenant + API keys
bootstrap:
	DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
	go run ./cmd/velox-bootstrap

# Run demo script (requires: make up, make bootstrap, make dev)
demo:
	@echo "Run: ./scripts/demo.sh <YOUR_SECRET_KEY>"

# Integration tests (requires running postgres)
test-integration:
	go test -p 1 ./... -v -count=1 -short=false

# Docker compose for local dev
up:
	docker compose up -d

down:
	docker compose down

# Project stats
stats:
	@echo "━━━ Velox Project Stats ━━━"
	@echo ""
	@printf "  Go files:     %s\n" "$$(find . -name '*.go' -not -path './vendor/*' | wc -l | tr -d ' ')"
	@printf "  Go lines:     %s\n" "$$(find . -name '*.go' -not -path './vendor/*' -exec cat {} + | wc -l | tr -d ' ')"
	@printf "  Test files:   %s\n" "$$(find . -name '*_test.go' | wc -l | tr -d ' ')"
	@printf "  SQL files:    %s\n" "$$(find . -name '*.sql' | wc -l | tr -d ' ')"
	@printf "  Packages:     %s\n" "$$(find ./internal -type d | wc -l | tr -d ' ')"
	@printf "  API routes:   ~69\n"
	@echo ""
	@printf "  Tests:        " && go test ./... -short -count=1 2>&1 | grep -c "^ok" && echo ""
	@echo "━━━━━━━━━━━━━━━━━━━━━━━━━━"
