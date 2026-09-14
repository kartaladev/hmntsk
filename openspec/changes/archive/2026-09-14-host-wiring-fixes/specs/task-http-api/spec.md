## ADDED Requirements

### Requirement: A host can serve its own routes beside the contract

When a binding offers a constructor that returns a framework application the host can add routes to, a route the host adds to that application after construction SHALL be served. A path, or a method on a contract path, that neither the contract nor the host serves SHALL still answer `404` in the contract's JSON error shape, with the same status and body on every binding. The system SHALL also let a host that builds its own application get the same unknown-route answer while keeping its own error handling.

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
