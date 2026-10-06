package recovery

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"gostalgia/internal/vfs"
)

// ExportOptions controls what data is gathered during backup creation.
type ExportOptions struct {
	ProfileID      string   // If set, only exports this profile; otherwise exports all profiles
	IncludeSystem  bool     // Whether to include /config/system.json (default true)
	ExcludeSecrets []string // Secret strings (e.g. operator tokens) that must not appear in any exported file
	SourceVersion  string   // Environment version string
	Description    string   // Optional user-supplied backup note
}

// ExportResult contains summary metrics of a completed export.
type ExportResult struct {
	TotalFiles  int       `json:"total_files"`
	TotalBytes  int64     `json:"total_bytes"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	Profiles    []string  `json:"profiles"`
	ArchivePath string    `json:"archive_path,omitempty"`
}

// sanitizeWorkspace removes or redacts sensitive credentials from workspace.json content.
func sanitizeWorkspace(raw []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	// Redact or wipe command history to prevent secret leakage
	if hist, ok := m["history"].([]any); ok {
		cleanHist := make([]any, 0, len(hist))
		for _, item := range hist {
			if s, ok := item.(string); ok {
				lower := strings.ToLower(s)
				if strings.HasPrefix(lower, "auth") || strings.HasPrefix(lower, "token") || strings.HasPrefix(lower, "login") {
					continue
				}
				cleanHist = append(cleanHist, s)
			}
		}
		m["history"] = cleanHist
	}
	cleaned, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return raw
	}
	return cleaned
}

// Export gathers personal and system state from vfs and writes a verified .gbar ZIP archive to out.
func Export(ctx context.Context, vfsInstance vfs.FS, out io.Writer, opts ExportOptions) (*ExportResult, error) {
	if vfsInstance == nil {
		return nil, errors.New("recovery: VFS is required for export")
	}

	createdAt := time.Now().UTC()
	manifest := Manifest{
		FormatVersion: FormatVersion,
		CreatedAt:     createdAt,
		SourceVersion: opts.SourceVersion,
		Description:   opts.Description,
		Profiles:      []ProfileSummary{},
		Files:         []FileEntry{},
	}

	// Buffer payload files in memory during assembly to verify bounds before writing output.
	type payloadFile struct {
		entry   FileEntry
		content []byte
	}
	var payloads []payloadFile
	var totalBytes int64

	addFile := func(vfsPath string, content []byte, modTime time.Time) error {
		if !IsAllowedVFSPath(vfsPath) {
			return nil // silently skip non-portable or forbidden paths
		}

		// Security scan: ensure no live credentials or configured secrets are present in content
		for _, secret := range opts.ExcludeSecrets {
			if secret != "" && bytes.Contains(content, []byte(secret)) {
				return fmt.Errorf("recovery: refusal: file %q contains sensitive credential or secret", vfsPath)
			}
		}

		cleanVFS := path.Clean(vfsPath)
		arcName := DataDirPrefix + strings.TrimPrefix(cleanVFS, "/")
		if !PortablePath(arcName) {
			return fmt.Errorf("recovery: nonportable archive path generated for %q", vfsPath)
		}

		size := int64(len(content))
		if size > MaxSingleFileBytes {
			return fmt.Errorf("recovery: file %q exceeds single file limit of %d bytes", vfsPath, MaxSingleFileBytes)
		}
		if totalBytes+size > MaxExtractBytes {
			return fmt.Errorf("recovery: total export size exceeds limit of %d bytes", MaxExtractBytes)
		}
		totalBytes += size

		entry := FileEntry{
			VFSPath:  cleanVFS,
			ArcName:  arcName,
			Size:     size,
			SHA256:   ComputeSHA256(content),
			Modified: modTime.UTC().Format(time.RFC3339),
		}
		payloads = append(payloads, payloadFile{entry: entry, content: content})
		return nil
	}

	// 1. Export profiles metadata if present
	profilesRaw, err := vfsInstance.ReadFile("/config/profiles.json")
	if err == nil {
		if err := addFile("/config/profiles.json", profilesRaw, time.Now()); err != nil {
			return nil, err
		}
		var profRegistry struct {
			Profiles []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"profiles"`
		}
		if err := json.Unmarshal(profilesRaw, &profRegistry); err == nil {
			for _, p := range profRegistry.Profiles {
				if opts.ProfileID == "" || opts.ProfileID == p.ID {
					manifest.Profiles = append(manifest.Profiles, ProfileSummary{ID: p.ID, Name: p.Name})
				}
			}
		}
	}

	// 2. Export system configuration if requested
	if opts.IncludeSystem {
		systemCfg, err := vfsInstance.ReadFile("/config/system.json")
		if err == nil {
			if err := addFile("/config/system.json", systemCfg, time.Now()); err != nil {
				return nil, err
			}
		}
	}

	// Determine which profile directories to scan
	targetProfiles := make(map[string]bool)
	if opts.ProfileID != "" {
		targetProfiles[opts.ProfileID] = true
	} else if len(manifest.Profiles) > 0 {
		for _, p := range manifest.Profiles {
			targetProfiles[p.ID] = true
		}
	} else {
		// Fallback: discover user directories directly under /users
		entries, err := vfsInstance.ReadDir("/users")
		if err == nil {
			for _, ent := range entries {
				if ent.IsDir() && !strings.HasPrefix(ent.Name(), ".") {
					targetProfiles[ent.Name()] = true
					manifest.Profiles = append(manifest.Profiles, ProfileSummary{ID: ent.Name(), Name: ent.Name()})
				}
			}
		}
	}

	// Helper to recursively walk an approved directory
	var walkDir func(dirPath string) error
	walkDir = func(dirPath string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		entries, err := vfsInstance.ReadDir(dirPath)
		if err != nil {
			return nil // directory may not exist yet, safe to skip
		}
		for _, ent := range entries {
			childPath := path.Join(dirPath, ent.Name())
			if ent.IsDir() {
				// Skip trash, temporary, or hidden folders
				if ent.Name() == ".trash" || strings.HasPrefix(ent.Name(), ".tmp") {
					continue
				}
				if err := walkDir(childPath); err != nil {
					return err
				}
			} else {
				// Regular file
				lowerName := strings.ToLower(ent.Name())
				if strings.Contains(ent.Name(), ".tmp.") || strings.HasSuffix(ent.Name(), ".recover") ||
					strings.HasSuffix(lowerName, FileExtension) || strings.HasSuffix(lowerName, ".zip") {
					continue
				}
				content, err := vfsInstance.ReadFile(childPath)
				if err != nil {
					continue
				}

				info, _ := vfsInstance.Stat(childPath)
				modTime := time.Now()
				if info != nil {
					modTime = info.ModTime()
				}

				// If it's a workspace.json file, sanitize it
				if ent.Name() == "workspace.json" {
					content = sanitizeWorkspace(content)
				}

				if err := addFile(childPath, content, modTime); err != nil {
					return err
				}
			}
		}
		return nil
	}

	// 3. Scan user directories
	for profID := range targetProfiles {
		userRoot := path.Join("/users", profID)
		subdirs := []string{"documents", "downloads", "desktop", "config"}
		for _, sub := range subdirs {
			if err := walkDir(path.Join(userRoot, sub)); err != nil {
				return nil, err
			}
		}
	}

	if len(payloads) > MaxFiles {
		return nil, fmt.Errorf("recovery: total file count %d exceeds ceiling of %d", len(payloads), MaxFiles)
	}

	// Sort payloads deterministically by ArcName
	sort.Slice(payloads, func(i, j int) bool {
		return payloads[i].entry.ArcName < payloads[j].entry.ArcName
	})

	for _, p := range payloads {
		manifest.Files = append(manifest.Files, p.entry)
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("recovery: marshal manifest: %w", err)
	}

	// Write ZIP archive
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	// Add backup.json
	mh := &zip.FileHeader{
		Name:     ManifestFilename,
		Method:   zip.Deflate,
		Modified: createdAt,
	}
	mw, err := zw.CreateHeader(mh)
	if err != nil {
		return nil, fmt.Errorf("recovery: write manifest header: %w", err)
	}
	if _, err := mw.Write(manifestBytes); err != nil {
		return nil, fmt.Errorf("recovery: write manifest content: %w", err)
	}

	// Add data files
	for _, p := range payloads {
		fh := &zip.FileHeader{
			Name:     p.entry.ArcName,
			Method:   zip.Deflate,
			Modified: createdAt,
		}
		fw, err := zw.CreateHeader(fh)
		if err != nil {
			return nil, fmt.Errorf("recovery: write entry header %q: %w", p.entry.ArcName, err)
		}
		if _, err := fw.Write(p.content); err != nil {
			return nil, fmt.Errorf("recovery: write entry content %q: %w", p.entry.ArcName, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("recovery: close archive writer: %w", err)
	}

	archiveBytes := buf.Bytes()
	if len(archiveBytes) > MaxArchiveBytes {
		return nil, fmt.Errorf("recovery: compressed archive size %d exceeds limit of %d", len(archiveBytes), MaxArchiveBytes)
	}

	if _, err := out.Write(archiveBytes); err != nil {
		return nil, fmt.Errorf("recovery: write archive output: %w", err)
	}

	profileNames := make([]string, 0, len(manifest.Profiles))
	for _, p := range manifest.Profiles {
		profileNames = append(profileNames, p.ID)
	}

	return &ExportResult{
		TotalFiles: len(manifest.Files),
		TotalBytes: totalBytes,
		SHA256:     ComputeSHA256(archiveBytes),
		CreatedAt:  createdAt,
		Profiles:   profileNames,
	}, nil
}

// ExportToVFS exports backup data atomically to a target file path on the VFS.
func ExportToVFS(ctx context.Context, vfsInstance vfs.FS, destPath string, opts ExportOptions) (*ExportResult, error) {
	destClean := path.Clean(destPath)
	if !strings.HasPrefix(destClean, "/") {
		destClean = "/" + destClean
	}
	if !strings.HasSuffix(strings.ToLower(destClean), FileExtension) && !strings.HasSuffix(strings.ToLower(destClean), ".zip") {
		destClean += FileExtension
	}

	// Ensure parent directory exists
	parent := path.Dir(destClean)
	if err := vfsInstance.MkdirAll(parent); err != nil {
		return nil, fmt.Errorf("recovery: create destination directory %q: %w", parent, err)
	}

	buf := new(bytes.Buffer)
	res, err := Export(ctx, vfsInstance, buf, opts)
	if err != nil {
		return nil, err
	}

	if err := vfsInstance.SaveAtomic(destClean, buf.Bytes(), 0o644); err != nil {
		return nil, fmt.Errorf("recovery: save archive: %w", err)
	}

	res.ArchivePath = destClean
	return res, nil
}
