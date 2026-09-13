# Releasing

Twenty modules live in this repository. Fifteen make up hmntsk and are tagged
and released from here, each independently. That is ongoing operational cost,
accepted deliberately: it is what keeps `go get github.com/kartaladev/hmntsk`
free of pgx, GORM, gin and Fiber, which is the entire point of the split.

The other five are sqlkit — `sqlkit`, `sqlkit/sqlkittest`, `sqlkit/stdsql`,
`sqlkit/pgx` and `sqlkit/gorm` — the domain-free SQL toolkit `store/sqlcore` is
built on. They are developed here and are never tagged from here: they move to
their own repository before their first release (see step 0 below), so no
consumer ever imports them under a path that later changes. `make split-check`
keeps that move mechanical, by failing the build if anything under `sqlkit/`
imports a module that stays behind.

## Tagging scheme

Go's module resolution decides the scheme, not taste. A module in a
subdirectory is tagged with its path as the prefix:

| Module | Tag |
| --- | --- |
| `github.com/kartaladev/hmntsk` | `v1.2.3` |
| `github.com/kartaladev/hmntsk/store/sqlcore` | `store/sqlcore/v1.2.3` |
| `github.com/kartaladev/hmntsk/store/sql` | `store/sql/v1.2.3` |
| `github.com/kartaladev/hmntsk/store/pgx` | `store/pgx/v1.2.3` |
| `github.com/kartaladev/hmntsk/store/gorm` | `store/gorm/v1.2.3` |
| `github.com/kartaladev/hmntsk/transport/core` | `transport/core/v1.2.3` |
| `github.com/kartaladev/hmntsk/transport/http` | `transport/http/v1.2.3` |
| `github.com/kartaladev/hmntsk/transport/gin` | `transport/gin/v1.2.3` |
| `github.com/kartaladev/hmntsk/transport/fiber` | `transport/fiber/v1.2.3` |
| `github.com/kartaladev/hmntsk/delivery/webhook` | `delivery/webhook/v1.2.3` |
| `github.com/kartaladev/hmntsk/delivery/redis` | `delivery/redis/v1.2.3` |
| `github.com/kartaladev/hmntsk/delivery/nats` | `delivery/nats/v1.2.3` |
| `github.com/kartaladev/hmntsk/storetest` | `storetest/v1.2.3` |
| `github.com/kartaladev/hmntsk/transporttest` | `transporttest/v1.2.3` |
| `github.com/kartaladev/hmntsk/relaytest` | `relaytest/v1.2.3` |

Modules are versioned independently. A patch to the Fiber binding does not move
the core's version, and a host pinning the core is not dragged forward by it.

## Release order

A module can only be released after everything it depends on, because its
`go.mod` has to name a version that already exists.

**Step 0, before anything below: split sqlkit out and tag it.** Move `sqlkit/`
to `github.com/kartaladev/sqlkit` with `git filter-repo --path sqlkit/`, rewrite
its import paths once, and tag it there. `store/sqlcore` and `storetest`
require it, so hmntsk's first tag waits for this step. In the same pass, before
the first hmntsk tag, point `store/sql`, `store/pgx` and `store/gorm` at sqlkit
directly and remove the aliases `store/sqlcore` keeps for them today.

```
1.  .                      core: domain, state machine, ports, relay
2.  store/sqlcore          depends on core, sqlkit (step 0)
3.  storetest              depends on core, sqlkittest (step 0)
4.  relaytest              depends on core
5.  transport/core         depends on core
6.  transporttest          depends on core, transport/core
7.  store/sql              depends on core, sqlcore   (+ storetest, relaytest, for tests)
8.  store/pgx              depends on core, sqlcore   (+ storetest, relaytest, for tests)
9.  store/gorm             depends on core, sqlcore   (+ storetest, relaytest, for tests)
10. delivery/webhook       depends on core            (+ relaytest, for tests)
11. delivery/redis         depends on core            (+ relaytest, for tests)
12. delivery/nats          depends on core            (+ relaytest, for tests)
13. transport/http         depends on core, transport/core (+ transporttest)
14. transport/gin          depends on core, transport/core (+ transporttest)
15. transport/fiber        depends on core, transport/core (+ transporttest)
```

Core and `store/sql` land first and prove the shape; the rest follow. `make
release-order` prints this list, and a test asserts that the list this document
gives and the list the tooling prints are the same, so the two cannot drift.

## During development

`go.work` resolves the intra-repository dependencies, and the satellite modules'
`go.mod` files therefore do not name the core module at all. That is deliberate
for an unreleased tree: a `replace` directive would be ignored by consumers, and
a placeholder version would be unresolvable.

### `go mod tidy` and a package that is not tagged yet

`go mod tidy` resolves imports against *published* versions, and `go.work` does
not help it. That is fine while a satellite imports only
`github.com/kartaladev/hmntsk`, which every tag has — but it breaks the moment a
satellite imports a **new package inside core that no tag contains yet**:

```
go: github.com/kartaladev/hmntsk/delivery/webhook imports
	github.com/kartaladev/hmntsk/relay: module github.com/kartaladev/hmntsk@latest
	found (v0.0.0-...), but does not contain package github.com/kartaladev/hmntsk/relay
```

`delivery/webhook`, `delivery/redis`, `delivery/nats` and `relaytest` all import
`github.com/kartaladev/hmntsk/relay`, so `make tidy` fails for the whole
workspace until core is tagged with that package.

sqlkit makes the same true of more modules, and for longer. Nothing under
`github.com/kartaladev/hmntsk/sqlkit` will ever be published at that path, so
every module that imports it — `store/sqlcore`, `storetest`, `store/sql`,
`store/pgx`, `store/gorm`, and the sqlkit modules themselves — cannot be tidied
until step 0 has published sqlkit under its own path. This is not a
misconfiguration and there is nothing to fix in those modules: `go build`,
`go test` and `go vet` all work, because those read `go.work`.

**`make tidy` does not fail cleanly.** It iterates the modules in order and
stops at the first one that cannot resolve — but every module it reached first
has already been rewritten, and what `go mod tidy` writes into them is a
hardcoded `require github.com/kartaladev/... v0.0.0-<pseudo-version>` pointing
at whatever commit is currently published. That is precisely the thing the
comment at the top of each satellite `go.mod` exists to prevent, and it is easy
to miss because the modules still build afterwards. If you run it by accident,
`git checkout --` the `go.mod` and `go.sum` files it touched.

Until then, tidy only the modules that import neither `relay` nor `sqlkit`:

```sh
for m in . transport/core transport/http transport/gin transport/fiber \
         transporttest; do
    (cd $m && go mod tidy)
done
go work sync
```

Curate every other module's `go.mod` by hand in the meantime, following the
shape `store/sql` uses. After the core tag that first contains
`relay`, `make tidy` works again for everything.

**Before the first tag**, each satellite module's `go.mod` gains a real
`require` on the modules it uses, with the version just tagged. Release step by
release step:

```sh
# 1. Tag the core.
git tag v1.2.3 && git push origin v1.2.3

# 2. For each module in release order, add the requires it needs at the
#    versions already tagged, commit, then tag it.
cd store/sqlcore
go mod edit -require=github.com/kartaladev/hmntsk@v1.2.3
go mod tidy
git commit -am "chore(sqlcore): require core v1.2.3"
git tag store/sqlcore/v1.2.3 && git push origin store/sqlcore/v1.2.3
```

`make release-order` exists so that this loop can be driven from the list rather
than from memory.

## Compatibility surfaces

Three things are public contracts from the first release, and all three are
harder to change than Go code:

- **The Go API** — the `Service`, the ports, the `Kind[In, Out]` facade. A
  breaking change here is at least visible: it does not compile.
- **The REST contract** — routes, shapes and status codes. A bad URL cannot be
  fixed with a deprecation and a compiler error, which is why the version sits
  in the path from the first release and why the OpenAPI document is generated
  from the route table rather than maintained beside it.
- **The database schema** — tables, columns and collation. Adopters have applied
  it through their own pipeline; changing it means writing them a migration.

Treat all three as v1 from the day they ship.
