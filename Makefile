# Alcove — dev tasks. `make check` runs everything CI runs that needs no Docker.
#
# There is no `make` in Git Bash on Windows by default. This file is for CI and
# Linux; on the Windows dev box use `.\bootstrap.ps1`, which runs the same steps.
# See SETUP.md.

SHELL := /bin/bash
BIN   := bin/alcove
PKG   := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)

.DEFAULT_GOAL := check

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Compile to ./bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/alcove

.PHONY: test
test: ## Run the test suite (with the race detector)
	go test -race $(PKG)

.PHONY: lint
lint: ## go vet + gofmt check
	go vet $(PKG)
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@echo "lint OK"

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	go mod tidy

# Guards live in scripts/guards.sh so the Makefile, bootstrap.ps1 and CI all run
# the same check (principle 4). Do not reimplement a guard here.
.PHONY: guards
guards: ## Run all constraint guards
	bash scripts/guards.sh all

.PHONY: check
check: lint test guards ## Everything CI runs that does not need Docker

.PHONY: clean
clean:
	rm -rf bin/
