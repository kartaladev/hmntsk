## Context

See proposal.md for why. This is the current state, confirmed with gopls and by reading the code.

- **Pure transitions.** `Task.Cancel` (`transitions.go:289`) and `Task.Escalate` (`:246`) take an actor but never check it. `Task.Suspend` and `Task.Resume` call `requireAssigneeIfHeld` (`:457`), which on an unheld task checks only that an actor is present.
- **`Authorize` (`assignment.go:288`)** is the per-operation actor rule. For `OpCreate`, `OpEscalate`, `OpCancel`, `OpObsolete` and `OpFault` it returns nil, and its godoc says "the host decides who may invoke them". The `Service` calls it only for claim and delegate. Every other operation relies on checks inside the transition itself.
- **The pipeline (`service.go`, `mutate`).** It validates `TaskID`, then reads the task, then compares `req.Version` with `current.Version` and returns a `*ConflictError` carrying `Current`. Only after that does it run the step that checks the actor. So a caller with no actor learns whether the task exists and what its version is.
- **Ordering rule.** `requireFrom`'s godoc makes this rule deliberate: the state machine runs before any identity check, so an illegal move is a conflict even when the actor is wrong too. The spec's "Illegal transition is refused" scenario depends on it.
- **The sweeper.** It escalates through `Service.Escalate` with `Actor: s.owner`, which is `"sweeper-<uuid>"` or `WithSweepOwner`. A swept escalation already names an actor.
- **Empty actors.** `TransitionRecord.Actor` and `Event.Actor` are documented as empty "when the system acted". Today that covers faults, but also anonymous cancels.
- **Transport.** `operation()` (`transport/core/api.go:388`) and `createTask` (`:192`) pass `req.Actor` straight through, even when it is empty. `respond` (`:458`) encodes `result.Task` in full. `authorizedTask` (`:300`) already implements "anonymous `403` before lookup, `404`, then policy" for reads. `authorize.go` holds `QueryAuthorizer`, `TaskReadAuthorizer`, `ParticipantsOnly`, `AllowAllPolicy` and `refuse`/`policyRefusedError`. The last of these unwraps to `ErrUnauthorized` alone, except that a group-resolution error passes through as a `500`.
- **The suite.** `transporttest/lifecycle.go` escalates as `Owner` over HTTP (line 62). It also creates with a client id and callback (`:128`) and with explicit candidates (`:119`). Under the new defaults these cases need an explicit policy.

## Goals / Non-Goals

**Goals:**
- Strict, zero-configuration defaults on the engine and the HTTP contract. Every default can be replaced through a port or an explicit opt-out.
- A clear split:
  - the **engine** holds invariants that make history truthful (a named actor) and the ownership rules that every entry point must share;
  - the **transport** holds policy about what a *remote* client may ask for (escalation, create overrides) and what it may *see* (response filtering).
- No change to the rule that the state machine runs before identity checks.

**Non-Goals:**
- Hiding existence or version from *authenticated* actors (Decision 6).
- Validating override values (`engine-input-validation`) or id shape (`transport-binding-parity`).
- Any change to claim, release, start, save-progress, complete, fail or delegate rules.

## Decisions

### 1. The actor is an engine invariant, checked before the task is read

`mutate` refuses `req.Actor == ""` with an `*AuthorizationError` (`Reason: "no acting actor was supplied"`). This happens right after the `TaskID` check, before `store.Get`. `Create` does the same before it looks up the type.

- **Why here:**
  - Presence of an actor needs no task, so it can run first without breaking the "state machine before identity" rule. That rule is about *which* actor, not *whether* there is one.
  - Running it first closes E3 in the engine as well as over HTTP. A Go host that exposes its own API over `Service` gets the same protection.
  - History stays truthful: an empty actor means the system acted.
- **Default:** always on.
- **Override:** none, by design. This is a stated limit (library-design rule 4): the engine never records an anonymous human transition. A host that wants "anonymous" semantics names a pseudonymous actor itself, such as `"anonymous"`, and that name is what history shows.
- **Faults** (`Task.Fault`, `OpFault`) keep the empty actor. The godoc on `TransitionRecord.Actor` and `Event.Actor` is narrowed to say an empty actor appears only on faults.
- **Alternatives considered:**
  - *Check the actor inside each transition only.* This leaves the version and existence oracle in place, because `mutate` compares versions first.
  - *A `ValidationError` for a missing actor.* An authorization failure is what a missing identity is. `requireActor` already returns `*AuthorizationError`, and both map to a status the specs already define.

### 2. Ownership rules live in the engine, behind a `LifecycleAuthorizer` port

```go
// hmntsk
type LifecycleCheck struct {
    Operation Operation // OpCancel, OpSuspend, OpResume or OpEscalate
    Actor     string    // never empty: Decision 1 has already run
    Task      Task      // as read inside the transaction
    // unexported: bound eligibility check
}
func (c LifecycleCheck) Eligible(ctx context.Context) (bool, error)

type LifecycleAuthorizer interface {
    AuthorizeLifecycle(ctx context.Context, check LifecycleCheck) error
}
type LifecycleAuthorizerFunc func(ctx context.Context, check LifecycleCheck) error

var OwnershipRules LifecycleAuthorizer // the default
func WithLifecycleAuthorizer(policy LifecycleAuthorizer) Option // nil → ConfigurationError from New
func NewLifecycleCheck(op Operation, actor string, task Task, eligible func(context.Context) (bool, error)) LifecycleCheck
```

`OwnershipRules`:

| Operation | Permitted |
|---|---|
| `Cancel` | `task.CreatedBy == actor` |
| `Suspend`, `Resume`, task held | `task.Assignee == actor` |
| `Suspend`, `Resume`, task unheld | the creator, or `Eligible` (resolved lazily, after the creator check) |
| `Escalate` | any named actor |

- **Where it runs:**
  - The `Service` calls the policy for these four operations inside `mutate`'s step, after the pure transition has accepted the move. That keeps the conflict-before-authorization ordering.
  - `Authorize` stays the non-replaceable rule for the other operations. Its `OpCancel`/`OpEscalate` nil branch is removed from the godoc's "host decides" wording.
  - `requireAssigneeIfHeld` leaves the transitions. The transitions keep only `requireFrom` and, where they have one, their invariant assignee checks.
  - A policy error that matches `ErrGroupResolution` propagates as itself. Any other refusal is wrapped so it matches `ErrUnauthorized`. This mirrors `policyRefusedError`.
- **Why the engine and not only the transport:**
  - The audit reproduced E1/E2 through `Service` directly. Hosts call the engine in-process from their own handlers, queues and jobs.
  - A rule that exists only on HTTP would protect one entry point of many. "The creator cancels" is a domain rule about the task, and every entry point should share it.
- **Why a port and not hard-coded:**
  - Library-design rule 2. Real hosts want an administrator who cancels anything, managers who suspend team work, or no pooled suspension at all.
  - The default is a plain value, so a host policy can call `hmntsk.OwnershipRules.AuthorizeLifecycle` for the cases it does not override. This is the same composition style as `SelfOnly` and `ParticipantsOnly`.
- **Why only four operations:**
  - Claim eligibility and assignee-only completion are the work-holding invariants every other guarantee rests on: who may produce the output, and delegation to eligible targets only.
  - Letting a policy widen them would let an output be recorded by someone who never held the task.
  - This is a stated limit, and the spec has a scenario for it.
- **Alternatives considered:**
  - *Extend `Authorize` with a resolver-free creator check and no port.* This is strict, but it can't be replaced without forking, which violates rule 2.
  - *A port over all twelve operations.* This gives a host the power to break the assignee invariant quietly. Rejected under rule 4.
  - *Put the policy only in `transportcore`.* This leaves in-process calls wide open (E1/E2 reproduce on `Service`).

### 3. System escalations: the sweeper names itself and bypasses the port

- **Representation:** the sweeper already escalates as its owner (`"sweeper-<uuid>"`, or `WithSweepOwner`), so history and the event name the sweeper. No new constant is introduced.
- **In-process operator escalations** name their own actor. `examples/escalation` already uses `"operations-desk"`.
- **Bypass:**
  - `Sweeper.Sweep` calls an unexported `Service.escalate(ctx, req, swept bool)`. `Service.Escalate` is that call with `swept=false`.
  - A swept escalation still requires its (always non-empty) actor, still runs the state machine and the escalation policy, and skips only the `LifecycleAuthorizer`.
- **Why:** deadlines are the engine enforcing a policy the task type already declared. A host lifecycle policy written for humans, such as "only managers escalate", must not silently stop deadline enforcement.
- **Default:** as above.
- **Override:** a host that wants to stop sweeping a type uses the existing `WithSweepTypes`, or does not run a sweeper. There is deliberately no way to make the port apply to sweeps.
- **Alternative considered:** a `LifecycleCheck.Swept` flag that a policy must remember to honour. Rejected: every host policy would have to repeat the same guard, and forgetting it fails silently.

### 4. HTTP: anonymous refusal first, then an `OperationAuthorizer` that withholds escalation

`operation()` and `createTask` first refuse `req.Actor == ""` with `refuse(...)`, which gives `403`. This happens before `decode`, so a malformed body from an anonymous caller is still `403`, not `400`. It mirrors `authorizedTask`.

- **Status 403, not 401:** this is consistent with anonymous reads and queries. The contract leaves authentication to the host, and the host's middleware owns any `401`.

Then comes the new transport port:

```go
// transportcore
type OperationCall struct {
    Actor     string
    Operation hmntsk.Operation
    TaskID    hmntsk.TaskID
}
type OperationAuthorizer interface {
    AuthorizeOperation(ctx context.Context, call OperationCall) error
}
type OperationAuthorizerFunc func(ctx context.Context, call OperationCall) error
var WithholdEscalation OperationAuthorizer // default: refuse OpEscalate, permit the rest
func WithOperationAuthorizer(policy OperationAuthorizer) Option // nil → ConfigurationError
```

- **The task is not provided.** Loading it before the engine would be a second read outside the engine's transaction, which invites time-of-check/time-of-use gaps. It would also give policies an existence oracle. A host whose rule depends on the task writes a `hmntsk.LifecycleAuthorizer`, which sees the task inside the transaction.
- **Why escalation is withheld over HTTP only:** escalating widens who may see and act on a task. From a remote client that is a privilege, while in-process calls are code the host wrote. The engine's `OwnershipRules` therefore permits any named actor to escalate, and the transport withholds it by default.
- **Default:** `WithholdEscalation`.
- **Override:** `WithOperationAuthorizer(policy)`. `AllowAll` gains `AuthorizeOperation` and permits every call; the engine's own rules still apply behind it.
- **Alternative considered:** removing the escalate route unless enabled. Rejected: the spec requires every operation to have a route, and a `403` is clearer to clients than a `404`.

### 5. HTTP: `CreateAuthorizer`, plain creates by default

```go
type TaskCreate struct {
    Actor   string
    Request hmntsk.CreateRequest
}
// Overrides names the restricted fields the request sets: "id", "candidates", "escalation", "callback".
func (c TaskCreate) Overrides() []string
type CreateAuthorizer interface {
    AuthorizeCreate(ctx context.Context, create TaskCreate) error
}
type CreateAuthorizerFunc func(ctx context.Context, create TaskCreate) error
var PlainCreatesOnly CreateAuthorizer
func WithCreateAuthorizer(policy CreateAuthorizer) Option // nil → ConfigurationError
```

- **Why these four fields:**
  - `candidates` and `escalation` decide who can see and act on the task.
  - `callback` makes the relay POST to an address the client chose, which is an SSRF and exfiltration channel for its reference parameters.
  - A client-chosen `id` enables squatting and probing of guessable ids.
  - Priority, deadline, input and correlation affect only the task the caller is asking for, and the type's schema already bounds the input.
- **Default:** `PlainCreatesOnly`. It refuses with a message listing `Overrides()`.
- **Override:** `WithCreateAuthorizer(policy)`. A typical host policy permits overrides for service accounts. `AllowAll` gains `AuthorizeCreate`.
- **Ordering with the other changes:**
  - The policy runs after `decode` and after `transport-binding-parity`'s id-shape validation. A malformed id is a `400` before anyone is asked whether ids are allowed.
  - It runs before `Service.Create`, so engine validation (`engine-input-validation`) applies only to permitted requests.
- **Alternative considered:** a single `OperationAuthorizer` that handles creates through an `OpCreate` call with an optional request pointer. It was rejected because the two calls carry different data, and a policy would have to type-switch.

### 6. Response filtering; existence and version oracles for authenticated actors

- **Filtering:**
  - `respond` gains the acting user. It builds a `TaskRead` for `result.Task` and asks the configured `TaskReadAuthorizer`.
  - When the policy permits, the response is the full task, as today.
  - When it refuses, or when `Eligible` fails with `ErrGroupResolution`, the response is `TaskReceipt{ID, Status, Version}` with the success status. The mutation has already committed, so a `403` or `500` would misreport what happened. Failing closed on the *body* is safe.
  - Create responses go through the same path.
  - The OpenAPI document adds the receipt as an alternative (`oneOf`) response for these routes.
- **Default:** whatever read policy is configured (`ParticipantsOnly` by default).
- **Override:** `WithTaskReadAuthorizer` already controls it. There is no separate knob, so what a client may *see* is decided in one place.
- **Oracles:**
  - Anonymous callers are closed off by Decision 1 on the engine and Decision 4 on the transport.
  - For *authenticated* actors, the engine still compares versions before the ownership check, so a stranger with a stale version gets `409 {currentVersion}`. This is a stated limit, consistent with `task-read-authorization` Decision 1, which already accepts a `404`/`403` existence difference for authenticated actors.
  - Moving identity checks before the version check would contradict the "conflict before authorization" rule the lifecycle spec depends on. Default ids are UUIDv7, and the default create policy now refuses client-chosen ids.
  - A host needing concealment enforces it in middleware. The docs say so.
- **Alternative considered:** `404` for a refused lifecycle call. Rejected for the same reasons as in `task-read-authorization`.

### 7. Naming and wiring summary

| Layer | Port / method | Default | Opt-out | Option |
|---|---|---|---|---|
| engine | `LifecycleAuthorizer.AuthorizeLifecycle(ctx, LifecycleCheck)` | `OwnershipRules` | host policy (no `AllowAll` in the engine: a permit-all is one line with `LifecycleAuthorizerFunc`) | `hmntsk.WithLifecycleAuthorizer` |
| transport | `OperationAuthorizer.AuthorizeOperation(ctx, OperationCall)` | `WithholdEscalation` | `AllowAll` | `WithOperationAuthorizer` |
| transport | `CreateAuthorizer.AuthorizeCreate(ctx, TaskCreate)` | `PlainCreatesOnly` | `AllowAll` | `WithCreateAuthorizer` |
| transport | `TaskReadAuthorizer` (existing) | `ParticipantsOnly` | `AllowAll` | `WithTaskReadAuthorizer` |

- A nil policy is a `ConfigurationError` from `hmntsk.New` or `transportcore.New`, and the message names the explicit opt-out.
- `AllowAllPolicy` gains `AuthorizeOperation` and `AuthorizeCreate`, and compile-time assertions cover all four interfaces.
- Refusals from every transport policy use the existing `policyRefusedError`.

## Risks / Trade-offs

- **[In-process hosts that call `Create` or lifecycle methods without an actor break]** → Recorded in "Unreleased breaking changes". The examples already pass actors, and the tasks audit them. The error names the missing actor.
- **[Hosts whose assignees cancel work]** → Now refused by default. The migration note shows a `LifecycleAuthorizerFunc` that permits the assignee and delegates everything else to `OwnershipRules`.
- **[The `transporttest` cases that escalate or create with overrides]** → They move to an API built with `AllowAll` for those policies, and the new group covers the defaults.
- **[A directory call on an unheld suspend/resume]** → The creator is checked before `Eligible`, and eligibility is resolved lazily.
- **[Existence/version leak to authenticated actors]** → A stated limit (Decision 6).
- **[Receipt bodies confuse clients that expect a full task]** → The OpenAPI `oneOf` documents it. Clients that need the task can `GET` it, and the same read policy applies.
- **[Rebase churn]** → `engine-input-validation` edits `Task.Escalate` and `Create`. `post-commit-dispatch` rewrites `respond`. `transport-binding-parity` edits `createTask`. The edits touch different lines and there is no semantic dependency.

## Migration Plan

1. The change can land before or after `engine-input-validation`, `post-commit-dispatch` and `transport-binding-parity`. The second change to land rebases:
   - `respond` keeps both the read filter and `post-commit-dispatch`'s dispatch removal;
   - `createTask` runs id-shape validation, then the create policy.
2. Hosts that relied on the old behaviour opt out explicitly:
   - `WithOperationAuthorizer(transportcore.AllowAll)` to re-enable HTTP escalation;
   - `WithCreateAuthorizer(transportcore.AllowAll)` for configured creates;
   - `hmntsk.WithLifecycleAuthorizer(...)` for broader cancel or suspend rules.
3. Rollback: the same options restore the previous transport behaviour without a code revert. The actor-required invariant has no rollback, by design.

## Open Questions

- Should `docs/inbox.md` gain a well-known actor-naming convention for service accounts (for example a `service:` prefix)? It would be documentation only and does not affect the specs or tasks.
