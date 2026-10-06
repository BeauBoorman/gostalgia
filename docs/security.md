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
- **Enforcement:** `ipc.RequireCap` guards filesystem read/write, process
  list/stop, app list/launch/stop, and shutdown. SDK calls and app route
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

## Isolation levels — honest labels

| Level | Status |
|---|---|
| Logical isolation (namespaces, capabilities, scoped storage) | **current** (SDK adapters enforce manifest grants; app-private storage and scoped VFS grants enforced in `internal/vfs` and `internal/services`) |
| Process isolation (child processes for apps) | **in progress** (distinct authenticated app identities and lifecycle revocation implemented in [#35](https://github.com/drawmeanelephant/gostalgia/issues/35); external app lifecycle tracked in [#36](https://github.com/drawmeanelephant/gostalgia/issues/36)) |
| OS sandboxing (job objects, sandbox profiles, landlock, seccomp) | not started (tracked in [#38](https://github.com/drawmeanelephant/gostalgia/issues/38)) |
| Host / kernel isolation | out of scope for the host runtime; separate VirelaiOS bring-up is tracked independently by the owner and not claimed here |

## What is NOT protected

- **No OS sandbox.** A local attacker who can read `<root>/runtime.json` can
  fully control the environment. The operator token is local authentication
  convenience (prevents accidental cross-user access), not a security boundary.
- **In-process memory sharing:** `inproc` applications share the runtime's
  address space. While SDK adapters enforce capability checks logically, a
  malicious or buggy app could bypass checks in memory. Do not run untrusted
  applications. Arbitrary external apps remain trusted-only until platform
  sandbox enforcement is implemented.
- **Host syscall boundary:** App-private storage (`/apps/data/<app_id>`) and
  path-scoped VFS grants are enforced at the service and VFS layers. However,
  direct host syscalls from in-process code or un-sandboxed child processes
  remain outside this logical boundary until OS sandbox enforcement
  ([#38](https://github.com/drawmeanelephant/gostalgia/issues/38)).

## Roadmap

Application isolation and trust is organized under Milestone 4:
- Distinct authenticated app identities and scoped credentials ([#35](https://github.com/drawmeanelephant/gostalgia/issues/35) — implemented).
- External Go application lifecycle over the environment protocol ([#36](https://github.com/drawmeanelephant/gostalgia/issues/36)).
- App-private storage and scoped VFS grants ([#37](https://github.com/drawmeanelephant/gostalgia/issues/37) — implemented).
- Platform-specific app execution and resource policies on macOS/Linux ([#38](https://github.com/drawmeanelephant/gostalgia/issues/38)).
- Adversarial isolation tests, IPC fuzzing, and threat model ([#39](https://github.com/drawmeanelephant/gostalgia/issues/39)).

Capabilities will only be claimed secure when backed by an enforcement
mechanism outside the protected code.
