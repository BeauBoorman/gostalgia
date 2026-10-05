# Security

Milestone 1 builds the vocabulary of security (identities, capabilities,
authentication) without claiming protections it does not have. This document
states exactly what is and is not protected.

## What exists

### Users and sessions

`internal/security` defines `User`; `internal/session` opens and closes
sessions. The runtime creates user `guest` and one session at boot. Process
specs record the owning session and user.

### Capabilities

Permission tokens (`security.Capabilities`) travel with IPC call contexts:

- Socket clients that complete the token handshake receive the admin set.
- Applications declare their complete grants in JSON manifests; the runtime
  attaches those grants to the process context and process info. Unknown,
  duplicate, or operator-only `admin` declarations are rejected.
- The public SDK exposes scoped Call and Init-only Handle adapters, not raw
  runtime managers. Calls always replace incoming caps with the app's grant;
  app handlers require `ipc` from their caller and execute with the app's own
  grant. An admin invoking a handler cannot lend it admin privileges.
- `ipc.RequireCap` guards filesystem read/write, process list/stop, app
  list/launch/stop, and shutdown. SDK calls and app route declaration/invocation
  require `ipc`. See [applications.md](applications.md) for method schemas and
  the exact capability table. Filesystem grants are service-wide, not per-path.
- Shell and gctl are trusted operator clients using the same token/admin grant;
  this is distinct from application execution authority.

### Transport authentication

The local socket requires a token handshake (constant-time compare) before
any other method. The token is generated per boot and stored in `runtime.json`
(mode 0600).

## Isolation levels — honest labels

| Level | Status |
|---|---|
| Logical isolation (namespaces, capabilities in-process) | **current** |
| Process isolation (child processes for apps) | **manager exists**; apps still run in-proc |
| OS sandboxing (job objects, sandbox profiles, landlock, seccomp) | not started |
| Hardware isolation | out of scope |

## What is NOT protected

- A local attacker who can read `<root>/runtime.json` can fully control the
  environment. The token is convenience (prevents accidental cross-user
  access), not a boundary.
- `inproc` applications share the runtime's address space: a malicious or
  buggy app can bypass every capability check today. Do not run untrusted
  applications.
- The VFS confines paths through `os.Root` (symlink and `..` escapes fail
  closed), but applications are trusted code; there is no per-app view yet.

## Roadmap

Per-app VFS views and deny-by-default cap enforcement (backlog #13),
out-of-proc apps (#9), OS-level sandboxing where the host offers it (#13),
threat model and IPC fuzzing (#19). Capabilities will only be claimed secure
when backed by an enforcement mechanism outside the protected code.
