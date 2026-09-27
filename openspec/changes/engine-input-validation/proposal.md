## Why

The verified audit found nine engine defects (C1–C5, C7 cap, C8, C9 sweeper part, C11) and one transport defect (T15). Each one has a failing reproduction test. All of them come down to one gap: the engine accepts data or configuration it cannot honour, then quietly does something else. Examples:

- a type registered without a priority makes every task maximally urgent;
- a lowercase `"widen"` policy registers and only ever notifies;
- a `deadlineSeconds` of 584 years becomes 290 ms;
- a failed SUPERSEDE is re-leased forever and never reported;
- a sweep keeps escalating after its caller cancelled it.

This breaks library-design rules 1, 4 and 6: safe defaults, no silent relaxation, and wiring mistakes that fail at construction.

## What Changes

- **Progress validation (C1).** The relaxed "shape-only" schema never refuses a draft that the full input schema accepts. Subschemas under `not` and `if` stay unrelaxed, and `oneOf` is checked as `anyOf` over its relaxed branches.
- **Default priority (C2), BREAKING (pre-tag).** `TypeSpec.DefaultPriority` becomes `*Priority`. Leaving it nil means `PriorityDefault` (5), and `Register` stores the resolved value. An explicit `PriorityHighest` (0) stays expressible.
- **Escalation policy validation (C3).** New `(*EscalationPolicy).Validate()`. It fails for an unknown action, a negative `MaxEscalations`, a WIDEN with nothing to add, and AddUsers or AddGroups on a non-WIDEN action. `Register` returns a `ConfigurationError`, and `Create` with a bad `Escalation` returns a `ValidationError`.
- **Create overrides (C4, T15).** `Create` refuses a priority outside 0..10 and a negative `Deadline` with a `ValidationError`. An explicit zero `Deadline` means "no deadline" rather than the type default. The transport refuses a `deadlineSeconds` that overflows `time.Duration` with a `400`.
- **Sweep error reporting (C5).** Only a stale-version conflict counts as a lost race. An illegal transition, such as SUPERSEDE on an `IN_PROGRESS` task, reaches the sweep error handler.
- **Manual escalation cap (C7).** `Task.Escalate` refuses once `MaxEscalations` is reached, so a manual escalation is capped as well as a swept one. `ExemptInProgress` stays sweep-only. That is now stated in the spec and godoc.
- **Event isolation (C8).** New `Event.Clone()`. memstore copies each event on `Append` and on every read, so a caller mutating `Result.Events` or an `OutboxEntry` cannot change the durable record.
- **Sweeper construction (C9, sweeper part only).** `NewSweeper` returns a `ConfigurationError` for a lease or batch of zero or less, instead of silently keeping the default.
- **Sweep cancellation (C11).** `Sweep` checks `ctx` before each task. Once `ctx` is cancelled it stops and returns the partial result with `ctx.Err()`.
- **Docs (C10, observation).** The godoc in `history.go`, `patch.go` and `SaveProgressRequest.Patch` stops claiming that patches are persisted as an audit record.

## Non-goals

- **Post-commit dispatch** (C6, T6). This belongs to change `post-commit-dispatch`.
- **Relay option validation** (C9, relay part) and relay fencing. These belong to change `relay-lease-fencing`.
- **Lifecycle and create authorization** (section A).
- **Re-validating escalation policies already stored** on existing task rows.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-types`: default priority when none is given, validated per-task overrides, validated escalation policies, and progress validation that is never stricter than full validation.
- `task-escalation`: manual escalation honours the cap, `ExemptInProgress` is sweep-only, and sweeps report failures, stop on cancellation and validate their options.
- `task-events`: recorded events are isolated from caller-held memory.
- `task-http-api`: out-of-range create overrides, including an overflowing `deadlineSeconds`, are `400`.

## Impact

- **Code:** `schema.go`, `registry.go`, `create.go`, `escalation_policy.go`, `transitions.go`, `sweep.go`, `event.go`, `memstore/`, `store/sqlcore/types.go` (pointer priority), `transport/core/api.go` and `dto.go`, and `examples/` and `transporttest/` fixtures that set `DefaultPriority`.
- **Tests:** new conformance rows in `storetest` (outbox isolation) and `transporttest` (create overrides), so every store and binding runs them.
- **API:**
  - `TypeSpec.DefaultPriority` type change. This is breaking, but it lands before the first tag and is recorded.
  - New additive API: `EscalationPolicy.Validate`, `Event.Clone`.
  - Behaviour changes:
    - an explicit zero `Deadline` now means no deadline;
    - `NewSweeper` refuses non-positive values;
    - `Sweep` returns `ctx.Err()`.
- **Coordination:** `sweep.go` is also touched by `post-commit-dispatch`. Whichever change lands second rebases.
