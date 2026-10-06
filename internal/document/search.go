package document

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"gostalgia/internal/security"
	"gostalgia/internal/vfs"
	"gostalgia/sdk"
)

// Search limits and traversal bounds to prevent resource exhaustion.
const (
	MaxSearchEntries   = 5000
	DefaultSearchLimit = 50
	MaxSearchLimit     = 500
	DefaultSearchDepth = 10
	MaxSearchDepth     = 20
)

// IndexEntry holds cached metadata for an indexed document.
type IndexEntry struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	IsDir     bool      `json:"is_dir"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Extension string    `json:"extension"`
	IndexedAt time.Time `json:"indexed_at"`
}

// Searcher provides permission-aware bounded VFS search and indexed document lookups.
type Searcher struct {
	vfs       vfs.DocumentFS
	grants    *vfs.GrantStore
	recents   *RecentsStore
	favorites *FavoritesStore

	indexMu sync.RWMutex
	index   map[string]*IndexEntry
}

// NewSearcher creates a new Searcher.
func NewSearcher(dfs vfs.DocumentFS, grants *vfs.GrantStore, recents *RecentsStore, favorites *FavoritesStore) *Searcher {
	return &Searcher{
		vfs:       dfs,
		grants:    grants,
		recents:   recents,
		favorites: favorites,
		index:     make(map[string]*IndexEntry),
	}
}

// IndexDocument indexes a single document path into the in-memory lookup index.
func (s *Searcher) IndexDocument(envPath string) error {
	if s.vfs == nil {
		return nil
	}
	norm, err := vfs.Normalize(envPath)
	if err != nil {
		return err
	}
	clean := "/" + norm

	fi, err := s.vfs.Stat(clean)
	if err != nil {
		return err
	}

	entry := &IndexEntry{
		Path:      clean,
		Name:      fi.Name(),
		IsDir:     fi.IsDir(),
		Size:      fi.Size(),
		ModTime:   fi.ModTime().UTC(),
		Extension: strings.ToLower(path.Ext(fi.Name())),
		IndexedAt: time.Now().UTC(),
	}

	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	s.index[clean] = entry
	return nil
}

// RemoveFromIndex removes a document path from the lookup index.
func (s *Searcher) RemoveFromIndex(envPath string) {
	norm, err := vfs.Normalize(envPath)
	if err != nil {
		return
	}
	clean := "/" + norm

	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	delete(s.index, clean)
}

// RebuildIndex scans standard document locations and indexes existing files.
func (s *Searcher) RebuildIndex(ctx context.Context) error {
	if s.vfs == nil {
		return nil
	}

	roots := []string{"/users/guest/documents", "/users/guest/desktop", "/users/guest/downloads"}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = s.scanAndIndex(root, 0, 5)
	}

	// Also index items from recents and favorites
	if s.recents != nil {
		for _, r := range s.recents.List("", false) {
			_ = s.IndexDocument(r.Path)
		}
	}
	if s.favorites != nil {
		for _, f := range s.favorites.List("", false) {
			_ = s.IndexDocument(f.Path)
		}
	}

	return nil
}

func (s *Searcher) scanAndIndex(dirPath string, depth, maxDepth int) error {
	if depth > maxDepth {
		return nil
	}
	entries, err := s.vfs.ReadDir(dirPath)
	if err != nil {
		return err
	}

	for _, e := range entries {
		childPath := path.Clean(path.Join(dirPath, e.Name()))
		if e.IsDir() {
			if e.Name() != ".trash" {
				_ = s.scanAndIndex(childPath, depth+1, maxDepth)
			}
		} else {
			_ = s.IndexDocument(childPath)
		}
	}
	return nil
}

// Search performs a permission-aware bounded traversal search of the VFS.
func (s *Searcher) Search(ctx context.Context, caller security.Principal, req sdk.DocumentSearchQuery) (*sdk.DocumentSearchResponse, error) {
	if s.vfs == nil {
		return &sdk.DocumentSearchResponse{Query: req.Query}, nil
	}

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	} else if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	maxDepth := req.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultSearchDepth
	} else if maxDepth > MaxSearchDepth {
		maxDepth = MaxSearchDepth
	}

	reqExt := normalizeExt(req.Extension)
	queryLower := strings.ToLower(strings.TrimSpace(req.Query))

	// Determine starting search roots based on caller identity and permissions
	var roots []string
	if strings.TrimSpace(req.Path) != "" {
		norm, err := vfs.Normalize(req.Path)
		if err != nil {
			return nil, &vfs.Error{Op: "search", Path: req.Path, Code: vfs.ErrInvalid, Message: fmt.Sprintf("invalid path: %v", err)}
		}
		cleanPath := "/" + norm

		if caller.IsApp() {
			if err := s.grants.CheckAccess(caller.AppID, cleanPath, vfs.AccessRead); err != nil {
				return nil, &vfs.Error{
					Op:      "search",
					Path:    cleanPath,
					Code:    vfs.ErrPermission,
					Message: fmt.Sprintf("permission denied: application %q not authorized to search %s", caller.AppID, cleanPath),
				}
			}
		}
		roots = []string{cleanPath}
	} else {
		if caller.IsApp() {
			// Find all directories granted to this app + app-private directory
			appID := caller.AppID
			privDir := vfs.AppDataDir(appID)
			if _, err := s.vfs.Stat(privDir); err == nil {
				roots = append(roots, privDir)
			}
			if s.grants != nil {
				for _, g := range s.grants.List(appID) {
					if !g.Revoked {
						roots = append(roots, g.Path)
					}
				}
			}
			if len(roots) == 0 {
				return &sdk.DocumentSearchResponse{Query: req.Query}, nil
			}
		} else {
			roots = []string{"/users/guest"}
		}
	}

	var results []sdk.DocumentSearchResult
	visitedCount := 0
	truncated := false
	seenPaths := make(map[string]bool)

	for _, root := range roots {
		if len(results) >= limit || visitedCount >= MaxSearchEntries {
			truncated = true
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		s.traverse(ctx, caller, root, 0, maxDepth, queryLower, reqExt, req.IncludeDirs, limit, &visitedCount, &results, seenPaths, &truncated)
	}

	return &sdk.DocumentSearchResponse{
		Query:     req.Query,
		Results:   results,
		Total:     len(results),
		Truncated: truncated,
	}, nil
}

func (s *Searcher) traverse(
	ctx context.Context,
	caller security.Principal,
	currPath string,
	depth int,
	maxDepth int,
	queryLower string,
	reqExt string,
	includeDirs bool,
	limit int,
	visitedCount *int,
	results *[]sdk.DocumentSearchResult,
	seenPaths map[string]bool,
	truncated *bool,
) {
	if *visitedCount >= MaxSearchEntries || len(*results) >= limit {
		*truncated = true
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}

	// Permission check on currPath if caller is an app
	if caller.IsApp() && s.grants != nil {
		if err := s.grants.CheckAccess(caller.AppID, currPath, vfs.AccessRead); err != nil {
			return // skip ungranted paths
		}
	}

	fi, err := s.vfs.Stat(currPath)
	if err != nil {
		return
	}

	*visitedCount++

	if !fi.IsDir() {
		// Single file match check
		if !seenPaths[currPath] && s.matches(fi.Name(), currPath, queryLower, reqExt) {
			seenPaths[currPath] = true
			*results = append(*results, sdk.DocumentSearchResult{
				Path:      currPath,
				Name:      fi.Name(),
				IsDir:     false,
				Size:      fi.Size(),
				ModTime:   fi.ModTime().UTC().Format(time.RFC3339),
				Extension: strings.ToLower(path.Ext(fi.Name())),
			})
		}
		return
	}

	// If directory and includeDirs is true, check if dir matches
	if includeDirs && depth > 0 && !seenPaths[currPath] && s.matches(fi.Name(), currPath, queryLower, "") {
		seenPaths[currPath] = true
		*results = append(*results, sdk.DocumentSearchResult{
			Path:    currPath,
			Name:    fi.Name(),
			IsDir:   true,
			Size:    fi.Size(),
			ModTime: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}

	if depth >= maxDepth {
		return
	}

	entries, err := s.vfs.ReadDir(currPath)
	if err != nil {
		return
	}

	for _, e := range entries {
		if *visitedCount >= MaxSearchEntries || len(*results) >= limit {
			*truncated = true
			return
		}
		if err := ctx.Err(); err != nil {
			return
		}

		childName := e.Name()
		// Skip trash unless currPath is within trash
		if childName == ".trash" && !strings.Contains(currPath, ".trash") {
			continue
		}

		childPath := path.Clean(path.Join(currPath, childName))

		// Cross-app isolation: never traverse another app's private storage
		if isPriv, owner := vfs.ParseAppPrivatePath(childPath); isPriv {
			if !caller.IsOperator() && (!caller.IsApp() || owner != caller.AppID) {
				continue
			}
		}

		if e.IsDir() {
			s.traverse(ctx, caller, childPath, depth+1, maxDepth, queryLower, reqExt, includeDirs, limit, visitedCount, results, seenPaths, truncated)
		} else {
			*visitedCount++
			if !seenPaths[childPath] && s.matches(childName, childPath, queryLower, reqExt) {
				if caller.IsApp() && s.grants != nil {
					if err := s.grants.CheckAccess(caller.AppID, childPath, vfs.AccessRead); err != nil {
						continue
					}
				}

				seenPaths[childPath] = true
				var size int64
				var modTime string
				if fi, err := e.Info(); err == nil {
					size = fi.Size()
					modTime = fi.ModTime().UTC().Format(time.RFC3339)
				}
				*results = append(*results, sdk.DocumentSearchResult{
					Path:      childPath,
					Name:      childName,
					IsDir:     false,
					Size:      size,
					ModTime:   modTime,
					Extension: strings.ToLower(path.Ext(childName)),
				})
			}
		}
	}
}

func (s *Searcher) matches(name, fullPath, queryLower, reqExt string) bool {
	if reqExt != "" {
		ext := strings.ToLower(path.Ext(name))
		if ext != reqExt {
			return false
		}
	}
	if queryLower == "" {
		return true
	}
	nameLower := strings.ToLower(name)
	pathLower := strings.ToLower(fullPath)
	return strings.Contains(nameLower, queryLower) || strings.Contains(pathLower, queryLower)
}

// Lookup queries the document index for matching documents respecting caller permissions
// and actively verifies whether indexed files still exist (pruning stale paths).
func (s *Searcher) Lookup(ctx context.Context, caller security.Principal, query string, extension string, limit int) (*sdk.DocumentSearchResponse, error) {
	if limit <= 0 {
		limit = DefaultSearchLimit
	} else if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	reqExt := normalizeExt(extension)

	s.indexMu.RLock()
	var candidates []*IndexEntry
	for _, entry := range s.index {
		candidates = append(candidates, entry)
	}
	s.indexMu.RUnlock()

	var results []sdk.DocumentSearchResult
	var stalePaths []string

	for _, entry := range candidates {
		if len(results) >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if !s.matches(entry.Name, entry.Path, queryLower, reqExt) {
			continue
		}

		// Permission check: if caller is an app, verify read access
		if caller.IsApp() && s.grants != nil {
			if err := s.grants.CheckAccess(caller.AppID, entry.Path, vfs.AccessRead); err != nil {
				continue // Caller not authorized to see this document
			}
		}

		// Stale path check: verify file still exists on VFS
		if s.vfs != nil {
			fi, err := s.vfs.Stat(entry.Path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					stalePaths = append(stalePaths, entry.Path)
				}
				continue // Skip stale or missing file
			}
			entry.Size = fi.Size()
			entry.ModTime = fi.ModTime().UTC()
		}

		results = append(results, sdk.DocumentSearchResult{
			Path:      entry.Path,
			Name:      entry.Name,
			IsDir:     entry.IsDir,
			Size:      entry.Size,
			ModTime:   entry.ModTime.Format(time.RFC3339),
			Extension: entry.Extension,
		})
	}

	// Prune stale paths from index
	if len(stalePaths) > 0 {
		s.indexMu.Lock()
		for _, sp := range stalePaths {
			delete(s.index, sp)
		}
		s.indexMu.Unlock()
	}

	return &sdk.DocumentSearchResponse{
		Query:     query,
		Results:   results,
		Total:     len(results),
		Truncated: len(results) >= limit,
	}, nil
}
