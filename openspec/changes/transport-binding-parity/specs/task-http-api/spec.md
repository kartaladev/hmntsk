## ADDED Requirements

### Requirement: Server errors reveal no internal detail

Every `500` response SHALL carry the contract's JSON error with code `internal` and a fixed message that contains nothing from the underlying error. This includes a failure to resolve group membership. The system SHALL hand the underlying error, unmasked, to a server-side error handler.

By default, that handler SHALL log the error through the process's default structured logger. The host SHALL be able to replace the handler. Supplying an empty handler SHALL be refused when the contract is constructed.

The status mapping is unchanged: a group-resolution failure is still `500`, never `403`.

#### Scenario: A directory failure's cause stays on the server

- **WHEN** a client queries its inbox and the group resolver fails with an error naming the directory's host and bind DN, on any binding
- **THEN** the response status is `500`, the body's code is `internal`, and the body contains neither the host nor the bind DN

#### Scenario: The cause reaches the default handler

- **WHEN** the same failure occurs and the host supplied no error handler
- **THEN** the unmasked error is logged through the default structured logger

#### Scenario: A host routes the cause to its own handler

- **WHEN** the host supplies its own error handler and the same failure occurs
- **THEN** the host's handler receives an error from which the resolver's cause can be unwrapped, and the response body is unchanged

#### Scenario: An empty error handler is a wiring mistake

- **WHEN** a host constructs the contract with an empty error handler
- **THEN** construction fails with a configuration error

### Requirement: Request bodies are bounded identically on every binding

By default, every binding SHALL read at most 8 MiB of a request body. A body over the limit SHALL be refused with `413` and the contract's JSON error, and SHALL never be passed on truncated. The status and body SHALL be the same on every binding.

The host SHALL be able to set a different positive limit on each binding. A limit that is zero or negative SHALL be refused when the binding is constructed.

Where the host owns the framework application and has set a smaller framework-level limit, that limit applies first. The binding's documentation SHALL say so.

#### Scenario: An oversized body is refused, not truncated

- **WHEN** a client creates a task with a body of 9 MiB on any binding, and the host set no limit
- **THEN** the response status is `413`, the body is the contract's JSON error, and no task is created

#### Scenario: A tail that would complete the JSON is not read

- **WHEN** a client sends a 9 MiB body whose last bytes complete a valid JSON object
- **THEN** the response status is `413`, not `201` and not `400`

#### Scenario: A body under the default limit is accepted everywhere

- **WHEN** a client creates a task with a valid 5 MiB body on any binding, and the host set no limit
- **THEN** the response status is `201`

#### Scenario: A host raises the limit

- **WHEN** a host serves a binding with a 16 MiB limit and a client sends a valid 9 MiB body
- **THEN** the response status is `201`

#### Scenario: A non-positive limit is a wiring mistake

- **WHEN** a host constructs a binding with a body limit of zero
- **THEN** construction fails with a configuration error and no route is served

### Requirement: Request bodies are read exactly as sent

The contract SHALL NOT decode a request body's content coding. A request whose `Content-Encoding` is present and is not `identity` SHALL be refused with `400` and the contract's JSON error, identically on every binding.

A host that accepts compressed bodies SHALL decode them in its own middleware and remove the header before the request reaches the binding. The body limit then applies to the bytes the binding receives.

#### Scenario: A gzip body is refused on every binding

- **WHEN** a client creates a task with a gzip-compressed body and `Content-Encoding: gzip`
- **THEN** the response status is `400` on every binding, and no task is created

#### Scenario: Host middleware decodes first

- **WHEN** the host's middleware decompresses a gzip body and removes the `Content-Encoding` header before the binding
- **THEN** the create succeeds with `201`

### Requirement: Client-supplied task identifiers are addressable

A task identifier supplied on create SHALL be one a client can read back at `{base}/tasks/{id}` on every binding. By default, the contract SHALL refuse a create with `400`, and a problem pointing at `/id`, when the identifier:
- is longer than 128 characters;
- contains a character outside ASCII letters, digits, `-`, `.`, `_` and `~`;
- is `.` or `..`;
- equals a literal route segment in the identifier's position, such as `count`.

The host SHALL be able to replace the character and length rule with its own policy. Whatever the policy, an identifier containing `/`, `.`, `..`, or a reserved route segment SHALL still be refused, because no binding can route to it. Supplying an empty policy SHALL be refused when the contract is constructed.

Every binding SHALL percent-decode path parameters before handing them to the contract.

#### Scenario: An identifier that collides with a route is refused

- **WHEN** a client creates a task with the identifier `count`
- **THEN** the response status is `400`, and its problem points at `/id`

#### Scenario: An identifier with a space is refused by default

- **WHEN** a client creates a task with the identifier `a b`, and the host supplied no policy
- **THEN** the response status is `400`

#### Scenario: An identifier with a slash is refused by default

- **WHEN** a client creates a task with the identifier `x/y`
- **THEN** the response status is `400`

#### Scenario: A safe identifier round-trips

- **WHEN** a client creates a task with the identifier `order-42_v1.a~b`
- **THEN** the create answers `201`, and a read of `{base}/tasks/order-42_v1.a~b` answers `200` on every binding

#### Scenario: A host policy admits a wider charset

- **WHEN** the host supplies a policy that also admits spaces, and a client creates a task with the identifier `a b`
- **THEN** the create answers `201`, and a read of `{base}/tasks/a%20b` answers `200` on every binding

#### Scenario: A host policy cannot admit a reserved segment

- **WHEN** the host supplies a policy that admits every identifier, and a client creates a task with the identifier `count`
- **THEN** the response status is `400`

#### Scenario: An empty policy is a wiring mistake

- **WHEN** a host constructs the contract with an empty identifier policy
- **THEN** construction fails with a configuration error

### Requirement: Path and method matching is exact and identical on every binding

A request SHALL be served by a contract route only when its method and path match that route exactly:
- path matching SHALL be case-sensitive;
- a trailing slash, or an empty path segment, SHALL make a path different from the route;
- `HEAD` SHALL be a method the contract does not serve.

Such a request SHALL answer `404` in the contract's JSON error shape, with no redirect and no `Allow` header, identically on every binding's convenience constructor.

Where the host owns the router and has configured it to redirect or clean paths before a binding sees them, that redirect is the host's. The binding's documentation SHALL name the router settings that keep parity.

#### Scenario: An upper-cased path is not found

- **WHEN** a client requests `GET /V1/TASKS/count` on any binding's convenience constructor
- **THEN** the response status is `404` with the contract's JSON error

#### Scenario: A trailing slash is not found and not redirected

- **WHEN** a client requests `GET {base}/tasks/count/`
- **THEN** the response status is `404` with the contract's JSON error, not a redirect

#### Scenario: A double slash is not found and not redirected

- **WHEN** a client requests `GET /v1//tasks/count`
- **THEN** the response status is `404` with the contract's JSON error, not a redirect

#### Scenario: HEAD on a read route is not found

- **WHEN** a client sends `HEAD {base}/tasks/count`
- **THEN** the response status is `404` on every binding

#### Scenario: HEAD is refused on a host-owned Fiber application

- **WHEN** a host mounts the contract on a Fiber application it built with default settings, and a client sends `HEAD {base}/tasks/count` or `GET` with an upper-cased path
- **THEN** the response status is `404` with the contract's JSON error

### Requirement: The base path is validated at construction

The contract SHALL refuse, when it is constructed, a base path that is empty or `/`, that does not begin with `/`, that has an empty, `.` or `..` segment, or that contains a character outside ASCII letters, digits, `-`, `.`, `_` and `~` in any segment. A single trailing `/` SHALL be accepted and removed.

The default base path SHALL remain `/v1`.

#### Scenario: A base path without a leading slash is refused

- **WHEN** a host constructs the contract with the base path `v2`
- **THEN** construction fails with a configuration error and no route is served

#### Scenario: An empty base path is refused

- **WHEN** a host constructs the contract with the base path `""` or `/`
- **THEN** construction fails with a configuration error

#### Scenario: A valid base path is served on every binding

- **WHEN** a host constructs the contract with the base path `/api/tasks-v2/`
- **THEN** every binding serves the task-type list at `/api/tasks-v2/task-types`

### Requirement: Request bodies are decoded strictly

The contract SHALL refuse with `400`, and a problem pointing at the offending member, a request body in which:
- a member name matches a contract field only when case is ignored;
- a member name appears twice in the same object.

This SHALL hold for every object the contract defines, at any depth. Member names inside consumer-owned data SHALL NOT be inspected. That data is task input and output payloads, patches, correlation `extra` values and callback `referenceParameters`.

By default, a member that is not a contract field SHALL also be refused with `400`. The host SHALL be able to have such members ignored instead. Case-folded and duplicate members SHALL be refused either way. Supplying an unknown mode SHALL be refused when the contract is constructed.

#### Scenario: A case-folded duplicate does not override the type

- **WHEN** a client creates a task with the body `{"type":"approval","input":{…},"Type":"note"}`
- **THEN** the response status is `400`, and no task is created

#### Scenario: A duplicated member is refused

- **WHEN** a client creates a task with a body containing `"type"` twice
- **THEN** the response status is `400`

#### Scenario: An unknown member is refused by default

- **WHEN** a client creates a task with the body `{"type":"note","createdBy":"ceo"}`, and the host did not change the mode
- **THEN** the response status is `400`, and its problem points at `/createdBy`

#### Scenario: A host chooses to ignore unknown members

- **WHEN** the host sets unknown members to be ignored, and a client sends the same body
- **THEN** the response status is `201`, and the task's creator is the acting user, not `ceo`

#### Scenario: Payload members are never inspected

- **WHEN** a client creates a task whose `input` contains the members `Amount` and `amount`, and a member the schema does not describe
- **THEN** the contract's decoding does not refuse it, and the payload is passed to the engine unchanged

## MODIFIED Requirements

### Requirement: A host can serve its own routes beside the contract

When a binding offers a constructor that returns a framework application the host can add routes to, a route the host adds to that application after construction SHALL be served. A path, or a method on a contract path, that neither the contract nor the host serves SHALL still answer `404` in the contract's JSON error shape, with the same status and body on every binding. The system SHALL also let a host that builds its own application get the same unknown-route answer while keeping its own error handling.

When a binding mounts the contract on a router the host owns, it SHALL register nothing outside the contract's base path. It SHALL be mountable whether the host registered its own root route before or after it. A route pattern that conflicts with one the host already registered SHALL be reported as a configuration error from the mount call, never as a panic.

#### Scenario: A route added after construction is served

- **WHEN** a host builds an application with a binding's convenience constructor and then adds its own route
- **THEN** a request to that route is answered by the host's handler

#### Scenario: Unknown routes still answer in the contract's shape

- **WHEN** a client requests a path that neither the contract nor the host serves, on an application built by a convenience constructor
- **THEN** the response status is `404`, the body is the contract's JSON error with code `not_found`, and no `Allow` header is sent

#### Scenario: A wrong method on a contract path answers not found

- **WHEN** a client sends a method the contract does not serve to a path the contract serves
- **THEN** the response status is `404` in the contract's JSON error shape, identical across bindings

#### Scenario: A host with its own application keeps its error handling

- **WHEN** a host mounts the contract on an application it built, with its own error handling and the binding's unknown-route handling wrapped around it
- **THEN** unknown routes answer the contract's JSON `404`, and every other error from the host's handlers reaches the host's error handling unchanged

#### Scenario: Mounting beside a host root route

- **WHEN** a host registers its own `/` route on a standard-library mux, either before or after mounting the contract on the same mux
- **THEN** mounting succeeds without a panic, `/` is answered by the host's handler, and `{base}/task-types` is answered by the contract

#### Scenario: Unknown paths under the base path still answer in the contract's shape

- **WHEN** a host mounts the contract on its own standard-library mux, and a client requests `{base}/nothing`
- **THEN** the response status is `404` with the contract's JSON error

#### Scenario: A conflicting host pattern is a configuration error

- **WHEN** a host has already registered exactly the pattern the binding needs for its base path, and then mounts the contract
- **THEN** the mount call returns a configuration error and does not panic
