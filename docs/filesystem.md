# Filesystem

The environment filesystem lives in `internal/vfs`.

## The model

Applications and services speak **environment paths** (`/users/guest/documents`,
`/apps/manifests`); host paths never appear in any API, IPC payload, or error
message. `/` maps onto `<root>/vfs` on the host, and a mount table lets
subtrees shadow the root.

## Interface

```go
type FS interface {
    fs.FS          // Open
    fs.StatFS      // Stat
    fs.ReadDirFS   // ReadDir
    fs.ReadFileFS  // ReadFile
    MkdirAll(path string) error
    WriteFile(path string, data []byte, perm fs.FileMode) error
    Remove(path string) error
}
```

Two path forms are accepted where documented:

- **Environment form** (`/users/guest`, `/tmp/x`) — at the `*VFS` layer and in
  IPC params. Leading slash, `/`-separated. Dot segments (`.`/`..`),
  backslashes, and interior empty elements are rejected outright.
- **io/fs form** (`users/guest`) — the raw backends (`HostFS`, `MemFS`), which
  enforce `fs.ValidPath` strictly so they work with `testing/fstest`,
  `fs.WalkDir`, and the rest of the stdlib.

## Backends

- **HostFS** — a directory on the host, opened through `os.Root`. Path escapes
  via `..` or symlinks fail closed; errors reference the environment path, not
  the host path.
- **MemFS** — in-memory FS, used for `/tmp` (the environment's tmpfs) and as
  the platform test double.

## Mounts

`VFS.Mount("/tmp", vfs.NewMem())` shadows the root's `tmp` subtree; longest
prefix wins; `Unmount` restores the root view. Planned mounts: memory
filesystems, host-directory mounts, remote filesystems, per-application
private storage.

## Layout

```text
/               ← <root>/vfs on the host
├── users/<user>/{documents,downloads,desktop,config}
├── apps/manifests/    application manifests (JSON)
├── data/
├── mounts/            (reserved)
└── tmp/               ← memfs mount (ephemeral)
```

## Testing

Both backends pass `testing/fstest.TestFS`. Escape rejection (dot segments,
symlink traversal) and mount shadowing have dedicated tests.
