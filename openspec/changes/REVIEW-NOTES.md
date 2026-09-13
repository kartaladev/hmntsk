# Review notes: notification changes

Big decisions taken while planning, for review. Each is recorded in the named artifact; nothing here is implemented. Reversing any of them is a planning edit (`/opsx:update <change>`) before `/opsx:apply`.

Small questions that were settled with a recommendation are under "Resolved open questions" in each `design.md`, not listed here.

## Order

### 1. Apply and archive order

- **Decided:** `event-audience-snapshot` and `task-read-authorization` (independent) → `sqlkit` → `notify-core` → `tasknotify`, `notify-realtime-adapters`, `notify-email` (independent of each other). Archive in the same order.
  - `notify-realtime-adapters` adds requirements to `notification-realtime` and must be archived after `notify-core`.
  - `tasknotify` needs `event-audience-snapshot` and `notify-core`; its task links rely on `task-read-authorization`.
- **Where:** each proposal's Impact; `notify-realtime-adapters/design.md` decision 1; `tasknotify/tasks.md` 1.1.
- **Alternative:** one combined change.
- **If reversed:** one large PR instead of seven reviewable ones; no design change.

## Breaking or default-changing

### 2. Single-task reads default to `ParticipantsOnly`

- **Decided:** `GET /tasks/{id}` and `/history` permit only the holder, the creator or an eligible candidate. `AllowAll` restores open reads. Unreleased, recorded as a default change.
- **Where:** `task-read-authorization/design.md`, spec `task-http-api`.
- **Alternative:** keep open reads by default and make the policy opt-in.
- **If reversed:** notification task links point at an endpoint anyone can read.

### 3. Status order for single-task reads: 403 before 404

- **Decided:** no actor → 403 before any lookup; actor + unknown task → 404; policy refusal → 403; directory failure → 500. Stated limit: a signed-in actor can tell 404 from 403.
- **Where:** `task-read-authorization/design.md` decision (a).
- **Alternative:** 404 for every refusal (hides existence, breaks consistency with query 403s).
- **If reversed:** clients following a stale link can't tell "gone" from "not yours".

### 4. `EvictOldestActive` is the retention default (your decision; reminder)

- **Consequence:** a user over 500 notifications loses their oldest **unread** ones. "Active until read" holds only under `RetainActive`.
- **Where:** `notify-core/design.md` decision 8, spec `notification-retention`.

### 5. Retention default numbers

- **Decided:** 500 per recipient; 90 days after leaving ACTIVE; watermarks kept 7 days; 1,000 rows per prune statement.
- **Where:** `notify-core/design.md` decision 8.
- **If changed:** constants and docs tests only.

## Engine and core API

### 6. Two new engine methods: `Service.Eligible` and `Service.ResolveCandidates`

- **Decided:** add both, keeping the group resolver private. `Eligible` checks one actor ("may this actor act?"); `ResolveCandidates` expands a pool ("who may act?").
- **Where:** `task-read-authorization/design.md` decision (b); `tasknotify/design.md` decision 1.
- **Alternative:** expose `Service.GroupResolver()`, or give the projector its own resolver.
- **If reversed:** eligibility rules get copied outside the engine, and a projector can disagree with the engine.

### 7. `notify` gains close-with-successors and coalescing drafts

- **Decided:** folded into `notify-core` (decision 13) for `tasknotify`: a close can publish "taken" to everyone it closed in one transaction; a coalescing draft creates nothing when an open notification of that kind exists.
- **Where:** `notify-core/design.md` decision 13, spec `notification-inbox`, tasks 3.5 and 3.6.
- **Alternative:** close, then publish in two calls.
- **If reversed:** a crash between the calls loses "taken" notices; the degraded fallback notifies eligible users who never had an offer.

### 8. `notify` gains a public hub subscription, `WriteError` and a signal codec

- **Decided:** folded into `notify-core` (decision 13) so SSE and WebSocket share one per-user connection cap and Redis and NATS carry identical messages.
- **Where:** `notify-core/design.md` decision 13, tasks 8.4, 8.5, 9.4, 9.5.
- **If reversed:** each transport and broadcaster duplicates the cap and the codec.

## Behaviour

### 9. Subscriptions default to `SelfOnly`, not "user plus their groups"

- **Decided:** every notification is addressed to one recipient, so a user follows only their own. `tasknotify` expands groups when writing offers. `notify` has no `GroupResolver`.
- **Where:** `notify-core/design.md` decision 2; `notify-core/proposal.md`.
- **Alternative:** the earlier "user plus groups" default.
- **If reversed:** `notify` needs a `GroupResolver` port and group-addressed signals.

### 10. The actor is never notified of their own action; start, suspend and resume produce nothing

- **Decided:** by default. A host extends `DefaultRules` through `WithRules`.
- **Where:** `tasknotify/design.md` decision 3, spec `task-notifications`.
- **Alternative:** notify the actor too, or notify the holder on suspend and resume.
- **If reversed:** rule-table edits and tests only.

### 11. Email: at-most-once, 24-hour maximum lag, every kind by default

- **Decided:** a crash mid-send loses that batch's email rather than duplicating it (`AtLeastOnce` is the opt-in). Notifications older than 24 hours are never emailed. Every kind is emailed once a mailer is wired; `EmailKinds` narrows it.
- **Where:** `notify-email/design.md` decisions 3 and 4.
- **Alternative:** at-least-once by default; only offer and assigned kinds by default.
- **If reversed:** option defaults and docs.

### 12. No WebSocket on Fiber

- **Decided:** Fiber's adaptor cannot hand over the raw connection, so Fiber hosts use SSE. Stated limit.
- **Where:** `notify-realtime-adapters/design.md` decision 6.
- **Alternative:** a Fiber-native WebSocket module.
- **If reversed:** a new module with its own dependency.

### 13. WebSocket library: `github.com/coder/websocket`

- **Decided:** chosen over `gorilla/websocket` for context support, recent releases and a safe origin default.
- **Where:** `notify-realtime-adapters/design.md` decision 3.
- **If reversed:** confined to `notify/websocket`.

## Release and repository structure

### 14. hmntsk's first release waits for `sqlkit` (and `notify`) to move out

- **Decided:** under P2, `sqlkit` and `notify` are never tagged from this repository. `store/sqlcore` needs a tagged `sqlkit`, so hmntsk's first tag waits for that split; a release including `tasknotify` also waits for `notify`.
- **Where:** `sqlkit/design.md` decision 9; `notify-core/design.md` decision 12; `docs/releasing.md` step 0 (planned).
- **Alternative:** tag `sqlkit` and `notify` in this repository first and accept one import-path change later (P3).
- **If reversed:** consumers of early tags rewrite imports at the split.

### 15. Five `sqlkit` modules, and container helpers move from `storetest` to `sqlkittest`

- **Decided:** `sqlkittest` is its own module so `sqlkit` carries no testcontainers dependency. `storetest` keeps its helper names as delegates.
- **Where:** `sqlkit/design.md` decisions 1 and 7.
- **If reversed:** `sqlkit` depends on testcontainers, or helpers are duplicated.

### 16. Notification writes never join the host's task transaction

- **Decided:** stated limit; notification stores run in their own transactions.
- **Where:** `notify-core/design.md` decision 9; `sqlkit/design.md` decision 3 (explicit hand-off possible).
- **If reversed:** couples `notify` to `store/*` transaction keys, against the split rule.
