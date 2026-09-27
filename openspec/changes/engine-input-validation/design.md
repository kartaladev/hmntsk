## Context

See proposal.md for why. This is the current state, checked in the code. Every defect below has a failing reproduction in the scratch module `$V = /private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`, listed in `$V/VERIFIED-FINDINGS.md` section B.

- **`schema.go` `relaxSchema`** walks every subschema keyword and deletes `required`, `minItems`, `minProperties` and `dependentRequired` everywhere. Under `not` and `if`, deleting a requirement makes the subschema easier to match. That inverts the result: `not` fails, and `then` now applies. Under `oneOf`, it lets several branches match, so "exactly one" fails. (C1)
- **`registry.go` `TypeSpec.DefaultPriority`** is a `Priority`, where `0` is `PriorityHighest`. An unset field is therefore the most urgent priority. `Priority.Valid()` accepts it, and `create.go` copies it onto every task. (C2)
- **`escalation_policy.go`** has `EscalationAction.Valid()`, but nothing calls it. `Register` checks the priority and deadline only, and `Create` checks no override at all. (C3)
- **`create.go` `resolveDueAt`** applies `req.Deadline` only when it is greater than 0. Otherwise it falls through to the type default, so a negative or zero value is silently replaced. `req.Priority` is copied unchecked. (C4)
- **`transport/core/api.go:217`** computes `time.Duration(*body.DeadlineSeconds) * time.Second`, which wraps for values above about 9.2e9 seconds. (T15)
- **`sweep.go:181`** treats `errors.Is(err, ErrConflict)` as a lost race. But `TransitionError` unwraps to `ErrIllegalTransition`, which wraps `ErrConflict`, so an illegal SUPERSEDE of an `IN_PROGRESS` task is swallowed and the task is re-leased every period. The sweep pins `Version`, so a real race always surfaces as `*ConflictError`, never as `*TransitionError`. (C5)
- **`transitions.go` `Task.Escalate`** does not consult `MaxEscalations`. Only the sweep's `exemption()` does, so a direct `Service.Escalate` is uncapped. (C7)
- **`memstore`** stores the `Event` value as given (`Append`) and returns it as stored (`outboxRow.entry`). Its `CandidatePool` slices, `Correlation.Extra` map, `Callback` pointer and `Output` bytes alias the `Result.Events` the caller holds. `Event` has no `Clone`. The SQL stores serialise, so they are already isolated. (C8)
- **`WithLeaseDuration` and `WithSweepBatch`** ignore values of 0 or less, and `NewSweeper` never validates. (C9)
- **`Sweep`** loops over the claimed batch without checking `ctx`, and returns `nil` error. (C11)
- **No tags exist yet**, so a change to an exported field type is free. It is recorded here, as `library-design.md` rule 7 requires.

## Goals / Non-Goals

**Goals:**
- Every value the engine accepts is honoured exactly, and every value it cannot honour is refused where it is supplied:
  - configuration fails with a `ConfigurationError`;
  - request data fails with a `ValidationError`, which the transport answers as `400`.
- Each defect's scratch reproduction becomes the red test of its fix.

**Non-Goals:**
- Post-commit dispatch in `Sweep` (C6), owned by `post-commit-dispatch`.
- Relay option validation (C9, relay part), owned by `relay-lease-fencing`.
- Validating `WithSweepOwner("")`, `WithSweepErrorHandler(nil)`, and `LeaseRequest.Duration`/`Limit` on the public `ClaimOverdue`. These keep their documented "empty means default" behaviour. See Open Questions.
- Re-validating policies on task rows that are already stored.
- Rejecting a `DueAt` that is in the past. It is a meaningful value: the task is overdue immediately.

## Decisions

### 1. Progress shape check is never stricter than full validation (C1)

The relaxation is sound only in positive positions. The walk becomes polarity-aware:
- **`not` and `if`:** copied verbatim, not relaxed. Relaxing a negated or conditional subschema makes it match more documents, and that makes the enclosing check stricter.
- **`then`, `else`, `dependentSchemas`, `allOf`, `anyOf`, `properties` and the other positive keywords:** relaxed as today.
- **`oneOf`:** rewritten to `anyOf` over its relaxed branches. A document that fully matches exactly one branch still matches that branch once relaxed, so `anyOf` accepts it. Keeping `oneOf` would fail whenever relaxation makes two branches match.

The invariant is that every document valid against the full schema is valid against the relaxed one. It is tested with the three C1 rows plus a property-style row that runs the existing schema fixtures through both validators.
- **Default:** the built-in relaxed check.
- **Override:** none new. A host that wants a stricter or looser draft check validates in its own handler before `SaveProgress`, and the input schema is fully the host's to write. A dedicated draft-schema option was considered and rejected: it duplicates the input schema and drifts from it.
- **Alternative considered:** *dropping `not`, `if` and `oneOf` from the relaxed schema entirely.* It is looser than needed, since a draft could then violate `not` constraints freely. Rejected.

### 2. `TypeSpec.DefaultPriority` becomes `*Priority` (C2)

- **Default:** nil means `PriorityDefault` (5). `Register` resolves it and stores a spec whose `DefaultPriority` is non-nil, so `Registry.Lookup`, the SQL type rows (`store/sqlcore/types.go`, same `int64` column, no migration) and the transport DTO all see the effective value.
- **Override:** set any `Priority` in range, including `&PriorityHighest`. An out-of-range value is still a `ConfigurationError`.
- **Equality:** a nil and an explicit `&PriorityDefault` are `Equal`, because they behave identically. Re-registering either one is idempotent.
- **JSON:** `defaultPriority` stays `omitempty`. It is absent for nil and present for `0`, so a type registered from configuration data can finally express 0.
- **Compatibility:** changing an exported field type is breaking. It is free before the first tag. It is recorded here and in the proposal, because the repository has no CHANGELOG yet. Callers that set `DefaultPriority: hmntsk.PriorityDefault` change to a pointer. There are three non-test sites: `examples/internal/invoicing`, `transporttest`, and `store/sqlcore/types.go`.
- **Alternatives considered:**
  - *Renumber priorities so 0 is invalid.* It changes stored data and the documented range. Rejected.
  - *Treat 0 as "unset" and add a `HighestPriority bool`.* Two fields for one value. Rejected.
  - *Documentation only.* It breaks rule 1, because the zero-config default is unsafe. Rejected.
  - *A sentinel constant such as `PriorityUnset = -1`.* The zero value would still be 0. Rejected.

### 3. `EscalationPolicy.Validate()` checks policies where they enter (C3)

`func (p *EscalationPolicy) Validate() error` returns nil for a nil policy. Otherwise it returns the first problem as plain detail text:
- the action is not `Valid()`, which is exact and case-sensitive;
- `MaxEscalations < 0`;
- a WIDEN policy with no `AddUsers` and no `AddGroups`;
- `AddUsers` or `AddGroups` on a non-WIDEN action.

Callers:
- **`Registry.Register`:** wraps the problem in `*ConfigurationError` naming the type.
- **`Service.Create`:** wraps it in `*ValidationError` with pointer `/escalation`, before any store work.
- **Transport:** maps it to `400` through the existing `ErrValidation` mapping.

The last two rules go beyond the reproduction. They follow rule 4: a configuration that would be silently ignored is refused. The nearest existing test fixture, `task_test.go:154` (a notify policy with `AddGroups`), goes through `TypeSpec.NewTask`, not `Create`, so it is unaffected. That is recorded in case the implementer finds otherwise.
- **Default:** a nil policy means notify-only, as today.
- **Override:** any valid policy, per type or per task.
- **Alternative considered:** *normalising case (`"widen"` becomes `WIDEN`).* It hides a typo and makes the stored value differ from the one supplied. Rejected.

### 4. Create overrides are validated, not replaced (C4, T15)

In `Service.Create`, before building the task:
- a `req.Priority` that is not `Valid()` fails with `*ValidationError{Pointer: "/priority"}`;
- a negative `req.Deadline` fails with `*ValidationError{Pointer: "/deadline"}`.

`resolveDueAt` changes one branch: an explicit `req.Deadline` of zero yields no due date, instead of falling through to the type default. The `CreateRequest` godoc already promises that pointers distinguish "not supplied" from "deliberate zero". `TypeSpec.DefaultDeadline` already uses zero to mean no deadline. This is a behaviour change, recorded here.

The transport checks `*body.DeadlineSeconds` against `math.MaxInt64 / int64(time.Second)` in either direction before multiplying. Past that bound it answers a `ValidationError` with pointer `/deadlineSeconds`, which becomes `400`. A negative value passes through and the engine refuses it, so all bindings share one message.

The OpenAPI document gains `minimum: 0` and `maximum: 10` on `priority`, and `minimum: 0` on `deadlineSeconds`. It is regenerated with `-update`.
- **Default:** omitted overrides take the type's defaults, as today.
- **Override:** any in-range value.
- **Stated limit:** a deadline must fit in a `time.Duration`, which is about 292 years.

### 5. Only a stale-version conflict is a lost race in `Sweep` (C5)

The condition becomes: skip silently only if `errors.As(err, &conflict *ConflictError)`. Everything else, including `*TransitionError`, is wrapped with the task ID and sent to `onError`. The lease is still left to expire, which gives one report per lease period rather than one per sweep.
- **Default:** the handler discards reports, unchanged.
- **Override:** `WithSweepErrorHandler`.
- **Alternatives considered:**
  - *Exempting SUPERSEDE on `IN_PROGRESS` in `exemption()`.* It silently changes policy semantics.
  - *Making `Obsolete` legal from `IN_PROGRESS`.* It changes the transition table, which is a separate spec decision.

  Both were rejected in favour of reporting. The host sees the report and can choose `ExemptInProgress` or a different action.

### 6. The escalation cap moves into `Task.Escalate` (C7)

`Task.Escalate` refuses with a `*TransitionError` when `policy.MaxEscalations > 0 && t.EscalationCount >= policy.MaxEscalations`. The error matches `ErrIllegalTransition` and answers `409` over HTTP. To make the message say why, `TransitionError` gains an optional `Reason string` field. This is additive, and it is appended to `Error()` when set. The sweep's `exemption()` keeps its own cap check, so a capped task is still counted as exempted rather than reported.
- **Default:** a cap of zero means no cap, unchanged.
- **Override:** a per-task policy at creation (`CreateRequest.Escalation`) with a different or zero cap.
- **Stated limit:** there is no "force past the cap" path. An operator who must reach more people after the cap uses `Delegate`. The godoc says so.

**`ExemptInProgress` (OBSERVATION → spec decision):** it stays sweep-only. A direct escalation is an explicit operator decision about one task, and the exemption exists to stop *automatic* escalation of work already being done. The spec now says so, with a scenario, and the `ExemptInProgress` godoc says so too. There is no code change.

### 7. `Event.Clone()` and copy-on-boundary in memstore (C8)

New `func (e Event) Clone() Event` deep-copies:
- `Candidates` (`CandidatePool.Clone`);
- `Correlation` (`CorrelationData.Clone`);
- `Callback` (`CallbackTarget.Clone`);
- `Output` (`cloneRaw`);
- any reference-typed field of `Transition`.

memstore calls it in `Append`, when storing, and in `outboxRow.entry()`, when reading. The conformance test lives in `storetest` (Outbox → isolation), so it runs on memstore and every SQL dialect. The SQL rows already pass. `Result.Events` stays the slice the service produced, and isolation is guaranteed at the store boundary, which is where the durable record lives.
- **Default:** isolation, always.
- **Override:** none. Isolation is a guarantee, not a policy. A store that wants to share memory has no legitimate reason to.

### 8. `NewSweeper` validates its options (C9, sweeper part)

The options assign unconditionally. `NewSweeper` validates `lease > 0` and `batch > 0` after applying them, and returns `*ConfigurationError` naming the option. The godoc on both options drops "ignored" and names the default each one replaces (`DefaultLeaseDuration`, `DefaultSweepBatch`).
- **Default:** 5 minutes and 100, unchanged.
- **Override:** the two options.
- `SweepOption` stays `func(*Sweeper)`. Deferring the check to the constructor avoids changing the option type.

### 9. `Sweep` honours cancellation (C11)

Before each claimed task, `Sweep` checks `ctx.Err()`. If it is set, it returns the partial `SweepResult` and `ctx.Err()`, and does not call `onError` for the unreached tasks. Their leases expire as they would after a crash. `Run` already returns `ctx.Err()` when `Sweep` errors with `ctx` done, so it needs no change. There is no override: cancellation is the caller's control.

### 10. Patch "audit record" docs are corrected (C10, OBSERVATION)

This is documentation only:
- **`history.go` (`TransitionRecord`), `patch.go` (`applyJSONPatch`) and `service.go` (`SaveProgressRequest.Patch`):** each is changed to say that a patch is applied to the stored draft and is not persisted. Only the merged progress is stored.
- **`README.md` and `docs/`:** checked for the same claim.

There is no behaviour change. Persisting patches would be a schema change and is not requested.

## Risks / Trade-offs

- **[`DefaultPriority` pointer breaks callers]** → Pre-tag, three non-test sites, recorded. The compiler finds every one.
- **[Stricter policy rules refuse a configuration a host relied on]** → Only configurations the engine was silently ignoring are refused. Each refusal names the type and the rule.
- **[An explicit zero `Deadline` changes meaning]** → It aligns with the documented pointer semantics. It is covered by a test and recorded here.
- **[The polarity-aware relaxation still misses a keyword]** → A property-style test checks "full-valid implies shape-valid" over the existing schema fixtures plus the three C1 schemas.
- **[`sweep.go` conflicts with `post-commit-dispatch`]** → The two changes touch different lines (the dispatch block versus the error condition and loop head). Whichever lands second rebases.

## Migration Plan

This is additive, except for the `TypeSpec.DefaultPriority` type change and the three behaviour changes listed in the proposal. The examples and `transporttest` are updated in the same change. Rollback is reverting the change. No schema migration is needed, because the priority column stays a non-null integer holding the resolved value.

## Open Questions

- Should `WithSweepOwner("")`, `WithSweepErrorHandler(nil)` and a negative `LeaseRequest.Duration` also become construction or validation errors, for full rule-6 consistency? They are documented "empty means default" today, and none has a failing reproduction. Deferred. The answer does not change this change's specs or tasks.
