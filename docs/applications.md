# Applications

The application model lives in `internal/app`; builtin applications live in
`apps/`.

## Manifest

A manifest declares an application:

```json
{
  "id": "com.gostalgia.echo",
  "name": "Echo",
  "version": "0.1.0",
  "entrypoint": "echo",
  "permissions": ["ipc"],
  "description": "Returns whatever it is sent; the environment's hello-world application."
}
```

- `id` — reverse-DNS (`^[a-z][a-z0-9]*(\.[a-z0-9-]+)+$`), the application's
  identity everywhere (IPC routes, process names, listing).
- `entrypoint` — the registered factory name for builtin apps; will name a
  binary for out-of-proc apps (backlog #9 in status.md).
- `permissions` — capabilities the application requires; granted to its
  process and checked at IPC handler boundaries.

Manifests live as JSON documents in the VFS at `/apps/manifests/<id>.json`
(builtin manifests are seeded there at boot, so installed applications are
data, not just code). Registry validation rejects malformed documents.

## Instance

A factory receives a `LaunchContext` (manifest, log, router, bus) and returns
an `Instance`:

```go
type Instance interface {
    Run(ctx context.Context, p *process.Process) error   // blocks until stopped
    RegisterRoutes(r *ipc.Router, base string) error      // e.g. base+"/echo"
}
```

## Lifecycle

`Manager.Launch(ctx, id)`:

1. Resolve manifest and factory.
2. Enforce the single-instance policy (a second launch is rejected with the
   running pid).
3. Create the instance; register its routes under `app/<id>/…`.
4. Start an `inproc` process whose spec carries the manifest's permissions.
5. Publish `app.state {launched}`.

On process exit (user request or crash), routes are retracted, the app is
unmarked running, and `app.state {exited}` is published. `Manager.Stop` stops
the instance's process; `Run` must return promptly when its context is
canceled.

## The echo application

`apps/echo` is the reference implementation: it registers `echo` and `stats`
under `app/com.gostalgia.echo/`, blocks in `Run` until stopped, and demonstrates
manifest seeding, launch-by-the-runtime, single-instance policy, and clean
shutdown. Talk to it with `gctl echo hello` or
`gctl call 'app/com.gostalgia.echo/echo' '{"msg":"hello"}'`.
