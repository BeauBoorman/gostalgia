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
Gostalgia environment over an authenticated NDJSON protocol via a dedicated,
ephemeral IPC socket.

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

1. **Dedicated child socket listener:** The runtime creates a dedicated,
   ephemeral listener (`unix:///...` domain socket on Unix, loopback TCP on
   Windows) using `platform.ListenChildIPC`.
2. **Environment variable injection:** The runtime generates an app token
   bound to the app ID and declared permissions, and starts the child with a
   sanitized environment containing:
   - `GOSTALGIA_ENDPOINT`: dedicated child IPC listener address.
   - `GOSTALGIA_APP_TOKEN`: scoped authentication token.
   - `GOSTALGIA_APP_ID`: application identity.
   - `GOSTALGIA_APP_PROTOCOL_VERSION`: negotiated protocol version (`1`).
3. **Connection and authentication:** `sdk.Serve` connects to the endpoint,
   sends an `auth` request with the token, and verifies authentication success.
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
4. Issues a scoped, non-recursive VFS grant (`recursive: false`) conferring **only** the selected document and mode (`read` or `read-write`) to the target application. This strictly prevents conferring directory-level access or sibling document access.
5. Launches the target application if not already running (using the runtime-wide lifetime context).
6. Dispatches `app/<app_id>/open` with document path, mode, and `grant_id`.
7. Automatically records the document access into persistent recents.

### Persistent Recents and Favorites Stores
- Recents and favorites are persisted to user private state: `/users/guest/config/recents.json` and `/users/guest/config/favorites.json`.
- Both stores are bounded (up to 100 entries) and use atomic VFS writes.
- Recents records application ID, access mode, access count, and timestamp. Missing or stale paths are pruned or marked during verification.
- Favorites supports ordering, ranking, and custom user-provided labels.
- Both stores filter returned entries by caller permissions so applications cannot discover files outside their granted directory trees.

### Permission-Aware Search and Lookup
- `doc/search` performs recursive directory traversal bounded by depth (max 16) and result count (max 100), respecting directory boundaries and caller permissions.
- In-memory index provides fast exact name and extension lookups, verifying existence and checking caller permissions at query time.

