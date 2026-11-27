SHELL := /bin/bash
.DEFAULT_GOAL := help

MODULE := github.com/rcss-labs/yamdrake
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)
BINARY_PACKAGES := orchestrator runner

# Compose builds read VERSION and COMMIT from the environment (see compose.yaml).
export VERSION COMMIT

.PHONY: help build lint test dev-up dev-down test-integration test-e2e images smoke web-install web-lint web-test web-build

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

build: ## Build yumdrake-orchestrator, yumdrake-runner and the yumdrake CLI into bin/
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/yumdrake ./cmd/yumdrake
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
# Exported (not written into the recipe) so make never echoes the URL.
export YUMDRAKE_TEST_DATABASE_URL YUMDRAKE_TEST_VALKEY_ADDR

test-integration: ## Run integration tests against the local services (see dev-up)
	go test -race -tags integration ./...

test-e2e: ## Run the end-to-end tests (in-process orchestrator, fake GitHub) against the local services
	go test -race -tags e2e -timeout 3m ./test/e2e/...

images: ## Build the yumdrake-orchestrator and yumdrake-runner images (tag :dev)
	@for svc in orchestrator runner; do \
		docker build -f deploy/docker/$$svc.Dockerfile \
			--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" \
			-t yumdrake-$$svc:dev . || exit 1; \
	done

smoke: ## Build and start the app profile in Compose and check it is healthy
	./scripts/smoke.sh

web-install: ## Install the web application's dependencies from the lockfile
	npm --prefix web ci

web-lint: ## Lint and type-check the web application
	npm --prefix web run lint

web-test: ## Run the web application's unit tests once
	npm --prefix web run test

web-build: ## Build the web application for production into web/dist
	npm --prefix web run build
