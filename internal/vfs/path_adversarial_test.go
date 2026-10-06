package vfs

import (
	"fmt"
	"strings"
	"testing"
)

func FuzzNormalize(f *testing.F) {
	seeds := []string{
		"",
		"/",
		".",
		"..",
		"a",
		"/a/b/c",
		"users/guest/documents",
		"/users/guest/documents/",
		"/a/../b",
		"a//b",
		"/./x",
		"dir\\file",
		"file\x00name",
		"/apps/data/com.test.app/data.txt",
		"/apps/data/com.test.app/../other/secret.txt",
		"../../../../etc/passwd",
		"\\Windows\\System32\\cmd.exe",
		strings.Repeat("a/", 200),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got, err := Normalize(in)
		if err == nil {
			// Invariant: normalized path must never contain backslashes or NUL bytes
			if strings.ContainsRune(got, '\\') {
				t.Fatalf("Normalize(%q) returned backslash in %q", in, got)
			}
			if strings.ContainsRune(got, 0) {
				t.Fatalf("Normalize(%q) returned NUL byte in %q", in, got)
			}
			// Invariant: cannot start or end with slash unless root "."
			if got != "." {
				if strings.HasPrefix(got, "/") || strings.HasSuffix(got, "/") {
					t.Fatalf("Normalize(%q) returned leading/trailing slash in %q", in, got)
				}
				for _, seg := range strings.Split(got, "/") {
					if seg == "." || seg == ".." {
						t.Fatalf("Normalize(%q) returned dot segment in %q", in, got)
					}
				}
			}
		}
	})
}

func TestAdversarialPathNormalization(t *testing.T) {
	traversals := []string{
		"..",
		"../",
		"/..",
		"/../",
		"../../",
		"/../../",
		"a/..",
		"/a/..",
		"/a/../b",
		"a/../b",
		"a/b/../../..",
		"/users/guest/../../../etc/passwd",
		"/apps/data/com.test.app/../com.other.app/secret",
		"dir/./sub",
		"/./dir",
		"dir/.",
		"//",
		"///",
		"////",
		"a//b",
		"/a//b/",
		"dir\\file",
		"\\dir\\file",
		"c:\\windows\\system32",
		"path\x00null",
		"/path/\x00/null",
	}

	for _, p := range traversals {
		t.Run(fmt.Sprintf("Reject_%s", p), func(t *testing.T) {
			got, err := Normalize(p)
			if err == nil {
				t.Fatalf("Normalize(%q) unexpectedly succeeded, got %q", p, got)
			}
		})
	}
}

func TestValidPathNormalization(t *testing.T) {
	valid := map[string]string{
		"":                                 ".",
		"/":                                ".",
		".":                                ".",
		"a":                                "a",
		"/a":                               "a",
		"a/b":                              "a/b",
		"/a/b":                             "a/b",
		"/a/b/":                            "a/b",
		"users/guest":                      "users/guest",
		"/users/guest/documents":           "users/guest/documents",
		"/apps/data/com.gostalgia.echo":    "apps/data/com.gostalgia.echo",
		"/apps/data/com.gostalgia.echo/db": "apps/data/com.gostalgia.echo/db",
	}

	for in, want := range valid {
		got, err := Normalize(in)
		if err != nil {
			t.Fatalf("Normalize(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
