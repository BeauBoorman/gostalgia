# IPC

The environment's IPC layer lives in `internal/ipc`; the host-specific
listener lives in `platform/`.

## One router, two transports

The same `ipc.Router` serves:

- **In-process callers** — `Router.Dispatch(ctx, req)` directly (used by the
  runtime self-test, tests, and any in-process code).
- **Local socket clients** — `cmd/gctl`, the Charm shell, and later
  out-of-process applications.

Because both go through the same routes, the wire never defines a second API.

## Protocol

NDJSON: one JSON value per line, request then response, correlated by `id`.

```json
{"id":1,"method":"app/com.gostalgia.echo/echo","params":{"msg":"hi"}}
{"id":1,"ok":true,"data":{"msg":"hi","echoes":1}}
```

Errors: `{"id":1,"ok":false,"error":"..."}`. Handlers may not panic through
(`Dispatch` recovers). One line is bounded at 4 MiB.

## Authentication

The first request on every connection must be `auth` with the environment
token (`{"method":"auth","params":{"token":"..."}}`). The token lives in
`runtime.json` (mode 0600) under the environment root. Authenticated
connections receive the admin capability set.

This protects against accidental cross-user access only. It is local trust,
not a security boundary — see security.md.

## Method namespaces

| Namespace | Owner |
|---|---|
| `sys/*` (`ping`, `status`, `shutdown`) | sys service |
| `proc/*` (`list`, `stop`) | process service |
| `fs/*` (`list`, `read`, `write`, `mkdir`, `remove`) | fs service |
| `app/list`, `app/launch`, `app/stop`, `session/whoami` | sys service |
| `app/<app-id>/<method>` | the application instance |

App routes require `ipc` from the incoming caller, then execute under the
app's manifest grant (not the caller's grant). SDK service calls likewise
replace capabilities. `app/stop` checks `proc.stop`; `app/launch` uses the
runtime lifetime, not the request lifetime. The app contract includes the
[complete method schemas and capability table](applications.md#5-routes-and-scoped-service-calls).

## Endpoints per platform

- macOS/Linux: unix domain socket at `$TMPDIR/gostalgia-<hash>.sock` (path
  derived from the root to stay under the 104-byte limit).
- Windows: loopback TCP on an ephemeral port.

Clients dial via `platform.DialIPC(endpoint)`; both schemes are recorded in
`runtime.json`.

## Client

`ipc.Client` authenticates on construction and serializes calls (one
outstanding request per client). Each call honors the context deadline
(default 30 s). Multiplexing and server-push notifications are planned
(backlog #7 in status.md).
