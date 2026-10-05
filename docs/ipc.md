# IPC

The environment's IPC layer lives in `internal/ipc`; the host-specific
listener lives in `platform/`.

## One router, two transports

The same `ipc.Router` serves:

- **In-process callers** — `Router.Dispatch(ctx, req)` directly (used by the
  runtime self-test, tests, and any in-process code).
- **Local socket clients** — `cmd/gctl`, and later out-of-process
  applications.

Because both go through the same routes, the wire never defines a second API.

## Protocol

NDJSON: one JSON value per line, request then response, correlated by `id`.

```json
{"id":1,"method":"app/com.gostalgia.echo/echo","params":{"msg":"hi"}}
{"id":1,"ok":true,"data":{"msg":"hi","echoes":1}}
```

Errors: `{"id":1,"ok":false,"error":"..."}`. Handlers may not panic through
(`Dispatch` recovers). One line is bounded at 4 MiB.

Route retraction is safe against in-flight dispatches: `Unhandle` and
`UnhandlePrefix` wait for dispatches that already resolved a route to
finish before returning, so after either returns no dispatch has or will
reach a retracted handler. Handlers run outside the router lock (a handler
may register routes — application launch does), but a handler must not
synchronously retract its own route.

## Authentication

The first request on every connection must be `auth` with the environment
token (`{"method":"auth","params":{"token":"..."}}`). The token lives in
`runtime.json` (mode 0600 on unix; on Windows the mode does not map to an
ACL — the file inherits the environment directory's permissions) under the
environment root. Authenticated connections receive the admin capability set.

This protects against accidental cross-user access only. It is local trust,
not a security boundary — see security.md.

## Method namespaces

| Namespace | Owner |
|---|---|
| `sys/*` (`ping`, `status`, `shutdown`) | sys service |
| `proc/*` (`list`, `stop`) | process service |
| `fs/*` (`list`, `read`, `write`, `mkdir`, `remove`) | fs service |
| `app/list`, `app/launch`, `session/whoami` | sys service |
| `app/<app-id>/<method>` | the application instance |

## Endpoints per platform

- macOS/Linux: unix domain socket at `$TMPDIR/gostalgia-<hash>.sock` (path
  derived from the root to stay under the 104-byte limit).
- Windows: loopback TCP on an ephemeral port.

Clients dial via `platform.DialIPC(endpoint)`; both schemes are recorded in
`runtime.json`.

## Client

`ipc.Client` authenticates on construction and serializes calls (one
outstanding request per client). Each call honors both the context deadline
(default 30 s) and plain cancellation: a context that is cancelled without
a deadline aborts the in-flight call immediately instead of waiting out the
default. An aborted call gives up mid-protocol — the response may still
arrive later — so the client marks itself broken and further calls fail
immediately; use a fresh client. Multiplexing and server-push notifications
are planned (backlog #7 in status.md).

## Shutdown

`Server.Close` stops the listener and closes every live connection,
including idle ones, and never waits on a connection it did not close
itself. Connections accepted concurrently with `Close` are covered by the
same sweep, so shutdown cannot hang on a connected client.
