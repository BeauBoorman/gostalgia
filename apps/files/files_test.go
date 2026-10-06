package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gostalgia/sdk"
)

type mockVFSNode struct {
	isDir   bool
	data    []byte
	modTime time.Time
	mode    string
}

type mockVFS struct {
	mu           sync.Mutex
	nodes        map[string]*mockVFSNode
	trash        []trashItem
	launchedApps map[string]bool
	notesOpenErr error
	listErr      error
	readErr      error
	trashSeq     int
}

func newMockVFS() *mockVFS {
	m := &mockVFS{
		nodes:        make(map[string]*mockVFSNode),
		trash:        make([]trashItem, 0),
		launchedApps: make(map[string]bool),
	}
	// Seed standard initial directories
	m.mkdir("/users")
	m.mkdir("/users/guest")
	m.mkdir("/users/guest/documents")
	m.mkdir("/users/guest/.trash")
	m.mkdir("/tmp")
	return m
}

func (v *mockVFS) mkdir(p string) {
	clean := path.Clean(p)
	v.nodes[clean] = &mockVFSNode{
		isDir:   true,
		modTime: time.Now(),
		mode:    "drwxr-xr-x",
	}
}

func (v *mockVFS) writeFile(p string, data []byte) {
	clean := path.Clean(p)
	v.nodes[clean] = &mockVFSNode{
		isDir:   false,
		data:    data,
		modTime: time.Now(),
		mode:    "-rw-r--r--",
	}
}

func (v *mockVFS) call(ctx context.Context, method string, params, out any) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	rawParams, _ := json.Marshal(params)

	switch method {
	case "fs/list":
		if v.listErr != nil {
			return v.listErr
		}
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		target := path.Clean(p.Path)
		node, ok := v.nodes[target]
		if !ok {
			return errors.New("directory not found")
		}
		if !node.isDir {
			return errors.New("not a directory")
		}

		var entries []fileEntry
		targetPrefix := target
		if targetPrefix != "/" {
			targetPrefix += "/"
		}

		for pth, n := range v.nodes {
			if pth == target {
				continue
			}
			if strings.HasPrefix(pth, targetPrefix) {
				sub := strings.TrimPrefix(pth, targetPrefix)
				if !strings.Contains(sub, "/") {
					entries = append(entries, fileEntry{
						Name:    sub,
						IsDir:   n.isDir,
						Size:    int64(len(n.data)),
						Mode:    n.mode,
						ModTime: n.modTime.UTC().Format("2006-01-02 15:04"),
					})
				}
			}
		}

		res := map[string]any{"path": target, "entries": entries}
		raw, _ := json.Marshal(res)
		return json.Unmarshal(raw, out)

	case "fs/stat":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		target := path.Clean(p.Path)
		n, ok := v.nodes[target]
		if !ok {
			return errors.New("file not found")
		}
		res := map[string]any{
			"path":     target,
			"name":     path.Base(target),
			"is_dir":   n.isDir,
			"size":     int64(len(n.data)),
			"mode":     n.mode,
			"mod_time": n.modTime.UTC().Format(time.RFC3339),
		}
		raw, _ := json.Marshal(res)
		return json.Unmarshal(raw, out)

	case "fs/read":
		if v.readErr != nil {
			return v.readErr
		}
		var p struct {
			Path   string `json:"path"`
			Offset int64  `json:"offset"`
			Limit  int64  `json:"limit"`
		}
		_ = json.Unmarshal(rawParams, &p)
		target := path.Clean(p.Path)
		n, ok := v.nodes[target]
		if !ok {
			return errors.New("file not found")
		}
		if n.isDir {
			return errors.New("is a directory")
		}
		data := n.data
		if p.Offset > 0 && p.Offset < int64(len(data)) {
			data = data[p.Offset:]
		}
		if p.Limit > 0 && int64(len(data)) > p.Limit {
			data = data[:p.Limit]
		}
		res := map[string]any{
			"path":        target,
			"size":        int64(len(data)),
			"total_size":  int64(len(n.data)),
			"data_base64": base64.StdEncoding.EncodeToString(data),
		}
		raw, _ := json.Marshal(res)
		return json.Unmarshal(raw, out)

	case "fs/save":
		var p struct {
			Path       string `json:"path"`
			DataBase64 string `json:"data_base64"`
			Overwrite  bool   `json:"overwrite"`
		}
		_ = json.Unmarshal(rawParams, &p)
		target := path.Clean(p.Path)
		decoded, _ := base64.StdEncoding.DecodeString(p.DataBase64)
		v.nodes[target] = &mockVFSNode{
			isDir:   false,
			data:    decoded,
			modTime: time.Now(),
			mode:    "-rw-r--r--",
		}
		return nil

	case "fs/mkdir":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		target := path.Clean(p.Path)
		if _, exists := v.nodes[target]; exists {
			return errors.New("directory already exists")
		}
		v.nodes[target] = &mockVFSNode{
			isDir:   true,
			modTime: time.Now(),
			mode:    "drwxr-xr-x",
		}
		return nil

	case "fs/rename":
		var p struct {
			Src       string `json:"src"`
			Dst       string `json:"dst"`
			Overwrite bool   `json:"overwrite"`
		}
		_ = json.Unmarshal(rawParams, &p)
		src, dst := path.Clean(p.Src), path.Clean(p.Dst)
		node, ok := v.nodes[src]
		if !ok {
			return errors.New("source not found")
		}
		if _, exists := v.nodes[dst]; exists && !p.Overwrite {
			return errors.New("destination exists")
		}
		delete(v.nodes, src)
		v.nodes[dst] = node
		return nil

	case "fs/copy":
		var p struct {
			Src       string `json:"src"`
			Dst       string `json:"dst"`
			Overwrite bool   `json:"overwrite"`
		}
		_ = json.Unmarshal(rawParams, &p)
		src, dst := path.Clean(p.Src), path.Clean(p.Dst)
		node, ok := v.nodes[src]
		if !ok {
			return errors.New("source not found")
		}
		if _, exists := v.nodes[dst]; exists && !p.Overwrite {
			return errors.New("destination exists")
		}
		copiedData := append([]byte(nil), node.data...)
		v.nodes[dst] = &mockVFSNode{
			isDir:   node.isDir,
			data:    copiedData,
			modTime: time.Now(),
			mode:    node.mode,
		}
		return nil

	case "fs/move":
		var p struct {
			Src       string `json:"src"`
			Dst       string `json:"dst"`
			Overwrite bool   `json:"overwrite"`
		}
		_ = json.Unmarshal(rawParams, &p)
		src, dst := path.Clean(p.Src), path.Clean(p.Dst)
		node, ok := v.nodes[src]
		if !ok {
			return errors.New("source not found")
		}
		if _, exists := v.nodes[dst]; exists && !p.Overwrite {
			return errors.New("destination exists")
		}
		delete(v.nodes, src)
		v.nodes[dst] = node
		return nil

	case "fs/trash":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		target := path.Clean(p.Path)
		node, ok := v.nodes[target]
		if !ok {
			return errors.New("path not found")
		}
		delete(v.nodes, target)
		v.trashSeq++
		entry := trashItem{
			ID:           fmt.Sprintf("t_%d", v.trashSeq),
			OriginalPath: target,
			TrashPath:    path.Join("/users/guest/.trash/files", path.Base(target)),
			Name:         path.Base(target),
			IsDir:        node.isDir,
			Size:         int64(len(node.data)),
			TrashedAt:    time.Now().UTC().Format(time.RFC3339),
		}
		v.trash = append(v.trash, entry)
		if out != nil {
			raw, _ := json.Marshal(entry)
			return json.Unmarshal(raw, out)
		}
		return nil

	case "fs/trash/list":
		res := map[string]any{"entries": v.trash}
		if out != nil {
			raw, _ := json.Marshal(res)
			return json.Unmarshal(raw, out)
		}
		return nil

	case "fs/trash/empty":
		cnt := len(v.trash)
		v.trash = nil
		res := map[string]any{"count": cnt}
		if out != nil {
			raw, _ := json.Marshal(res)
			return json.Unmarshal(raw, out)
		}
		return nil

	case "fs/restore":
		var p struct {
			ID        string `json:"id"`
			Dst       string `json:"dst"`
			Overwrite bool   `json:"overwrite"`
		}
		_ = json.Unmarshal(rawParams, &p)
		idx := -1
		for i, t := range v.trash {
			if t.ID == p.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return errors.New("trash entry not found")
		}
		entry := v.trash[idx]
		dest := entry.OriginalPath
		if p.Dst != "" {
			dest = path.Clean(p.Dst)
		}
		if _, exists := v.nodes[dest]; exists && !p.Overwrite {
			return errors.New("destination already exists")
		}
		v.nodes[dest] = &mockVFSNode{
			isDir:   entry.IsDir,
			modTime: time.Now(),
			mode:    "-rw-r--r--",
		}
		v.trash = append(v.trash[:idx], v.trash[idx+1:]...)
		res := map[string]any{"id": p.ID, "restored_path": dest}
		if out != nil {
			raw, _ := json.Marshal(res)
			return json.Unmarshal(raw, out)
		}
		return nil

	case "app/launch":
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rawParams, &p)
		v.launchedApps[p.ID] = true
		return nil

	case "app/com.gostalgia.notes/open":
		if v.notesOpenErr != nil {
			return v.notesOpenErr
		}
		return nil

	default:
		return fmt.Errorf("mockVFS: unsupported method %s", method)
	}
}

type filesTestHarness struct {
	files    *Files
	vfs      *mockVFS
	handlers map[string]sdk.Handler
	reqSeq   uint64
}

func setupFilesTest(t *testing.T) *filesTestHarness {
	t.Helper()
	vfs := newMockVFS()
	handlers := make(map[string]sdk.Handler)

	inst, err := Factory()
	if err != nil {
		t.Fatal(err)
	}
	f := inst.(*Files)

	call := func(ctx context.Context, method string, params, out any) error {
		return vfs.call(ctx, method, params, out)
	}
	handle := func(name string, h sdk.Handler) error {
		handlers[name] = h
		return nil
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := sdk.NewContext(Manifest(), logger, call, handle)

	if err := f.Init(ctx); err != nil {
		t.Fatal(err)
	}

	return &filesTestHarness{
		files:    f,
		vfs:      vfs,
		handlers: handlers,
	}
}

func (h *filesTestHarness) view(ctx context.Context) (sdk.View, error) {
	vHandler, ok := h.handlers["view"]
	if !ok {
		return sdk.View{}, errors.New("view handler not registered")
	}
	res, err := vHandler(ctx, json.RawMessage(`{"version":1}`))
	if err != nil {
		return sdk.View{}, err
	}
	v := res.(sdk.View)
	if err := v.Validate(); err != nil {
		return v, fmt.Errorf("view validation failed: %w", err)
	}
	return v, nil
}

func (h *filesTestHarness) act(ctx context.Context, action string, itemID string, values map[string]string) (sdk.View, error) {
	currView, err := h.view(ctx)
	if err != nil {
		return sdk.View{}, err
	}
	aHandler, ok := h.handlers["action"]
	if !ok {
		return sdk.View{}, errors.New("action handler not registered")
	}
	seq := atomic.AddUint64(&h.reqSeq, 1)
	req := sdk.ActionRequest{
		Version:   sdk.PresentationVersion,
		Instance:  currView.Instance,
		RequestID: fmt.Sprintf("req_%d", seq),
		Action:    action,
		ItemID:    itemID,
		Values:    values,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return sdk.View{}, err
	}
	res, err := aHandler(ctx, raw)
	if err != nil {
		return sdk.View{}, err
	}
	v := res.(sdk.View)
	if err := v.Validate(); err != nil {
		return v, fmt.Errorf("action view validation failed: %w", err)
	}
	return v, nil
}

func TestManifestValidity(t *testing.T) {
	m := Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	if m.ID != ID {
		t.Errorf("manifest ID = %q, want %q", m.ID, ID)
	}
	if len(ManifestJSON()) == 0 {
		t.Error("ManifestJSON returned empty")
	}
}

func TestInitialBrowsingAndNavigation(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	// 1. Check initial view starts in /users/guest
	v, err := h.view(ctx)
	if err != nil {
		t.Fatalf("view failed: %v", err)
	}
	if !strings.HasPrefix(v.Title, "Files - /users/guest") {
		t.Fatalf("unexpected title %q", v.Title)
	}
	if v.State != sdk.ViewReady {
		t.Fatalf("state = %q, want %q", v.State, sdk.ViewReady)
	}

	// 2. Add some test files
	h.vfs.writeFile("/users/guest/documents/report.txt", []byte("Q3 Report Content"))
	h.vfs.writeFile("/users/guest/notes.txt", []byte("My secret notes"))

	// Refresh and navigate into documents
	v, err = h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Find the item for documents
	var docItemID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "documents") {
			docItemID = it.ID
			break
		}
	}
	if docItemID == "" {
		t.Fatalf("documents directory not found in items: %+v", v.Items)
	}

	// Open documents directory
	v, err = h.act(ctx, "open", docItemID, nil)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	if !strings.HasPrefix(v.Title, "Files - /users/guest/documents") {
		t.Fatalf("title after open = %q, want /users/guest/documents", v.Title)
	}

	// Should have parent directory entry [..]
	if len(v.Items) == 0 || v.Items[0].ID != "item_parent" {
		t.Fatalf("first item should be parent item, got: %+v", v.Items)
	}

	// Navigate up
	v, err = h.act(ctx, "up", "", nil)
	if err != nil {
		t.Fatalf("up action: %v", err)
	}
	if !strings.HasPrefix(v.Title, "Files - /users/guest") {
		t.Fatalf("title after up = %q, want /users/guest", v.Title)
	}
}

func TestSortingAndFiltering(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	h.vfs.writeFile("/users/guest/alpha.txt", []byte("A"))
	h.vfs.writeFile("/users/guest/beta.txt", []byte("BBBBBBBBBB"))
	h.vfs.writeFile("/users/guest/gamma.txt", []byte("CCC"))

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Test sort cycling
	v, err = h.act(ctx, "sort", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "size") {
		t.Fatalf("expected sort by size, got: %s", v.Status)
	}

	// Test live filter
	v, err = h.act(ctx, "refresh", "", map[string]string{"filter": "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	var fileCount int
	for _, it := range v.Items {
		if strings.HasPrefix(it.ID, "item_") && it.ID != "item_parent" {
			fileCount++
			if !strings.Contains(it.Label, "alpha") {
				t.Fatalf("unexpected entry in filtered list: %s", it.Label)
			}
		}
	}
	if fileCount != 1 {
		t.Fatalf("expected 1 item matching filter, got %d", fileCount)
	}

	// Clear filter
	v, err = h.act(ctx, "refresh", "", map[string]string{"filter": ""})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelectionSurvival(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	h.vfs.writeFile("/users/guest/file1.txt", []byte("111"))
	h.vfs.writeFile("/users/guest/file2.txt", []byte("222"))

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Select file2.txt
	var file2ID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "file2.txt") {
			file2ID = it.ID
			break
		}
	}
	if file2ID == "" {
		t.Fatal("file2.txt item not found")
	}

	v, err = h.act(ctx, "preview", file2ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "222") {
		t.Fatalf("status did not show preview: %s", v.Status)
	}

	// Refresh without changing selection
	v, err = h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Selection should survive
	if h.files.selectedName != "file2.txt" {
		t.Fatalf("selectedName = %q, want file2.txt", h.files.selectedName)
	}
}

func TestTextAndBinaryPreview(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	h.vfs.writeFile("/users/guest/hello.txt", []byte("Hello Gostalgia VFS!"))
	h.vfs.writeFile("/users/guest/binary.bin", []byte{0x00, 0xFF, 0xFE, 0x01, 0x02})

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Select hello.txt
	var helloID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "hello.txt") {
			helloID = it.ID
			break
		}
	}
	v, err = h.act(ctx, "preview", helloID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "Hello Gostalgia VFS!") {
		t.Fatalf("expected text preview in status, got: %s", v.Status)
	}

	// Select binary.bin
	var binID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "binary.bin") {
			binID = it.ID
			break
		}
	}
	v, err = h.act(ctx, "preview", binID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "Binary data") {
		t.Fatalf("expected binary indicator in status, got: %s", v.Status)
	}
}

func TestFileOperationsCRUD(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	// 1. Create new file
	v, err := h.act(ctx, "new_file", "", map[string]string{"target": "document.md"})
	if err != nil {
		t.Fatalf("new_file: %v", err)
	}
	if !strings.Contains(v.Status, "Created file document.md") {
		t.Fatalf("status = %s", v.Status)
	}
	if _, exists := h.vfs.nodes["/users/guest/document.md"]; !exists {
		t.Fatal("file was not created in VFS")
	}

	// 2. Create new folder
	v, err = h.act(ctx, "new_dir", "", map[string]string{"target": "my_folder"})
	if err != nil {
		t.Fatalf("new_dir: %v", err)
	}
	if !strings.Contains(v.Status, "Created folder my_folder") {
		t.Fatalf("status = %s", v.Status)
	}
	if node, exists := h.vfs.nodes["/users/guest/my_folder"]; !exists || !node.isDir {
		t.Fatal("folder was not created as directory in VFS")
	}

	// 3. Rename document.md -> renamed.md
	var docID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "document.md") {
			docID = it.ID
			break
		}
	}
	v, err = h.act(ctx, "rename", docID, map[string]string{"target": "renamed.md"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !strings.Contains(v.Status, "Renamed document.md to renamed.md") {
		t.Fatalf("status = %s", v.Status)
	}
	if _, exists := h.vfs.nodes["/users/guest/document.md"]; exists {
		t.Fatal("old file name still exists")
	}
	if _, exists := h.vfs.nodes["/users/guest/renamed.md"]; !exists {
		t.Fatal("renamed file does not exist")
	}

	// 4. Copy renamed.md -> copied.md
	var renID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "renamed.md") {
			renID = it.ID
			break
		}
	}
	v, err = h.act(ctx, "copy", renID, map[string]string{"target": "copied.md"})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if !strings.Contains(v.Status, "Copied renamed.md") {
		t.Fatalf("status = %s", v.Status)
	}
	if _, exists := h.vfs.nodes["/users/guest/copied.md"]; !exists {
		t.Fatal("copied file does not exist")
	}

	// 5. Move copied.md -> moved.md
	var copID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "copied.md") {
			copID = it.ID
			break
		}
	}
	v, err = h.act(ctx, "move", copID, map[string]string{"target": "moved.md"})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if !strings.Contains(v.Status, "Moved copied.md") {
		t.Fatalf("status = %s", v.Status)
	}
	if _, exists := h.vfs.nodes["/users/guest/copied.md"]; exists {
		t.Fatal("moved source still exists")
	}
	if _, exists := h.vfs.nodes["/users/guest/moved.md"]; !exists {
		t.Fatal("moved destination does not exist")
	}
}

func TestConflictDetectionAndResolution(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	h.vfs.writeFile("/users/guest/fileA.txt", []byte("Content A"))
	h.vfs.writeFile("/users/guest/fileB.txt", []byte("Content B"))

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	var itemAID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "fileA.txt") {
			itemAID = it.ID
			break
		}
	}

	// Try renaming fileA to fileB (conflict!)
	v, err = h.act(ctx, "rename", itemAID, map[string]string{"target": "fileB.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Files - Destination Exists" {
		t.Fatalf("expected conflict prompt, got title %q", v.Title)
	}

	// Test Cancel
	v, err = h.act(ctx, "cancel", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.Title, "Files - /users/guest") {
		t.Fatalf("expected return to browsing, got: %q", v.Title)
	}

	// Trigger conflict again and confirm overwrite
	v, err = h.act(ctx, "rename", itemAID, map[string]string{"target": "fileB.txt"})
	if err != nil {
		t.Fatal(err)
	}
	v, err = h.act(ctx, "confirm_overwrite", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "overwritten") {
		t.Fatalf("status = %s", v.Status)
	}
	if _, exists := h.vfs.nodes["/users/guest/fileA.txt"]; exists {
		t.Fatal("fileA still exists after overwrite rename")
	}
}

func TestTrashAndRestoreLifecycle(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	h.vfs.writeFile("/users/guest/to_trash.txt", []byte("trashable data"))

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	var trashID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "to_trash.txt") {
			trashID = it.ID
			break
		}
	}

	// 1. Trigger trash prompt
	v, err = h.act(ctx, "trash", trashID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Files - Confirm Move to Trash" {
		t.Fatalf("expected confirm trash prompt, got title %q", v.Title)
	}

	// 2. Confirm trash
	v, err = h.act(ctx, "confirm_trash", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "Moved to_trash.txt to trash") {
		t.Fatalf("status = %s", v.Status)
	}
	if _, exists := h.vfs.nodes["/users/guest/to_trash.txt"]; exists {
		t.Fatal("file still exists in original path after trash")
	}

	// 3. Switch to Trash Bin view
	v, err = h.act(ctx, "trash_bin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Files - Trash Bin" {
		t.Fatalf("expected trash bin view, got: %q", v.Title)
	}
	if len(v.Items) == 0 || !strings.Contains(v.Items[0].Label, "to_trash.txt") {
		t.Fatalf("expected to_trash.txt in trash items: %+v", v.Items)
	}

	// 4. Restore from trash
	v, err = h.act(ctx, "restore", v.Items[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "Restored") {
		t.Fatalf("status = %s", v.Status)
	}
	if _, exists := h.vfs.nodes["/users/guest/to_trash.txt"]; !exists {
		t.Fatal("file was not restored to original path")
	}

	// 5. Back to Files
	v, err = h.act(ctx, "back", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.Title, "Files - /users/guest") {
		t.Fatalf("expected browsing title, got: %q", v.Title)
	}
}

func TestHandoffToNotes(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	h.vfs.writeFile("/users/guest/notes.txt", []byte("Notes document content"))

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	var notesID string
	for _, it := range v.Items {
		if strings.Contains(it.Label, "notes.txt") {
			notesID = it.ID
			break
		}
	}

	// Trigger open action
	v, err = h.act(ctx, "open", notesID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Status, "Opened notes.txt in Notes") {
		t.Fatalf("status = %s", v.Status)
	}
	if !h.vfs.launchedApps[NotesAppID] {
		t.Fatal("Notes app was not launched")
	}

	// Test Notes open error (e.g. unsaved changes in Notes)
	h.vfs.notesOpenErr = errors.New("document has unsaved changes")
	v, err = h.act(ctx, "open", notesID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Error, "document has unsaved changes") {
		t.Fatalf("expected error banner in view, got: %q", v.Error)
	}
}

func TestLargeDirectoryHandling(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	// Create 120 files
	for i := 0; i < 120; i++ {
		h.vfs.writeFile(fmt.Sprintf("/users/guest/file_%03d.txt", i), []byte("test"))
	}

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Verify items count is bounded to <= 100
	if len(v.Items) > 100 {
		t.Fatalf("len(v.Items) = %d exceeds 100", len(v.Items))
	}

	// Verify truncated indicator is present
	var foundTruncated bool
	for _, it := range v.Items {
		if it.ID == "item_more" {
			foundTruncated = true
			break
		}
	}
	if !foundTruncated {
		t.Fatal("expected item_more truncation indicator")
	}
}

func TestDeniedPathsAndErrors(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	// Simulate permission denied on browse
	h.vfs.listErr = errors.New("permission denied")
	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Error, "permission denied") {
		t.Fatalf("expected error in view, got: %q", v.Error)
	}
	// View should still be valid and interactive
	if err := v.Validate(); err != nil {
		t.Fatalf("view validation failed on error state: %v", err)
	}
}

func TestLongAndUnicodeFilenames(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	unicodeName := "日本語_ドキュメント_🚀.txt"
	longName := strings.Repeat("long_name_", 30) + ".txt"

	h.vfs.writeFile("/users/guest/"+unicodeName, []byte("Unicode content"))
	h.vfs.writeFile("/users/guest/"+longName, []byte("Long name content"))

	v, err := h.act(ctx, "refresh", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("view validation failed with unicode/long names: %v", err)
	}

	var foundUnicode bool
	for _, it := range v.Items {
		if strings.Contains(it.Label, "日本語") {
			foundUnicode = true
			break
		}
	}
	if !foundUnicode {
		t.Fatal("unicode item not found in view")
	}
}

func TestProgrammaticIPCRoutes(t *testing.T) {
	h := setupFilesTest(t)
	ctx := context.Background()

	h.vfs.writeFile("/users/guest/doc.txt", []byte("Content for programmatic test"))

	// 1. browse route
	browseH, ok := h.handlers["browse"]
	if !ok {
		t.Fatal("browse route missing")
	}
	res, err := browseH(ctx, json.RawMessage(`{"path":"/users/guest"}`))
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	bMap := res.(map[string]any)
	if bMap["cwd"] != "/users/guest" {
		t.Fatalf("browse cwd = %v", bMap["cwd"])
	}

	// 2. stat route
	statH, ok := h.handlers["stat"]
	if !ok {
		t.Fatal("stat route missing")
	}
	_, err = statH(ctx, json.RawMessage(`{"path":"/users/guest/doc.txt"}`))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// 3. preview route
	previewH, ok := h.handlers["preview"]
	if !ok {
		t.Fatal("preview route missing")
	}
	res, err = previewH(ctx, json.RawMessage(`{"path":"/users/guest/doc.txt","limit":100}`))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	pMap := res.(map[string]any)
	if !strings.Contains(pMap["preview"].(string), "Content for programmatic test") {
		t.Fatalf("preview content mismatch: %+v", pMap)
	}

	// 4. open route
	openH, ok := h.handlers["open"]
	if !ok {
		t.Fatal("open route missing")
	}
	_, err = openH(ctx, json.RawMessage(`{"path":"/users/guest/doc.txt","app_id":"com.gostalgia.notes"}`))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
}
