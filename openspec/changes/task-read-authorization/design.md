## Context

See proposal.md for why. The current state:

- **Unguarded reads.** `transportcore.API.getTask` calls `Service.Get` and returns the task to any caller. `getHistory` calls `Service.Get` (only to turn a missing task into `404`) and then `Service.History`. Neither consults the acting user.
- **The query pattern to mirror.** `transport/core/authorize.go` already ships it:
  - `QueryAuthorizer` with an `AuthorizeQuery(ctx, actor, query)` method, and `QueryAuthorizerFunc`;
  - `SelfOnly`, the default, a package-level value;
  - `AllowAll`, declared as a `QueryAuthorizer` value;
  - `WithQueryAuthorizer`, with a nil policy rejected by `New`;
  - `queryRefusedError`, which unwraps to `hmntsk.ErrUnauthorized` alone, so any policy error is `403`.
- **Group membership is private to the engine.** `Service` holds the `GroupResolver` in an unexported field set by `WithGroupResolver`. gopls confirms there is no accessor. Eligibility lives in the package function `hmntsk.IsEligible(ctx, resolver, pool, actor)`: exclusion wins first, then candidate users, then groups through the resolver. A resolver failure is a `*GroupResolutionError`, which `StatusFor` maps to `500`.
- **The creator is recorded.** `Task.CreatedBy` is set from `CreateRequest.Actor` (`create.go`).
- **The shared suite already reads single tasks.** `transporttest` reads single tasks as `Alice` in `lifecycle.go`, `errors.go` and `payloads.go`. In every case she holds the task or belongs to its candidate pool, so the new default leaves these cases green.
- **The OpenAPI document.** `openapi.go` declares `getTask` and `getTaskHistory` with `404` as their only failure. The `403` description mentions only eligibility and query policy.
- **The docs.** `docs/inbox.md`, under "Who may query", states as a limit that reading one task does not pass through any policy. That limit is what this change removes.

## Goals / Non-Goals

**Goals:**
- Safe, zero-configuration protection of `GET /tasks/{id}` and `GET /tasks/{id}/history`, with the same shape, naming and refusal semantics as query authorization.
- A default policy that is an ordinary value a host can call from its own policy, exactly as hosts compose `SelfOnly` today.
- Eligibility decided by the engine's own rule, never re-implemented in the transport.

**Non-Goals:**
- Authorizing lifecycle operations, which keep the engine's assignee and eligibility rules.
- Authorizing task-type reads.
- Concealing whether a task exists from authenticated actors (see Decision 1).
- Authorization for a Go host calling `Service.Get` directly. As with queries, authorization lives where the actor arrives as an input: the HTTP layer.

## Decisions

### 1. Unknown tasks: `403` without an actor, `404` with one, `403` when refused

The handler runs checks in this order:
1. No acting user established: `403`, before the store is touched.
2. Look up the task: `404` if it does not exist.
3. Ask the policy: `403` if it refuses.
4. Return the task, or for the history route, read and return the history.

- **Why:** the policy needs the task to decide anything (its holder, pool and creator), so an authenticated read of a task that doesn't exist cannot be "refused"; there is nothing to evaluate. Refusing anonymous reads before the lookup removes the cheap probing vector, where anyone without an identity asks whether a task ID exists.
- **Default:** as above.
- **Override:** none for step order. A host that wants anonymous reads writes a policy permitting them, but step 1 still refuses first. That is a stated limit: the contract never serves an anonymous single-task read.
- **Alternatives considered:**
  - *`404` for refused reads too*, to conceal existence, as some public APIs do. Rejected because it contradicts the query endpoints' `403`, hides a real refusal from legitimate users, and makes "not yours" indistinguishable from "gone" in UIs that follow notification links.
  - *`403` for unknown tasks too.* Rejected because it breaks the existing `404` contract and scenario for unknown tasks, and a UI can no longer tell a deleted link from a permission problem.
- **Stated limit:** an authenticated actor can learn whether a task ID exists by the `404`/`403` difference. Default IDs are UUIDv7, which are not guessable in practice. A host supplying its own guessable IDs through `CreateRequest.ID`, and needing concealment, must enforce it in its middleware. The docs say so.

### 2. The default reaches group membership through a lazily evaluated `TaskRead`

The policy receives one value describing the read:

```go
type TaskRead struct {
    Actor string
    Task  hmntsk.Task
    // unexported: the engine's eligibility check, bound by the contract
}

// Eligible reports whether Actor is eligible for Task by the engine's own rule,
// resolving group membership only when called.
func (r TaskRead) Eligible(ctx context.Context) (bool, error)
```

The contract builds each `TaskRead` with a closure over a new engine method:

```go
// Eligible reports whether actor may act on task as a candidate, applying
// exclusion, candidate users and group membership exactly as a claim does.
func (s *Service) Eligible(ctx context.Context, task hmntsk.Task, actor string) (bool, error)
```

`Service.Eligible` is `IsEligible(ctx, s.resolver, task.Candidates, actor)`.

- **Why:**
  - The default policy stays a plain package-level value (`ParticipantsOnly`), exactly like `SelfOnly`, so a host composes it without wiring: `transportcore.ParticipantsOnly.AuthorizeRead(ctx, read)`.
  - Eligibility is decided by the engine's single rule, the same one claim uses, so the transport cannot drift from it.
  - The resolver stays private.
  - The check is lazy: `AllowAll`, or a host policy that decides on the actor alone, never costs a directory call. The default checks the cheap cases (holder, creator) before calling `Eligible`.
  - A resolver failure surfaces as `ErrGroupResolution`. The contract passes that through as `500` rather than wrapping it as a refusal (see Decision 4).
  - Construction errors stay at construction: the policy is a value, so there is nothing to wire, and nil is refused by `New`.
- **Default:** `ParticipantsOnly`. It permits the holder, then the creator, then an eligible candidate, and refuses everything else, including a read with no actor, as defence in depth behind Decision 1.
- **Override:** `WithTaskReadAuthorizer(policy)` replaces it wholesale, with nothing chained. `TaskReadAuthorizerFunc` adapts a function.
- **Alternatives considered:**
  - *Expose `Service.GroupResolver()`.* Leaks a port the engine deliberately encapsulates, and invites every host policy to re-implement eligibility, exclusion ordering included.
  - *A policy that takes a `GroupResolver` at construction* (`ParticipantsOnly(resolver)`). The default could no longer be a value, so `New` would need the resolver, which brings back the accessor. A host composing the default would also have to thread a resolver it already gave the engine.
  - *`Service.CanRead(ctx, actor, task)` with the whole read rule in the engine.* It puts an HTTP-authorization default into the engine, which today holds no read policy, and makes the rule harder to replace piecewise. `Service.Eligible` is a smaller, reusable primitive, useful to hosts.
- **Relation to `Service.ResolveCandidates`.** The `tasknotify` change adds a second engine primitive, `Service.ResolveCandidates(ctx, pool)`. The two answer different questions over the same private resolver: `Eligible` checks one actor against a task ("may this actor act?"); `ResolveCandidates` expands a pool into every eligible actor ("who may act?"), as creation does. Both stay, both keep the resolver private, and their godoc cross-references the other.

### 3. Names mirror query authorization

| Query (existing) | Read (new) |
|---|---|
| `QueryAuthorizer` / `AuthorizeQuery(ctx, actor, query)` | `TaskReadAuthorizer` / `AuthorizeRead(ctx, read TaskRead)` |
| `QueryAuthorizerFunc` | `TaskReadAuthorizerFunc` |
| `SelfOnly` (default) | `ParticipantsOnly` (default) |
| `WithQueryAuthorizer` | `WithTaskReadAuthorizer` |
| `AllowAll` | `AllowAll` (the same value) |

- **The read method takes a struct rather than positional arguments** because the lazy `Eligible` needs a receiver, and because future inputs (for example a history-specific flag) can be added without breaking policies.
- **`AllowAll` satisfies both interfaces.** It changes from a `QueryAuthorizer`-typed variable to a value of an exported, empty type `AllowAllPolicy` with both methods. `WithQueryAuthorizer(transportcore.AllowAll)` keeps compiling unchanged.
  - Why one value: a host that authorizes in middleware in front of the contract wants one named opt-out. It is still never an accident, because each option must be passed explicitly.
  - Alternative considered: a separate `AllowAllReads`. More names, and no safety gained.
- **Wiring:** `New` refuses a nil read policy with a `ConfigurationError` naming `transportcore.AllowAll` as the explicit opt-out, mirroring the query message.

### 4. One refusal error for both policies

`queryRefusedError` is generalised into a single `policyRefusedError` used by both the query and read paths. It keeps the rule that it unwraps to `hmntsk.ErrUnauthorized` alone, so any policy error, including one that also matches `ErrValidation`, answers `403` with the policy's message.

**Exception:** an error matching `hmntsk.ErrGroupResolution`, returned by `TaskRead.Eligible` and propagated by a policy, is returned unwrapped, so `StatusFor` maps it to `500`. It means the engine could not decide, not that it decided against the caller. The existing query path has no such case, because its default never resolves groups.

- **Alternative considered:** a second, parallel error type for reads. It duplicates the unwrap rule, which is the subtle part.

### 5. OpenAPI and docs

- `getTask` and `getTaskHistory` failures become `403`, `404` and `500`.
- The `403` description adds "or the read authorization policy refused the read. By default an actor may read only tasks they hold, are eligible for, or created".
- `docs/inbox.md` gains "Who may read a task" beside "Who may query". The old "Limit, stated" paragraph is rewritten to say lifecycle operations keep the engine's rules. "Unreleased breaking changes" gains an entry.

## Risks / Trade-offs

- **[A host relied on unrestricted reads]** → The default change is recorded in "Unreleased breaking changes" with the `AllowAll` escape hatch. Nothing is tagged, so no released consumer breaks (`.claude/rules/library-design.md`, rule 7).
- **[Directory load on every read of a pooled task]** → Holder and creator are checked before `Eligible`, and eligibility resolves lazily. The `GroupResolver` godoc already tells hosts to cache behind the port.
- **[Existence leak to authenticated actors via `404` vs `403`]** → Stated limit, Decision 1. The default UUIDv7 IDs make probing impractical.
- **[A former holder loses access after delegation if no longer eligible]** → Only eligible actors can hold a task, and delegation doesn't change the pool, so a former holder stays eligible unless the pool excludes them later. A host wanting history-long access writes a policy.
- **[`AllowAll` type change]** → Source-compatible for every existing use (`WithQueryAuthorizer(AllowAll)`, `AllowAll.AuthorizeQuery(...)`). Only code declaring `var x transportcore.QueryAuthorizer = transportcore.AllowAll` still works, since the new type satisfies the interface.

## Migration Plan

1. Land before any notification links to task details are released (`tasknotify`).
2. Hosts that serve task details to non-participants (auditors, back-office) either supply a `TaskReadAuthorizer` or pass `AllowAll`.
3. Rollback: a host passes `WithTaskReadAuthorizer(transportcore.AllowAll)` to restore the previous behaviour with no code revert.
