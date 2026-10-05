package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestResolveRootCoversPrecedence: explicit flag beats $GOSTALGIA_ROOT
// beats ~/.gostalgia.
func TestResolveRootCoversPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	envRoot := t.TempDir()
	t.Setenv("GOSTALGIA_ROOT", envRoot)

	// Explicit flag wins.
	got, err := ResolveRoot(filepath.Join(home, "explicit"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "explicit"); got != want {
		t.Errorf("explicit: ResolveRoot = %q, want %q", got, want)
	}

	// Environment variable is next.
	got, err = ResolveRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if got != envRoot {
		t.Errorf("env: ResolveRoot = %q, want %q", got, envRoot)
	}

	// Home fallback last.
	os.Unsetenv("GOSTALGIA_ROOT")
	got, err = ResolveRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".gostalgia"); got != want {
		t.Errorf("home: ResolveRoot = %q, want %q", got, want)
	}
}

func TestInitRootCreatesLayoutAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := InitRoot(root); err != nil {
		t.Fatalf("first InitRoot: %v", err)
	}
	for _, dir := range []string{
		"config",
		"logs",
		"vfs",
		filepath.Join("vfs", "users", "guest", "documents"),
		filepath.Join("vfs", "users", "guest", "downloads"),
		filepath.Join("vfs", "users", "guest", "desktop"),
		filepath.Join("vfs", "users", "guest", "config"),
		filepath.Join("vfs", "apps", "manifests"),
		filepath.Join("vfs", "data"),
		filepath.Join("vfs", "mounts"),
	} {
		if info, err := os.Stat(filepath.Join(root, dir)); err != nil || !info.IsDir() {
			t.Errorf("layout dir %s missing or not a directory (err %v)", dir, err)
		}
	}
	// Second run must succeed unchanged.
	if err := InitRoot(root); err != nil {
		t.Fatalf("second InitRoot (idempotency): %v", err)
	}
}

func TestNewTokenIsStrongAndUnique(t *testing.T) {
	tokenHex := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		token, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		if !tokenHex.MatchString(token) {
			t.Fatalf("token %q is not 32 hex chars (128 bits)", token)
		}
		if seen[token] {
			t.Fatalf("token repeated after %d generations", i+1)
		}
		seen[token] = true
	}
}
