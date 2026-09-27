# Verified audit findings (per .claude/rules/error-reproducible.md)

Scratch test modules (outside the repo), `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`:
- $V/core      — module verifycore, run: `cd $V/core && GOWORK=off go test -run '<Name>' -count=1 ./...`
- $V/transport — run: `cd $V/transport && GOWORK=off go test -count=1 -run '<Name>' .`
- $V/relay     — run: `cd $V/relay && go test -run '<Name>' -count=1 .` (uses $V/go.work)
- $V/stores    — run: `cd $V/stores && go test -run 'TestFindings/<dialect>/<Sn>' -count=1 -v .` ; modernc/ subpackage for SQLite driver
- $V/notify    — run: `cd $V/notify && go test -count=1 -run '<Name>' .`
All CONFIRMED items fail deterministically (-count=20 where race-dependent; container ones -count=3+). Each test asserts correct behaviour.

Verdicts: CONFIRMED = failing test proves defect. SPEC-GAP = test fails, but no current spec forbids the behaviour (needs a spec decision). OBSERVATION = docs/design, no wrong behaviour. REFUTED = test passed. UNCONFIRMED = no reachable test.

## A. Lifecycle & create authorization (engine + transport)
- E1/T1 CONFIRMED-as-SPEC-GAP, all bindings: anonymous or non-owner DELETE /v1/tasks/{id} cancels (200, EXITED); anonymous POST /escalate escalates (widen adds groups; supersede obsoletes). Task.Cancel (transitions.go:289) and Task.Escalate (:246) never check actor; Authorize returns nil for OpCancel/OpEscalate. task-lifecycle spec: "The task's owner SHALL be able to cancel" (does not say others may not). A no-actor cancel writes history with empty actor (= "system did this" per TransitionRecord doc). Tests: $V/core service_test.go (E1*), $V/transport authz_test.go TestLifecycleAuthorization/*/T1_*.
- E2/T2 SPEC-GAP: non-candidate authenticated actor can suspend/resume an unheld READY task (requireAssigneeIfHeld transitions.go:192,218). task-assignment spec says "acting actor is the current assignee on suspend/resume" — ambiguous for unheld tasks. Tests: TestLifecycleAuthorization/*/T2_*.
- T3 CONFIRMED all bindings: lifecycle responses (respond, transport/core/api.go:458 encode(result.Task)) return full task (input, callback referenceParameters) to an actor refused by TaskReadAuthorizer on GET. Test: authz_test.go TestLifecycleResponseHonoursReadPolicy.
- E3/T4 CONFIRMED (transport): anonymous lifecycle call with stale version answers 409 {"currentVersion":1} for an existing task vs 404 for a missing one — existence oracle. Version check before actor check (service.go:685). task-lifecycle spec requires conflicts to report current version (engine level) — the fix belongs in transport/actor ordering. Test: authz_test.go T4_*.
- T5 CONFIRMED (spec silent): anonymous POST /tasks accepted with client-chosen id, candidates, escalation, callback (attacker webhook address); createdBy "". Test: authz_test.go TestAnonymousCreate.
- Existing ports: transport/core/authorize.go has QueryAuthorizer and TaskReadAuthorizer only.

## B. Engine validation & sweeper
- C1 CONFIRMED: schema.go relaxSchema strips `required` inside not/oneOf/if → SaveProgress rejects drafts that pass the full ValidateInput ("'not' failed", "'oneOf' failed, subschemas 0, 1 matched", "/n: minimum: got 3, want 10"). Test: $V/core schema_test.go TestSaveProgress_AcceptsDraftValidAgainstFullSchema.
- C2 CONFIRMED: TypeSpec.DefaultPriority zero value = PriorityHighest(0); task without priority gets 0 not PriorityDefault(5). Field shape can't distinguish unset from explicit 0 → design decision (pointer / option / sentinel). Test: TestCreate_TypeWithoutDefaultPriorityGetsPriorityDefault.
- C3 CONFIRMED: EscalationPolicy never validated (EscalationAction.Valid() unused); "widen" lowercase registers and behaves notify-only; negative MaxEscalations accepted. Tests: TestRegister_RejectsInvalidEscalationPolicy, TestSweep_LowercaseWidenPolicyWidensOrIsRefused.
- C4 CONFIRMED: CreateRequest priority 99 / -1 accepted; negative deadline silently replaced by type default. Test: TestCreate_RejectsInvalidOverrides.
- T15 CONFIRMED all bindings: deadlineSeconds 18446744074 overflows (api.go:217) to ~290ms; negative deadline → type default; priority -5/11 → 201. Test: $V/transport request_test.go T15_*.
- C5 CONFIRMED: sweep.go:181 errors.Is(err, ErrConflict) also matches TransitionError → overdue IN_PROGRESS task with SUPERSEDE policy re-leased every period, no error reported. Test: TestSweep_ReportsIllegalSupersedeOfInProgressTask.
- C7 CONFIRMED (cap part): manual Service.Escalate ignores MaxEscalations (spec: policy caps "the total number of times that task is escalated"). ExemptInProgress part = OBSERVATION (spec reads sweep-only). Test: TestServiceEscalate_HonoursPolicyLimits.
- C8 CONFIRMED (memstore): Result.Events / OutboxEntry.Event share slices/maps with stored outbox; mutating returned event changes stored one. Test: TestOutbox_StoredEventIsIsolatedFromCallerMutation.
- C9 CONFIRMED: NewSweeper accepts lease<=0, batch<=0; NewRelay accepts lease<=0, batch 0, attempts 0, backoff (0,0)/(-1s,1h), jitter<0, WithSinks(nil) — silently ignored (godoc documents "ignored" for some) → violates library-design rule 6. Tests: TestNewSweeper_RejectsMeaninglessOptions, TestNewRelay_RejectsMeaninglessOptions.
- C11 CONFIRMED: Sweep keeps escalating remaining batch after ctx cancelled; returns nil. Test: TestSweep_StopsWhenContextCancelledMidBatch.
- C10 OBSERVATION: history.go / service.go SaveProgressRequest.Patch / patch.go docs say patches are the audit record; nothing persists them.

## C. Post-commit dispatch (host-led transactions)
- C6 CONFIRMED: Sweep inside host tx dispatches in-process handlers before host commit (sweep.go:191-195); rollback leaves handler having seen uncommitted escalation. Test: $V/core TestSweep_InHostTransactionDoesNotDispatchBeforeCommit.
- T6 CONFIRMED all bindings: transport/core/api.go:459 `_ = result.Dispatch(ctx)` inside host-led tx (host wraps handler in store.Do): consumer sees READY while... ran before commit; consumer error discarded (WithDispatchErrorHandler never called). Test: $V/transport dispatch_test.go TestHostLedDispatch.
- Result.Dispatch doc + README:181 say "after committing, never before".

## D. Relay lease fencing & pass discipline
- R1/S6 CONFIRMED on memstore, SQLite, Postgres, MySQL: RecordAttempt / MarkAccepted / MarkDeadLettered filter only WHERE id=? (store/sqlcore/query.go:628,650,689; memstore/outbox.go:153-205). Stale relay A after B reclaimed: SQL un-publishes (published_at NULL), memstore loses accepted sink; dead letter resurrected; attempts regress; B's live lease released; relay C claims concurrently. AttemptRecord/Acceptance/DeadLetter carry no owner (relayport.go:58-78) so the port can't express fencing. Tests: $V/relay r1_fence_test.go TestR1StaleSettlementIsNotFenced; $V/stores TestFindings/*/S6_outbox_settlement_not_fenced.
- R2 CONFIRMED: relay/relay.go:360 no lease-deadline check between events; with injected clock, 21 of 50 events delivered by both A and B. Defaults contradict: 50 batch × 10s webhook timeout = 500s > 5m lease. Test: r2_overrun_test.go TestR2PassRunsPastItsLease. R2b (sink ctx not bounded by lease) CONFIRMED but judgement call (WithRelayLease doc says host sizes lease): TestR2SinkContextBoundedByLease.
- R3 CONFIRMED memstore/SQLite/Postgres: after ctx cancel, e2 still offered; e1 accepted by sink but settlement lost (settled with cancelled ctx) → redelivery; unattempted events stay leased until lease expiry. Test: r3_cancel_test.go TestR3CancelledContextMidPass.
- R8 CONFIRMED: next-attempt computed from pass start (relay.go:345,441,455) → event failing late in pass is due before it failed; zero effective backoff. Test: TestR8BackoffMeasuredFromPassStart.
- R9 CONFIRMED amd64 only: float→Duration overflow with huge ceiling (relay.go:603-615) → next attempt in year 1733. Run `GOARCH=amd64 go test -run TestR9 -count=1 .`. Jitter exceeding ceiling = OBSERVATION (spec says "up to a configured ceiling"; code comment intends it).
- R4 OBSERVATION: default budget (5 attempts, 30s base) dead-letters after 7m30s; 1h DefaultBackoffCeiling unreachable with defaults.

## E. Delivery sink hardening
- R5 CONFIRMED real Redis: delivery/redis/redis.go:317 go-redis default MaxRetries=3; proxy severs after XADD reply → one Deliver appended 2 stream entries (same deliveryId). Control with MaxRetries:-1 passes. Breaks relay.Sink "must not retry internally". NATS sink already disables retries. Test: $V/relay r5_redis_test.go TestR5RedisOnePublicationPerAttempt.
- R7 CONFIRMED (caveat: webhook spec only requires body+timestamp signed; docs call Hmntsk-Event-Id "the de-duplication key"): Verifier.Verify accepts delivery with altered Hmntsk-Event-Id / Hmntsk-Delivery-Id. Test: r7_r11_webhook_test.go TestR7SignatureCoversDedupHeader.
- R11 CONFIRMED: callback URL query-string secrets (?token=) appear in last_error and error handler (sink.go:387,415; *url.Error). Also doubled "webhook: webhook:" prefix. Test: TestR11CallbackQuerySecretRedacted.
- R6 OBSERVATION: webhook sink has no TLS/mTLS option (private CA / client cert impossible without fork) — library-design rule 2.
- REFUTED (hold): redirects refused, SSRF policy.

## F. Store robustness
- S1 CONFIRMED postgres (sql,pgx,gorm) + mysql (sql,gorm), 20/20; SQLite REFUTED (_txlock=immediate serialises): concurrent Create same ID → raw 23505 / 1062, not ErrConflict (store/sql/store.go:250, store/pgx/store.go:238, store/gorm/store.go:247). No driver error classification anywhere. Deadlock/serialization classification UNCONFIRMED (not attempted). Test: $V/stores TestFindings/<d>/S1_concurrent_create_same_id.
- S2 CONFIRMED sqlite/postgres/mysql, all adapters; memstore passes: duplicate candidates → PK violation (store/sqlcore/builder.go:310). Test: S2_duplicate_candidates.
- S3 CONFIRMED: docs/schema.md:247 SQLite DSN lacks _txlock=immediate → losing writer gets "database is locked (517)" not ErrConflict. Test: modernc/TestSQLiteDocsDSNRace.
- S4 CONFIRMED sqlite+postgres: store/gorm (store.go:155) and sqlkit/gorm (executor.go:136) ignore per-call ctx inside tx. Test: S4_cancelled_ctx_inside_tx.
- S5 CONFIRMED mysql 20/20: ConflictError.Current stale (= Expected) under REPEATABLE READ snapshot (store/sql/store.go:351 version re-read). Test: S5_conflict_current_stale.
- S7 CONFIRMED: candidate pools exceed bind limits (SQLite 9000 users; PG/MySQL 17000). Test: S7_large_candidate_pool.
- S8 CONFIRMED mysql: 65-char TaskID → Error 1406 (id VARCHAR(64)); other dialects accept. Test: S8_long_task_id.
- S9 CONFIRMED mysql: utf8mb4_0900_as_cs orders a-1 A-1 b-1 … vs byte-wise elsewhere (sqlkit/dialect.go:130). Test: S9_identifier_paging_order.
- S10 CONFIRMED sqlite (COLLATE NOCASE) + postgres (missing COLLATE "C"): VerifySchema returns nil (sqlkit/verify.go:207). Test: S10_verify_schema_collation.
- S12a CONFIRMED: sqlstore.New(nil,…)/New(db,nil)/gormstore.New(nil)/pgxstore.New(nil) accepted, nil-pointer panic at first use. S12b CONFIRMED sqlite: table prefix with quote/; executes injected DDL (RenderSchema raw substitution sqlkit/schema.go:17) — dropped a victim table; postgres REFUTED (rolled back). S12c CONFIRMED postgres: prefix len 48/54 → truncated identifier collision "already exists (42P07)". Tests: S12_nil_handles, S12_prefix_injection, postgres/S12_long_prefix.
- S13 CONFIRMED (port states no order): memstore ClaimOverdue orders by id vs SQL due_at,id → different task claimed when Limit < backlog (memstore/memstore.go:602). Test: memstore/S13_claim_overdue_order.
- S11 UNCONFIRMED: readText rows not closed on scan error — unreachable via supported drivers.

## G. Notify realtime & access
- N1 CONFIRMED: WebSocket mark-read/mark-all-read act on connection recipient; follower alice (allowed to follow bob) marks bob's notifications READ (notify/websocket/handler.go:227-236,419-423). Spec line ~176 says "apply to the connection's recipient" — spec needs fixing too. Test: $V/notify websocket_test.go TestWebSocketFollowerCannotMarkRecipientsNotifications.
- N2 CONFIRMED real NATS: after conn CLOSED, Hub.Run never returns, Running() stays true, Subscribe still accepted (notify/nats/broadcaster.go:198,214; ChanSubscribe channel never closed). Test: nats_test.go TestNATSListenEndsWhenConnectionCloses.
- N3 CONFIRMED: after Hub.Run returns, open SSE streams keep heartbeating and WS stays answering (notify/hub.go:151-190). Test: hub_test.go TestHubStopClosesOpenStreams.
- N6 CONFIRMED-as-design-gap: followers exhaust per-recipient stream cap → recipient gets 429 (hub.go:250-261). Test: TestFollowersCannotExhaustRecipientsOwnStreamCap.
- N7 CONFIRMED at port (Service.List with ~70k kinds → 500-class driver error "too many SQL variables" / PG 65535); HTTP→500 REFUTED (Go net/url stops at 10000 params). N7' NEW CONFIRMED: notify/http.go:179 r.URL.Query() discards parse error → over-long query silently drops the kind filter, 200 with unfiltered results. Tests: sqlstore_test.go TestListFilterCountIsValidated, TestListHTTPDropsFiltersPastQueryParamLimit.
- N9 OBSERVATION: notify.SelfOnly / AllowAll / tasknotify.DefaultRules are exported mutable vars.

## H. Notify email
- N4 CONFIRMED SQLite: slow pass lets lease lapse; RecordEmails checks owner only, not lease_until>now (notify/sqlstore/email.go:388; email_dispatcher.go:471 record.At = pass start, :685). Outcomes: mailer-accepted email recorded ABANDONED; never-sent email ABANDONED (never retried). Test: email_test.go TestEmailLeaseLapseMidPass.
- N5a CONFIRMED: AtLeastOnce in-doubt resends unbounded (12 sends with WithEmailMaxAttempts(3)) and ignore max lag (sent 11m after creation with 5m lag). Test: TestAtLeastOnceInDoubtResendsAreBounded.
- N5b CONFIRMED (vs AtLeastOnce godoc "over exactly the same notifications"): resend under same idempotency key covers a shrunken set. Test: TestAtLeastOnceResendCoversExactlyTheOriginalNotifications.
- N8 CONFIRMED mysql: host id generator >64 bytes → Error 1406 on publish (notify sqlstore mysql id VARCHAR(64)); not caught at construction. Test: sqlstore_test.go TestLongGeneratedIDsPublishOnEveryDialect/mysql.

## I. Transport binding parity & hardening
- T7 CONFIRMED all: GroupResolutionError cause (LDAP host/bind DN) in 500 body; transport/core/errors.go:77 explicitly exempts it from masking. Test: errors_test.go TestGroupResolutionFailureBody.
- T8 CONFIRMED gin+fiber: gin io.LimitReader silently truncates (9MiB → 201; or 400 "unexpected end of JSON"); fiber has no WithMaxBodyBytes, fasthttp 4MiB default → text/plain 413 for 5MiB. net/http correct (8MiB, JSON 413). Test: request_test.go TestCreateRequestHandling/*/T8_*.
- T9 CONFIRMED fiber: gzip Content-Encoding body decoded → 201; others 400. Test: T9_gzip.
- T10 CONFIRMED: fiber passes percent-encoded params ("a b" → 404 "task a%20b not found"); "x/y" unreachable on gin+fiber; id "count" unreachable everywhere (GET /tasks/count route). Client ids unvalidated. Test: routing_test.go TestClientSuppliedIDIsAddressable.
- T11 CONFIRMED divergent: net/http `//` → 307 html, HEAD → 200; gin trailing slash → 301 html; fiber case-insensitive 200, trailing slash 200, HEAD 200. Test: TestUnservedPathVariants.
- T12 CONFIRMED net/http: Mount registers "/" catch-all (transport/http/handler.go:113) → panics if host has "/" (before or after). Test: wiring_test.go TestMountBesideHostRoutes.
- T13 CONFIRMED net/http: WithBasePath("v2") accepted, routes 404. Test: TestBasePathWithoutLeadingSlash.
- T14a CONFIRMED all: case-folded duplicate "Type" overrides "type". T14b OBSERVATION: unknown fields silently dropped.
