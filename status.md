# Gostalgia Status

The living status document: what works right now, how it is verified, what is
honestly missing, and the itemized milestone list. This is the **canonical
tracker** — `docs/architecture.md` §5 summarizes the milestone arc and points
here.

- **Current position:** M1 complete · Charm experience + capability-scoped app SDK implemented · next core item: **#2 Child-process support end-to-end**
- **Last verified:** 2026-10-05 (`go build` / `go vet` / `gofmt` clean · `go test -race ./...` green · pure-Go + Windows/Linux cross-builds OK · compiled Charm CLI exercised in a real PTY; terminal restored and runtime cleaned up)

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
| App SDK: embedded JSON manifest, Init/Run/Stop, scoped calls/routes, launch/stop | ✅ working | `sdk`, `internal/app`, services + shell socket lifecycle tests |
| Charm shell: DOS-style prompt, app shelf, history/completion, VFS/process commands | ✅ working | `internal/experience/shell` (real Bubble Tea + authenticated IPC) |
| Event bus (typed, synchronous, wildcard) | ✅ working | `internal/events` tests |
| Config store (dotted paths, atomic persist) | ✅ working | `internal/config` tests |
| Sessions + capability checks on privileged IPC methods | ✅ working | `internal/session`, `internal/services` tests |
| Cross-platform compile (darwin/linux/windows) | ✅ compiles | `GOOS=` builds in CI matrix |
| Windows/Linux **behavior** parity | ❌ not yet tested | — (backlog #12) |

Quick check from a clean checkout:

```sh
go build ./... && go vet ./... && go test -race ./...
gofmt -l .  # empty output = clean
go run ./cmd/gostalgia shell --root /tmp/gs # interactive; exit shuts down
# Or headless:
go run ./cmd/gostalgia boot --root /tmp/gs   # terminal 1
go run ./cmd/gctl --root /tmp/gs status      # terminal 2
```

## Known gaps (honest list)

- **No OS sandbox.** SDK calls/routes enforce manifest capability scoping and
  prevent borrowing caller permissions, but apps still run in-process; malicious
  Go code could bypass the SDK boundary. The socket token is local trust, not a
  boundary. Details and roadmap: `docs/security.md`.
- **No supervision yet:** exited processes stay listed (no reaping), no
  restart policies, no per-process log capture.
- **Single-user, single-session:** user `guest` is fixed; no login.
- **Config has one layer** (system); per-user/per-app layers are backlog #5.
- **IPC client is serialized** (one outstanding call per client); no
  server-push notifications.
- **Desktop, networking, packages, notifications** do not exist yet — by
  design, they are behind the core milestones.
- **Windows/Linux runtime behavior is untested** (compile-only so far).

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
- [x] **4. Native shell** — `gostalgia shell` (default command), Bubble Tea +
  Lip Gloss experience, fs/ps/apps/launch/stop via authenticated IPC only;
  app shelf, history, completion, bounded safe scrollback. `gsh` is not a
  separate binary. See `docs/shell.md`.
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
| 2026-10-05 | Charm shell + public capability-scoped SDK; one embedded-manifest Echo demo; spec read against implementation | Build/vet/gofmt/race green; pure-Go and Windows/Linux builds OK; real Bubble Tea socket integration + compiled CLI PTY smoke (echo, scoped identity, stop/relaunch, F2 shelf, exit/terminal restoration/runtime cleanup); no second demo built |
| 2026-10-05 | Rebrand FakeDOS → Gostalgia: module `gostalgia`, binaries `gostalgia`/`gctl`, app id `com.gostalgia.echo`, root `~/.gostalgia`, `$GOSTALGIA_ROOT`, socket `gostalgia-*.sock` | 171 references renamed, zero old names remain; full gate re-run green; live e2e clean |
| 2026-10-04 | `go test -race ./...` | 65 tests, 10 packages, all pass |
| 2026-10-04 | `go vet ./...`, `gofmt -l .` | clean |
| 2026-10-04 | Cross-compile windows/amd64, linux/amd64 | OK |
| 2026-10-04 | Manual e2e: boot → gctl status/ps/apps/echo/ls/cat/fs-write → shutdown | clean shutdown, `runtime.json` removed |
| 2026-10-04 | Duplicate-boot guard on a live root | refused with pid/endpoint |
