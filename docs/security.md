# Security

Milestone 1 builds the vocabulary of security (identities, capabilities,
authentication) without claiming protections it does not have. This document
states exactly what is and is not protected.

## What exists

### Users, sessions, and principals

`internal/security` defines `User`, `PrincipalKind` (`operator` vs `app`),
`Principal`, and `Credential`. `internal/session` opens and closes sessions.
The runtime creates user `guest` and one session at boot. Process specs record
the owning session and user. Every IPC connection resolves to a verified
`Principal` attached to the request context.

### Capabilities and authority separation

Permission tokens (`security.Capabilities`) travel with IPC call contexts:

- **Operator socket clients:** Socket clients that authenticate with the
  operator token (such as `gctl` and the interactive Charm shell) receive the
  `admin` capability set and operator principal identity. They act as trusted
  operator clients with full system authority.
- **Applications:** Applications declare their complete grants in JSON
  manifests; the runtime attaches those grants to the process context and
  process info. Unknown, duplicate, or operator-only `admin` declarations are
  rejected.
- **Distinct application credentials:** When an app launches, the runtime
  generates a distinct, cryptographically random app token via
  `internal/security.TokenStore`, bound to the application's ID, process ID,
  session, and declared manifest capabilities. App tokens are never written to
  `runtime.json`, leaked to process listings (`proc/list`), or inherited by
  unrelated child processes (`process.CleanEnv`). External applications receive
  their scoped token explicitly via `GOSTALGIA_APP_TOKEN` over a dedicated child
  IPC socket, and the credential is immediately revoked upon process exit or
  launch failure.
- **SDK capability scoping:** The public SDK exposes scoped `Call` and
  Init-only `Handle` adapters, not raw runtime managers. `Context.Call`
  replaces incoming capabilities with the app's manifest grant, and app
  handlers require `ipc` from callers while executing under the app's own
  grant. An operator invoking an app cannot lend it admin privileges (the
  confused-deputy boundary).
- **Enforcement:** `ipc.RequireCap` guards filesystem read/write (`fs.read`/`fs.write`),
  shared host folder access (`hostfs.read`/`hostfs.write`), process list/stop (`proc.list`/`proc.stop`),
  app list/launch/stop (`app.list`/`app.launch`/`app.stop`), clipboard operations (`clipboard.read`/`clipboard.write`),
  network egress (`net.egress`), and shutdown (`shutdown`). SDK calls and app route
  declaration/invocation require `ipc`. Because app `Call` and `Handle`
  adapters replace caller capabilities with manifest grants, **`RequireCap`
  does fail in production** if an app attempts an undeclared operation (for
  example, an app without `fs.write` or `shutdown` attempting those methods).
  Direct operator calls continue to pass because operators possess the `admin`
  set. See [applications.md](applications.md) for method schemas and the exact
  capability table.

### App-private storage and scoped VFS grants

Rather than allowing arbitrary open-ended access to the entire filesystem root,
access is partitioned and scoped:

- **Automatic app-private storage:** Each application is automatically allotted
  an isolated private storage partition under `/apps/data/<app_id>`. Applications
  have inherent read/write access to their own partition without requiring
  global filesystem permissions. An application cannot enumerate, read, write,
  stat, remove, rename, copy, or trash files belonging to another application's
  private storage through any exposed `fs/*` method. Directory listings of
  `/apps/data` present a principal-aware view displaying only the calling
  application's partition.
- **Scoped VFS grants:** Open-ended `fs.read`/`fs.write` access across the
  shared filesystem is replaced by path-scoped capability grants managed by
  `internal/vfs.GrantStore`. An operator issues grants via `fs/grant` specifying
  the target application ID, environment path, access mode (`read` or `read-write`),
  and whether descendants are included recursively.
- **Confinement & escape prevention:** Grant expansion is strictly prevented.
  Dot segments (`..`), host symlinks pointing outside the granted scope or into
  another app's private partition, cross-mount renames, and copy/move trickery
  fail closed with structured errors (`path_escape` or `permission_denied`).
- **Immediate revocation & restart semantics:** Revoking a grant via
  `fs/grant/revoke` invalidates the grant immediately; subsequent file operations
  by the application fail with `permission_denied: grant revoked`. Grants are
  scoped to `app_id`. Active grants persist across application process restarts
  until explicitly revoked by an operator; revoked grants remain revoked across
  restarts.
- **Trash isolation:** The environment trash preserves isolation: applications
  only view, restore, and empty trash entries originating from paths they are
  authorized to access.

### Transport authentication and lifecycle revocation

The local socket requires a token handshake before any other method. The IPC
server verifies tokens using `internal/security.TokenStore`:

- **Operator authentication:** The operator token is generated per boot and
  stored in `runtime.json` (mode 0600 on unix; on Windows the mode does not map
  to an ACL — the file inherits the environment directory's permissions).
- **App authentication:** App clients authenticate with their assigned app
  token, establishing an app principal with strictly scoped capabilities.
- **Per-request validation:** The server continuously validates token validity
  on every request (`auth.Validate(token)`).
- **Lifecycle revocation:** When an app stops or exits, its credential is
  immediately revoked in `TokenStore`. In-flight and subsequent calls on active
  connections fail with `unauthorized: credential revoked` and the connection
  is terminated; reconnection attempts fail handshake with `unauthorized: token
  revoked`. Stale credentials cannot be replayed.
- **Child environment scrubbing:** `process.CleanEnv` prevents child processes
  from inheriting host environment secrets or tokens.

### Platform-specific app execution and sandbox policies

External applications declare an `isolation` level in their manifest (`sdk.Manifest`):

- `inproc`: Compiled into the runtime and executed in-process. Manifest validation
  forbids `inproc` applications from requesting `sandbox` or `strict` isolation.
- `trusted`: Spawned out-of-process as a supervised host child process with
  environment sanitization (`process.CleanEnv`) and process group management, but
  without host sandbox restrictions.
- `sandbox`: Spawned out-of-process under platform OS sandbox confinement:
  - **Linux:** Uses kernel namespaces (`CLONE_NEWUSER | CLONE_NEWNET`) to block
    host network egress without requiring root or setuid helpers, and enforces
    resource ceilings (`RLIMIT_AS`, `RLIMIT_NOFILE`, `RLIMIT_CPU`) via `prlimit64`.
  - **macOS:** Generates dynamic Apple Seatbelt profiles executed via
    `/usr/bin/sandbox-exec` to deny network egress and restrict filesystem access.
- `strict`: Enforces maximal containment:
  - Network egress denied at the kernel level.
  - Descendant process execution denied (on macOS via Seatbelt `(deny process-fork)`,
    on Linux via real-time process supervision and immediate termination).
  - Virtual memory space capped (2 GB virtual address space for 64-bit runtime).
  - Maximum open file descriptors capped (512).
  - Read-only host filesystem enforcement.

#### Fail-closed execution guarantee

If an application requests `sandbox` or `strict` isolation on an unsupported host
(such as Windows, generic Unix, or Linux without unprivileged user namespaces enabled),
or if sandbox configuration fails, the runtime **fails closed** with
`ErrSandboxUnsupported`. The runtime **never silently degrades** to trusted or
in-process execution.

#### Host security capabilities & diagnostics

`platform.GetHostSecurityCapabilities()` probes live host enforcement mechanisms:
- `sys/status` exposes the `security` capabilities block, live process isolation levels, and active operator policy.
- `proc/info` and `proc/list` report the effective `isolation` level and active
  `policy` parameters for every process.
- Process termination (`platform.KillProcessTree`) kills the entire process group
  (`SIGKILL` to `-pid`), ensuring no orphan descendant processes can escape.

### Opt-in platform integration and operator policies

Host integration adapters (clipboard, shared folders, network egress) default to
strictly disabled/internal-only and require two distinct levels of authorization:
1. **Application capability declaration:** The app must declare the corresponding
   capability token (`clipboard.read`, `clipboard.write`, `hostfs.read`, `hostfs.write`,
   or `net.egress`) in its manifest.
2. **Explicit operator policy:** The operator must explicitly enable the integration
   in `OperatorPolicy` (`internal/security.PolicyStore`), inspected via `sys/policy`
   and updated by operator clients via `sys/policy/update`:
   - **Clipboard (`clipboard.*`):** When disabled, clipboard reads and writes operate
     purely within an internal, in-memory clipboard buffer. When enabled, incoming and
     outgoing data is strictly sanitized (`platform.SanitizeClipboard`), stripping
     terminal control sequences, DCS sequences, and OSC 52 sequences (which could hijack
     the host terminal or exfiltrate clipboard state) before interacting with the host clipboard.
   - **Shared host folders (`hostfs.*`):** Host paths cannot be accessed directly; they
     must be explicitly mounted by an operator via `hostfs/mount`. Backed by `SharedHostFS`,
     shared mounts use Go 1.24 `os.Root` confinement to guarantee that path traversals and
     symlinks cannot escape the designated host folder root. If mounted read-only, all write
     operations fail closed with `ErrReadOnly`.
   - **Network egress (`net.egress`):** Outbound network access is disabled by default. When
     enabled by policy, requests via `net/fetch` are checked against operator-configured host
     whitelists (`allowed_hosts`), blacklists (`blocked_hosts`), port filters (`allowed_ports`),
     and HTTPS-only transport requirements (`allow_insecure: false`). In addition, for external
     sandboxed apps, host network access at the kernel/sandbox level is denied unless both the app
     manifest grants `net.egress` and the operator policy permits network egress.

## Isolation levels — honest labels

| Level | Status |
|---|---|
| Logical isolation (namespaces, capabilities, scoped storage) | **current** (SDK adapters enforce manifest grants; app-private storage and scoped VFS grants enforced in `internal/vfs` and `internal/services`) |
| Process isolation (child processes for apps) | **current** (supervised out-of-process child execution over dedicated IPC sockets with token binding and revocation; [#36](https://github.com/drawmeanelephant/gostalgia/issues/36)) |
| OS sandboxing (namespaces, Seatbelt, prlimit64, fail-closed) | **current** (enforced on Linux and macOS; fail-closed on Windows and unsupported hosts; [#38](https://github.com/drawmeanelephant/gostalgia/issues/38)) |
| Host / kernel isolation | out of scope for the host runtime; separate VirelaiOS bring-up is tracked independently by the owner and not claimed here |

## What is NOT protected

- **Operator token exposure:** A local attacker who can read `<root>/runtime.json` can
  fully control the environment. The operator token is local authentication
  convenience (prevents accidental cross-user access), not a security boundary.
- **In-process memory sharing:** `inproc` applications share the runtime's
  address space. While SDK adapters enforce capability checks logically, a
  malicious or buggy in-proc app could bypass checks in memory. Do not run
  untrusted applications in-process. Untrusted applications should use `sandbox`
  or `strict` mode.
- **Windows host sandboxing:** Windows does not support native unprivileged
  sandboxing without hypervisor containers. As a result, requesting `sandbox` or
  `strict` isolation on Windows fails closed. External apps on Windows may only
  run under `trusted` isolation.

## Roadmap

Application isolation and trust is organized under Milestone 4:
- Distinct authenticated app identities and scoped credentials ([#35](https://github.com/drawmeanelephant/gostalgia/issues/35) — implemented).
- External Go application lifecycle over the environment protocol ([#36](https://github.com/drawmeanelephant/gostalgia/issues/36) — implemented).
- App-private storage and scoped VFS grants ([#37](https://github.com/drawmeanelephant/gostalgia/issues/37) — implemented).
- Platform-specific app execution and resource policies on macOS/Linux ([#38](https://github.com/drawmeanelephant/gostalgia/issues/38) — implemented).
- Adversarial isolation tests, IPC fuzzing, and threat model ([#39](https://github.com/drawmeanelephant/gostalgia/issues/39)).

Capabilities will only be claimed secure when backed by an enforcement
mechanism outside the protected code.
