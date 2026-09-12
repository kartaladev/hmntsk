MODULES := . store/sqlcore store/sql store/pgx store/gorm \
           transport/core transport/http transport/gin transport/fiber \
           storetest transporttest

# RELEASE_ORDER is the order the modules must be tagged in: a module can only
# be released once everything it depends on has a version to require. The list
# is documented in docs/releasing.md, and a test asserts that the two agree.
RELEASE_ORDER := . store/sqlcore storetest transport/core transporttest \
                 store/sql store/pgx store/gorm \
                 transport/http transport/gin transport/fiber

GO ?= go
GOLANGCI_LINT ?= golangci-lint

# golangci-lint and govulncheck both type-check the standard library from
# source, so neither can read a toolchain newer than the one it was itself built
# with. Pin them to the module baseline (go.mod's `go` directive) so they stay
# usable when the developer's default toolchain runs ahead of them.
TOOL_TOOLCHAIN ?= go1.26.8
TOOL_ENV := GOTOOLCHAIN=$(TOOL_TOOLCHAIN)

.PHONY: all build lint fmt test test-integration test-race tidy vuln generate \
        store-matrix transport-matrix release-order clean

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
		(cd $$m && $(TOOL_ENV) $(GOLANGCI_LINT) run ./...); \
	done

## fmt: apply the configured formatters in place.
fmt:
	@set -e; for m in $(MODULES); do \
		(cd $$m && $(TOOL_ENV) $(GOLANGCI_LINT) fmt ./...); \
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
		(cd $$m && $(TOOL_ENV) govulncheck ./...); \
	done

## generate: regenerate mocks and other generated code.
generate:
	@set -e; for m in $(MODULES); do \
		(cd $$m && $(GO) generate ./...); \
	done

# STORE_MATRIX is the seven valid driver-by-dialect combinations. It is sparse
# because pgx is PostgreSQL-only: seven, not nine.
STORE_MATRIX := store/sql:TestStoreOnPostgres store/sql:TestStoreOnMySQL store/sql:TestStoreOnSQLite \
                store/pgx:TestStoreOnPostgres \
                store/gorm:TestStoreOnPostgres store/gorm:TestStoreOnMySQL store/gorm:TestStoreOnSQLite

# TRANSPORT_MATRIX is the three framework bindings, each running the shared
# transport suite.
TRANSPORT_MATRIX := transport/http transport/gin transport/fiber

## store-matrix: run the storage conformance suite over all seven combinations.
store-matrix:
	@set -e; for entry in $(STORE_MATRIX); do \
		m=$${entry%%:*}; run=$${entry##*:}; \
		echo "==> $$m $$run"; \
		(cd $$m && $(GO) test -count=1 -timeout 30m -run "^$$run$$" ./...); \
	done

## transport-matrix: run the transport conformance suite over all three bindings.
transport-matrix:
	@set -e; for m in $(TRANSPORT_MATRIX); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) test -count=1 -timeout 15m ./...); \
	done

## release-order: print the order the modules must be tagged in.
release-order:
	@for m in $(RELEASE_ORDER); do echo $$m; done

clean:
	$(GO) clean -cache -testcache
