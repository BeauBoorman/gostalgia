package services

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gostalgia/internal/pkg"
	"gostalgia/internal/security"
	"gostalgia/sdk"
)

func serviceArchive(t *testing.T, version string, caps []string) []byte {
	t.Helper()
	man := sdk.Manifest{ID: "com.test.package", Name: "Package", Version: version, Mode: sdk.ModeExternal,
		Isolation: sdk.IsolationSandbox, Executable: "app", Permissions: caps}
	raw, _ := json.Marshal(man)
	files := map[string][]byte{"manifest.json": raw, "app": []byte("executable")}
	m := pkg.Metadata{FormatVersion: pkg.FormatVersion, ID: man.ID, Version: version, Checksums: make(map[string]string)}
	for name, data := range files {
		sum := sha256.Sum256(data)
		m.Checksums[name] = hex.EncodeToString(sum[:])
	}
	files["package.json"], _ = json.Marshal(m)
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, data := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func packageEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	s := NewPackage()
	must(t, s.Init(env.ctx))
	must(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return env
}

func TestPackageRoutes(t *testing.T) {
	env := packageEnv(t)
	ctx := context.Background()
	admin := security.AdminCapabilities()
	archivePath := "/users/guest/documents/app.gpkg"
	must(t, env.ctx.VFS.WriteFile(archivePath, serviceArchive(t, "1.0.0", nil), 0o600))
	for _, method := range []string{"pkg/install", "pkg/update", "pkg/uninstall", "pkg/list", "pkg/inspect", "pkg/rollback"} {
		resp := env.call(ctx, security.NewCapabilities(), method, nil)
		if resp.OK || !strings.Contains(resp.Error, "missing capability") {
			t.Fatalf("%s not capability gated: %+v", method, resp)
		}
	}
	read := security.NewCapabilities(security.CapPackageRead, security.CapFileRead)
	if resp := env.call(ctx, read, "pkg/inspect", pkg.Params{Path: archivePath}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := env.call(ctx, read, "pkg/install", pkg.Params{Path: archivePath}); resp.OK {
		t.Fatal("read capability installed package")
	}
	if resp := env.call(ctx, admin, "pkg/install", pkg.Params{Path: archivePath}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := env.call(ctx, read, "pkg/list", nil); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := env.call(ctx, read, "pkg/inspect", pkg.Params{ID: "com.test.package"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	must(t, env.ctx.VFS.WriteFile(archivePath, serviceArchive(t, "2.0.0", []string{sdk.CapNetEgress}), 0o600))
	if resp := env.call(ctx, admin, "pkg/update", pkg.Params{Path: archivePath}); resp.OK || !strings.Contains(resp.Error, "permission expansion") {
		t.Fatalf("update bypassed confirmation: %+v", resp)
	}
	app := security.AppPrincipal("com.test.caller", 123, "", security.User{Name: "guest"})
	appCaps := security.NewCapabilities(security.CapPackageWrite, security.CapFileRead)
	resp := env.callAs(ctx, app, appCaps, "pkg/update", pkg.Params{Path: archivePath, ConfirmPermissions: true})
	if resp.OK || !strings.Contains(resp.Error, "only the operator") {
		t.Fatalf("app confirmed permissions: %+v", resp)
	}
	if resp := env.call(ctx, admin, "pkg/update", pkg.Params{Path: archivePath, ConfirmPermissions: true}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := env.call(ctx, admin, "pkg/rollback", pkg.Params{ID: "com.test.package"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := env.call(ctx, admin, "pkg/uninstall", pkg.Params{ID: "com.test.package"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := env.call(ctx, read, "pkg/inspect", pkg.Params{ID: "com.test.package"}); resp.OK {
		t.Fatal("uninstall retained package")
	}
	// A builtin cannot be uninstalled through packages.
	if resp := env.call(ctx, admin, "pkg/uninstall", pkg.Params{ID: "com.gostalgia.echo"}); resp.OK {
		t.Fatal("uninstalled builtin")
	}
}

func TestPackageArchiveReadsStayScoped(t *testing.T) {
	env := packageEnv(t)
	name := "/users/guest/documents/app.gpkg"
	must(t, env.ctx.VFS.WriteFile(name, serviceArchive(t, "1.0.0", nil), 0o600))
	app := security.AppPrincipal("com.test.caller", 123, "", security.User{Name: "guest"})
	caps := security.NewCapabilities(security.CapPackageRead, security.CapFileRead)
	resp := env.callAs(context.Background(), app, caps, "pkg/inspect", pkg.Params{Path: name})
	if resp.OK {
		t.Fatal("package read bypassed path grants")
	}
	_, err := env.ctx.Apps.GrantStore().Issue(app.AppID, name, "read", false)
	must(t, err)
	resp = env.callAs(context.Background(), app, caps, "pkg/inspect", pkg.Params{Path: name})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	// A package cannot smuggle an arbitrary host filename through the VFS route.
	hostPath := filepath.Join(t.TempDir(), "host.gpkg")
	must(t, os.WriteFile(hostPath, serviceArchive(t, "1.0.0", nil), 0o600))
	resp = env.call(context.Background(), security.AdminCapabilities(), "pkg/inspect", pkg.Params{Path: hostPath})
	if resp.OK {
		t.Fatal("read arbitrary host archive")
	}
	for _, params := range []pkg.Params{{}, {ID: app.AppID, Path: name}, {Path: name, ConfirmPermissions: true}} {
		if resp := env.call(context.Background(), security.AdminCapabilities(), "pkg/inspect", params); resp.OK {
			t.Fatal("accepted ambiguous inspection")
		}
	}
}

func TestPackageTrustConfiguration(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	name := filepath.Join(t.TempDir(), "trust.json")
	raw, _ := json.Marshal(map[string]string{"publisher": base64.StdEncoding.EncodeToString(public)})
	must(t, os.WriteFile(name, raw, 0o600))
	trust, err := loadPackageTrust(name)
	must(t, err)
	if !bytes.Equal(trust["publisher"], public) {
		t.Fatal("trust key changed")
	}
	must(t, os.WriteFile(name, []byte(`{"publisher":"bad"}`), 0o600))
	if _, err := loadPackageTrust(name); err == nil {
		t.Fatal("accepted malformed key")
	}
}
