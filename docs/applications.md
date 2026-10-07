# Gostalgia app SDK — authoring specification

This is the spec for adding an app without consulting runtime internals. The
public Go package is `gostalgia/sdk` (standard library only). Applications run
in one of two distinct modes:

- `inproc`: **trusted, compiled-in Go packages**, one live instance per ID,
  executing as supervised in-process goroutines.
- `external`: **out-of-process external Go applications**, running as
  supervised host child processes that communicate with the environment over
  an authenticated NDJSON protocol via dedicated IPC endpoints.

## 1. Files and manifest

Create `apps/<shortname>/manifest.json` and `apps/<shortname>/<shortname>.go` (for
in-process builtins) or a standalone executable manifest (for external applications).
The JSON file is the source of truth; embed it with `//go:embed manifest.json`.
Exactly one JSON object, no unknown fields or trailing values:

```json
{
  "id": "com.example.counter",
  "name": "Counter",
  "version": "0.1.0",
  "mode": "inproc",
  "entrypoint": "counter",
  "permissions": ["ipc"],
  "description": "Counts requests within one launch."
}
```

For an external application:

```json
{
  "id": "com.example.externalapp",
  "name": "External App",
  "version": "0.1.0",
  "mode": "external",
  "protocol_version": 1,
  "executable": "/path/to/app-binary",
  "args": ["--flag"],
  "permissions": ["ipc", "fs.read"],
  "description": "External Go application communicating over the environment protocol."
}
```

| Field | Required | Meaning / validation |
|---|---|---|
| `id` | yes | Unique reverse-DNS, `^[a-z][a-z0-9]*(\.[a-z0-9-]+)+$`. Process name and route namespace. |
| `name` | yes | Nonblank display name. |
| `version` | yes | Numeric `MAJOR.MINOR.PATCH`, no prerelease suffix. |
| `mode` | no | Execution mode: `"inproc"` (default if omitted) or `"external"`. |
| `protocol_version` | no (external: yes) | Integer protocol version for environment communication. Defaults to `1`. Required to be `1` for external mode. |
| `entrypoint` | inproc: yes | Compiled factory key, `^[a-z][a-z0-9_-]*$`. Required for in-proc mode; forbidden for external mode. |
| `executable` | external: yes | Host executable binary path or command name. Required for external mode; forbidden for in-proc mode. |
| `args` | no | Array of string arguments passed to the executable when launched. Forbidden for in-proc mode. |
| `permissions` | no | Array of distinct capability strings; omitted/empty means no grants. Unknown, duplicate, and `admin` permissions are rejected. |
| `path_grants` | no | Package-managed scoped VFS requests (`path`, `access`, `recursive`), see [packages](packages.md). |
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

`apps/echo` and `apps/notes` are the installed exemplars. Echo demonstrates
basic IPC operations and presentation stats, while Notes demonstrates a complete
document editor with safe saving, dirty state, and crash recovery.

## 3. Register and seed (both required)

Edit `apps/apps.go`:

1. Import `gostalgia/apps/counter` alongside the existing Echo import.
2. In `Register`, add the following before `return nil`:

   ```go
   if err := r.RegisterBuiltin(counter.Manifest(), counter.Factory); err != nil {
       return fmt.Errorf("apps: register counter: %w", err)
   }
   ```

3. Add `counter.Manifest()` to the `[]sdk.Manifest{echo.Manifest(), notes.Manifest()}` slice in
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
already have a compiled factory or launch fails. External applications can be
distributed and installed through the [package manager](packages.md).

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

## 4a. External applications and `sdk.Serve`

External applications run as distinct host child processes (`mode: "external"`)
supervised by the process manager (`KindChild`). They interact with the
Gostalgia environment over an authenticated NDJSON protocol — via an inherited
pre-connected descriptor for sandboxed children, or a dedicated, ephemeral IPC
socket for trusted children.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"

    "gostalgia/sdk"
)

type App struct{}

func (a *App) Init(ctx *sdk.Context) error {
    return ctx.Handle("greet", func(_ context.Context, raw json.RawMessage) (any, error) {
        var p struct{ Name string `json:"name"` }
        _ = sdk.DecodeParams(raw, &p)
        return map[string]string{"message": fmt.Sprintf("Hello, %s!", p.Name)}, nil
    })
}

func (a *App) Run(ctx context.Context) error {
    <-ctx.Done()
    return nil
}

func (a *App) Stop(ctx context.Context) error {
    return nil
}

func main() {
    if err := sdk.Serve(&App{}); err != nil {
        panic(err)
    }
}
```

### Startup handshake and protocol negotiation

When an external app launches:

1. **Child IPC channel:** The channel depends on the isolation level:
   - `sandbox`/`strict` children (Unix): the runtime creates a pre-connected
     `socketpair` (`platform.ChildIPC`) and passes the child's end as an
     inherited file descriptor (fd 3). This is deliberate: neither Seatbelt
     nor network namespaces can scope `connect()` to a single unix-socket
     path, so a filesystem socket endpoint would force the sandbox to allow
     connections to *any* remote unix socket — including `docker.sock`,
     `ssh-agent`, and the runtime's own IPC listener. An inherited,
     already-connected fd requires no connect capability at all.
   - `trusted` children, and all children on Windows (which lacks fd
     passing): the runtime creates a dedicated, ephemeral listener
     (`unix:///...` domain socket on Unix, loopback TCP on Windows) using
     `platform.ListenChildIPC`.
2. **Environment variable injection:** The runtime generates an app token
   bound to the app ID and declared permissions, and starts the child with a
   sanitized environment containing:
   - `GOSTALGIA_IPC_FD`: inherited IPC descriptor number (sandboxed Unix
     children only; currently `3`).
   - `GOSTALGIA_ENDPOINT`: dedicated child IPC listener address (trusted and
     Windows children).
   - `GOSTALGIA_APP_TOKEN`: scoped authentication token.
   - `GOSTALGIA_APP_ID`: application identity.
   - `GOSTALGIA_PROTOCOL_VERSION`: negotiated protocol version (`1`).
3. **Connection and authentication:** `sdk.Serve` uses the inherited
   descriptor when `GOSTALGIA_IPC_FD` is set, otherwise dials the endpoint;
   it then sends an `auth` request with the token and verifies
   authentication success.
4. **Readiness and version handshake:** `sdk.Serve` invokes `app/ready` with
   its `app_id` and `protocol_version`. If the protocol version does not match
   the runtime's supported protocol (`ProtocolVersion = 1`), the handshake fails.
5. **Startup timeout:** The supervisor enforces a strict startup timeout
   (5 seconds). If the child fails to complete the handshake, crashes, or hangs
   during startup, the supervisor kills the child process, cleans up the socket,
   revokes credentials, and marks the app failed.

### Supervision, logs, and containment

- **Process supervision:** External apps are tracked in `proc/list` as
  `kind: "child"` with complete PID, status, start/exit timestamps, and exit code.
- **Log ring buffers:** Stdout and stderr from the child process are captured
  into bounded diagnostic ring buffers. View logs and stream diagnostics via
  `proc/logs` with optional line tailing or stream filtering (`stdout`/`stderr`).
- **Crash and hang containment:** If an external app crashes, panics, exits, or
  is terminated, the runtime cleans up the dedicated socket, revokes credentials,
  retracts all registered `app/<id>/*` routes, and transitions the process to
  `stopped` or `failed`.
- **Presentation contract:** External applications implement `app.Present`
  identically to in-proc apps. View snapshots and action dispatches flow over the
  child socket, rendering inside the Charm shell without the external process
  ever owning the host terminal.

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
| `clipboard.read` | `clipboard/read` | `{"target?":"auto"}` | `{text,source}` |
| `clipboard.write` | `clipboard/write` | `{"text":"...","target?":"auto"}` | `{written:true,bytes,source}` |
| `clipboard.write` | `clipboard/clear` | none | `{cleared:true}` |
| `hostfs.read` | `fs/list`, `fs/read` on shared mounts | path on shared mount | directory entries or file content |
| `hostfs.write` | `fs/write`, `fs/save`, `fs/mkdir` on shared mounts | path on shared mount | write confirmation |
| `net.egress` | `net/fetch` | `{"url":"...","method?":"GET","headers?":{},"body_base64?":""}` | `{status,headers,data_base64,size}` |
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
Gloss live in `internal/experience/`; apps do not implement tea.Model and
never own the terminal. Shell IPC clients are trusted operators with the same
local-token grant as gctl; apps receive only their manifests' grant.

Capability scoping is enforced at the SDK/service boundary but is **not an OS
sandbox**. Trusted in-process Go could deliberately import host APIs or forge
an internal capability context. Do not run untrusted apps. See
[security.md](security.md) and [architecture.md §4.1](architecture.md#41-dependency-policy).

## 9. Notes: document model, safe saving, dirty state, and crash recovery

`apps/notes` is Gostalgia's standard document editor application. It proves the
document and presentation architecture using the visual language kit, safe VFS
document operations, and the presentation contract.

### Manifest and permissions

```json
{
  "id": "com.gostalgia.notes",
  "name": "Notes",
  "version": "0.1.0",
  "entrypoint": "notes",
  "permissions": ["ipc", "fs.read", "fs.write"],
  "description": "Text editor with safe saving, dirty state, and crash recovery."
}
```

Notes requires `ipc` for service communication and route handling, `fs.read` to
open notes, and `fs.write` to perform safe atomic saves and manage recovery staging.

### Document model and dirty state

- **Explicit document identity:** Notes tracks the target VFS path (or `[Untitled]`
  when unsaved), the loaded/saved disk content, and the disk modification timestamp
  (`mod_time`) and file size at load/save time.
- **Dirty state tracking:** Content buffer modifications mark the document dirty.
  The presentation view updates with a dirty indicator (`*` in the title and
  `[modified]` in the document info item). Reverting the buffer back to disk content
  clears the dirty state.
- **Character and rune metrics:** Tracks both UTF-8 rune counts and byte counts for
  accurate Unicode display.

### Safe saving and conflict detection

- **Atomic document saving:** Notes calls `fs/save` with base64-encoded payload,
  ensuring documents are written safely via temporary staging files and atomic replacement.
- **External modification detection:** Before writing, Notes calls `fs/stat` on the
  target path. If the file exists and its disk `mod_time` or size differs from when
  Notes loaded it, Notes enters `PromptConflict` (`Notes - Save Conflict*`) without
  overwriting the disk copy.
- **Conflict actions:** The user can select `force_save` (overwriting the disk file),
  `save_as` (saving to a different path), or `cancel` (preserving local dirty edits).
- **Save failure preservation:** If a save fails (e.g. permission denied or disk full),
  Notes displays an error banner while preserving the dirty buffer and recovery draft.

### Crash recovery in app-private storage

- **App-private staging:** On any edit, Notes stages an unsaved draft to its private
  partition at `/apps/data/com.gostalgia.notes/recovery.json`. Other applications cannot
  access or tamper with this partition.
- **Launch detection:** When Notes boots, it inspects its private storage for an
  unsaved draft. If found, Notes presents `PromptRecovery` (`Notes - Crash Recovery Draft Found`),
  displaying the file path, staging timestamp, and a content preview.
- **Restoration options:** The user can invoke `restore` to reinstate the unsaved draft
  buffer and dirty state, or `discard_recovery` to remove the draft and open a clean note.
- **Draft cleanup:** Once a document is cleanly saved or changes are discarded, the
  staged recovery draft is automatically removed from app-private storage via `fs/remove`.

### Unsaved changes prompts

When a document has unsaved edits (`dirty == true`), attempting to open another file
or create a new document transitions Notes to `PromptUnsaved` (`Notes - Unsaved Changes*`).
The user can select:
- `save_and_proceed`: Saves the active document to disk, then completes the pending action.
- `discard_and_proceed`: Discards unsaved edits and completes the pending action.
- `cancel`: Cancels the pending action and returns to the current dirty buffer.

Programmatic IPC calls to `open` and `new` similarly require `force: true` when dirty.

### Presentation actions and programmatic routes

Notes exposes semantic actions over the Presentation Contract (rendered in the shell
via F4):
- Normal mode: `save`, `save_as`, `open`, `new`
- Recovery mode: `restore`, `discard_recovery`
- Unsaved changes mode: `save_and_proceed`, `discard_and_proceed`, `cancel`
- Conflict mode: `force_save`, `save_as`, `cancel`

Notes also provides programmatic IPC routes under `app/com.gostalgia.notes/`:
- `doc`: Returns `{path, content, dirty, mod_time, size, recovery_available, prompt}`
- `open`: Opens a document `{path, force?}`
- `save`: Saves the document `{path?, content?, force?, overwrite?}`
- `save_as`: Saves to a new path `{path, content?, overwrite?}`
- `new`: Creates a new note `{force?}`
- `edit`: Updates content `{content}`
- `recover`: Restores or discards staged draft `{restore: bool}`

## 10. Files: VFS-backed browser, preview, and handoff

`apps/files` is Gostalgia's standard file manager and document browser. It provides
a keyboard-first directory browser adhering to the SDK presentation contract (`sdk.View`
and `sdk.ActionRequest`), integrating with VFS services and enabling handoff to Notes
without escaping environment confinement.

### Manifest and permissions

```json
{
  "id": "com.gostalgia.files",
  "name": "Files",
  "version": "0.1.0",
  "entrypoint": "files",
  "permissions": ["ipc", "fs.read", "fs.write", "app.launch", "app.list"],
  "description": "VFS file manager and document browser."
}
```

Files requires `ipc` for service communication and route handling, `fs.read` for
directory listing, file metadata, and bounded content previews, `fs.write` for file
and folder creation, copying, moving, renaming, and trash management, and `app.launch`
and `app.list` for seamless handoff to companion applications like Notes.

### Browsing, sorting, and filtering

- **Breadcrumb and directory navigation:** Files opens in `/users/guest` by default and
  allows descending into child directories or ascending to parent directories using the
  `[..] Up` item or the `up` action.
- **Sorting modes:** Supports sorting by Name (alphabetical), Size (largest first),
  and Date (newest first) via the `sort` action. Directories are always grouped ahead of files.
- **Incremental filtering:** Ingests live filter input from the presentation `filter` field,
  performing case-insensitive prefix and substring matching on directory entries.
- **Snapshot limits:** Enforces presentation constraints by bounding visible entries to
  at most 100 items and presenting an explicit truncation indicator (`item_more`) when
  directories contain additional items.
- **Selection survival:** Preserves the current selection across directory refreshes and
  sort changes, gracefully falling back to the first available entry when a selected
  item is removed.

### Bounded preview

- Inspects selected files and fetches bounded content previews (up to 4 KB read, 256 runes
  displayed) via `fs/read`.
- Distinguishes plain text from binary data using null-byte and UTF-8 validation, displaying
  a formatted binary size indicator (`[Binary data: <size>]`) for binary files.

### File operations and conflict resolution

- **CRUD operations:** Supports creating new empty files (`new_file`) and directories (`new_dir`),
  copying files (`copy`), moving files (`move`), and renaming files (`rename`).
- **Conflict detection:** Before executing copy, move, or rename, Files checks if the
  destination path already exists. If a collision is detected, Files transitions to
  `PromptConflict` (`Files - Destination Exists`), prompting the user to either
  `confirm_overwrite` or `cancel` without performing destructive writes.

### Trash and restore lifecycle

- **Safety-first deletion:** Selecting `trash` enters `PromptConfirmTrash` to prevent
  accidental data loss.
- **VFS trash integration:** Confirming trash invokes `fs/trash`, moving the item to
  the user's `.trash` hierarchy.
- **Trash bin view:** The user can switch to the dedicated Trash Bin view (`trash_bin`),
  inspect trashed items with original path metadata, restore items to their original
  locations (`restore`), or permanently empty the trash bin (`empty_trash`).

### Handoff to companion applications

- Selecting a file and invoking `open` resolves registered applications. For text and
  markdown documents, Files launches Notes (`com.gostalgia.notes`) and issues an
  `app/com.gostalgia.notes/open` IPC request to immediately display the document.
- Any errors or unsaved-change warnings returned by the target application are captured
  and rendered in the Files presentation error banner.

### Presentation actions and programmatic routes

Files exposes semantic actions over the Presentation Contract (rendered in the shell):
- Normal mode: `open`, `up`, `preview`, `new_file`, `new_dir`, `rename`, `copy`, `move`, `trash`, `trash_bin`, `sort`, `refresh`
- Confirm trash mode: `confirm_trash`, `cancel`
- Conflict mode: `confirm_overwrite`, `cancel`
- Trash bin mode: `restore`, `empty_trash`, `back`

Files also provides programmatic IPC routes under `app/com.gostalgia.files/`:
- `browse`: Lists entries and metadata for a given path `{path}`
- `stat`: Retrieves detailed file or directory metadata `{path}`
- `preview`: Fetches bounded text or binary preview `{path, limit?}`
- `open`: Opens a directory or hands off a file to an application `{path, app_id?}`
- `trash_list`: Returns all items currently in the trash bin

---

## 9. Reference application: Settings (`com.gostalgia.settings`)

Settings is the interactive preferences hub for Gostalgia. It manages themes, accessibility, keyboard shortcuts, startup behavior, and storage statistics via the presentation contract and the configuration service.

```json
{
  "id": "com.gostalgia.settings",
  "name": "Settings",
  "version": "0.1.0",
  "mode": "inproc",
  "entrypoint": "settings",
  "permissions": [
    "ipc",
    "fs.read",
    "fs.write",
    "app.list",
    "config.read",
    "config.write"
  ],
  "description": "System preferences, appearance, accessibility, and shortcut configuration."
}
```

### Features and presentation flow

1. **Preference categories:**
   - **Appearance & Theme:** Browse themes (`nostalgia`, `midnight`, `monochrome`, `high-contrast`, `high-contrast-light`), preview colors, toggle live previews.
   - **Accessibility & Motion:** Configure terminal color fidelity (`plain`, `ansi256`, `truecolor`) and toggle reduced motion.
   - **Keyboard Shortcuts:** View effective keybindings, detect and prevent conflicting assignments.
   - **Startup & Boot:** Choose the default boot landing view (`home`, `prompt`, `launcher`, `tasks`, `notifications`).
   - **Storage & System:** Inspect storage statistics, configuration file provenance, and layer explanations.

2. **Live preview & rollback:**
   - Selecting a theme triggers a live in-memory preview via `config/preview`.
   - The shell updates its styling dynamically.
   - The user can commit the changes (`save`) or revert to prior settings (`revert`).

3. **Presentation actions:**
   - Category navigation: `cat_appearance`, `cat_accessibility`, `cat_shortcuts`, `cat_startup`, `cat_storage`.
   - Option toggles: `select` (activates or toggles selected item).
   - Lifecycle controls: `save` (commits preview to disk), `revert` (cancels preview), `reset` (restores factory defaults).

4. **Programmatic IPC routes (`app/com.gostalgia.settings/*`):**
   - `preferences`: Returns active settings, effective values, and layer breakdown.
   - `preview`: Applies an in-memory preview override for a setting path `{path, value}`.
   - `save`: Commits in-memory preview changes to the user configuration layer.
   - `revert`: Cancels in-memory preview overrides and restores prior configuration.
   - `reset`: Restores system default preferences.

---

## 11. Documents: Search, Recents, Favorites, and Open-With Handoff

The document subsystem provides unified document workflows across Gostalgia:

### Document Type Associations
Applications declare supported document extensions or types in `manifest.json` under `document_types` (e.g. `[".txt", ".md", ".json"]` for Notes, or `["directory"]` for Files). The document association registry maps file extensions and types to default and alternative application IDs.

### Versioned Open-With Handoff (`doc/handoff`)
When an application (such as Files) or operator hands off a document to another application:
1. Validates the versioned contract (`sdk.DocumentHandoffVersion = 1`).
2. Checks that the caller has permission to access the document path.
3. Resolves the target application via document associations or caller request.
4. Launches the target application if not already running (using the runtime-wide lifetime context).
5. Issues a scoped, non-recursive, **session-bound** VFS grant (`recursive: false`) conferring **only** the selected document and mode (`read` or `read-write`; default `read`) to the target application. This strictly prevents conferring directory-level access or sibling document access.
6. Dispatches `app/<app_id>/open` with document path, mode, and `grant_id`. A failed launch or dispatch never leaves a live grant: the grant is issued only after launch succeeds and revoked if dispatch fails.
7. Automatically records the document access into persistent recents.

Handoff grants are session-bound rather than permanent: the runtime revokes them when the receiving application's run ends (exit, stop, or crash), matching the launch-bound token lifecycle. Revoking on dispatch completion was rejected because the app needs the grant throughout its session to read and write the opened document; a TTL was rejected because any fixed duration either outlives the session (leak) or cuts a live editing session short.

### Persistent Recents and Favorites Stores
- Recents and favorites are persisted to user private state: `/users/guest/config/recents.json` and `/users/guest/config/favorites.json`.
- Both stores are bounded (up to 100 entries) and use atomic VFS writes.
- Recents records application ID, access mode, access count, and timestamp. Missing or stale paths are pruned or marked during verification.
- Favorites supports ordering, ranking, and custom user-provided labels.
- Both stores filter returned entries by caller permissions so applications cannot discover files outside their granted directory trees.

### Permission-Aware Search and Lookup
- `doc/search` performs recursive directory traversal bounded by depth (max 16) and result count (max 100), respecting directory boundaries and caller permissions.
- In-memory index provides fast exact name and extension lookups, verifying existence and checking caller permissions at query time.

## 12. Compendium: local-first knowledge base

`apps/compendium` (display name **Compendium**, ID `com.gostalgia.compendium`)
is a personal knowledge base: Markdown notes with `[[wikilinks]]`, backlinks,
`#tags`, ranked full-text search, a daily note, and a note graph. It is
standard-library only and follows the presentation contract (§7): it
publishes items, fields, and actions; the shell renders them. The package
directory is lowercase like every other app directory.

### Manifest and capabilities

```json
{
  "id": "com.gostalgia.compendium",
  "name": "Compendium",
  "version": "0.1.0",
  "entrypoint": "compendium",
  "permissions": ["ipc"],
  "description": "Local-first knowledge base: Markdown notes, wikilinks, backlinks, search, tags, and a note graph."
}
```

`ipc` is the whole grant. Compendium keeps everything in its automatic
app-private partition, which needs no `fs.read`/`fs.write`. It has no path
grants, so it cannot see `/users/...`. Import from and export to a
user-visible location work only for paths an operator grants it
(`fs/grant`, or a single-document `doc/handoff`). Those calls use
Compendium's own grant and fail closed without one.

### Storage layout

Everything lives under `/apps/data/com.gostalgia.compendium/`:

| Path | Role |
|---|---|
| `vault/<stem>.md` | **Source of truth.** One plain Markdown file per note, bytes exactly as written. |
| `index.json` | Derived cache: per note, title, size, SHA-256, links, and tags. Verified against the sources on every open. |
| `history/<stem>/<seq>.md` | Previous versions (the last 20) captured before each save. |
| `drafts/<stem>.json` | Unsaved edits (crash recovery). |
| `trash/<id>/` | A trashed note with its `history/` and `draft.json`. |
| `journal.json` | Present only while a multi-file operation runs (redo log). |
| `exports/` | The most recent single-note and vault exports. |
| `quarantine/` | Only if something could not be used: a journal that could not be completed, or an unreadable draft. Kept for inspection, never deleted. |

A note's title is its identity. The file stem is the title with host-reserved
bytes percent-encoded (`% / \ : * ? " < > |`, controls, a leading dot,
trailing dots or spaces, and Windows device names), so `Colon: A*B?` is
stored as `Colon%3A A%2AB%3F.md`. Titles are case-insensitive (`[[b]]`
resolves to `B`), and two notes cannot differ only by case. Case is compared
with Unicode simple case folding (the same rule as `strings.EqualFold`), so
final and medial sigma, long s, and the Kelvin sign fold together.

Titles are not Unicode-normalized (the standard library has no NFC/NFD),
but titles that a normalization-insensitive host such as APFS would store
under one file name (`Caf\u00e9` and `Cafe\u0301`) can never overwrite
each other. Two layers enforce it:

1. *Logical:* every title also has a collision key, `normKey`: case fold,
   decompose with a generated canonical-decomposition table
   (`normtable.go`: Latin-1 Supplement, Latin Extended-A/B and Additional,
   Greek and Greek Extended, Cyrillic, and the Ohm/Kelvin/Angstrom
   singletons; Hangul syllables algorithmically), fold again, then sort
   each run of combining marks. A new note, draft, rename target, restore,
   or import whose key matches an existing note or draft under a different
   title is refused with an error naming both. The key is deliberately
   conservative (sorting marks can call two titles equal that real NFD
   would keep apart); it only ever refuses a title, it never merges or
   rewrites one. Links still resolve by case fold only, so `[[Caf\u00e9]]`
   does not resolve to a note spelled `Cafe\u0301`.
2. *Physical:* new note files and new draft files are created with
   `overwrite: false`, so the host itself refuses a name it already
   resolves to another file, even for characters outside the table or a
   file written by someone else after the vault was loaded. On-disk
   collision checks before a rename or restore compare names by `normKey`.

A rename that changes only case or normalization of the same note goes
through a temporary name, so it works on case- and
normalization-insensitive hosts. A title may not contain `[ ] | # / \` or control characters, and is
at most 200 bytes. Each note is limited to 1 MiB, each vault to 10,000
notes and 256 MiB.

### Links, tags, rendering

- `[[Title]]`, `[[Title|alias]]`, and `[[Title#heading]]` are links;
  `[[Title\|alias]]` (pipe escaped inside a table cell) works too. Links
  and tags inside fenced code blocks and inline code are ignored, following
  CommonMark: a fence closes only on a run of the same character at least
  as long as the opener with nothing after it, and a code span closes only
  on a backtick run of exactly the opener's length, possibly on a later
  line of the same paragraph. One scanner serves indexing, rendering,
  backlinks, and rename rewriting, so they never disagree about what counts
  as a link. It runs in linear time on any input.
- Rendering is plain text: `«Title»`, `«alias → Title»`, or
  `«Title (missing)»`. In the note view, each broken link is an item, and
  **Follow** on it creates the target note and opens it (one key).
- `#tag` starts after whitespace or punctuation and must contain a letter
  (`#42` is not a tag). Tags are lowercase and may nest (`#garden/herbs`).
  A `#` in a markdown anchor link (`[x](#sec)`) or inside a URL-like word
  (`scheme://…`, `www.…`, `mailto:…`) is not a tag.
- Backlinks list every live note linking to this one, with the line that
  contains the link.

### Search

The inverted index lives in memory only. It is built from the sources at
load and updated on every mutation, so there is no on-disk search index to
go stale. Words are case-folded like titles; combining marks stay inside
their word; Han, Hiragana, and Katakana characters are indexed one by one
(so a word inside unspaced CJK text is found, a multi-character query
matching as the conjunction of its characters); words longer than 64 bytes
are indexed by their first 64 bytes. Queries match all terms; a term of two
or more characters also matches as a prefix (prefix matches carry less
weight). Prefix expansion has no cap, so a conjunction is exact however
many words a prefix matches; terms are evaluated cheapest first, and once
the candidates are few a broad prefix is checked against each candidate's
own words instead of every posting. An empty or punctuation-only query matches nothing. Results are ranked by
BM25 with a title boost. Snippets come from the note text with matches
marked `«like this»`; an excerpt never starts or ends inside a word, a
snippet is at most 320 bytes (rune-safe), and a matched word is shown to
at most 80 bytes, so a page of results stays small even for notes that
hold one enormous word. With 2,000 synthetic notes, `BenchmarkSearch2000`
measures about 1.2 ms per query through the route. `TestAcceptanceSearchUnder100ms`
enforces the 100 ms bound for every query.

### Graph as presentation data

The `graph` route returns `{nodes, edges, dead_ends, orphans, unresolved}`.
A **dead end** has incoming links but no outgoing resolved links. An
**orphan** has no resolved links in either direction. Unresolved targets are
listed with the notes that link to them. The graph view uses only the
existing item vocabulary: a summary item, one item per note (`in`/`out`
counts, `→`/`←` neighbours, role, tags), and one item per missing target.
No shell changes were needed. The view is capped at 100 items with
explicit "more" items; the route always returns the full graph.

### Crash safety

Every mutation is one or more single atomic VFS operations, ordered so that a
kill between any two of them leaves a consistent, recoverable state:

- **Edit**: the buffer is staged to `drafts/` atomically.
- **Save**: (1) the old version goes to `history/`, (2) the note is replaced
  atomically, (3) `index.json` is replaced, (4) the draft is removed. Before
  (2) completes, the old note is intact and the draft holds the edit. After
  (2) completes, the new note is committed, and a leftover draft identical to
  it is dropped on the next open.
- **Rename, trash, restore**: all steps (file moves and link rewrites across
  notes and drafts) go into `journal.json`, which is written atomically
  before the first step. Each step is idempotent. On open, an existing
  journal is replayed to completion. So a rename is all-or-nothing:
  either every `[[Old]]` is still `[[Old]]` or every one is `[[New]]`.
  A capitalization-only rename (`note` → `Note`) moves each file through a
  deterministic temporary name inside the journal step, so it works on
  case-insensitive hosts (APFS, NTFS) and replays from any intermediate
  state. Rename and restore reject a destination that is taken *before*
  writing the journal: another note, a pending draft with that title, or a
  vault, history, or draft entry whose name matches ignoring case and
  normalization. Rename also preflights every derived write: a rewritten
  note or draft that would exceed 1 MiB, or growth past the vault's byte
  limit, rejects the rename with an error naming the notes, so a rename can
  never make a note unloadable. Restore checks the note and vault limits
  the same way.
- **A journal that cannot be completed never stops the vault from
  opening.** The steps that are safe are finished (moves whose destination
  is free; writes to existing files that are not the destination of a
  move that failed), the journal is moved to `quarantine/`, and the
  problem is reported in `status` (`index.warnings`) and on the home view.
  Salvage never deletes what a failed move left behind: a remove step is
  skipped when a move from inside it failed, a trash entry whose note could
  not move out stays whole (note, history, and draft, still listed and
  restorable), and a note that could not move into the trash keeps its
  history and draft beside it.
  If a restore already moved its note but cannot move its history or draft,
  the journal stays pending instead of being quarantined. The draft remains
  visible in recovery, and the next mutation completes the restore once
  storage works again. Until then, mutations are refused without changing
  the retained files.
- **Ordinary I/O failures** (the process keeps running) during a journaled
  operation leave the journal pending. Every later change first completes
  it and reloads the vault from storage, or is refused with an error until
  it can; a pending journal is never overwritten. Reopening (or
  `rebuild`) applies the open-time rule above. Completing a pending
  operation reloads memory, so it is done before any route or action looks
  anything up; no note or draft reference is held across that reload.
- **Index**: on open, each entry is checked against the SHA-256 of its
  source file. Missing, corrupt, or stale entries are reparsed and the index
  is rewritten. Deleting `index.json` causes a full rebuild with no content
  loss. The index is only a cache: one over ~2.9 MiB (the IPC frame after
  base64) or unreadable is discarded and rebuilt; if a new one cannot be
  written it is simply not persisted (`status` shows `index_problem`) and
  the notes are reparsed on the next open. Writing the index never fails
  the operation that triggered it. Note files over 1 MiB, unreadable ones,
  and anything past the note-count or byte limits are skipped with a
  warning and left untouched on disk.
- **Interrupted saves**: an orphaned `*.tmp.*` staging file is deleted.
  A `.recover` artifact from a save whose final rename failed becomes a
  recovery draft, or a draft under a `(recovered)` title if the user already
  has a different draft. A draft's own artifact replaces the staged draft
  only when it is strictly newer (by `staged_at`); an older or equally old
  one is kept beside it under a `(recovered)` title. The artifact is
  removed only after its content is durably written; if staging fails, the
  artifact stays and is reported. A successful draft save removes the
  artifact an earlier failed save of the same draft left in this process,
  so a stale edit is never offered over a newer one.
- **External edits**: save and rename compare each file they would replace
  with the hash Compendium loaded. A file changed outside Compendium is
  never overwritten: save reports a conflict, keeps the edit as a draft on
  top of the external version (which the note now shows; restoring the
  draft saves over it deliberately, sending the external version to
  history). A deleted source is removed from the cached model, so explicitly
  restoring its draft can recreate it. Rename checks every loaded note
  before selecting backlinks, and is refused with changed notes reloaded
  so a retry also rewrites links newly introduced by an external edit.
- On launch, drafts that differ from disk open the **Recovery drafts**
  view (`restore_draft` / `discard_draft`).
- **Editor revisions**: the note editor remembers the hash of the version
  it was opened on (a restored draft carries its own). **Save** in a clean
  editor writes nothing; if the note changed through the routes meanwhile,
  the editor reloads it. **Save** in a dirty editor whose base is no longer
  current is a conflict: nothing is written, the edits stay staged as a
  draft, and the view offers **Overwrite with mine** (`overwrite`) and
  **Reload saved version** (`reload`).

These guarantees are proven with failure injection at every mutating
fs call: kill before or after the call, torn staging file, and failed final
rename. A second suite injects failures inside the real HostFS
stage/rename path. For each case the tests reopen the vault and check
committed content, draft recoverability, and that the index matches a full
rebuild.

### Rename propagation

Renaming rewrites the target of every link to the old title in live notes
and pending drafts. Aliases and `#heading` anchors are kept byte for byte.
Links inside trashed notes are not rewritten. They are reported in
`orphaned`, and the view status names them. Remaining broken links always
appear as unresolved in the graph.

### Import and export

- `export {title}` returns the note's exact bytes (base64) and writes
  `exports/<stem>.md`. `export {}` zips every note as `<stem>.md`
  (`archive/zip`). Exports are byte-identical to the stored Markdown. An
  optional `dest` also writes the export to a granted VFS path.
- `import` accepts `{title, content}`, `{zip_base64}`, or `{path}` (a
  granted `.md` or `.zip`). Existing titles are skipped unless
  `overwrite: true`. Zip entries are path-checked, and the archive is
  bounded by what is actually inflated, counted while reading (header
  sizes are not trusted): at most 10,000 entries, 1 MiB per note, and
  32 MiB in total. There is no compression-ratio rule (ordinary repetitive
  Markdown compresses far past 100:1); the inflated-byte limits bound what
  an archive can cost. The import is planned before anything is written
  (invalid, oversized, existing, unchanged, duplicate, and colliding files
  are skipped), and the note and byte limits are checked against exactly
  what will be written, including intermediate growth in archive order, so
  a capacity refusal writes nothing. Folded titles and normalization keys
  use separate duplicate sets; a literal title such as `norm:X` cannot
  collide with the bookkeeping for `X`. Each imported
  note is created atomically, so re-running an interrupted import finishes
  it.
- Exports are limited to about 2.9 MiB, under the 4 MiB IPC frame after
  base64, and a vault export is refused when the vault is larger than one
  import accepts (32 MiB or 10,000 notes), so every archive Compendium
  writes imports again. Export larger vaults note by note.

### Presentation and routes

- Home: a search/command field (`today`, `new <title>`, `open <title>`,
  `tag <name>`, `graph`, `tags`, `trash`, `drafts`, `help`; anything else
  is a search). Actions: `run`, `open`, `new`, `today`, `graph`, `tags`,
  `trash_bin`, `recovery`, `export_vault`, `rebuild_index`, `help`,
  `home`.
- Note: rendered text, link, backlink, and tag items; a `Markdown` field
  (only when the note fits the 4,096-byte field limit, so a larger note is
  never truncated by an edit) and a `Rename to` field. Actions: `save`,
  `follow`, `reload`, `rename`, `trash`, `export_note`, `today`, `home`,
  plus `overwrite` while a save conflict is pending.
- Graph, Tags, Tag, Trash (`restore`), Recovery drafts, and Help views.

Programmatic routes under `app/com.gostalgia.compendium/`: `create`, `open`,
`save`, `edit`, `rename`, `trash`, `trash_list`, `restore`, `history`,
`search`, `graph`, `tags`, `tag`, `today`, `list`, `export`, `import`,
`drafts`, `recover`, `rebuild`, `status`.

Responses stay inside the 4 MiB IPC frame for any note. `encoding/json`
writes `<`, `>`, `&`, and control bytes as six bytes each, so a 1 MiB note
can encode to 6 MiB: `open` sends text that would not fit as
`content_base64` (and omits `rendered`, flagged `rendered_omitted`), and
`drafts` lists drafts with content while it fits, the rest flagged
`content_omitted`, fetched one at a time with `{"title": ...}`. Drafts are
stored without HTML escaping so a 1 MiB markup draft fits the write frame.

`open` budgets the complete response, including JSON-escaped links,
backlinks, and tags. Large collections return a prefix with `<collection>_total`
and `<collection>_next_offset`; fetch the next page with `links_offset`,
`backlinks_offset`, or `tags_offset`. Offsets must be between zero and the
collection's total. Use `include_content: false` for metadata-only pages,
and set offsets for collections already consumed to their totals. A single
metadata item too large for a response is counted in
`<collection>_omitted_items`; its exact text remains in the note's source
content or byte-identical export.

### Known limitations

- **No encryption at rest.** Notes, history, drafts, and exports are plain
  files in the app-private partition on the host. Partition isolation
  protects them from other apps, not from anyone who can read the host
  directory. Use host disk encryption if that matters.
- No sync, sharing, accounts, plugins, query language, HTML preview, or
  WYSIWYG editing (non-goals). Version 1 fields are single-line, so
  multi-line editing in the shell is limited; the `edit`/`save` routes
  carry full documents.
- The daily note uses the runtime host's local date.
- Titles are not Unicode-normalized (stdlib only): normalization variants
  are refused as collisions rather than unified, and links resolve by case
  fold only; see Storage layout.
- `graph` and `list` return the whole vault in one response; a very large
  vault with long titles can exceed the 4 MiB IPC frame there (not
  paginated yet).
- A vault whose index exceeds ~2.9 MiB (roughly tens of thousands of links)
  runs without a persisted index and reparses every note on open.
