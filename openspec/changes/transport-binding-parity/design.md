## Context

See proposal.md for why. Each finding has a failing reproduction in the scratch module `$V/transport`, where `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`. Run them with `cd $V/transport && GOWORK=off go test -count=1 -run '<Name>' .`.

The current state, checked in the code and in the pinned dependencies (gin v1.12.0, Fiber v3.5.0):

- **T7, `transport/core/errors.go:77`:** `errorDetail` masks every `CodeInternal` message except `ErrGroupResolution`, whose `err.Error()` (resolver cause included) goes to the client. The engine and notify use `func(ctx, err)` hooks for side-channel errors (`WithDispatchErrorHandler`, `WithSweepErrorHandler`, …). `transportcore` has none. Reproduction: `errors_test.go` `TestGroupResolutionFailureBody`.
- **T8:**
  - `transport/http/handler.go:122` uses `http.MaxBytesReader` and answers a hand-written JSON `413`. This is correct.
  - `transport/gin/router.go:148` uses `io.LimitReader`, which truncates silently. A 9 MiB body gives `201`, or `400 "unexpected end of JSON"`.
  - `transport/fiber/app.go` has no limit option. fasthttp's default `BodyLimit` (4 MiB, `fiber/app.go:614`) answers `413` in plain text through `DefaultErrorHandler`.
  - Every `WithMaxBodyBytes(limit<=0)` is ignored silently.
  - Reproduction: `request_test.go` `TestCreateRequestHandling/*/T8_*`.
- **T9:** `fibertransport.handle` reads `c.Body()`, which decodes `Content-Encoding` (`fiber/req.go:150`). net/http and gin pass the raw bytes. `transportcore.Request` carries no headers. Reproduction: `TestCreateRequestHandling/*/T9_gzip*`.
- **T10:**
  - `createTask` passes `body.ID` to the engine unchecked.
  - Fiber's `c.Params` returns the raw, percent-encoded segment.
  - gin and net/http route on the decoded path, so `%2F` splits a segment.
  - `GET {base}/tasks/count` is registered before `{id}`, so an id `count` is unreadable everywhere.
  - Reproduction: `routing_test.go` `TestClientSuppliedIDIsAddressable`.
- **T11:**
  - net/http's `ServeMux` redirects unclean paths (`//` → `301`/`307`, HTML), and a `GET` pattern also matches `HEAD`.
  - `gin.New()` defaults `RedirectTrailingSlash: true` (`gin.go:211`).
  - `fiber.New()` defaults `CaseSensitive: false` and `StrictRouting: false`, and serves `HEAD` for `GET` routes.
  - Reproduction: `TestUnservedPathVariants`.
- **T12:** `httptransport.Mount` registers `"/"` on the host's mux (`handler.go:113`). The mux panics on the duplicate whichever side registers second. Reproduction: `wiring_test.go` `TestMountBesideHostRoutes`.
- **T13:** `WithBasePath` trims trailing `/` and ignores empty values, and nothing else. Reproduction: `TestBasePathWithoutLeadingSlash`.
- **T14:** `decode` is `json.Unmarshal`. It matches member names case-insensitively (the last match wins) and drops unknown members. Reproduction: `TestCreateRequestHandling/*/T14_*`.
- **Shared suite:** `transporttest.RunSuite(t, mount)` runs over the net/http harness (`Handler`), the gin harness (`gin.New()` + `Mount` + `NoRoute`/`NoMethod`), and two Fiber harnesses (`fiber.New()` + `Mount`, and `App()`).
- **Releases:** no git tag exists yet, so default changes are free but recorded (`library-design.md` §7).

## Goals / Non-Goals

**Goals:**
- Every case in the scratch reproductions, ported into `transporttest`, passes on every harness.
- Each new behaviour has a default that needs no configuration and an override that needs no fork. Where the override has a line it cannot cross, the line is enforced and documented.

**Non-Goals:**
- Supporting `HEAD` or content-coded request bodies in the contract.
- Changing the status mapping in `StatusFor`/`CodeFor`.
- Rewriting a host-owned router's settings from inside `Mount`.

## Decisions

### 1. `500` bodies are always masked; the cause goes to `WithInternalErrorHandler` (T7)

```go
// WithInternalErrorHandler receives every error the contract answers with 500,
// unmasked. The default logs it with slog.Default().ErrorContext.
func WithInternalErrorHandler(handler func(ctx context.Context, err error)) Option
```

- `errorDetail` masks every `CodeInternal`, the group-resolution case included:
  - a group-resolution failure gets the message `"group membership could not be resolved"`, so a client can still tell it from a generic failure without learning the cause;
  - everything else keeps `"the request could not be completed"`.
- `fail` becomes a method on `*API`, so it can reach the handler. The response-encoding failure in `encode` is reported too.
- **Default:** log through `slog.Default()` at error level, with the error as an attribute. Losing the cause entirely would be safe but useless to operate. Before this change the cause was at least visible (to the wrong party).
- **Override:** `WithInternalErrorHandler(fn)`. A host that wants silence passes a no-op. A `nil` handler is a `ConfigurationError` from `New`, matching `WithQueryAuthorizer(nil)`.
- **Alternative:** a no-op default, like `WithDispatchErrorHandler`. Rejected because a `500` with no trace anywhere fails the "sensible default" test. The difference from the engine's hooks is noted in the godoc.

### 2. One body limit, one `413`, on every binding (T8)

- `transportcore.DefaultMaxBodyBytes = 8 << 20`. Each binder's `MaxBodyBytes` constant equals it (Fiber gains one).
- `transportcore.StatusPayloadTooLarge = 413` and `transportcore.PayloadTooLargeResponse()`. The body is the one net/http writes today: code `validation_failed`, message "the request body is too large". All three binders write this one response.
- **gin:** `http.MaxBytesReader(c.Writer, c.Request.Body, limit)`. On `*http.MaxBytesError` it writes the `413`, and any other read error keeps the existing `400`.
- **Fiber:**
  - `WithMaxBodyBytes(limit)`. The binder checks `len(c.Request().Body()) > limit` and answers the `413` itself, so the limit holds on a host-owned app too.
  - `App()` sets `fiber.Config{BodyLimit: limit}`, so fasthttp stops reading at the same size. It installs the new `ErrorHandler(next)`, which answers `ErrNotFound` and `ErrMethodNotAllowed` with the contract `404` (as `NotFoundErrorHandler` does) and `ErrRequestEntityTooLarge` with the contract `413`.
  - `NotFoundErrorHandler` stays, unchanged, for hosts already using it.
- **Default:** 8 MiB, with a JSON `413` everywhere.
- **Override:** `WithMaxBodyBytes(n)` on each binder.
- **Stated limit:** on a host-owned Fiber app, fasthttp's `BodyLimit` (default 4 MiB) is applied first. To get the contract's answer and size, the host sets `BodyLimit` to at least the binder's limit and wraps its error handler with `ErrorHandler`. The `Mount` godoc says so.
- **Wiring mistake:** `WithMaxBodyBytes(n <= 0)` makes `Mount`/`Handler`/`Engine`/`App` return a configuration error. Options record the bad value, and the constructor checks it.
- **Alternative:** a single limit option on `transportcore.API`. Rejected. The limit is enforced where the bytes are read, which is the binder, and the host-owns-transport requirement keeps it there.

### 3. The contract refuses content-coded bodies; Fiber reads the raw body (T9)

- `transportcore.Request` gains `ContentEncoding string`, the request's `Content-Encoding` header as received. Each binder fills it in.
- The core's body decoding refuses a non-empty value other than `identity` (case-insensitive) with `400`, pointer `""`, message "Content-Encoding <x> is not supported; decode the body before the contract". The check runs only on routes that read a body.
- Fiber reads `c.Request().Body()`, not `c.Body()`.
- **Default:** no decoding; refused with `400`, identically everywhere.
- **Override:** host middleware decompresses and deletes the header before the binder, with its own decompressed-size limit.
- **Why `400` and not `415`:** the contract's error mapping has no `415`, and a content coding is a malformed request as far as the contract is concerned. Adding a status to the mapping is a larger contract change than this defect needs.
- **Alternative:** decode gzip in every binding. Rejected. It is a decompression-bomb surface, and it is a transport concern the host already owns.

### 4. Client-supplied ids must be addressable (T10)

```go
// TaskIDPolicy decides whether a client-supplied task id is acceptable.
type TaskIDPolicy func(id string) error

// DefaultTaskIDPolicy admits 1–128 characters from [A-Za-z0-9._~-].
func DefaultTaskIDPolicy(id string) error

func WithTaskIDPolicy(policy TaskIDPolicy) Option
```

- `createTask` checks a non-empty `body.ID` in two steps:
  1. The fixed rule, which is not replaceable: no `/`, not `.` or `..`, and not a literal segment at the `{id}` position of any route. That set is computed from `Routes()`, so a future literal route is covered automatically. Today it is `count`.
  2. The policy.
  
  A failure is a `ValidationError` with pointer `/id`, answered `400`.
- **Fiber:** `request.Params[name]` is `url.PathUnescape(c.Params(name))`. A malformed escape answers the contract `404`, because such a path cannot name a task. gin already unescapes (`UnescapePathValues: true`), and net/http's `PathValue` is decoded.
- **Default:** the URL-unreserved charset of RFC 3986, 1–128 characters. It survives every router without escaping.
- **Override:** `WithTaskIDPolicy(fn)`. A `nil` policy is a `ConfigurationError`.
- **Stated limit:** the fixed rule cannot be relaxed, because no binding can route to those ids. The godoc says so.
- **Stated limit:** ids given to the Go API directly (`Service.Create`) are not checked by the transport. Such an id outside the policy may be unreachable over HTTP. This is documented, and the engine-level rule belongs to `engine-input-validation`.
- **Alternative:** rename the count route (`/tasks:count`). Rejected: it breaks every client, and it does not fix `/` or spaces.

### 5. Exact path matching on every convenience constructor; checks in the adapter where possible (T11)

- **Method check in every adapter:** each binder compares the request's actual method with `route.Method`, and answers the contract `404` on a mismatch. This covers net/http's implicit `HEAD` and Fiber's auto-`HEAD`, including on host-owned routers.
- **Fiber path check in the adapter:** the adapter compares `c.Path()` with the route's pattern, literal segments case-sensitively and no trailing slash, and answers the contract `404` on a mismatch. This covers host-owned Fiber apps too. `App()` also sets `CaseSensitive: true` and `StrictRouting: true`.
- **gin:** `Engine()` sets `RedirectTrailingSlash = false` and `RedirectFixedPath = false`. `RemoveExtraSlash` stays `false`.
- **net/http:**
  - `Handler()` wraps the mux in a front handler that answers the contract `404` for a path that is not canonical (`path.Clean(p) != p`, or a trailing `/`), before the mux can redirect.
  - `Mount` cannot intercept the host mux's redirect.
- **Default:** the convenience constructors (`Handler`, `Engine`, `App`) give identical answers.
- **Override:** a host that wants redirects, or case-folding, owns its router and configures it.
- **Stated limit:** on a host-owned gin engine or net/http mux, the router's own redirects happen before the binding. The `Mount` godoc names the settings for parity: gin `RedirectTrailingSlash=false`, `RedirectFixedPath=false`; net/http wrap with `httptransport.CanonicalPaths(h)`, a small exported middleware that is also used by `Handler()`.
- **Suite harness:** gin's suite harness keeps `gin.New()` + `Mount`, and sets the two settings as a documented host would. A second gin harness runs `Engine()`, as Fiber already does with `App()`.

### 6. net/http `Mount` claims only its base path, and never panics (T12)

- `Mount` registers the routes plus `{base}/` → contract `404`, not `/`. `Handler()` still registers `/` on its own fresh mux, because the whole mux is the contract's.
- `mux.Handle` panics on a conflicting pattern. `Mount` recovers the panic around registration and returns a configuration error naming the pattern.
- **Stated limit:** routes registered before the panic stay on the host's mux, because a `ServeMux` cannot unregister. The godoc says so, and the error is meant to fail start-up.
- New `NotFoundHandler()` gives the contract `404`, for a host that wants it elsewhere.
- **Default:** the host's `/` is the host's.
- **Override:** the host registers `NotFoundHandler()` wherever it wants the contract's answer.

### 7. `WithBasePath` is validated in `New` (T13)

- The option records the raw value. `New` validates it:
  - one trailing `/` is removed;
  - the result must begin with `/`, have at least one segment, and have no empty, `.` or `..` segments;
  - each segment must use only `[A-Za-z0-9._~-]`, which excludes `{`, `}`, spaces, `?` and `#`.
  
  A failure is a `hmntsk.ConfigurationError` naming the value.
- **Default:** `/v1`.
- **Override:** any valid path.
- **Why refuse `""` and `/`:** serving the contract at the root would put its not-found handling over the host's whole namespace. The contract is versioned from the first release (`DefaultBasePath` godoc). A host that truly wants root can still route a prefix to it.

### 8. Strict body decoding, unknown members refused by default (T14a, T14b)

```go
type UnknownFields int
const (
    UnknownFieldsReject UnknownFields = iota // default
    UnknownFieldsIgnore
)
func WithUnknownFields(mode UnknownFields) Option
```

- A token walk over the body, driven by the target struct's `json` tags through reflection, checks every contract-defined object (`CreateTaskRequest`, `OperationRequest`, `CandidatePool`, `EscalationPolicy`, `CorrelationData`, `CallbackTarget`). It finds:
  - an exact-case duplicate;
  - a case-folded match of a field that is not an exact match;
  - an unknown member (in reject mode).
- `json.RawMessage` fields and `map` fields (`input`, `output`, `patch`, `extra`, `referenceParameters`) are skipped as opaque values. Their members are never inspected, which keeps the payload promise.
- The walk runs before `json.Unmarshal`, so the struct is only filled from a body that passed.
- The problem's pointer is the JSON Pointer of the offending member, such as `/createdBy` or `/Type`.
- **T14b decision: strict.** A silently dropped member makes a client believe it set something it did not. `createdBy` is the audit's example, and the misspelling `deadlineSecond` would silently take the type's default deadline. Refusing is the safe default. The cost is forward compatibility, since an older server refuses a newer client's new field. A host can buy that back with `WithUnknownFields(UnknownFieldsIgnore)`.
- **Stated limit:** case-folded and duplicate members are refused in both modes. Ignoring them would let one member override another silently, which is the defect.
- **Wiring mistake:** an out-of-range mode is a `ConfigurationError`.
- **Alternative:** `encoding/json/v2`, which is case-sensitive and rejects duplicates. Rejected for now: it is behind `GOEXPERIMENT` in the pinned toolchain.

## Risks / Trade-offs

- [Strict decoding breaks clients sending extra fields] → pre-tag, so free to change. The `400` names the member. `UnknownFieldsIgnore` is one option away.
- [The default `slog` handler logs in a host that routes logs elsewhere] → it uses `slog.Default()`, which the host already controls. `WithInternalErrorHandler` replaces it.
- [The token walk costs a second pass over the body] → bodies are at most 8 MiB by default, and the walk allocates no values. A benchmark lands in task 2.5.
- [`HEAD` now answers `404` on net/http and Fiber] → pre-tag. The contract never documented `HEAD`, and gin already answered `404`.
- [A host-owned router keeps its redirects] → stated limit, documented settings, and `CanonicalPaths` for net/http.

## Migration Plan

Pre-tag. No data migration. Hosts that relied on a silently ignored `WithBasePath("")`, a non-positive body limit, unknown request members, or `HEAD` must adjust. Each is a construction error or a `400` whose body names the cause. Rollback is a revert of the change.

## Assumptions

Recorded instead of asking, per the workflow:

- The `413` body keeps code `validation_failed`, matching net/http today, rather than a new code.
- The fixed id rule includes `.` and `..`, which net/http cleans away, as well as literal route segments.
- The id length cap of 128 is an assumption. It is ample for UUIDs, ULIDs and composite business keys. The implementer confirms that no store dialect declares a narrower id column; a narrower one lowers the cap.
- `HEAD` is unserved rather than implemented, because the contract lists no `HEAD` operation.

## Open Questions

- Should the engine's own hooks later move to a logging default, for consistency with decision 1? That is deferrable and does not change this change's specs or tasks.
