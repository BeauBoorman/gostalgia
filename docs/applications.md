# Gostalgia app SDK — authoring specification

This is the spec for adding an app without consulting runtime internals. The
public Go package is `gostalgia/sdk` (standard library only). Apps are currently
**trusted, compiled-in Go packages**, one live instance per ID. JSON declares
an app but cannot load arbitrary Go code or a host executable. No plugins,
package installer, subprocess app launcher, or VirelaiOS userspace port yet.

## 1. Files and manifest

Create `apps/<shortname>/manifest.json` and `apps/<shortname>/<shortname>.go`.
The JSON file is the source of truth; embed it with `//go:embed manifest.json`.
Exactly one JSON object, no unknown fields or trailing values:

```json
{
  "id": "com.example.counter",
  "name": "Counter",
  "version": "0.1.0",
  "entrypoint": "counter",
  "permissions": ["ipc"],
  "description": "Counts requests within one launch."
}
```

| Field | Required | Meaning / validation |
|---|---|---|
| `id` | yes | Unique reverse-DNS, `^[a-z][a-z0-9]*(\.[a-z0-9-]+)+$`. Process name and route namespace. |
| `name` | yes | Nonblank display name. |
| `version` | yes | Numeric `MAJOR.MINOR.PATCH`, no prerelease suffix. |
| `entrypoint` | yes | Unique compiled factory key, `^[a-z][a-z0-9_-]*$`. Not a host path. |
| `permissions` | no | Array of distinct capability strings; omitted/empty means no grants. Unknown, duplicate, and `admin` permissions are rejected. |
| `description` | no | App shelf description. |

`sdk.ParseManifest([]byte)` decodes and validates; `Manifest.Validate()` checks
a Go value. Registration takes a defensive copy of permissions. Mutating the
informational `Context.Manifest` never changes the runtime grant.

## 2. Complete implementation template

This is an illustrative **second-app template**, not another installed demo.
Copy it, choose your own identity, and supply the JSON above. No internal
packages, Charm imports, terminal writes, or host filesystem access needed:

```go
package counter

import (
    "context"
    _ "embed"
    "encoding/json"
    "sync"

    "gostalgia/sdk"
)

//go:embed manifest.json
var manifestJSON []byte

func Manifest() sdk.Manifest {
    m, err := sdk.ParseManifest(manifestJSON)
    if err != nil { panic(err) } // invalid compiled-in data: programmer error
    return m
}

func Factory() (sdk.Instance, error) { return &Counter{}, nil }

type Counter struct {
    mu sync.Mutex
    count int
}

func (c *Counter) Init(app *sdk.Context) error {
    return app.Handle("next", func(ctx context.Context, raw json.RawMessage) (any, error) {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.count++
        return struct { Count int `json:"count"` }{c.count}, nil
    })
}

func (c *Counter) Run(ctx context.Context) error {
    <-ctx.Done()
    return nil
}

func (c *Counter) Stop(ctx context.Context) error { return nil }
```

`apps/echo` is the sole installed exemplar. Its `identity` method also shows a
system-service call using `Context.Call`.

## 3. Register and seed (both required)

Edit `apps/apps.go`:

1. Import `gostalgia/apps/counter` alongside the existing Echo import.
2. In `Register`, add the following before `return nil`:

   ```go
   if err := r.RegisterBuiltin(counter.Manifest(), counter.Factory); err != nil {
       return fmt.Errorf("apps: register counter: %w", err)
   }
   ```

3. Add `counter.Manifest()` to the `[]sdk.Manifest{echo.Manifest()}` slice in
   `apps.Manifests`. The runtime passes these declarations to
   `app.SeedManifests`, which creates
   `/apps/manifests/com.example.counter.json` in the environment VFS, only if
   absent. App packages never receive the VFS or import runtime managers.

Rebuild `go build ./...`. The app now appears in the shelf and `app/list`.
Echo is auto-launched at boot; **new apps are not auto-launched**. Use
`launch com.example.counter` in the shell. Duplicate IDs/factory keys fail
registration. Registry loading validates the whole manifest directory before
adding anything. Builtin definitions win over disk copies: editing the seeded
JSON does **not** change a compiled builtin's permissions. Change the source
manifest and rebuild. For loaded, non-builtin manifests, an entrypoint must
already have a compiled factory or launch fails; dynamic install is deferred.

## 4. Lifecycle — exact contract

```go
type Factory func() (sdk.Instance, error)
type Instance interface {
    Init(*sdk.Context) error
    Run(context.Context) error
    Stop(context.Context) error
}
```

1. Manager reserves the ID (also excludes simultaneous initializing launches).
2. Factory returns a **fresh, non-nil instance**. Allocate only; defer acquiring
   resources to Init. If Factory fails, it owns cleanup of its allocations.
3. Init runs synchronously and must finish promptly. Store the supplied context,
   acquire resources, declare routes. Init must not start a forever-loop. Routes
   are staged until successful Init and registered atomically; failures expose
   no partially initialized route set.
4. Run starts as a tracked `inproc` process. Its context has exactly the
   manifest grant, never an inherited operator grant. Run blocks while the app
   is alive, returns nil on normal cancellation, or an error on failure.
5. On Run return or cancellation: reject new handlers, cancel app lifetime,
   retract `app/<id>/` routes, and drain in-flight handlers (five-second cleanup
   deadline). Then call Stop **once**, even after failed Init. Stop receives a
   fresh context, not Run's canceled one, and must honor its deadline. If handler
   draining exhausts the deadline, Stop sees an expired context: release what
   you safely can and return an error, not success.
6. Remove running state; publish `app.state` with `launched`/`exited` and PID.
   `Manager.Stop` waits for process completion including cleanup. Natural exit
   also cleans up. A later launch creates a new instance with new state.

Lifecycle panics become errors; route panics become IPC errors. A panic is not
successful execution. All app goroutines must observe Run's cancellation and
be joined before Run returns. Handlers are concurrent with Run and each other;
use mutexes/channels for shared state. Stop runs after handler drain in the
normal case. Handle partial initialization safely in Stop.

**Cooperative, not preemptive:** the runtime cannot kill an in-process goroutine.
Ignoring cancellation/deadlines can hang cleanup; stop requests time out, not
magically succeed. Do not synchronously stop yourself, call `proc/stop` on your
own PID, or shut down the environment from a handler: cleanup would wait for
that handler. To exit yourself, signal your Run loop to return rather than calling Stop on
yourself. Control apps should return their handler before synchronously stopping
another app, avoiding cycles of handlers waiting for one another.

## 5. Routes and scoped service calls

```go
type Handler func(context.Context, json.RawMessage) (any, error)
func (c *sdk.Context) Handle(name string, h sdk.Handler) error
func (c *sdk.Context) Call(ctx context.Context, method string, params, out any) error
func sdk.DecodeParams(raw json.RawMessage, out any) error
```

- Handle is **Init-only**. Names match `^[a-z][a-z0-9_-]*$`; no slashes, global
  methods, or overriding system routes. `Handle("next", h)` serves
  `app/com.example.counter/next`. Duplicate routes are errors. Both publishing
  app routes and invoking them require `ipc`.
- Handler params are the request's JSON; validate all required fields.
  DecodeParams leaves a value untouched for absent params. Return a
  JSON-serializable value (struct/map, nil, or json.RawMessage), or an error.
  Errors become `{ok:false,error:...}`; do not encode errors as successful data.
- Handler context preserves caller cancellation/deadline but **replaces caller
  permissions with this app's grant**, preventing confused-deputy privilege
  borrowing. App lifetime cancellation also cancels handler/call contexts.
- Call accepts a method, JSON-encodable params (nil for none), and a pointer to
  decode the result into (nil to discard). It returns transport/service/decode
  errors. Every SDK Call requires `ipc` plus the method-specific grant. Calls
  are rejected after the app stops. Pass the Run/handler context, not a detached
  background context. No direct router, VFS, event bus, or process manager is
  provided. `Context.Log` is a structured, app-labeled logger; do not print to
  stdout (it belongs to the shell).

### Capability and method reference

Capabilities are service-level grants, **not per-path** filesystem grants.
There is no implicit `admin`, no wildcard, and no approval dialog yet: trusted
builtin manifests are approved by compilation/registration.

| Capability | Methods / purpose | Params | Result |
|---|---|---|---|
| `ipc` | All SDK calls; app handlers; `sys/status` | none for status | runtime status JSON |
| `fs.read` | `fs/list` | `{"path":"/users/guest"}` (default `/`) | `{path,entries:[{name,is_dir,size,mode}]}` |
| `fs.read` | `fs/read` | `{"path":"/users/guest/documents/note.txt"}` | `{path,size,data_base64}` |
| `fs.write` | `fs/write` | `{path,data_base64}` | `{path,written}` |
| `fs.write` | `fs/mkdir`, `fs/remove` | `{path}` | `{path,created:true}` / `{path,removed:true}` |
| `proc.list` | `proc/list` | none | array `{id,name,kind,state,caps,...}` |
| `proc.stop` | `proc/stop` | `{id,timeout_seconds?}` (default 5) | `{id,stopped:true}` |
| `app.list` | `app/list` | none | array `{manifest,running,pid?}` |
| `app.launch` | `app/launch` | `{"id":"com.example.counter"}` | `{id,pid}` |
| `proc.stop` | `app/stop` | `{id}` (app ID string) | `{id,stopped:true}` |
| `shutdown` | `sys/shutdown` | `{reason?}` | `{stopping:true,reason}`; teardown may close connection |

`sys/ping` returns `{pong:true,version}`. `session/whoami` returns
`{user,user_id,session,capabilities}`. These two service methods have no
additional method grant, but **SDK calls still require ipc**. Files use
standard base64 (`encoding/base64`); all filesystem paths are environment
paths, never host paths. `fs.write` does not create parent directories: call
`fs/mkdir` first. Read/write permissions apply across the environment VFS;
per-app roots are future work.

Example of writing with `permissions:["ipc","fs.write"]`:

```go
err := app.Call(ctx, "fs/write", map[string]string{
    "path": "/users/guest/documents/note.txt",
    "data_base64": base64.StdEncoding.EncodeToString([]byte("hello")),
}, nil)
```

`app/launch` uses the runtime lifetime, not the request deadline, so the new
app survives the launcher's request finishing or socket disconnecting.

## 6. Run and verify your app

```sh
go run ./cmd/gostalgia shell --root /tmp/gs
```

In the shell:

```text
apps
launch com.example.counter
call app/com.example.counter/next
call app/com.example.counter/next
stop com.example.counter
launch com.example.counter
call app/com.example.counter/next
```

Expect counts 1, 2, then 1 after relaunch. F2 opens the shelf, Enter launches,
F3 stops, F4 opens the selected running app's view, and Esc returns to the
prompt. `ps` shows manifest grants on the process. Echo's interactive view is
available without another installed demo.

Headless equivalent:

```sh
go run ./cmd/gostalgia boot --root /tmp/gs
go run ./cmd/gctl --root /tmp/gs call app/launch '{"id":"com.example.counter"}'
go run ./cmd/gctl --root /tmp/gs call app/com.example.counter/next
go run ./cmd/gctl --root /tmp/gs call app/stop '{"id":"com.example.counter"}'
```

Write tests for Init failure cleanup, cancellation, invalid params,
concurrent handler state, single-instance rejection, relaunch reset, route
retraction, and denial of an undeclared capability via Context.Call. Invoke a
handler as an operator too: it must not borrow operator capabilities.

Run `go build ./...`, `go vet ./...`, `gofmt -l .` (empty output), and
`go test -race ./...`. Do not add another external dependency to an app.

## 7. Data/action presentation contract (version 1)

**There is exactly one terminal owner: the experience/shell.** Only it enters
raw mode, renders, restores the terminal, chooses styles, edits inputs, moves
focus, and handles global hotkeys. An app publishes data and semantic actions,
not a `tea.Model`, widget tree, ANSI output, terminal handle, or keymap. The
SDK and all production `apps/` packages import only the SDK, other app
packages, and standard library packages. Runtime registration uses a narrow
`apps.Registrar` interface; manifest file seeding stays in the runtime.

### App adapter and wire methods

During Init, call:

```go
func (c *sdk.Context) Present(
    snapshot func(context.Context) (sdk.View, error),
    action func(context.Context, sdk.ActionRequest) (sdk.View, error),
) error
```

This reserves three ordinary app-scoped IPC routes, atomically published with
the app's other routes after successful Init. Apps without a presentation can
continue exposing only operation routes. Both callbacks follow the existing
manifest grants, handler concurrency, cancellation, and error rules.

| Method (`app/<app-id>/...`) | Params | Result |
|---|---|---|
| `view` | `sdk.ViewRequest`: `{"version":1}` | complete `sdk.View` snapshot |
| `action` | `sdk.ActionRequest`: `{version,instance,request_id,action,item_id?,values?}` | complete updated `sdk.View` |
| `cancel` | `sdk.CancelRequest`: `{version,instance,request_id}` | `{"canceled":true}`, cancellation intent recorded |

`version` is the presentation protocol version, independent of manifest
version. Missing/unsupported versions, unknown request fields, malformed
params, and stale instances are errors. The SDK assigns a fresh opaque
`instance` on every Init. Action and cancel requests must echo it; an old
screen can never target a new launch by accident. Each action uses a distinct
`request_id` matching `[a-z][a-z0-9_-]*` (up to 64 bytes). It identifies
in-flight work, **not** a durable idempotency key; do not automatically retry a
mutating action after a lost response.

### Typed snapshot, not widgets

`sdk.View` has `version`, `instance`, `title`, `state`, `items`, `fields`,
`actions`, `status`, and `error`. It is a full replacement, including explicit
empty values, not a patch. Its small concrete vocabulary is:

- `sdk.Item`: stable local `id`, `label`, and textual `detail`.
- `sdk.Field`: local `id`, `label`, initial text `value`, and `required`.
  Version 1 fields are single-line text. The shell owns editing; periodic
  snapshots preserve drafts for surviving field IDs. To reset an input, change
  its ID or reopen the view.
- `sdk.Action`: local `id`, `label`, and `disabled`.
- `sdk.ViewState`: `ready`, `loading`, or `error`. An error state requires an
  error banner. `status` is informational text, not an IPC error.

IDs are unique within each collection and match `[a-z][a-z0-9_-]*`, up to 64
bytes. Collections are bounded to 100 items, 16 fields, and 16 actions.
Titles/labels are bounded to 256 bytes; field values, item details, status, and
error to 4096 bytes each. `View.Validate()` checks the contract. The SDK stamps
and validates callback results; the shell validates replies and strips terminal
controls before rendering every piece of app text.

Before invoking an action callback, the SDK validates the current snapshot,
rejects unknown/disabled actions and actions on a loading view, rejects
unrecognized item/field IDs and oversized inputs, and checks required fields.
`values` is a `map[string]string` keyed by field ID, and `item_id` is an
optional selection. App callbacks still validate domain rules and
authorization, and must synchronize shared state.

This deliberately supports a concrete list/text-form/action screen, not a
speculative layout or widget framework. Files and Notes can build on these
data types and scoped filesystem service calls; rich editors, pagination,
multiple views, and app-defined shortcuts are not part of version 1.

### Echo, interactive and headless

`apps/echo` calls `app.Present(e.view, e.act)` from Init. Its snapshot contains
a required `msg` field, an `echo` action, the last echoed item, and an echo-count
status. Both its presentation action and existing `echo` operation call the
same synchronized operation. Neither path imports Charm or accesses a host
file or terminal.

In the shell, press F2, select a running Echo instance, and press F4. Type a
message and press Enter. Tab/Shift-Tab move field/action focus; Enter invokes
the focused action (the first action when a field is focused); Up/Down select
an item. F2 always returns to the shelf, Ctrl-C/Ctrl-D always exit, and apps
cannot intercept these global hotkeys.

The same operations run without any UI:

```sh
go run ./cmd/gostalgia boot --root /tmp/gs
go run ./cmd/gctl --root /tmp/gs call app/com.gostalgia.echo/view '{"version":1}'
```

Copy the returned `instance` into an action request:

```sh
go run ./cmd/gctl --root /tmp/gs call app/com.gostalgia.echo/action \
  '{"version":1,"instance":"COPY-FROM-VIEW","request_id":"cli-1","action":"echo","values":{"msg":"no terminal required"}}'
go run ./cmd/gctl --root /tmp/gs call app/com.gostalgia.echo/stats
```

The action snapshot and stats report the same count. Existing
`app/com.gostalgia.echo/echo` calls also update the view. The socket test in
`test/e2e/presentation_test.go` exercises these routes without a UI; the
shell test exercises the same action through a real Bubble Tea loop.

### Loading, errors, cancellation, and retraction

The shell immediately displays loading while fetching a view or awaiting an
action. App snapshots can also expose `loading` for app-owned asynchronous
work. A callback error becomes an IPC error and a shell-owned banner; a
`ViewError` snapshot carries a recoverable app error banner and may offer
enabled recovery actions.

Esc during an action cancels its local wait **and** sends `cancel` over IPC,
keeping the view available. Esc when idle dismisses the view and returns focus
to the prompt. Dismissal, shell exit, action timeout, and switching to the
shelf also cancel pending work. The separate route is necessary because
canceling a socket client's context alone does not cancel server execution.
The SDK remembers the latest 64 cancellation intents so cancel-before-action
dispatch is honored, and permits at most 16 concurrent actions per instance.
Cancellation is cooperative: callbacks must observe `ctx.Done()` and check
before committing. An acknowledgment does not promise rollback of work
already committed. Process lifetime and socket disconnect also cancel
handler contexts.

No presentation survives its owner. Existing app cleanup retracts all three
routes and cancels/drains handlers on stop, normal exit, error, panic, or failed
Init. The shell checks `app/list` and snapshots at 500 ms intervals (two-second
IPC deadline), retracts data and focus on exit, PID/instance replacement, or
lost connectivity, and ignores replies from dismissed/canceled views. The
shell never relaunches an app merely to render it. Snapshots and action input
are not published to the runtime event history.

## 8. Trust and dependency boundary

The SDK and runtime core remain standard-library-only. Bubble Tea and Lip
Gloss live in `internal/experience/shell`; apps do not implement tea.Model and
never own the terminal. Shell IPC clients are trusted operators with the same
local-token grant as gctl; apps receive only their manifests' grant.

Capability scoping is enforced at the SDK/service boundary but is **not an OS
sandbox**. Trusted in-process Go could deliberately import host APIs or forge
an internal capability context. Do not run untrusted apps. See
[security.md](security.md) and [architecture.md §4.1](architecture.md#41-dependency-policy).
