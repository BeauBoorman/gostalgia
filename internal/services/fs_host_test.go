package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gostalgia/internal/security"
	"gostalgia/internal/vfs"
)

func TestHostFSMountAndCapabilityEnforcement(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Create a host directory with a test file
	tempHostDir := t.TempDir()
	testHostFile := filepath.Join(tempHostDir, "hello.txt")
	must(t, os.WriteFile(testHostFile, []byte("shared host content"), 0o644))

	operator := security.OperatorPrincipal(security.User{Name: "admin"})
	appPrinc := security.AppPrincipal("com.test.app", 10, "sess-1", security.User{Name: "guest"})

	capsAdmin := security.AdminCapabilities()
	capsNone := security.NewCapabilities()
	capsHostRead := security.NewCapabilities(security.CapHostFSRead)
	capsHostWrite := security.NewCapabilities(security.CapHostFSRead, security.CapHostFSWrite)

	// 1. Non-operator cannot mount
	res := env.callAs(ctx, appPrinc, capsNone, "hostfs/mount", map[string]any{
		"host_path":  tempHostDir,
		"mount_path": "/mnt/shared",
		"read_only":  true,
	})
	if res.OK {
		t.Fatal("expected non-operator hostfs/mount to fail")
	}

	// 2. Operator mount fails if policy disables hostfs
	policy := env.ctx.Policy.Get()
	policy.HostFS.Enabled = false
	env.ctx.Policy.Set(policy)

	res = env.callAs(ctx, operator, capsAdmin, "hostfs/mount", map[string]any{
		"host_path":  tempHostDir,
		"mount_path": "/mnt/shared",
		"read_only":  true,
	})
	if res.OK {
		t.Fatal("expected mount to fail when HostFS policy is disabled")
	}

	// 3. Enable HostFS in policy with allowed path
	policy.HostFS.Enabled = true
	policy.HostFS.AllowRead = true
	policy.HostFS.AllowedPaths = []string{tempHostDir}
	env.ctx.Policy.Set(policy)

	// Mount as read-only
	res = env.callAs(ctx, operator, capsAdmin, "hostfs/mount", map[string]any{
		"host_path":  tempHostDir,
		"mount_path": "/mnt/shared",
		"read_only":  true,
	})
	if !res.OK {
		t.Fatalf("hostfs/mount failed: %s", res.Error)
	}

	// 4. hostfs/list returns the mount
	res = env.call(ctx, capsAdmin, "hostfs/list", nil)
	if !res.OK {
		t.Fatalf("hostfs/list failed: %s", res.Error)
	}
	var listData struct {
		Mounts []vfs.SharedHostMountInfo `json:"mounts"`
	}
	must(t, json.Unmarshal(res.Data, &listData))
	if len(listData.Mounts) != 1 || listData.Mounts[0].MountPath != "/mnt/shared" {
		t.Fatalf("unexpected mounts: %+v", listData.Mounts)
	}

	// 5. App without CapHostFSRead cannot list or read
	res = env.callAs(ctx, appPrinc, capsNone, "fs/list", map[string]string{
		"path": "/mnt/shared",
	})
	if res.OK {
		t.Fatal("expected fs/list without CapHostFSRead to fail")
	}

	res = env.callAs(ctx, appPrinc, capsNone, "fs/read", map[string]string{
		"path": "/mnt/shared/hello.txt",
	})
	if res.OK {
		t.Fatal("expected fs/read without CapHostFSRead to fail")
	}

	// 6. App with CapHostFSRead CAN list and read
	res = env.callAs(ctx, appPrinc, capsHostRead, "fs/list", map[string]string{
		"path": "/mnt/shared",
	})
	if !res.OK {
		t.Fatalf("fs/list with CapHostFSRead failed: %s", res.Error)
	}

	res = env.callAs(ctx, appPrinc, capsHostRead, "fs/read", map[string]string{
		"path": "/mnt/shared/hello.txt",
	})
	if !res.OK {
		t.Fatalf("fs/read with CapHostFSRead failed: %s", res.Error)
	}
	var readData struct {
		DataBase64 string `json:"data_base64"`
	}
	must(t, json.Unmarshal(res.Data, &readData))
	contentBytes, _ := base64.StdEncoding.DecodeString(readData.DataBase64)
	if string(contentBytes) != "shared host content" {
		t.Fatalf("expected 'shared host content', got %q", string(contentBytes))
	}

	// 7. Write to read-only mount fails even with CapHostFSWrite
	payload := base64.StdEncoding.EncodeToString([]byte("new content"))
	res = env.callAs(ctx, appPrinc, capsHostWrite, "fs/write", map[string]any{
		"path":        "/mnt/shared/hello.txt",
		"data_base64": payload,
	})
	if res.OK {
		t.Fatal("expected fs/write to read-only mount to fail")
	}

	// 8. Unmount and re-mount as read-write
	res = env.callAs(ctx, operator, capsAdmin, "hostfs/unmount", map[string]string{
		"mount_path": "/mnt/shared",
	})
	if !res.OK {
		t.Fatalf("hostfs/unmount failed: %s", res.Error)
	}

	// Policy allow read-write
	policy.HostFS.AllowWrite = true
	env.ctx.Policy.Set(policy)

	res = env.callAs(ctx, operator, capsAdmin, "hostfs/mount", map[string]any{
		"host_path":  tempHostDir,
		"mount_path": "/mnt/shared",
		"read_only":  false,
	})
	if !res.OK {
		t.Fatalf("hostfs/mount read-write failed: %s", res.Error)
	}

	// 9. Write without CapHostFSWrite fails
	res = env.callAs(ctx, appPrinc, capsHostRead, "fs/write", map[string]any{
		"path":        "/mnt/shared/hello.txt",
		"data_base64": payload,
	})
	if res.OK {
		t.Fatal("expected fs/write without CapHostFSWrite to fail")
	}

	// 10. Write with CapHostFSWrite succeeds
	res = env.callAs(ctx, appPrinc, capsHostWrite, "fs/write", map[string]any{
		"path":        "/mnt/shared/hello.txt",
		"data_base64": payload,
	})
	if !res.OK {
		t.Fatalf("fs/write with CapHostFSWrite failed: %s", res.Error)
	}

	// Verify written to actual host disk!
	diskBytes, err := os.ReadFile(testHostFile)
	if err != nil {
		t.Fatalf("read host disk file: %v", err)
	}
	if string(diskBytes) != "new content" {
		t.Fatalf("expected 'new content' on host disk, got %q", string(diskBytes))
	}
}
