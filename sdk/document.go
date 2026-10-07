package sdk

import (
	"fmt"
	"strings"
	"time"
)

// DocumentHandoffVersion is the current version of the open-with handoff protocol.
const DocumentHandoffVersion = 1

// HandoffRequest specifies the parameters for an open-with handoff operation.
type HandoffRequest struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
	AppID   string `json:"app_id,omitempty"`
	Mode    string `json:"mode,omitempty"` // "read" or "read-write", default "read"
}

// Validate checks the handoff request parameters.
func (r HandoffRequest) Validate() error {
	if r.Version != DocumentHandoffVersion {
		return fmt.Errorf("handoff: unsupported contract version %d (want %d)", r.Version, DocumentHandoffVersion)
	}
	if strings.TrimSpace(r.Path) == "" {
		return fmt.Errorf("handoff: path is required")
	}
	switch strings.ToLower(strings.TrimSpace(r.Mode)) {
	case "", "read", "ro", "r", "read-write", "readwrite", "rw", "w", "write":
		// valid modes
	default:
		return fmt.Errorf("handoff: invalid mode %q (must be 'read' or 'read-write')", r.Mode)
	}
	return nil
}

// HandoffResult reports the outcome of a document handoff.
type HandoffResult struct {
	Version  int    `json:"version"`
	Path     string `json:"path"`
	AppID    string `json:"app_id"`
	GrantID  string `json:"grant_id,omitempty"`
	Mode     string `json:"mode"`
	Launched bool   `json:"launched"`
	Success  bool   `json:"success"`
	Message  string `json:"message,omitempty"`
}

// DocumentTypeAssociation maps a file extension or MIME type to an application ID.
type DocumentTypeAssociation struct {
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type,omitempty"`
	AppID     string `json:"app_id"`
	Name      string `json:"name,omitempty"`
	Default   bool   `json:"default"`
}

// RecentDocument records a recently opened document in the user's private state.
type RecentDocument struct {
	Path       string    `json:"path"`
	AppID      string    `json:"app_id,omitempty"`
	AccessedAt time.Time `json:"accessed_at"`
	Exists     bool      `json:"exists"`
	Size       int64     `json:"size"`
	ModTime    string    `json:"mod_time,omitempty"`
}

// FavoriteDocument records a pinned/favorite document in the user's private state.
type FavoriteDocument struct {
	Path    string    `json:"path"`
	Label   string    `json:"label,omitempty"`
	AddedAt time.Time `json:"added_at"`
	Rank    int       `json:"rank"`
	Exists  bool      `json:"exists"`
	Size    int64     `json:"size"`
	ModTime string    `json:"mod_time,omitempty"`
}

// DocumentSearchQuery parameters for bounded VFS and indexed document lookups.
type DocumentSearchQuery struct {
	Query       string `json:"query"`
	Path        string `json:"path,omitempty"`
	Extension   string `json:"extension,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	MaxDepth    int    `json:"max_depth,omitempty"`
	IncludeDirs bool   `json:"include_dirs,omitempty"`
}

// DocumentSearchResult represents a single match from a document search.
type DocumentSearchResult struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	ModTime   string `json:"mod_time"`
	Extension string `json:"extension,omitempty"`
}

// DocumentSearchResponse wraps the results and bounded metadata of a document search.
type DocumentSearchResponse struct {
	Query     string                 `json:"query"`
	Results   []DocumentSearchResult `json:"results"`
	Total     int                    `json:"total"`
	Truncated bool                   `json:"truncated"`
}
