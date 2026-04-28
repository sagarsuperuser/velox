.PHONY: build run test lint migrate dev clean cli gen gen-go gen-ts

# Build the velox binary
build:
	go build -o bin/velox ./cmd/velox

# Build the operator CLI binary (Week 7)
cli:
	go build -o bin/velox-cli ./cmd/velox-cli

# Run the server locally
run: build
	./bin/velox

# Run server locally — loads .env if present
# Setup: cp .env.example .env && edit .env
-include .env
export

dev:
	go run ./cmd/velox

# Run all tests
test:
	go test ./... -count=1

# Run tests with short flag (unit only, sequential to avoid env var leaks)
test-unit:
	go test -p 1 ./... -short -count=1

# Run tests verbose (for debugging)
test-verbose:
	go test ./... -v -short -count=1

# Run linter
lint:
	golangci-lint run ./...

# Run benchmarks
bench:
	go test ./internal/domain/ -bench=. -benchmem -count=1

# Tidy dependencies
tidy:
	go mod tidy

# Migration management
migrate:
	go run ./cmd/velox migrate

migrate-status:
	go run ./cmd/velox migrate status

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

# Code generation from the OpenAPI spec at api/openapi.yaml — see
# docs/dev/openapi-workflow.md. CI runs `make gen` followed by
# `git diff --exit-code` so any drift between the spec and the
# committed generated artifacts fails the build (same pattern Stripe
# uses internally with its openapi repo). Run locally after editing
# the spec; commit the regenerated files alongside the spec change.
gen: gen-go gen-ts

# Generate Go server interface + DTO types from api/openapi.yaml.
# oapi-codegen is pinned in tools/tools.go and resolved through
# go.mod, so the version is reproducible and bumps land like any
# other dep change.
gen-go:
	go run -tags tools github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen \
		--config api/cfg.yaml api/openapi.yaml

# Generate TypeScript types (openapi-typescript) and typed
# react-query hooks (orval) into web-v2/src/lib/{api.gen.ts,
# queries.gen.ts}. Also refreshes web-v2/public/openapi.yaml so the
# /docs/api Scalar viewer serves the canonical spec.
gen-ts:
	cd web-v2 && npm run gen

# Frontend
web-install:
	cd web-v2 && npm install

web-dev:
	cd web-v2 && npm run dev

web-build:
	cd web-v2 && npm run build

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
	@printf "  API routes:   ~76\n"
	@printf "  TS/TSX files: %s\n" "$$(find ./web-v2/src -name '*.ts' -o -name '*.tsx' 2>/dev/null | wc -l | tr -d ' ')"
	@echo ""
	@printf "  Tests:        " && go test ./... -short -count=1 2>&1 | grep -c "^ok" && echo ""
	@echo "━━━━━━━━━━━━━━━━━━━━━━━━━━"
