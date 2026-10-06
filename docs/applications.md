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

3. Add `counter.Manifest()` to the `[]app.Manifest{echo.Manifest()}` slice in
   `SeedManifests`. Seeding creates `/apps/manifests/com.example.counter.json`
   in the environment VFS, only if absent.

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
F3 stops, Esc returns to the prompt. `ps` shows manifest grants on the process.

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

## 7. Trust and dependency boundary

The SDK and runtime core remain standard-library-only. Bubble Tea and Lip
Gloss live in `internal/experience/`; apps do not implement tea.Model and
never own the terminal. Shell IPC clients are trusted operators with the same
local-token grant as gctl; apps receive only their manifests' grant.

Capability scoping is enforced at the SDK/service boundary but is **not an OS
sandbox**. Trusted in-process Go could deliberately import host APIs or forge
an internal capability context. Do not run untrusted apps. See
[security.md](security.md) and [architecture.md §4.1](architecture.md#41-dependency-policy).
