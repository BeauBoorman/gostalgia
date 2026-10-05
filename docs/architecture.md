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
│  DESKTOP ENVIRONMENT (M4+)  windows · panels · notifications │
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
│   ├── app/            # application model, manifests, launcher
│   └── runtime/        # boot, wiring, shutdown
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
produces an `Instance` with `Run(ctx, proc)` and `RegisterRoutes(router,
base)`. Launching: manifest → factory → instance routes registered under
`app/<id>/…` (namespaced per app; routes are **not** scoped to the
manifest's capabilities — enforcement is backlog #13) → in-proc process
started, carrying the manifest's permissions on the process record and on
the application's call context → `app.launched` event. Single instance per
app id for now (duplicates are rejected). Out-of-proc apps reuse the same
manifest and talk to the runtime over the socket transport.

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
**capabilities**. A process's capabilities are its manifest permissions;
they are stored on the process record and attached to the application's own
call context. Handler guards exist (`ipc.RequireCap`, backed by
`security.Capabilities.Has`), but they are **structurally unreachable in
production today**: both production dispatch paths (authenticated socket
clients and the boot self-test) present the admin capability set, so a
guard can only fail in tests. Real enforcement with per-app call contexts
is backlog #13; until it lands, capabilities are carried, not enforced.

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
- Shutdown signal set: unix requests `os.Interrupt` + `SIGTERM`; Windows
  requests `os.Interrupt` only (see below).
- Environment-root resolution defaults.

Signals are the one item on the "host integration" list that the standard
library does not make portable: which terminating signals a host delivers
differs per platform, so the requested set lives behind build tags in
`platform/`. On Windows only Ctrl-C / console close reaches the process;
other termination paths (`taskkill /f`, job-object teardown) deliver no
signal at all, so a Windows host currently gets graceful shutdown via
Ctrl-C or IPC only — a known gap recorded in `docs/platform.md` and
scheduled for the platform-parity milestone. Everything else (filesystem,
exec, clocks) is already portable in the standard library. As desktop/audio
arrive, their host bits get adapters here.

## 4. Decisions and trade-offs

### 4.1 Zero external dependencies (for now)

stdlib-only keeps the slice auditable, builds trivially on all three OSes,
and forces the abstractions to be ours. Costs accepted: hand-rolled
subcommand parsing, JSON manifests, no YAML. We will add dependencies when
they buy real capability (first likely: a pure-Go UI toolkit), evaluated
then — not before.

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
  event persistence; shell (gsh) over env APIs. *Next up.*
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
