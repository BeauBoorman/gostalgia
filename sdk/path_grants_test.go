package sdk

import "testing"

func TestManifestPackagePermissionsAndPathGrants(t *testing.T) {
	man := Manifest{ID: "com.test.package", Name: "Package", Version: "1.0.0", Entrypoint: "package",
		Permissions: []string{CapPackageRead, CapPackageWrite}}
	for _, g := range []PathGrant{
		{Path: "/users/guest/documents", Access: "read", Recursive: true},
		{Path: "/apps/data/com.test.package", Access: "read-write"},
	} {
		man.PathGrants = []PathGrant{g}
		if err := man.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []PathGrant{
		{Path: "/", Access: "read"}, {Path: "/apps", Access: "read"},
		{Path: "/apps/com.test.package", Access: "read"},
		{Path: "/apps/data/com.test.other", Access: "read"},
		{Path: "/APPS/com.test.package", Access: "read-write"},
		{Path: "/apps./com.test.package", Access: "read-write"},
		{Path: "relative", Access: "read"}, {Path: "/a/../b", Access: "read"},
		{Path: "/a", Access: "unknown"}, {Path: "/bad\x00path", Access: "read"},
	} {
		man.PathGrants = []PathGrant{g}
		if err := man.Validate(); err == nil {
			t.Fatalf("accepted grant %+v", g)
		}
	}
	man.PathGrants = []PathGrant{{Path: "/a", Access: "read"}, {Path: "/a", Access: "read-write"}}
	if err := man.Validate(); err == nil {
		t.Fatal("accepted duplicate grant")
	}
}
