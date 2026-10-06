# Security

Milestone 1 builds the vocabulary of security (identities, capabilities,
authentication) without claiming protections it does not have. This document
states exactly what is and is not protected.

## What exists

### Users and sessions

`internal/security` defines `User`; `internal/session` opens and closes
sessions. The runtime creates user `guest` and one session at boot. Process
specs record the owning session and user.

### Capabilities and authority separation

Permission tokens (`security.Capabilities`) travel with IPC call contexts:

- **Operator socket clients:** Socket clients that complete the token
  handshake (such as `gctl` and the interactive Charm shell) receive the
  `admin` capability set. They act as trusted operator clients with full
  system authority.
- **Applications:** Applications declare their complete grants in JSON
  manifests; the runtime attaches those grants to the process context and
  process info. Unknown, duplicate, or operator-only `admin` declarations are
  rejected.
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
  capability table. Filesystem grants are currently service-wide, not per-path.

### Transport authentication

The local socket requires a token handshake (constant-time compare) before
any other method. The token is generated per boot and stored in `runtime.json`
(mode 0600 on unix; on Windows the mode does not map to an ACL — the file
inherits the environment directory's permissions).

## Isolation levels — honest labels

| Level | Status |
|---|---|
| Logical isolation (namespaces, capabilities in-process) | **current** (SDK adapters enforce manifest grants; confused-deputy protection in place) |
| Process isolation (child processes for apps) | **manager exists** (`internal/process`); external app lifecycle tracked in [#36](https://github.com/drawmeanelephant/gostalgia/issues/36); distinct app identity in [#35](https://github.com/drawmeanelephant/gostalgia/issues/35) |
| OS sandboxing (job objects, sandbox profiles, landlock, seccomp) | not started (tracked in [#38](https://github.com/drawmeanelephant/gostalgia/issues/38)) |
| Host / kernel isolation | out of scope for the host runtime; separate VirelaiOS bring-up is tracked independently by the owner and not claimed here |

## What is NOT protected

- **No OS sandbox.** A local attacker who can read `<root>/runtime.json` can
  fully control the environment. The token is local authentication
  convenience (prevents accidental cross-user access), not a security boundary.
- **In-process memory sharing:** `inproc` applications share the runtime's
  address space. While SDK adapters enforce capability checks logically, a
  malicious or buggy app could bypass checks in memory. Do not run untrusted
  applications. Arbitrary external apps remain trusted-only until platform
  sandbox enforcement is implemented.
- **Shared filesystem root:** The VFS confines paths through `os.Root`
  (symlink and `..` escapes fail closed), but all applications currently share
  the environment root; scoped per-app private storage and per-document grants
  are not yet implemented (tracked in [#37](https://github.com/drawmeanelephant/gostalgia/issues/37)).

## Roadmap

Application isolation and trust is organized under Milestone 4:
- Distinct authenticated app identities and scoped credentials ([#35](https://github.com/drawmeanelephant/gostalgia/issues/35)).
- External Go application lifecycle over the environment protocol ([#36](https://github.com/drawmeanelephant/gostalgia/issues/36)).
- App-private storage and scoped VFS grants ([#37](https://github.com/drawmeanelephant/gostalgia/issues/37)).
- Platform-specific app execution and resource policies on macOS/Linux ([#38](https://github.com/drawmeanelephant/gostalgia/issues/38)).
- Adversarial isolation tests, IPC fuzzing, and threat model ([#39](https://github.com/drawmeanelephant/gostalgia/issues/39)).

Capabilities will only be claimed secure when backed by an enforcement
mechanism outside the protected code.
