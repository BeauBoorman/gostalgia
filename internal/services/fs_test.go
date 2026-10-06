package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"gostalgia/internal/security"
	"gostalgia/internal/vfs"
)

func TestFSDocumentOperationsOverIPC(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()

	// 1. fs/save
	payload := base64.StdEncoding.EncodeToString([]byte("hello document"))
	resp := env.call(context.Background(), admin, "fs/save", map[string]any{
		"path":        "/users/guest/documents/doc.txt",
		"data_base64": payload,
	})
	if !resp.OK {
		t.Fatalf("fs/save failed: %s", resp.Error)
	}

	// 2. fs/stat
	resp = env.call(context.Background(), admin, "fs/stat", map[string]string{
		"path": "/users/guest/documents/doc.txt",
	})
	if !resp.OK {
		t.Fatalf("fs/stat failed: %s", resp.Error)
	}
	var statOut struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size"`
	}
	must(t, json.Unmarshal(resp.Data, &statOut))
	if statOut.Name != "doc.txt" || statOut.IsDir || statOut.Size != int64(len("hello document")) {
		t.Fatalf("unexpected stat output: %+v", statOut)
	}

	// 3. fs/save overwrite=false collision
	overwriteFalse := false
	resp = env.call(context.Background(), admin, "fs/save", map[string]any{
		"path":        "/users/guest/documents/doc.txt",
		"data_base64": payload,
		"overwrite":   &overwriteFalse,
	})
	if resp.OK {
		t.Fatal("fs/save with overwrite=false should fail on existing file")
	}

	// 4. fs/read bounded read
	resp = env.call(context.Background(), admin, "fs/read", map[string]any{
		"path":   "/users/guest/documents/doc.txt",
		"offset": 6,
		"limit":  4,
	})
	if !resp.OK {
		t.Fatalf("fs/read bounded failed: %s", resp.Error)
	}
	var readOut struct {
		DataBase64 string `json:"data_base64"`
		Size       int    `json:"size"`
		TotalSize  int64  `json:"total_size"`
	}
	must(t, json.Unmarshal(resp.Data, &readOut))
	readDecoded, err := base64.StdEncoding.DecodeString(readOut.DataBase64)
	if err != nil || string(readDecoded) != "docu" {
		t.Fatalf("bounded read output = %q, want 'docu'", string(readDecoded))
	}

	// 5. fs/copy
	resp = env.call(context.Background(), admin, "fs/copy", map[string]any{
		"src":       "/users/guest/documents/doc.txt",
		"dst":       "/users/guest/documents/doc_copy.txt",
		"overwrite": false,
	})
	if !resp.OK {
		t.Fatalf("fs/copy failed: %s", resp.Error)
	}

	// 6. fs/copy cross-mount (/users/... to /tmp/...)
	resp = env.call(context.Background(), admin, "fs/copy", map[string]any{
		"src":       "/users/guest/documents/doc.txt",
		"dst":       "/tmp/doc_tmp.txt",
		"overwrite": false,
	})
	if !resp.OK {
		t.Fatalf("fs/copy cross-mount failed: %s", resp.Error)
	}

	// 7. fs/rename same-mount
	resp = env.call(context.Background(), admin, "fs/rename", map[string]any{
		"src":       "/users/guest/documents/doc_copy.txt",
		"dst":       "/users/guest/documents/doc_renamed.txt",
		"overwrite": false,
	})
	if !resp.OK {
		t.Fatalf("fs/rename failed: %s", resp.Error)
	}

	// 8. fs/rename cross-mount must fail explicitly
	resp = env.call(context.Background(), admin, "fs/rename", map[string]any{
		"src":       "/users/guest/documents/doc_renamed.txt",
		"dst":       "/tmp/doc_renamed.txt",
		"overwrite": false,
	})
	if resp.OK {
		t.Fatal("fs/rename cross-mount should have failed")
	}

	// 9. fs/move cross-mount succeeds
	resp = env.call(context.Background(), admin, "fs/move", map[string]any{
		"src":       "/tmp/doc_tmp.txt",
		"dst":       "/users/guest/documents/from_tmp.txt",
		"overwrite": false,
	})
	if !resp.OK {
		t.Fatalf("fs/move cross-mount failed: %s", resp.Error)
	}

	// 10. fs/trash, fs/trash/list, fs/restore, fs/trash/empty
	resp = env.call(context.Background(), admin, "fs/trash", map[string]string{
		"path": "/users/guest/documents/from_tmp.txt",
	})
	if !resp.OK {
		t.Fatalf("fs/trash failed: %s", resp.Error)
	}
	var trashEntry vfs.TrashEntry
	must(t, json.Unmarshal(resp.Data, &trashEntry))
	if trashEntry.Name != "from_tmp.txt" {
		t.Fatalf("unexpected trash entry: %+v", trashEntry)
	}

	// Trash list
	resp = env.call(context.Background(), admin, "fs/trash/list", nil)
	if !resp.OK {
		t.Fatalf("fs/trash/list failed: %s", resp.Error)
	}
	var listOut struct {
		Entries []vfs.TrashEntry `json:"entries"`
	}
	must(t, json.Unmarshal(resp.Data, &listOut))
	if len(listOut.Entries) == 0 {
		t.Fatal("fs/trash/list returned empty entries")
	}

	// Restore
	resp = env.call(context.Background(), admin, "fs/restore", map[string]any{
		"id":        trashEntry.ID,
		"dst":       "/users/guest/documents/restored.txt",
		"overwrite": false,
	})
	if !resp.OK {
		t.Fatalf("fs/restore failed: %s", resp.Error)
	}

	// Verify restored file exists
	if _, err := env.ctx.VFS.Stat("/users/guest/documents/restored.txt"); err != nil {
		t.Fatalf("restored file missing: %v", err)
	}

	// Trash again and empty
	resp = env.call(context.Background(), admin, "fs/trash", map[string]string{
		"path": "/users/guest/documents/restored.txt",
	})
	if !resp.OK {
		t.Fatalf("fs/trash second time failed: %s", resp.Error)
	}

	resp = env.call(context.Background(), admin, "fs/trash/empty", nil)
	if !resp.OK {
		t.Fatalf("fs/trash/empty failed: %s", resp.Error)
	}
	var emptyOut struct {
		Count int `json:"count"`
	}
	must(t, json.Unmarshal(resp.Data, &emptyOut))
	if emptyOut.Count != 1 {
		t.Fatalf("expected 1 item emptied, got %d", emptyOut.Count)
	}
}

func TestFSCapabilityDenials(t *testing.T) {
	env := newTestEnv(t)
	readOnly := security.NewCapabilities(security.CapFileRead)
	writeOnly := security.NewCapabilities(security.CapFileWrite)

	// fs/save requires write
	resp := env.call(context.Background(), readOnly, "fs/save", map[string]string{
		"path": "/users/guest/documents/test.txt",
	})
	if resp.OK {
		t.Fatal("fs/save should require CapFileWrite")
	}

	// fs/rename requires write
	resp = env.call(context.Background(), readOnly, "fs/rename", map[string]string{
		"src": "/users/guest/documents/a",
		"dst": "/users/guest/documents/b",
	})
	if resp.OK {
		t.Fatal("fs/rename should require CapFileWrite")
	}

	// fs/copy requires both read and write
	resp = env.call(context.Background(), readOnly, "fs/copy", map[string]string{
		"src": "/users/guest/documents/a",
		"dst": "/users/guest/documents/b",
	})
	if resp.OK {
		t.Fatal("fs/copy should require CapFileWrite")
	}
	resp = env.call(context.Background(), writeOnly, "fs/copy", map[string]string{
		"src": "/users/guest/documents/a",
		"dst": "/users/guest/documents/b",
	})
	if resp.OK {
		t.Fatal("fs/copy should require CapFileRead")
	}

	// fs/trash/list requires read
	resp = env.call(context.Background(), writeOnly, "fs/trash/list", nil)
	if resp.OK {
		t.Fatal("fs/trash/list should require CapFileRead")
	}

	// fs/trash requires write
	resp = env.call(context.Background(), readOnly, "fs/trash", map[string]string{
		"path": "/users/guest/documents/a",
	})
	if resp.OK {
		t.Fatal("fs/trash should require CapFileWrite")
	}

	// fs/restore requires write
	resp = env.call(context.Background(), readOnly, "fs/restore", map[string]string{
		"id": "item-1",
	})
	if resp.OK {
		t.Fatal("fs/restore should require CapFileWrite")
	}

	// fs/trash/empty requires write
	resp = env.call(context.Background(), readOnly, "fs/trash/empty", nil)
	if resp.OK {
		t.Fatal("fs/trash/empty should require CapFileWrite")
	}
}

func TestFSConfinementOverIPC(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()

	escapePaths := []string{
		"../../outside",
		"/../outside",
		"/users/guest/../../escaped",
	}

	for _, p := range escapePaths {
		resp := env.call(context.Background(), admin, "fs/stat", map[string]string{"path": p})
		if resp.OK {
			t.Errorf("fs/stat(%q) should have failed confinement", p)
		}
		resp = env.call(context.Background(), admin, "fs/save", map[string]string{"path": p})
		if resp.OK {
			t.Errorf("fs/save(%q) should have failed confinement", p)
		}
		resp = env.call(context.Background(), admin, "fs/rename", map[string]string{"src": p, "dst": "/valid"})
		if resp.OK {
			t.Errorf("fs/rename(%q) should have failed confinement", p)
		}
		resp = env.call(context.Background(), admin, "fs/copy", map[string]string{"src": p, "dst": "/valid"})
		if resp.OK {
			t.Errorf("fs/copy(%q) should have failed confinement", p)
		}
		resp = env.call(context.Background(), admin, "fs/trash", map[string]string{"path": p})
		if resp.OK {
			t.Errorf("fs/trash(%q) should have failed confinement", p)
		}
	}
}

func TestFSSizeLimitsOverIPC(t *testing.T) {
	env := newTestEnv(t)
	admin := security.AdminCapabilities()

	// Try to write payload exceeding MaxIPCWriteLimit (4MB)
	hugeData := make([]byte, vfs.MaxIPCWriteLimit+1024)
	hugeBase64 := base64.StdEncoding.EncodeToString(hugeData)

	resp := env.call(context.Background(), admin, "fs/write", map[string]string{
		"path":        "/users/guest/documents/huge.txt",
		"data_base64": hugeBase64,
	})
	if resp.OK {
		t.Fatal("fs/write exceeding MaxIPCWriteLimit should fail")
	}
	if !strings.Contains(resp.Error, "exceeds write limit") {
		t.Fatalf("unexpected error message: %s", resp.Error)
	}

	resp = env.call(context.Background(), admin, "fs/save", map[string]string{
		"path":        "/users/guest/documents/huge.txt",
		"data_base64": hugeBase64,
	})
	if resp.OK {
		t.Fatal("fs/save exceeding MaxIPCWriteLimit should fail")
	}
}
