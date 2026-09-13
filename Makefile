# The modules are grouped by the repository each will eventually live in.
# sqlkit and notify are built to move out before their first tag; hmntsk stays.
# A group is what a CI job per future repository runs, via GROUP.
HMNTSK_MODULES := . store/sqlcore store/sql store/pgx store/gorm \
                  transport/core transport/http transport/gin transport/fiber \
                  delivery/webhook delivery/redis delivery/nats \
                  storetest transporttest relaytest

SQLKIT_MODULES := sqlkit sqlkit/sqlkittest sqlkit/stdsql sqlkit/pgx sqlkit/gorm

NOTIFY_MODULES :=

# GROUP narrows every per-module target to one group: all, hmntsk, sqlkit or
# notify. `make test GROUP=sqlkit` runs only the sqlkit modules.
GROUP ?= all

ifeq ($(GROUP),all)
MODULES := $(HMNTSK_MODULES) $(SQLKIT_MODULES) $(NOTIFY_MODULES)
else ifeq ($(GROUP),hmntsk)
MODULES := $(HMNTSK_MODULES)
else ifeq ($(GROUP),sqlkit)
MODULES := $(SQLKIT_MODULES)
else ifeq ($(GROUP),notify)
MODULES := $(NOTIFY_MODULES)
else
$(error GROUP must be all, hmntsk, sqlkit or notify, not "$(GROUP)")
endif

# RELEASE_ORDER is the order the hmntsk modules must be tagged in: a module can
# only be released once everything it depends on has a version to require. The
# list is documented in docs/releasing.md, and a test asserts that the two agree.
#
# sqlkit is not in it. It is never tagged from this repository: step 0 of the
# release moves it to its own repository and tags it there, before any module
# below, because store/sqlcore requires it.
RELEASE_ORDER := . store/sqlcore storetest relaytest transport/core transporttest \
                 store/sql store/pgx store/gorm \
                 delivery/webhook delivery/redis delivery/nats \
                 transport/http transport/gin transport/fiber

GO ?= go
GOLANGCI_LINT ?= golangci-lint

# The project builds, tests and lints on Go 1.26 — the version every go.mod
# declares. Pinning GOTOOLCHAIN for every target, not just the linters, keeps a
# developer whose default toolchain has moved ahead on exactly what CI runs.
# Without it `make build` and `make test` silently use a newer toolchain than
# `make lint`, and the difference is only ever found in a pull request.
#
# `?=` leaves CI alone: actions/setup-go installs the pinned version and sets
# GOTOOLCHAIN=local itself, and an environment value wins here.
GOTOOLCHAIN ?= go1.26.8
export GOTOOLCHAIN

.PHONY: all build lint split-check fmt test test-integration test-race tidy vuln generate \
        store-matrix relay-matrix executor-matrix transport-matrix release-order clean

all: lint split-check test

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
		(cd $$m && $(GOLANGCI_LINT) run ./...); \
	done

## split-check: fail when a module built to move to its own repository depends
## on a module that is not moving with it.
##
## sqlkit may depend only on sqlkit; notify only on notify and sqlkit. Test
## dependencies count too, so a sqlkit test importing storetest fails here. This
## is the authoritative check: depguard in .golangci.yml is an earlier warning
## that cannot express "the root module but not its subdirectories".
split-check:
	@set -e; fail=0; \
	for entry in $(foreach m,$(SQLKIT_MODULES),$(m):sqlkit) $(foreach m,$(NOTIFY_MODULES),$(m):notify); do \
		m=$${entry%%:*}; group=$${entry##*:}; \
		case $$group in \
			sqlkit) allowed='sqlkit' ;; \
			notify) allowed='notify|sqlkit' ;; \
		esac; \
		echo "==> split-check $$m"; \
		out=$$(cd $$m && $(GO) list -deps -test -f '{{.ImportPath}}|{{join .Imports " "}}' ./... | \
			awk -F'|' -v allowed="$$allowed" -v module="$$m" '{ \
				n = split($$2, imports, " "); \
				for (i = 1; i <= n; i++) { \
					p = imports[i]; \
					if (p ~ /^github\.com\/kartaladev\/hmntsk(\/|$$)/ && p !~ ("^github\\.com/kartaladev/hmntsk/(" allowed ")(_test)?(/|$$)")) \
						print "    " module ": package " $$1 " imports " p; \
				} \
			}' | sort -u); \
		if [ -n "$$out" ]; then echo "$$out"; fail=1; fi; \
	done; \
	if [ $$fail -ne 0 ]; then \
		echo "split-check: a module that moves to its own repository imports one that does not"; \
		exit 1; \
	fi

## fmt: apply the configured formatters in place.
fmt:
	@set -e; for m in $(MODULES); do \
		(cd $$m && $(GOLANGCI_LINT) fmt ./...); \
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

# STORE_MATRIX is the seven valid driver-by-dialect combinations. It is sparse
# because pgx is PostgreSQL-only: seven, not nine.
STORE_MATRIX := store/sql:TestStoreOnPostgres store/sql:TestStoreOnMySQL store/sql:TestStoreOnSQLite \
                store/pgx:TestStoreOnPostgres \
                store/gorm:TestStoreOnPostgres store/gorm:TestStoreOnMySQL store/gorm:TestStoreOnSQLite

# RELAY_MATRIX is the same seven driver-by-dialect combinations as
# STORE_MATRIX, running the relay conformance suite instead. Claiming, retry
# scheduling and dead-lettering are storage behaviour, so they have to be proven
# on every dialect and not only on the one that is cheapest to run.
RELAY_MATRIX := store/sql:TestRelayOnPostgres store/sql:TestRelayOnMySQL store/sql:TestRelayOnSQLite \
                store/pgx:TestRelayOnPostgres \
                store/gorm:TestRelayOnPostgres store/gorm:TestRelayOnMySQL store/gorm:TestRelayOnSQLite

# EXECUTOR_MATRIX is the same seven driver-by-dialect combinations, running the
# sqlkit executor conformance suite: one Executor contract, proven identical on
# database/sql, pgx and GORM.
EXECUTOR_MATRIX := sqlkit/stdsql:TestExecutorOnPostgres sqlkit/stdsql:TestExecutorOnMySQL \
                   sqlkit/stdsql:TestExecutorOnSQLite \
                   sqlkit/pgx:TestExecutorOnPostgres \
                   sqlkit/gorm:TestExecutorOnPostgres sqlkit/gorm:TestExecutorOnMySQL \
                   sqlkit/gorm:TestExecutorOnSQLite

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

## relay-matrix: run the relay conformance suite over all seven combinations.
relay-matrix:
	@set -e; for entry in $(RELAY_MATRIX); do \
		m=$${entry%%:*}; run=$${entry##*:}; \
		echo "==> $$m $$run"; \
		(cd $$m && $(GO) test -count=1 -timeout 30m -run "^$$run$$" ./...); \
	done

## executor-matrix: run the sqlkit executor conformance suite over all seven
## combinations.
executor-matrix:
	@set -e; for entry in $(EXECUTOR_MATRIX); do \
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
