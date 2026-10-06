package document

import (
	"path"
	"sort"
	"strings"
	"sync"

	"gostalgia/sdk"
)

// AssociationTable tracks mappings from document types and file extensions
// to applications capable of opening them.
type AssociationTable struct {
	mu       sync.RWMutex
	byExt    map[string][]sdk.DocumentTypeAssociation
	defaults map[string]string // ext -> default appID
}

// NewAssociationTable creates a table pre-populated with standard default associations.
func NewAssociationTable() *AssociationTable {
	t := &AssociationTable{
		byExt:    make(map[string][]sdk.DocumentTypeAssociation),
		defaults: make(map[string]string),
	}
	t.seedBuiltins()
	return t
}

func (t *AssociationTable) seedBuiltins() {
	builtins := []sdk.DocumentTypeAssociation{
		{Extension: ".txt", MimeType: "text/plain", AppID: "com.gostalgia.notes", Name: "Text Document", Default: true},
		{Extension: ".md", MimeType: "text/markdown", AppID: "com.gostalgia.notes", Name: "Markdown Document", Default: true},
		{Extension: ".log", MimeType: "text/plain", AppID: "com.gostalgia.notes", Name: "Log File", Default: true},
		{Extension: ".json", MimeType: "application/json", AppID: "com.gostalgia.notes", Name: "JSON Document", Default: true},
		{Extension: ".csv", MimeType: "text/csv", AppID: "com.gostalgia.notes", Name: "CSV Document", Default: true},
		{Extension: ".text", MimeType: "text/plain", AppID: "com.gostalgia.notes", Name: "Plain Text Document", Default: true},
		{Extension: "directory", MimeType: "inode/directory", AppID: "com.gostalgia.files", Name: "Folder", Default: true},
	}
	for _, b := range builtins {
		_ = t.Register(b)
	}
}

// normalizeExt cleans and formats an extension (e.g. "TXT" -> ".txt").
func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if ext == "directory" || ext == "dir" || ext == "folder" {
		return "directory"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// Register adds or updates an association in the table.
func (t *AssociationTable) Register(assoc sdk.DocumentTypeAssociation) error {
	ext := normalizeExt(assoc.Extension)
	if ext == "" {
		return nil
	}
	appID := strings.TrimSpace(assoc.AppID)
	if appID == "" {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cleaned := sdk.DocumentTypeAssociation{
		Extension: ext,
		MimeType:  strings.TrimSpace(assoc.MimeType),
		AppID:     appID,
		Name:      strings.TrimSpace(assoc.Name),
		Default:   assoc.Default,
	}

	existing := t.byExt[ext]
	found := false
	for i, cur := range existing {
		if cur.AppID == appID {
			existing[i] = cleaned
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, cleaned)
	}
	t.byExt[ext] = existing

	if assoc.Default || t.defaults[ext] == "" {
		t.defaults[ext] = appID
		// Clear default flag on others
		for i := range t.byExt[ext] {
			t.byExt[ext][i].Default = (t.byExt[ext][i].AppID == appID)
		}
	}

	return nil
}

// RegisterFromManifest registers associations declared in an application manifest.
func (t *AssociationTable) RegisterFromManifest(m sdk.Manifest) {
	for _, ext := range m.DocumentTypes {
		_ = t.Register(sdk.DocumentTypeAssociation{
			Extension: ext,
			AppID:     m.ID,
			Name:      m.Name,
			Default:   false,
		})
	}
}

// Resolve identifies the default application and all registered handlers for a path or extension.
func (t *AssociationTable) Resolve(pathOrExt string) (defaultApp string, handlers []sdk.DocumentTypeAssociation, found bool) {
	clean := strings.TrimSpace(pathOrExt)
	var ext string
	if strings.Contains(clean, "/") || strings.Contains(clean, "\\") {
		ext = path.Ext(clean)
	} else if strings.HasPrefix(clean, ".") {
		ext = clean
	} else if clean == "directory" || clean == "dir" || clean == "folder" {
		ext = "directory"
	} else {
		ext = path.Ext(clean)
		if ext == "" {
			ext = "." + clean
		}
	}
	ext = normalizeExt(ext)
	if ext == "" {
		// Treat extensionless files with generic text default
		ext = ".txt"
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	list, ok := t.byExt[ext]
	if !ok || len(list) == 0 {
		return "", nil, false
	}

	out := make([]sdk.DocumentTypeAssociation, len(list))
	copy(out, list)

	def := t.defaults[ext]
	if def == "" && len(out) > 0 {
		def = out[0].AppID
	}

	return def, out, true
}

// List returns a sorted snapshot of all registered associations.
func (t *AssociationTable) List() []sdk.DocumentTypeAssociation {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var out []sdk.DocumentTypeAssociation
	for _, list := range t.byExt {
		out = append(out, list...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Extension != out[j].Extension {
			return out[i].Extension < out[j].Extension
		}
		return out[i].AppID < out[j].AppID
	})
	return out
}
