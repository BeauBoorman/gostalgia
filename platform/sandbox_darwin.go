//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	darwinCapsOnce sync.Once
	darwinCaps     HostSecurityCapabilities
)

// GetHostSecurityCapabilities probes and returns the sandbox enforcement mechanisms
// available on this Darwin/macOS host.
func GetHostSecurityCapabilities() HostSecurityCapabilities {
	darwinCapsOnce.Do(func() {
		caps := HostSecurityCapabilities{
			Platform: "darwin",
		}
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err == nil {
			caps.Supported = true
			caps.NetworkIsolation = true
			caps.DescendantControl = true
			caps.FilesystemSandbox = true
			caps.ResourceLimits = true
		} else {
			caps.Supported = false
			caps.Reason = "/usr/bin/sandbox-exec not found"
		}
		darwinCaps = caps
	})
	return darwinCaps
}

// ConfigureSandbox sets up host-level confinement for cmd on macOS using Seatbelt.
func ConfigureSandbox(cmd *exec.Cmd, policy ExecutionPolicy) (PostStartHook, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Isolation == "" || policy.Isolation == IsolationTrusted {
		return nil, nil
	}

	caps := GetHostSecurityCapabilities()
	if !caps.Supported {
		return nil, fmt.Errorf("%w: %s", ErrSandboxUnsupported, caps.Reason)
	}

	profile := buildDarwinSandboxProfile(policy, cmd.Path)
	origPath := cmd.Path
	origArgs := append([]string(nil), cmd.Args...)
	if len(origArgs) > 0 {
		origArgs = origArgs[1:]
	}

	cmd.Path = "/usr/bin/sandbox-exec"
	sandboxArgs := []string{"sandbox-exec", "-p", profile, origPath}
	cmd.Args = append(sandboxArgs, origArgs...)

	return nil, nil
}

// Host filesystem subtrees a sandboxed child may read: everything needed to
// execute (dynamic loader, system libraries, frameworks) plus ephemeral
// scratch space. Anything else — home directories, credentials, the
// environment root holding runtime.json — is denied by default.
var darwinReadRoots = []string{
	"/usr",
	"/bin",
	"/sbin",
	"/System",
	"/Library",
	"/private/etc",
	"/etc",
	"/dev",
	"/private/var/folders",
	"/var/folders",
	"/private/tmp",
	"/tmp",
}

// Subtrees that remain writable under a ReadOnlyFS (strict) policy.
var darwinWriteRoots = []string{
	"/private/var/folders",
	"/var/folders",
	"/private/tmp",
	"/tmp",
}

// sbCanonical resolves symlinks so the path matches the canonical vnode path
// Seatbelt actually evaluates. For non-existent paths it resolves the
// deepest existing ancestor so masks still hit canonical whitelisted parents.
func sbCanonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	// Walk up until an ancestor resolves, then re-join the remainder.
	rel := ""
	p := filepath.Clean(path)
	for p != "/" && p != "." && p != "" {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rel)
		}
		rel = filepath.Join(filepath.Base(p), rel)
		p = filepath.Dir(p)
	}
	return path
}

// sbPathFilter returns an SBPL path filter for prefix that excludes every
// masked path beneath it. Seatbelt deny rules cannot carve exceptions out of
// an allow, so masks must be expressed as require-not clauses inside the
// allow filter itself. masks may contain both raw and canonical spellings;
// each is matched against this prefix's spelling.
func sbPathFilter(prefix string, masks []string) string {
	var excludes strings.Builder
	for _, m := range masks {
		if maskUnder(prefix, m) {
			fmt.Fprintf(&excludes, " (require-not (subpath %q))", m)
		}
	}
	if excludes.Len() == 0 {
		return fmt.Sprintf("(subpath %q)", prefix)
	}
	return fmt.Sprintf("(require-all (subpath %q)%s)", prefix, excludes.String())
}

// maskUnder reports whether canonical path m is equal to or beneath canonical
// path prefix.
func maskUnder(prefix, m string) bool {
	return m == prefix || strings.HasPrefix(m, prefix+"/")
}

// sbAncestors returns every ancestor directory of path (both raw and
// canonical spellings), excluding "/" itself, in any order. These need
// file-read-metadata for path resolution to succeed.
func sbAncestors(path string) []string {
	seen := map[string]bool{}
	var out []string
	for _, start := range []string{filepath.Clean(path), sbCanonical(path)} {
		for p := filepath.Dir(start); p != "" && p != "/" && p != "." && !seen[p]; p = filepath.Dir(p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// sbMaskExcludes returns require-not filters for every masked path, applied
// to an otherwise unrestricted allow (e.g. file-write* under the sandbox
// tier where writes are otherwise permitted).
func sbMaskExcludes(masks []string) string {
	if len(masks) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range masks {
		fmt.Fprintf(&b, " (require-not (subpath %q))", m)
	}
	if len(masks) == 1 {
		return b.String()
	}
	return fmt.Sprintf(" (require-all%s)", b.String())
}

func buildDarwinSandboxProfile(policy ExecutionPolicy, exePath string) string {
	// Seatbelt evaluates file filters against both raw and canonicalized
	// spellings depending on the operation, so masks are emitted in both
	// forms and whitelist prefixes are emitted raw + canonical.
	var masks []string
	maskSeen := map[string]bool{}
	for _, m := range policy.MaskedPaths {
		if m == "" {
			continue
		}
		for _, f := range []string{m, sbCanonical(m)} {
			if !maskSeen[f] {
				maskSeen[f] = true
				masks = append(masks, f)
			}
		}
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	b.WriteString("(allow process-exec)\n")
	b.WriteString("(allow ipc-posix*)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow signal (target self))\n")

	if policy.DenyDescendants {
		b.WriteString("(deny process-fork)\n")
	} else {
		b.WriteString("(allow process-fork)\n")
	}

	// Sandboxed children receive their IPC channel as an inherited
	// descriptor (platform.ChildIPC), so no unix-socket connect, bind, or
	// listen capability is ever granted. Seatbelt cannot scope remote
	// unix-socket by path (literal/subpath filters silently never match),
	// which is why IPC moved off the filesystem for confined children.
	if policy.DenyNetwork {
		b.WriteString("(deny network*)\n")
	} else {
		b.WriteString("(allow network*)\n")
	}

	// Reads are confined to execution essentials and operator-granted paths.
	// Reading "/" itself is required for path resolution by dyld/libc.
	b.WriteString(`(allow file-read* (literal "/")`)
	seen := map[string]bool{}
	writePathFilter := func(path string) {
		for _, f := range []string{path, sbCanonical(path)} {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			b.WriteString(" " + sbPathFilter(f, masks))
		}
	}
	readPaths := append([]string(nil), darwinReadRoots...)
	readPaths = append(readPaths, policy.AllowedPaths...)
	for _, root := range readPaths {
		writePathFilter(root)
	}
	for _, p := range []string{exePath, sbCanonical(exePath)} {
		if p != "" && !seen["L:"+p] {
			seen["L:"+p] = true
			fmt.Fprintf(&b, " (literal %q)", p)
		}
	}
	b.WriteString(")\n")

	// Resolving a whitelisted path requires stat() on every ancestor
	// directory (e.g. /var and /private/var for anything under /var/folders,
	// whose intermediate components Seatbelt evaluates but which subpath
	// filters do not cover). Metadata-only literals permit traversal without
	// granting directory listing or file contents.
	var meta strings.Builder
	metaSeen := map[string]bool{}
	writeAncestors := func(path string) {
		for _, f := range sbAncestors(path) {
			if !metaSeen[f] {
				metaSeen[f] = true
				fmt.Fprintf(&meta, " (literal %q)", f)
			}
		}
	}
	for _, root := range append(readPaths, darwinWriteRoots...) {
		writeAncestors(root)
	}
	writeAncestors(exePath)
	fmt.Fprintf(&b, "(allow file-read-metadata%s)\n", meta.String())

	if policy.ReadOnlyFS {
		b.WriteString("(deny file-write*)\n")
		b.WriteString(`(allow file-write* (literal "/dev/null") (literal "/dev/zero")`)
		seenW := map[string]bool{}
		writeWriteFilter := func(path string) {
			for _, f := range []string{path, sbCanonical(path)} {
				if f == "" || seenW[f] {
					continue
				}
				seenW[f] = true
				b.WriteString(" " + sbPathFilter(f, masks))
			}
		}
		for _, root := range darwinWriteRoots {
			writeWriteFilter(root)
		}
		for _, p := range policy.AllowedPaths {
			writeWriteFilter(p)
		}
		b.WriteString(")\n")
	} else {
		// The sandbox tier permits host writes (user permission boundary);
		// masked paths are still carved out.
		b.WriteString("(allow file-write*" + sbMaskExcludes(masks) + ")\n")
	}

	return b.String()
}

// KillDescendants is a no-op on macOS. DenyDescendants is enforced as hard
// prevention by the Seatbelt profile's (deny process-fork), so no sandboxed
// descendant can exist to kill; trusted children are torn down via
// KillProcessTree instead.
func KillDescendants(pid int) {}
