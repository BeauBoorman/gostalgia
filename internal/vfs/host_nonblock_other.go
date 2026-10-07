//go:build !unix

package vfs

// Platforms without POSIX blocking-on-open special files (FIFOs do not
// exist behind path opens on Windows or Plan 9) need no nonblocking
// flag; the post-open mode check still rejects unusual files.
const openNonblock = 0
