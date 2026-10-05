# Gostalgia Architecture

Gostalgia is a cross-platform, pseudo-operating-system / portable computing
environment written in Go. It runs as a hosted process on top of Windows,
macOS, or Linux, but presents its **own** abstractions for applications,
processes, files, services, IPC, configuration, permissions, sessions, and
system management. The host OS is substrate, not the application model.

This document is the authoritative architecture. When implementation and
document disagree, one of them is wrong; fix both.

---

## 1. Layered model

```text
┌──────────────────────────────────────────────────────────────┐
│  APPLICATIONS        echo · terminal · editor · file mgr …   │
├──────────────────────────────────────────────────────────────┤
│  EXPERIENCE  Charm shell now · desktop/windows later (M4+) │
├──────────────────────────────────────────────────────────────┤
│  SYSTEM SERVICES     sys · process · fs · ipc · session …    │
├──────────────────────────────────────────────────────────────┤
│  OS RUNTIME          app model · process model · events ·    │
│                      config · security · lifecycle           │
├──────────────────────────────────────────────────────────────┤
│  PLATFORM ABSTRACTION platform/ (unix, windows)              │
├──────────────────────────────────────────────────────────────┤
│  HOST OS             Windows / macOS / Linux                 │
└──────────────────────────────────────────────────────────────┘
```

Rules that hold the layers together:

- Applications and services talk to **each other through the environment**
  (IPC router, VFS, process manager, event bus), never through host APIs.
- Platform-specific code lives only under `platform/` (and, rarely, behind
  build tags next to the subsystem that owns the concern). Everything else
  must compile for all three targets with no `GOOS` conditionals.
- Lower layers never import higher layers. `internal/app` may not know about
  the desktop; `internal/ipc` may not know about apps.

## 2. Package map and dependency direction

```text
gostalgia
├── cmd/
│   ├── gostalgia/        # the environment runtime (boot/serve)
│   └── gctl/          # client CLI for a running environment
├── internal/
│   ├── config/         # layered JSON config store          (leaf)
│   ├── events/         # typed pub/sub event bus            (leaf)
│   ├── ipc/            # router + NDJSON socket server/client
│   ├── vfs/            # virtual filesystem, mounts, memfs  (leaf)
│   ├── process/        # environment process manager
│   ├── security/       # users, capabilities                (leaf)
│   ├── session/        # user sessions
│   ├── service/        # service lifecycle framework
│   ├── services/       # concrete core services (sys, process, fs, ipc)
│   ├── experience/     # Charm shell (imports IPC client contracts only)
│   ├── app/            # application model, manifests, launcher
│   └── runtime/        # boot, wiring, shutdown
├── sdk/                # public stdlib-only app contract
├── apps/               # builtin applications: echo/ + registration & manifest seeding
├── platform/           # host-specific endpoints (sockets, paths)
├── cmd/gostalgia         # the environment runtime binary
├── cmd/gctl           # client CLI for a running environment
├── test/e2e            # full-environment test over a real socket
├── docs/
├── status.md           # living status + the milestone list (canonical tracker)
└── go.mod
```

Dependency flow (arrows = "imports"):

```text
config, events, vfs, security  ←  ipc, process, session  ←  app  ←  apps
                                   ↑ all of the above                ↑
                                   service → services → runtime → cmd
```

No cycles. `service.Context` carries the wiring (config, bus, router, vfs,
procs, apps, sessions) down to concrete services so they never import
`runtime`.

## 3. Subsystems

### 3.1 Runtime (`internal/runtime`)

Owns boot and shutdown. Boot sequence:

1. Resolve environment root (`--root` > `$GOSTALGIA_ROOT` > `~/.gostalgia`), create layout.
2. Open configuration store (`config/system.json`).
3. Set up logging (stdout + `<root>/logs/gostalgia.log`).
4. Construct leaf managers: event bus, IPC router, VFS (host-backed, `/tmp` memfs mount), process manager, session manager, app registry.
5. Register core services; start them in dependency order.
6. IPC service listens (platform adapter) and writes `<root>/runtime.json`
   (endpoint + auth token); guard against a second live instance.
7. Create default user + session; launch the builtin `com.gostalgia.echo` app.
8. Runtime is "ready"; serves until shutdown request (signal or IPC).

Shutdown is the reverse: stop apps → stop services (reverse order) → close
IPC → remove `runtime.json` → flush logs. Idempotent, driven by
`Runtime.Shutdown(reason)`.

### 3.2 Service framework (`internal/service`) + core services (`internal/services`)

A service has explicit states: `created → starting → running → stopping →
stopped` (or `failed`), plus `Name()`, `Depends()`, `Init()`, `Start()`,
`Stop()`. The manager topologically sorts by dependencies, starts in order,
rolls back on failure, and stops in reverse. Lifecycle transitions publish
events. Concrete services in the slice: `sys` (status/ping/shutdown/app
endpoints), `process` (list/stop), `fs` (list/read/write/mkdir over the VFS),
`ipc` (socket listener + auth).

### 3.3 Process model (`internal/process`)

Environment processes are explicit objects with ID, name, kind, state, owner
session, and lifecycle — never anonymous goroutines. Two kinds, deliberately
distinct:

| Kind | Meaning | Mechanism | Isolation |
|---|---|---|---|
| `inproc` | App/service logic inside the runtime | goroutine + cancellable context | logical only |
| `child`  | Real host child process | `os/exec` + `CommandContext` | host process |

The distinction is part of the API and of every listing/IPC payload. We do
**not** pretend goroutines are OS processes. Supervision (restart policies,
log capture, resource accounting) is deferred but the manager API anticipates
it. `process/child` sandboxing is a later milestone.

### 3.4 IPC (`internal/ipc`)

One router, two transports:

- **In-process**: direct `Router.Dispatch` calls. Same routing semantics as
  the wire — the wire is a transport detail, not a second API.
- **Local socket**: NDJSON request/response framed over a platform listener
  (unix domain socket on macOS/Linux, loopback TCP on Windows), with a token
  handshake. This is what `gctl` and out-of-process apps use.

Protocol: request `{id, method, params}`, response `{id, ok, data, error}`.
Methods are namespaced: `sys/status`, `proc/list`, `fs/read`,
`app/<app-id>/<method>`. Streaming and pub/sub over the socket are future
work; the framing allows it (one JSON value per line).

### 3.5 Filesystem (`internal/vfs`)

A virtual filesystem rooted at `/`, backed by a host directory
(`<root>/vfs`), with a mount table (the slice mounts an in-memory FS at
`/tmp`). Applications only ever see env paths like `/users/guest/documents`;
host paths never leak into the API. The host backend confines all paths
lexically inside the root and is `testing/fstest`-verified. Future mounts:
memory FS, remote FS, per-app private storage, host-dir mounts.

### 3.6 Application model (`internal/app`, `apps/`)

An application is declared by a **manifest** (id, name, version, entrypoint,
permissions, description; JSON — see 4.3) and implemented by a factory that
produces an `sdk.Instance` with `Init(*sdk.Context)`, `Run(ctx)`, and
`Stop(ctx)`. Factories/apps receive no raw router, VFS, or process manager.
Launch reserves the ID before factory/Init, stages routes atomically under
`app/<id>/…`, and runs a tracked process with manifest capabilities on its
context. SDK calls and handlers replace inherited caller permissions, including
operator permissions, with the app's grant. Cleanup drains handlers, calls Stop,
and retracts routes/state on failure or exit. Events use `app.state` with
`launched`/`exited`. Single instance per app ID; compiled builtin manifests win
over seeded disk copies. See [applications.md](applications.md) for the spec.

### 3.6a Terminal experience (`internal/experience/shell`)

Bubble Tea owns the event loop/terminal; Lip Gloss supplies the DOS-inspired
styling. The shell talks through authenticated IPC, never via runtime managers.
`gostalgia` (no args) or `gostalgia shell` owns boot → shell → graceful shutdown;
`gostalgia boot` remains the headless entrypoint. Runtime logging accepts an
`io.Writer` so the interactive host suppresses console logs without importing
Charm into runtime. App shelf, launch/stop, VFS navigation, process inspection,
history, completion, and safe bounded scrollback are documented in
[shell.md](shell.md).

### 3.7 Events (`internal/events`)

Typed events (`Event` interface with `Type()`), published as envelopes (id,
type, source, time, payload) on a bus with per-topic and wildcard
subscribers. Handlers run synchronously in the publisher goroutine (ordered,
deterministic, no hidden buffering); slow consumers must use their own
goroutine — the bus is not a queue. Each subsystem defines its own event
types; nothing publishes untyped maps.

### 3.8 Sessions & security (`internal/session`, `internal/security`)

Users and sessions exist as first-class environment concepts from day one
(the slice creates user `guest` and one session at boot). Security model:
**capabilities**. A process's capabilities come from its manifest
permissions; they are attached to the IPC context and enforced at handler
boundaries (`security.Capabilities.Has`). Read/write VFS methods, process
listing/stopping, app listing/launching/stopping, and shutdown check their
individual grants; app route publication/invocation checks `ipc`. See the
complete permission table in [applications.md](applications.md).

**Honesty clause:** this is *logical* isolation only. The auth token on the
socket protects against accidental cross-user access, not a determined local
attacker. OS-level sandboxing (job objects / sandbox profiles / landlock) is
a later milestone and is not claimed until it exists.

### 3.9 Configuration (`internal/config`)

One JSON document per environment (`config/system.json`) addressed by dotted
paths, persisted atomically on write. Defaults live in code. Per-user, and
per-app layers are future milestones; the store API already supports overlay
by construction.

### 3.10 Platform layer (`platform/`)

Only what is genuinely host-specific, kept thin and behind build tags:

- IPC listener/dial: unix socket (`unix` build tag) vs loopback TCP
  (`windows`).
- Environment-root resolution defaults.

Everything else (signals, filesystem, exec, clocks) is already portable in
the standard library. As desktop/audio arrive, their host bits get adapters
here.

## 4. Decisions and trade-offs

### 4.1 Dependency policy

**Core and SDK: standard library only. Experience: approved Charm family.**
Bubble Tea v1.3.10 and Lip Gloss v1.1.0 are direct dependencies only in
`internal/experience/shell`. They buy terminal lifecycle/event handling and
styling; no alternate UI framework or unrelated direct dependency is added.
Their required transitive dependencies (terminal, ANSI, Unicode width,
color/input helpers and `golang.org/x/*`) are pinned by go.mod/go.sum and are
part of this explicit exception, not hand-added runtime dependencies.

`go list -deps gostalgia/internal/runtime gostalgia/sdk` contains only Gostalgia
and standard-library packages. An automated test enforces that boundary.
`gctl` and the SDK/demo dependency closures likewise remain stdlib-only.
The repository shares one module, so module downloads include Charm, but
headless runtime packages never import it. No cgo requirement; verify with
`CGO_ENABLED=0 go build ./...`. JSON manifests and stdlib command parsing remain
intentional. A future VirelaiOS port can omit the experience layer entirely;
userspace porting is not implemented here.

### 4.2 JSON manifests, not YAML

Same schema as the brief's sketch, but `encoding/json` is stdlib and the
manifest is machine-facing (produced/consumed by tooling), not hand-edited
prose. Revisit if hand-authoring becomes common.

### 4.3 NDJSON IPC

Line-delimited JSON is trivially debuggable (`nc` + `tail`), unframes
correctly, and streams. Binary/shmem transports can be added behind the same
router if a workload demands it.

### 4.4 Synchronous event handlers

Determinism and ordering beat throughput at this stage. The bus documents
the contract; an async subscriber helper can be added without changing
publishers.

### 4.5 Host-backed VFS, not a real FS driver

The slice maps `/` onto `<root>/vfs` with lexical confinement. That is
enough to prove the abstraction boundary; real drivers/virtual backends
slide in behind the same `vfs.FS` interface later.

### 4.6 Desktop toolkit — deferred decision, leading candidate recorded

The desktop is Milestone M4. Leading candidate: **Gio** (pure Go, no cgo,
cross-platform); alternatives: Fyne (cgo/OpenGL, more batteries), Ebiten
(game-oriented). This decision is *not* made yet; the window service API
will be designed first so the toolkit sits behind `desktop/` adapters.

## 5. Milestones

Big-picture milestones (from the project brief, refined):

- **M0 Architecture** ✅ — this document + docs set.
- **M1 Runtime vertical slice** ✅ — boot → config → services → session → one
  app → IPC → clean shutdown.
- **M2 Hardened core** — child processes with log capture; config layering;
  event persistence; Charm shell over env APIs (**implemented**). Core hardening next.
- **M3 Application model v2** — manifests with deps; out-of-proc apps over
  the socket transport; per-app VFS views.
- **M4 Desktop** — window service; pick toolkit; one window; terminal app.
- **M5 Platform parity** — real behavior tests on all three OSes; packaging.
- **M6 Expansion** — packages, networking, notifications, sandboxing.

The itemized implementation backlog — 20 tracked items, each producing
runnable code with tests, with per-item status — lives in
[status.md](../status.md), which is the canonical tracker. Keep that list and
this section in sync: an item moves only when it boots, passes `go vet` and
`go test -race ./...`, and its documentation is updated.

## 6. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Windowing toolkit choice (M4) | rework of desktop layer | design window service API first; toolkit behind adapter; spike Gio early |
| Goroutine-vs-process confusion | wrong abstractions, false security | explicit `Kind` everywhere; docs state isolation level per kind |
| Windows divergence (named pipes vs AF_UNIX, path quirks, case-insensitivity) | broken parity | platform/ adapters + CI matrix + behavior tests, not just compilation |
| VFS symlink/TOCTOU escape | containment failure | confinement via `os.Root` now; per-app views + OS sandboxing in backlog #13; honest docs |
| Scope creep (kernel/browser envy) | nothing actually works | milestone gates: each must boot, run, and be tested before the next |
| Single-runtime coupling (crash kills env) | reliability | supervisor/restart of core services in M2; out-of-proc apps in M3 |
| Auth/token theater | false security claims | security doc states exactly what is and is not protected |

## 7. Testing strategy

- Unit tests per subsystem (bus, config, service ordering/rollback, process
  lifecycle incl. child procs via self-exec helper, VFS with
  `testing/fstest`, IPC router/server/client incl. auth, app manager).
- End-to-end: boot a real runtime in a temp root, talk to it over a real
  socket (`test/e2e`), verify clean shutdown.
- Manual: `gostalgia boot` + `gctl` walkthrough documented in
  `docs/development.md`.
- Gates: `go vet ./...`, `gofmt`, `go test ./...`, `go test -race ./...`
  (unix), CI matrix on all three OSes.
