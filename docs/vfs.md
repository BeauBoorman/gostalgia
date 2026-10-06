# Virtual Filesystem (VFS)

The virtual filesystem and document storage architecture documentation has been consolidated in:

- [docs/filesystem.md](filesystem.md)

See [docs/filesystem.md](filesystem.md) for details on:
- VFS core interfaces (`FS`, `DocumentFS`)
- Backends (`HostFS`, `MemFS`) and path normalization
- App-private storage partitioning (`/apps/data/<app_id>`)
- Path-scoped capability grants (`GrantStore`)
- Confinement checks, symlink escape prevention, and grant revocation
- Recoverable atomic saves, copy/move across mounts, and trash lifecycle
- IPC method surface (`fs/*` and `fs/grant/*`)
