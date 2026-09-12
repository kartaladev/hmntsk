MODULES := . store/sqlcore store/sql store/pgx store/gorm \
           transport/core transport/http transport/gin transport/fiber \
           storetest transporttest

GO ?= go
GOLANGCI_LINT ?= golangci-lint

# golangci-lint type-checks the standard library from source, so it cannot read
# a toolchain newer than the one it was itself built with. Pin lint and fmt to
# the module baseline (go.mod's `go` directive) so the linter stays usable when
# the developer's default toolchain runs ahead of it.
LINT_TOOLCHAIN ?= go1.26.8
LINT_ENV := GOTOOLCHAIN=$(LINT_TOOLCHAIN)

.PHONY: all build lint fmt test test-integration test-race tidy vuln generate clean

all: lint test

## build: compile every module in the workspace.
build:
	@set -e; for m in $(MODULES); do \
		echo "==> build $$m"; \
		(cd $$m && $(GO) build ./...); \
	done

## lint: run golangci-lint over every module in the workspace.
lint:
	@set -e; for m in $(MODULES); do \
		echo "==> lint $$m"; \
		(cd $$m && $(LINT_ENV) $(GOLANGCI_LINT) run ./...); \
	done

## fmt: apply the configured formatters in place.
fmt:
	@set -e; for m in $(MODULES); do \
		(cd $$m && $(LINT_ENV) $(GOLANGCI_LINT) fmt ./...); \
	done

## test: run unit tests (no external dependencies) in every module.
test:
	@set -e; for m in $(MODULES); do \
		echo "==> test $$m"; \
		(cd $$m && $(GO) test ./...); \
	done

## test-race: run unit tests with the race detector.
test-race:
	@set -e; for m in $(MODULES); do \
		echo "==> test -race $$m"; \
		(cd $$m && $(GO) test -race ./...); \
	done

## test-integration: the same tests, with the time and freshness a container run
## needs. There is deliberately no build tag: tests that provision a real
## database are part of `go test ./...`, because a tag is how integration tests
## end up broken for a fortnight without anyone noticing.
test-integration:
	@set -e; for m in $(MODULES); do \
		echo "==> integration $$m"; \
		(cd $$m && $(GO) test -count=1 -timeout 30m ./...); \
	done

## tidy: tidy every module and re-sync the workspace.
tidy:
	@set -e; for m in $(MODULES); do \
		(cd $$m && $(GO) mod tidy); \
	done
	$(GO) work sync

## vuln: scan every module for known vulnerabilities.
vuln:
	@set -e; for m in $(MODULES); do \
		echo "==> govulncheck $$m"; \
		(cd $$m && govulncheck ./...); \
	done

## generate: regenerate mocks and other generated code.
generate:
	@set -e; for m in $(MODULES); do \
		(cd $$m && $(GO) generate ./...); \
	done

clean:
	$(GO) clean -cache -testcache
