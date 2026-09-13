## Why

`GET /tasks/{id}` and `GET /tasks/{id}/history` return any task to any caller. Only the inbox query and count endpoints go through `QueryAuthorizer`. Notifications will put direct task-detail links in front of many users, and a notification can outlive the recipient's right to the task (someone else claimed it, or the task was escalated away). Reading a single task needs the same opinionated, overridable protection the inbox already has, before links to it are handed out.

## What Changes

- The HTTP contract authorizes reading a single task and reading its history against the acting user, before returning anything.
- **Default policy (safe):** permit the read when the acting user holds the task, is eligible for it (a candidate user or a member of a candidate group, and not excluded), or created it. Refuse otherwise, and refuse when no acting user is established.
- **Override:** a `TaskReadAuthorizer` port with a `Func` adapter, installed through `WithTaskReadAuthorizer`. `AllowAll`, the existing explicit opt-out, also permits every read. A nil policy is a configuration error from `transportcore.New`.
- **Refusal semantics:**
  - A refused read answers `403` with the policy's message, as a refused query does.
  - Whether a refused read of an unknown task answers `404` or `403` is settled in design, because it decides whether task identifiers can be probed.
- **BREAKING (unreleased):** a host that relied on unrestricted reads must opt out with `AllowAll`. Nothing is tagged, so no released consumer breaks. The change is recorded as a default change under `.claude/rules/library-design.md`.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-http-api`: reading one task and reading its history are authorized against the acting user, by a default policy the host can replace. The error mapping gains the refused-read case.

## Impact

- **Code:** `transport/core` (`authorize.go`, `api.go` handlers `getTask` and `getHistory`, `errors.go`, the OpenAPI document).
  - The default policy needs group membership. It uses the engine's existing eligibility check (`hmntsk.IsEligible`) through the service's resolver; how the transport reaches that resolver is settled in design.
- **Tests:** the shared transport suite in `transporttest` runs the new cases against the `http`, `gin` and `fiber` bindings.
- **Docs:** `docs/inbox.md` and the README describe the default and the override.
- **Order:** independent of the other notification changes, so it can land in parallel with `event-audience-snapshot`. It must land before notification links to task details are released.
