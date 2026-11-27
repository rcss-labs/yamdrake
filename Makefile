SHELL := /bin/bash
.DEFAULT_GOAL := help

MODULE := github.com/rcss-labs/yamdrake
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)
BINARY_PACKAGES := orchestrator runner

.PHONY: help build lint test dev-up dev-down test-integration

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

dev-up: ## Start local dependencies (stub until Task 3)
	@echo "make dev-up: not implemented yet (Task 3)" >&2
	@exit 1

dev-down: ## Stop local dependencies (stub until Task 3)
	@echo "make dev-down: not implemented yet (Task 3)" >&2
	@exit 1

test-integration: ## Run integration tests (stub until Task 3)
	@echo "make test-integration: not implemented yet (Task 3)" >&2
	@exit 1
