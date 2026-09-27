## Why

Any caller can drive a task through its lifecycle as long as the state machine allows the move. The verified audit (`VERIFIED-FINDINGS.md`, section A) confirmed this with failing reproduction tests, on the engine and on all three HTTP bindings:

- **E1 / T1.** An anonymous caller, or an authenticated stranger, can cancel any live task (`DELETE /tasks/{id}` answers `200`, `EXITED`). An anonymous caller can escalate any task, which widens its pool or supersedes it. `Task.Cancel` and `Task.Escalate` never check the actor, and `Authorize` returns nil for `OpCancel` and `OpEscalate`. A cancel with no actor is recorded with an empty actor, which history documents as "the system did this".
- **E2 / T2.** An authenticated actor who is not a candidate can suspend an unheld `READY` task, and can then resume it (`requireAssigneeIfHeld` checks only that an actor is present).
- **T3.** A lifecycle response returns the whole task, including its input and the callback's reference parameters, to an actor that the read policy refuses on `GET /tasks/{id}`.
- **E3 / T4.** An anonymous call carrying a stale version answers `409 {"currentVersion":1}` for a task that exists and `404` for one that doesn't. That reveals which tasks exist and their versions. The engine compares versions before it checks for an actor (`service.go`, `mutate`).
- **T5.** An anonymous `POST /tasks` is accepted with a client-chosen id, candidate pool, escalation policy and callback address (which could be an attacker's webhook), and it records `createdBy: ""`.

`host-wiring-fixes` named this change as a follow-up. Nothing is tagged yet, so the defaults can still become strict at no compatibility cost.

## What Changes

- **Engine: every actor-driven operation names its actor.** `Create`, `Claim`, `Release`, `Start`, `SaveProgress`, `Complete`, `Fail`, `Delegate`, `Suspend`, `Resume`, `Escalate` and `Cancel` refuse an empty actor with an authorisation error. The check runs before the task is read, so no version, existence or state is revealed. The empty actor is reserved for faults the system raises on its own behalf. **BREAKING** (pre-tag).
- **Engine: default ownership rules, behind a new port.**
  - `Cancel` is permitted only for the task's creator.
  - `Suspend` and `Resume` are permitted only for the assignee when someone holds the task. When nobody holds it, they are permitted for the creator or an eligible candidate.
  - `Escalate` is permitted for any named actor.
  - These rules are the default `hmntsk.OwnershipRules` of a new `hmntsk.LifecycleAuthorizer` port, installed with `hmntsk.WithLifecycleAuthorizer`. The sweeper's own escalations do not pass through the port. **BREAKING** (pre-tag).
- **Transport: anonymous calls refused first.** Every lifecycle route and `POST /tasks` answers `403` when no acting user is established. This happens before the body is used and before the store is read, the same way anonymous single-task reads are refused today.
- **Transport: escalation withheld over HTTP by default.** A new `transportcore.OperationAuthorizer` port decides which lifecycle operations an HTTP client may invoke. The default, `WithholdEscalation`, refuses `escalate` with `403` and passes every other operation on to the engine's rules. It is installed with `WithOperationAuthorizer`. **BREAKING** (pre-tag).
- **Transport: plain creates by default.** A new `transportcore.CreateAuthorizer` port decides what an HTTP create may carry. The default, `PlainCreatesOnly`, permits type, input, correlation, priority and deadline. It refuses a client-supplied `id`, `candidates`, `escalation` or `callback` with `403`, naming the field. It is installed with `WithCreateAuthorizer`. **BREAKING** (pre-tag).
- **Transport: lifecycle and create responses honour the read policy.** The task in a `200`/`201` response is put to the `TaskReadAuthorizer`. When the policy refuses, the body is a minimal receipt (`id`, `status`, `version`). The mutation has still happened.
- `AllowAll` also permits every operation and every create. A nil policy is a configuration error from `transportcore.New` or `hmntsk.New`.
- Docs, examples, the shared transport suite and the OpenAPI document are updated for these defaults.

## Non-goals

- Validation of the create overrides a policy permits: priority range, deadline overflow and escalation-policy shape belong to `engine-input-validation` (C3, C4, T15). The shape of a client-supplied id belongs to `transport-binding-parity` (T10).
- Post-commit dispatch in `respond` (T6), which belongs to `post-commit-dispatch`.
- Hiding task existence from *authenticated* actors (see design, Decision 6).
- Authentication. The host still establishes the actor.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-lifecycle`: every actor-driven operation requires an actor, checked before the version or existence of the task is revealed. Cancellation belongs to the creator by default, and the rules for who may cancel, suspend, resume or escalate can be replaced.
- `task-assignment`: the per-operation actor rules cover suspend and resume on unheld tasks, and cancellation.
- `task-escalation`: a manual escalation needs a named actor, while a swept escalation is recorded under the sweeper's owner and bypasses the host's lifecycle policy.
- `task-http-api`: the following are authorized:
  - anonymous lifecycle calls and creates, which are refused before any lookup;
  - escalation, which is withheld by default;
  - create overrides, which are plain by default;
  - lifecycle and create responses, which honour the read policy.

  The lifecycle coverage requirement states that the escalate route exists but the default refuses it.

## Impact

- **Code:** `transitions.go`, `assignment.go` (`Authorize`), `service.go` (`mutate`, `Create`, `Escalate`, `New`, a new option), a new `lifecycle_authorizer.go`, `sweep.go` (a path that bypasses the port), and the godoc for `history.go` and `event.go` (what an empty actor means). In `transport/core`: `api.go` (`operation`, `createTask`, `respond`), `authorize.go`, `dto.go` (receipt), `openapi.go` and `openapi.json`.
- **Tests:** engine tables next to `transitions_test.go` and `service_test.go`. A new `LifecycleAuthorization` group in `transporttest`, run by net/http, gin and fiber. Existing suite cases that escalate or create with overrides over HTTP need an explicit policy.
- **Examples / docs:** `examples/` cases that create, cancel, suspend or escalate need checking for an actor and for the creator rule. Also `README.md`, `docs/inbox.md` (a new "Who may change a task" section and "Unreleased breaking changes"), and the godoc on every new name.
- **Coordination:**
  - `engine-input-validation` also edits `Task.Escalate` (C7 cap) and `Create`.
  - `post-commit-dispatch` rewrites `respond`.
  - `transport-binding-parity` adds id validation to `createTask`.

  Whichever change lands second rebases. There is no semantic dependency (see design, Migration).
