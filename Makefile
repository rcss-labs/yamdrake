SHELL := /bin/bash
.DEFAULT_GOAL := help

MODULE := github.com/rcss-labs/yamdrake
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)
BINARY_PACKAGES := orchestrator runner

# Compose builds read VERSION and COMMIT from the environment (see compose.yaml).
export VERSION COMMIT

.PHONY: help build lint test dev-up dev-down test-integration images smoke

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

build: ## Build yumdrake-orchestrator and yumdrake-runner into bin/
	@mkdir -p bin
	@for pkg in $(BINARY_PACKAGES); do \
		if [ -d "cmd/$$pkg" ]; then \
			go build -ldflags "$(LDFLAGS)" -o "bin/yumdrake-$$pkg" "./cmd/$$pkg" || exit 1; \
		else \
			echo "skipping cmd/$$pkg (directory does not exist yet)"; \
		fi; \
	done

lint: ## Check gofmt, go vet and golangci-lint
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint is not on PATH. Install it: brew install golangci-lint (https://golangci-lint.run/welcome/install/)" >&2; \
		exit 2; }
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt reports unformatted files (run gofmt -w on them):" >&2; \
		echo "$$unformatted" >&2; \
		exit 1; \
	fi
	go vet ./...
	golangci-lint run

test: ## Run unit tests with the race detector
	go test -race ./...

dev-up: ## Start local PostgreSQL and Valkey and wait until healthy
	docker compose up -d --wait postgres valkey

dev-down: ## Stop local services and delete their volumes
	docker compose down -v

# Defaults point at the dev stack; CI and others may set these in the environment.
YUMDRAKE_TEST_DATABASE_URL ?= postgres://yumdrake:yumdrake@localhost:5432/yumdrake?sslmode=disable
YUMDRAKE_TEST_VALKEY_ADDR ?= localhost:6379

test-integration: ## Run integration tests against the local services (see dev-up)
	YUMDRAKE_TEST_DATABASE_URL='$(YUMDRAKE_TEST_DATABASE_URL)' \
	YUMDRAKE_TEST_VALKEY_ADDR='$(YUMDRAKE_TEST_VALKEY_ADDR)' \
	go test -race -tags integration ./...

images: ## Build the yumdrake-orchestrator and yumdrake-runner images (tag :dev)
	@for svc in orchestrator runner; do \
		docker build -f deploy/docker/$$svc.Dockerfile \
			--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" \
			-t yumdrake-$$svc:dev . || exit 1; \
	done

smoke: ## Build and start the app profile in Compose and check it is healthy
	./scripts/smoke.sh
