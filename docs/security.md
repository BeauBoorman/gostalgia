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
- Applications declare required permissions in their manifests. The runtime
  stores that set on the application's process and attaches it to the
  application's own call context, so the grant is carried and observable.
- `ipc.RequireCap(ctx, cap)` guards privileged methods: `fs.write`,
  `proc.stop`, `app.launch`, `shutdown`, `admin`.

**State this plainly: `RequireCap` cannot fail in production today.** Both
production dispatch paths (authenticated socket clients and the boot
self-test) present the admin set, so every guard passes. The checks are
exercised only by tests that hand-build a limited context. Deny-by-default
enforcement against the per-app grant is backlog #13.

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
