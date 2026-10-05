# Gostalgia Status

The living status document: what works right now, how it is verified, what is
honestly missing, and the itemized milestone list. This is the **canonical
tracker** — `docs/architecture.md` §5 summarizes the milestone arc and points
here.

- **Current position:** M1 complete · M2 starting · next item: **#2 Child-process support end-to-end**
- **Last verified:** 2026-10-05 (`go vet` clean · `gofmt` clean · 65 tests in 10 packages, all passing with `-race` · Windows + Linux cross-compile OK · rebrand to Gostalgia re-verified end to end)

Rules for touching this file:

1. An item moves to ✅ only when it **boots and runs**, passes
   `go vet ./...` and `go test -race ./...`, and its documentation in `docs/`
   is updated to match reality.
2. The "Snapshot" section describes what a user can actually do today — not
   what exists in code but is unexercised.
3. Known gaps stay listed until they are fixed; nothing is deleted to make a
   milestone look complete.

---

## Snapshot: what works today

| Capability | State | Verified by |
|---|---|---|
| Boot → services → session → app launch → IPC → clean shutdown | ✅ working | `test/e2e` (real socket, real auth, real shutdown) |
| Service framework (dep-ordered start, rollback, reverse stop) | ✅ working | `internal/service` unit tests |
| IPC: in-proc + socket transports, token handshake, NDJSON | ✅ working | `internal/ipc` tests + `gctl` manual/e2e |
| Process model: in-proc lifecycle, child spawn/kill/exit-status | ✅ working | `internal/process` tests (self-exec child helper) |
| VFS: env paths, host backend confined via `os.Root`, memfs `/tmp`, mounts | ✅ working | `testing/fstest` + escape/mount tests |
| Application model: manifests, builtin factories, single-instance launch | ✅ working | `internal/app` + services tests |
| Event bus (typed, synchronous, wildcard) | ✅ working | `internal/events` tests |
| Config store (dotted paths, atomic persist) | ✅ working | `internal/config` tests |
| Sessions + capability checks on privileged IPC methods | ✅ working | `internal/session`, `internal/services` tests |
| Cross-platform compile (darwin/linux/windows) | ✅ compiles | `GOOS=` builds in CI matrix |
| Cross-platform behavior: CI runs the full test suite on ubuntu, macOS, and Windows runners | ✅ CI-green (Windows: full fstest skipped — see `docs/filesystem.md` metadata caveat; structural checks run everywhere) | `.github/workflows/ci.yml` |

Quick check from a clean checkout:

```sh
go vet ./... && go test -race ./...
go run ./cmd/gostalgia boot --root /tmp/gs   # terminal 1
go run ./cmd/gctl --root /tmp/gs status      # terminal 2
```

## Known gaps (honest list)

- **Isolation is logical only.** Applications run in-process; a malicious app
  could bypass capability checks. The socket token is local trust, not a
  boundary. Details and roadmap: `docs/security.md`.
- **No supervision yet:** exited processes stay listed (no reaping), no
  restart policies, no per-process log capture.
- **Single-user, single-session:** user `guest` is fixed; no login.
- **Config has one layer** (system); per-user/per-app layers are backlog #5.
- **IPC client is serialized** (one outstanding call per client); no
  server-push notifications.
- **Desktop, networking, packages, notifications** do not exist yet — by
  design, they are behind the core milestones.
- **Windows filesystem metadata:** directory mtimes are advisory on Windows
  (OS-level API inconsistency; see `docs/filesystem.md`). Deep platform
  behavior beyond the CI suite (packaging, GUI paths) is still backlog #12.

---

## The list

Twenty tracked items. Each one produces runnable code with tests and updates
the affected docs. Grouped under the milestone arc from `docs/architecture.md`
§5.

### M1 — Runtime vertical slice ✅ (2026-10-04)

- [x] **1. Vertical slice** — boot/config/log/events/services/VFS/IPC/
  process/session/echo app/gctl, with unit + e2e tests and the docs set.

### M2 — Hardened core (next up)

- [ ] **2. Child-process support end-to-end** — child specs, exec, stop/kill,
  exit codes *(already partially in place: `StartChild` works and is tested —
  this item finishes it: output capture into per-process ring buffers,
  exit-code surfacing over IPC, `gctl` visibility)*.
- [ ] **3. Process supervision** — restart policies, process reaping,
  resource snapshots; `proc/list` grows history.
- [ ] **4. `gsh` native shell** — fs/ps/apps/launch built on IPC only; no
  privileged shell internals.
- [ ] **5. Config layering** — system + per-user + per-app layers, change
  events, `gctl config get/set`.
- [ ] **6. Event persistence** — event log on disk, `events/recent` endpoint
  (audit trail), `gctl events`.

### M3 — Application model v2

- [ ] **7. IPC v2** — server-push notifications, subscriptions over the
  socket, client multiplexing, method versioning.
- [ ] **8. App model v2** — app directories in VFS, manifest lint, dependency
  ordering between apps, `app/install` from a package file.
- [ ] **9. Out-of-proc app launcher** — spawn `gostalgia-app` binaries, socket
  handshake, capability negotiation; per-app call contexts enforced.

### M4 — Desktop

- [ ] **10. Window service API + toolkit spike** — design the window service
  first, then spike Gio (pure Go) behind a `desktop/` adapter: one window,
  lifecycle events on the bus.
- [ ] **11. Terminal app** — line-mode first, on the window service.

### M5 — Platform parity

- [ ] **12. Platform parity pass** — behavior tests (not just compilation) on
  Windows/macOS/Linux, packaging smoke (zip / app bundle / deb).

### M6 — Expansion

- [ ] **13. Permission enforcement pass** — per-app VFS views (scoped roots),
  cap checks on every fs/net route, deny-by-default, OS sandboxing where the
  host offers it.
- [ ] **14. Package format + manager** — signed tar+manifest packages;
  install/remove/update/list/search.
- [ ] **15. Network service** — env-level egress abstraction, per-app
  `network` capability, allow/deny policy.
- [ ] **16. Notification service** — event-driven toasts once the desktop
  exists.
- [ ] **17. Multi-session / multi-user** — login, per-session workspaces,
  secrets on OS keychains.
- [ ] **18. Desktop polish** — workspaces, window management, settings app,
  file manager app.
- [ ] **19. Security review** — threat model doc, fuzz the IPC parser,
  re-examine the token/transport story.
- [ ] **20. Release engineering** — versioned API, installers, update channel.

---

## Verification log

| Date | Check | Result |
|---|---|---|
| 2026-10-05 | Rebrand FakeDOS → Gostalgia: module `gostalgia`, binaries `gostalgia`/`gctl`, app id `com.gostalgia.echo`, root `~/.gostalgia`, `$GOSTALGIA_ROOT`, socket `gostalgia-*.sock` | 171 references renamed, zero old names remain; full gate re-run green; live e2e clean |
| 2026-10-04 | `go test -race ./...` | 65 tests, 10 packages, all pass |
| 2026-10-04 | `go vet ./...`, `gofmt -l .` | clean |
| 2026-10-04 | Cross-compile windows/amd64, linux/amd64 | OK |
| 2026-10-04 | Manual e2e: boot → gctl status/ps/apps/echo/ls/cat/fs-write → shutdown | clean shutdown, `runtime.json` removed |
| 2026-10-04 | Duplicate-boot guard on a live root | refused with pid/endpoint |
