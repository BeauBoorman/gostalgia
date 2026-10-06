# Gostalgia Status

The living status document: what works right now, how it is verified, what is
honestly missing, and the itemized milestone list. This is the **canonical
tracker** — `docs/architecture.md` §5 summarizes the milestone arc and points
here.

- **Current position:** Milestone 1 in progress (Charm experience + capability-scoped app SDK merged; active item: [#20](https://github.com/drawmeanelephant/gostalgia/issues/20) documentation and security claims reconciliation) · foundation unblocks Milestones 1–5.
- **Last verified:** 2026-10-05 (`go build` / `go vet` / `gofmt` clean · `go test -race ./...` green · test/package counts are CI's to report · pure-Go + Windows/Linux cross-builds OK · compiled Charm CLI exercised in a real PTY; terminal restored and runtime cleaned up · **CI green on ubuntu, macOS, and Windows** · repo: `drawmeanelephant/gostalgia`, private)

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
| Process model: in-proc lifecycle, child spawn/kill/exit-status | ✅ working | `internal/process` tests (self-exec child helper); `proc/list` returns `exit_code` over IPC |
| VFS: env paths, host backend confined via `os.Root`, memfs `/tmp`, mounts | ✅ working | `testing/fstest` + escape/mount tests |
| App SDK: embedded JSON manifest, Init/Run/Stop, scoped calls/routes, launch/stop | ✅ working | `sdk`, `internal/app`, services + shell socket lifecycle tests |
| Charm shell: DOS-style prompt, app shelf, history/completion, VFS/process commands | ✅ working | `internal/experience/shell` (real Bubble Tea + authenticated IPC) |
| Event bus (typed, synchronous, wildcard) | ✅ working | `internal/events` tests |
| Config store (dotted paths, atomic persist) | ✅ working | `internal/config` tests |
| Capability scoping: app Call/Handle adapters enforce manifest grants | ✅ working | SDK adapters replace caller grants; IPC methods reject unauthorized app calls; admin tokens remain trusted operator clients — see `docs/security.md` |
| Cross-platform compile (darwin/linux/windows) | ✅ compiles | `GOOS=` builds in CI matrix |
| Cross-platform behavior: CI runs the full test suite on ubuntu, macOS, and Windows runners | ✅ CI-green (Windows: full fstest skipped — see `docs/filesystem.md` metadata caveat; structural checks run everywhere) | `.github/workflows/ci.yml` |

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

- **No OS sandbox.** SDK calls and routes enforce manifest capability scoping
  and prevent borrowing caller permissions, but apps still run in-process;
  malicious Go code could bypass in-process checks. The socket token provides
  local operator authentication, not an isolation boundary. Arbitrary external
  apps remain trusted-only until platform sandboxing is implemented (Milestone 4,
  issues [#35](https://github.com/drawmeanelephant/gostalgia/issues/35)–[#39](https://github.com/drawmeanelephant/gostalgia/issues/39)).
- **No process supervision or log capture yet:** `proc/list` returns
  `process.Info` snapshots (including `exit_code`, timing, and error state) over
  IPC, but `gctl ps` and shell `ps` do not yet render exit codes; exited
  processes remain listed indefinitely (no reaping), restart policies and
  crash-loop protection are not yet implemented, and child stdout/stderr output
  is not captured into bounded buffers (Milestone 3, issues
  [#30](https://github.com/drawmeanelephant/gostalgia/issues/30)–[#33](https://github.com/drawmeanelephant/gostalgia/issues/33)).
- **Single-user, single-session:** user `guest` is fixed; personal profiles and
  session ownership are scheduled for Milestone 5 ([#41](https://github.com/drawmeanelephant/gostalgia/issues/41)).
- **Config has one layer** (system); layered preferences are scheduled for
  Milestone 2 ([#29](https://github.com/drawmeanelephant/gostalgia/issues/29)).
- **IPC client is serialized** (one outstanding call per client); subscriptions
  and multiplexing are scheduled for Milestone 3 ([#32](https://github.com/drawmeanelephant/gostalgia/issues/32)).
- **Windows filesystem metadata:** directory mtimes are advisory on Windows
  (OS-level API inconsistency; see `docs/filesystem.md`). Deep platform
  behavior beyond the CI suite is scheduled for host integration ([#44](https://github.com/drawmeanelephant/gostalgia/issues/44)).
- **VirelaiOS note:** Early VirelaiOS bring-up (toolchain, guest runner, kernel
  integration) is separately owned by the repository owner and is not treated as
  implemented; the shipped runtime remains standard-library Go on host platforms.

---

## The list

Twenty-five tracked issues across five GitHub milestones. Each one produces
runnable code with tests and updates the affected docs.

Foundational runtime vertical slice, Charm shell, and capability-scoped public
SDK are merged and verified (`test/e2e`, `internal/experience/shell`,
`internal/app`).

### Milestone 1: 01: A welcoming terminal workspace

- [ ] **[#20 Docs: reconcile the roadmap and security claims with the current implementation](https://github.com/drawmeanelephant/gostalgia/issues/20)** (active) — reconcile status.md, security.md, processes.md, and guides against merged SDK and process services.
- [ ] **[#21 Experience: establish a coordinated Charm version and dependency baseline](https://github.com/drawmeanelephant/gostalgia/issues/21)** — evaluate coordinated Bubble Tea/Lip Gloss/Bubbles baseline without cgo.
- [ ] **[#22 Experience: build a reusable Gostalgia visual language and component kit](https://github.com/drawmeanelephant/gostalgia/issues/22)** — tokens, panel borders, headers, tabs, dialogs, badges.
- [ ] **[#23 Experience: add a home screen, searchable launcher, and command palette](https://github.com/drawmeanelephant/gostalgia/issues/23)** — home screen, searchable app cards, keyboard command palette.
- [ ] **[#24 Experience: add accessible terminal modes and layout regression coverage](https://github.com/drawmeanelephant/gostalgia/issues/24)** — monochrome, high-contrast, reduced-motion, Unicode/layout regression tests.

### Milestone 2: 02: Everyday apps and documents

- [ ] **[#25 Filesystem: add safe VFS document operations and recoverable saves](https://github.com/drawmeanelephant/gostalgia/issues/25)** — atomic saves, copy/move, trash/restore, bounded reads/writes.
- [ ] **[#26 Apps: define a data/action presentation contract with one terminal owner](https://github.com/drawmeanelephant/gostalgia/issues/26)** — typed/versioned data and action contracts; shell alone owns rendering.
- [ ] **[#27 Apps: build a VFS-backed Files browser](https://github.com/drawmeanelephant/gostalgia/issues/27)** — keyboard browsing, sorting, previews, file operations.
- [ ] **[#28 Apps: build Notes with safe saving, dirty state, and crash recovery](https://github.com/drawmeanelephant/gostalgia/issues/28)** — editor, save/save-as, dirty indicator, crash recovery.
- [ ] **[#29 Settings: add layered preferences, live updates, and an interactive settings screen](https://github.com/drawmeanelephant/gostalgia/issues/29)** — layered system/user/app precedence, live change events.

### Milestone 3: 03: A live, observable computer

- [ ] **[#30 Processes: capture bounded child output and expose complete diagnostics](https://github.com/drawmeanelephant/gostalgia/issues/30)** — bounded stdout/stderr ring buffers, IPC diagnostics, gctl/shell presentation.
- [ ] **[#31 Processes: add supervision, bounded history, and crash-loop protection](https://github.com/drawmeanelephant/gostalgia/issues/31)** — restart policies, crash-loop backoff, bounded exit history, reaping.
- [ ] **[#32 IPC: add bounded event subscriptions, audit history, and responsive concurrent calls](https://github.com/drawmeanelephant/gostalgia/issues/32)** — non-blocking event subscriptions, multiplexing, audit trail.
- [ ] **[#33 Experience: add a live Task Manager, notification center, and crash receipts](https://github.com/drawmeanelephant/gostalgia/issues/33)** — live process screen, toasts, notifications, crash receipts.
- [ ] **[#34 Sessions: support detachable shells and persistent workspace state](https://github.com/drawmeanelephant/gostalgia/issues/34)** — attach/detach, persistent workspace state, headless survival.

### Milestone 4: 04: Application isolation and trust

- [ ] **[#35 Security: give external apps distinct authenticated identities and scoped grants](https://github.com/drawmeanelephant/gostalgia/issues/35)** — separate operator authority from app principals; lifecycle-bound tokens.
- [ ] **[#36 Apps: launch external Go applications over the environment protocol](https://github.com/drawmeanelephant/gostalgia/issues/36)** — external Go app execution, protocol compatibility, process tracking.
- [ ] **[#37 Security: add app-private storage and scoped VFS grants](https://github.com/drawmeanelephant/gostalgia/issues/37)** — per-app private storage roots, scoped file-selection grants.
- [ ] **[#38 Security: enforce platform-specific app execution and resource policies](https://github.com/drawmeanelephant/gostalgia/issues/38)** — host enforcement on macOS and Linux; honest unsupported reporting.
- [ ] **[#39 Security: add adversarial isolation tests, IPC fuzzing, and a threat model](https://github.com/drawmeanelephant/gostalgia/issues/39)** — adversarial test suite, protocol fuzzing, threat model documentation.

### Milestone 5: 05: A personal, extensible computer

- [ ] **[#40 Packages: add safe app installation, updates, rollback, and permission inspection](https://github.com/drawmeanelephant/gostalgia/issues/40)** — package format, verification, staging, rollback.
- [ ] **[#41 Profiles: add personal workspace profiles and session ownership](https://github.com/drawmeanelephant/gostalgia/issues/41)** — personal profile identities, isolated preferences/workspaces.
- [ ] **[#42 Documents: add permission-aware search, recents, favorites, and open-with handoff](https://github.com/drawmeanelephant/gostalgia/issues/42)** — bounded search, recent files, app handoff contracts.
- [ ] **[#43 Recovery: add portable backup, export/import, and verified restore](https://github.com/drawmeanelephant/gostalgia/issues/43)** — versioned backup archive, verified staging and restore.
- [ ] **[#44 Platform: add explicit opt-in clipboard, shared-folder, and network integration](https://github.com/drawmeanelephant/gostalgia/issues/44)** — capability-gated host adapters (clipboard, mounts, egress).

---

## Verification log

| Date | Check | Result |
|---|---|---|
| 2026-10-05 | Charm shell + public capability-scoped SDK; one embedded-manifest Echo demo; spec read against implementation | Build/vet/gofmt/race green; pure-Go and Windows/Linux builds OK; real Bubble Tea socket integration + compiled CLI PTY smoke (echo, scoped identity, stop/relaunch, F2 shelf, exit/terminal restoration/runtime cleanup); no second demo built |
| 2026-10-05 | FIFO regression (#16): `HostFS.Stat`/`ReadDir` no longer `os.Root.Open` special files — Lstat fallback for FIFOs, sockets, devices, symlinks; regression tests for `Stat`, `ReadDir`, and `ipc.Server.Close` with a `fs/list` handler in flight over a real socket | repro tests fail unfixed, pass fixed; full gate green (`vet`, `gofmt`, `-race`) |
| 2026-10-05 | Docs drift (#17): `status.md` "Last verified" no longer hardcodes test counts (CI is the source of truth); `security.md`/`ipc.md` scope the `runtime.json` 0600 mode to unix — Windows inherits directory ACLs | docs match the code |
| 2026-10-05 | Published private repo `drawmeanelephant/gostalgia`; CI matrix (ubuntu/macos/windows: vet, test, race-on-unix, gofmt) | **all three green** |
| 2026-10-05 | Windows CI hardening: HostFS handle lifecycle at shutdown, backslash-name rejection, single-source metadata; fstest metadata gate scoped to unix with structural checks everywhere (OS-level limitation, `docs/filesystem.md`) | Windows runner fully passing |
| 2026-10-05 | Rebrand FakeDOS → Gostalgia: module `gostalgia`, binaries `gostalgia`/`gctl`, app id `com.gostalgia.echo`, root `~/.gostalgia`, `$GOSTALGIA_ROOT`, socket `gostalgia-*.sock` | 171 references renamed, zero old names remain; full gate re-run green; live e2e clean |
| 2026-10-04 | `go test -race ./...` | 65 tests, 10 packages, all pass |
| 2026-10-04 | `go vet ./...`, `gofmt -l .` | clean |
| 2026-10-04 | Cross-compile windows/amd64, linux/amd64 | OK |
| 2026-10-04 | Manual e2e: boot → gctl status/ps/apps/echo/ls/cat/fs-write → shutdown | clean shutdown, `runtime.json` removed |
| 2026-10-04 | Duplicate-boot guard on a live root | refused with pid/endpoint |
