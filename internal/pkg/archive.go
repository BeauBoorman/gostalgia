// Package pkg implements bounded package verification and atomic application
// distribution. Publisher signatures establish origin, not runtime safety.
package pkg

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gostalgia/sdk"
)

const (
	FormatVersion       = 1
	MaxArchiveBytes     = 50 << 20
	MaxExtractBytes     = 50 << 20
	MaxFiles            = 500
	MaxCompressionRatio = 100
	maxJSONBytes        = 64 << 10
)

// Metadata is package.json inside a ZIP-based .gpkg. Checksums cover every
// regular file except package.json. Signatures cover SigningBytes().
type Metadata struct {
	FormatVersion int               `json:"format_version"`
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Publisher     string            `json:"publisher,omitempty"`
	KeyID         string            `json:"key_id,omitempty"`
	Checksums     map[string]string `json:"checksums"`
	Signature     string            `json:"signature,omitempty"`
}

func (m Metadata) SigningBytes() ([]byte, error) {
	m.Signature = ""
	return json.Marshal(m)
}

type TrustStore map[string]ed25519.PublicKey

type Provenance struct {
	Signed    bool   `json:"signed"`
	Verified  bool   `json:"verified"`
	Publisher string `json:"publisher,omitempty"`
	KeyID     string `json:"key_id,omitempty"`
}

type verified struct {
	Manifest   sdk.Manifest
	Metadata   Metadata
	Provenance Provenance
	files      map[string][]byte
}

// portablePath excludes platform-dependent aliases as well as traversal.
func portablePath(name string) bool {
	if !fs.ValidPath(name) || name == "." || len(name) > 1024 ||
		strings.ContainsAny(name, "\\\x00:") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, ". ") != part {
			return false
		}
		for _, r := range part {
			if r < 32 || strings.ContainsRune(`<>?"*|`, r) {
				return false
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
			return false
		}
	}
	return true
}

func decodeJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON object")
	}
	return nil
}

func verify(r io.Reader, trust TrustStore) (*verified, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxArchiveBytes {
		return nil, fmt.Errorf("package: compressed archive exceeds budget")
	}
	if err := checkDirectory(data); err != nil {
		return nil, err
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("package: malformed ZIP: %w", err)
	}
	if len(z.File) > MaxFiles {
		return nil, fmt.Errorf("package: too many archive entries")
	}
	v := &verified{files: make(map[string][]byte)}
	seen := make(map[string]bool)
	tree := make(map[string]string)
	var total uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !portablePath(name) || seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("package: unsafe or duplicate archive path %q", f.Name)
		}
		seen[strings.ToLower(name)] = true
		for p := name; p != "."; p = path.Dir(p) {
			key := strings.ToLower(p)
			if old, exists := tree[key]; exists && old != p {
				return nil, fmt.Errorf("package: case-alias archive path %q", f.Name)
			}
			tree[key] = p
		}
		if len(tree) > MaxFiles {
			return nil, fmt.Errorf("package: too many extracted paths")
		}
		mode := f.Mode()
		if mode.Type() == fs.ModeDir {
			if !strings.HasSuffix(f.Name, "/") || f.UncompressedSize64 != 0 {
				return nil, fmt.Errorf("package: malformed directory %q", f.Name)
			}
			continue
		}
		if !mode.IsRegular() || strings.HasSuffix(f.Name, "/") {
			return nil, fmt.Errorf("package: links and special files are forbidden: %q", f.Name)
		}
		size := f.UncompressedSize64
		if f.CompressedSize64 > uint64(len(data)) || size > MaxExtractBytes || total > MaxExtractBytes-size ||
			(size > 0 && (f.CompressedSize64 == 0 || size > f.CompressedSize64*MaxCompressionRatio)) {
			return nil, fmt.Errorf("package: extraction budget exceeded")
		}
		total += size
		if (name == "package.json" || name == "manifest.json") && size > maxJSONBytes {
			return nil, fmt.Errorf("package: JSON metadata exceeds budget")
		}
		rd, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("package: open entry: %w", err)
		}
		b, readErr := io.ReadAll(io.LimitReader(rd, int64(size)+1))
		closeErr := rd.Close()
		if readErr != nil || closeErr != nil || uint64(len(b)) != size {
			return nil, fmt.Errorf("package: corrupt entry %q", name)
		}
		v.files[name] = b
	}
	return validate(v, trust)
}

// Bound central-directory parsing before archive/zip allocates entry objects.
// Format v1 intentionally excludes ZIP64, multi-disk and self-extracting ZIPs.
func checkDirectory(data []byte) error {
	end := -1
	for i := len(data) - 22; i >= 0 && i >= len(data)-22-65535; i-- {
		if binary.LittleEndian.Uint32(data[i:i+4]) == 0x06054b50 &&
			i+22+int(binary.LittleEndian.Uint16(data[i+20:i+22])) == len(data) {
			end = i
			break
		}
	}
	if end < 0 {
		return fmt.Errorf("package: missing ZIP end record")
	}
	if len(data) < 4 || binary.LittleEndian.Uint32(data[:4]) != 0x04034b50 {
		return fmt.Errorf("package: invalid ZIP local header")
	}
	h := data[end:]
	count := int(binary.LittleEndian.Uint16(h[10:12]))
	if count > MaxFiles || count < 2 || binary.LittleEndian.Uint16(h[8:10]) != uint16(count) ||
		binary.LittleEndian.Uint32(h[4:8]) != 0 {
		return fmt.Errorf("package: invalid or oversized ZIP directory")
	}
	start := uint64(binary.LittleEndian.Uint32(h[16:20]))
	size := uint64(binary.LittleEndian.Uint32(h[12:16]))
	if start+size != uint64(end) {
		return fmt.Errorf("package: invalid ZIP directory extent")
	}
	directory := data[int(start):end]
	for n := 0; len(directory) > 0; n++ {
		if n >= count || len(directory) < 46 || binary.LittleEndian.Uint32(directory[:4]) != 0x02014b50 {
			return fmt.Errorf("package: corrupt ZIP directory")
		}
		if binary.LittleEndian.Uint16(directory[34:36]) != 0 ||
			binary.LittleEndian.Uint16(directory[8:10])&1 != 0 {
			return fmt.Errorf("package: encrypted or multi-disk entry")
		}
		length := 46 + int(binary.LittleEndian.Uint16(directory[28:30])) +
			int(binary.LittleEndian.Uint16(directory[30:32])) + int(binary.LittleEndian.Uint16(directory[32:34]))
		if length > len(directory) {
			return fmt.Errorf("package: truncated ZIP directory")
		}
		directory = directory[length:]
		if len(directory) == 0 && n+1 != count {
			return fmt.Errorf("package: ZIP entry count mismatch")
		}
	}
	if size == 0 {
		return fmt.Errorf("package: empty ZIP directory")
	}
	return nil
}

func validate(v *verified, trust TrustStore) (*verified, error) {
	if err := decodeJSON(v.files["package.json"], &v.Metadata); err != nil {
		return nil, fmt.Errorf("package: metadata: %w", err)
	}
	if v.Metadata.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("package: unsupported format version %d", v.Metadata.FormatVersion)
	}
	var err error
	v.Manifest, err = sdk.ParseManifest(v.files["manifest.json"])
	if err != nil {
		return nil, err
	}
	if v.Manifest.ID != v.Metadata.ID || v.Manifest.Version != v.Metadata.Version {
		return nil, fmt.Errorf("package: manifest identity does not match metadata")
	}
	m := v.Manifest
	if m.Mode != sdk.ModeExternal || (m.Isolation != sdk.IsolationSandbox && m.Isolation != sdk.IsolationStrict) {
		return nil, fmt.Errorf("package: external apps must explicitly request sandbox or strict isolation")
	}
	if !portablePath(m.Executable) || m.Executable == "manifest.json" || m.Executable == "package.json" ||
		len(v.files[m.Executable]) == 0 {
		return nil, fmt.Errorf("package: executable must name a nonempty archive binary")
	}
	if len(v.Metadata.Checksums) != len(v.files)-1 {
		return nil, fmt.Errorf("package: checksums must cover exactly every payload file")
	}
	for name, b := range v.files {
		if name == "package.json" {
			continue
		}
		h := sha256.Sum256(b)
		if v.Metadata.Checksums[name] != hex.EncodeToString(h[:]) {
			return nil, fmt.Errorf("package: checksum mismatch for %q", name)
		}
	}
	v.Provenance = Provenance{Publisher: v.Metadata.Publisher, KeyID: v.Metadata.KeyID}
	if v.Metadata.Signature != "" {
		v.Provenance.Signed = true
		key := trust[v.Metadata.KeyID]
		sig, err := base64.StdEncoding.DecodeString(v.Metadata.Signature)
		message, _ := v.Metadata.SigningBytes()
		if err != nil || len(key) != ed25519.PublicKeySize || v.Metadata.Publisher == "" ||
			!ed25519.Verify(key, message, sig) {
			return nil, fmt.Errorf("package: untrusted or invalid publisher signature")
		}
		v.Provenance.Verified = true
	} else if v.Metadata.KeyID != "" {
		return nil, fmt.Errorf("package: key_id requires a signature")
	}
	return v, nil
}

type PermissionDiff struct {
	AddedCapabilities   []string        `json:"added_capabilities"`
	RemovedCapabilities []string        `json:"removed_capabilities"`
	AddedPathGrants     []sdk.PathGrant `json:"added_path_grants"`
	RemovedPathGrants   []sdk.PathGrant `json:"removed_path_grants"`
	IsolationWeakened   bool            `json:"isolation_weakened"`
	Expansion           bool            `json:"expansion"`
}

// DiffPermissions treats a new grant as expansion unless an old grant covers
// its entire scope and access mode. Reductions never require confirmation.
func DiffPermissions(old, next sdk.Manifest) PermissionDiff {
	d := PermissionDiff{}
	for _, c := range next.Permissions {
		if !contains(old.Permissions, c) {
			d.AddedCapabilities = append(d.AddedCapabilities, c)
		}
	}
	for _, c := range old.Permissions {
		if !contains(next.Permissions, c) {
			d.RemovedCapabilities = append(d.RemovedCapabilities, c)
		}
	}
	for _, g := range next.PathGrants {
		if !covered(old.PathGrants, g) {
			d.AddedPathGrants = append(d.AddedPathGrants, g)
		}
	}
	for _, g := range old.PathGrants {
		if !covered(next.PathGrants, g) {
			d.RemovedPathGrants = append(d.RemovedPathGrants, g)
		}
	}
	sort.Strings(d.AddedCapabilities)
	sort.Strings(d.RemovedCapabilities)
	d.IsolationWeakened = old.Isolation == sdk.IsolationStrict && next.Isolation != sdk.IsolationStrict
	d.Expansion = len(d.AddedCapabilities) > 0 || len(d.AddedPathGrants) > 0 || d.IsolationWeakened
	return d
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func covered(grants []sdk.PathGrant, g sdk.PathGrant) bool {
	for _, old := range grants {
		if old.Access != "read-write" && g.Access == "read-write" {
			continue
		}
		if old.Path == g.Path && (!g.Recursive || old.Recursive) {
			return true
		}
		if old.Recursive && strings.HasPrefix(g.Path, path.Clean(old.Path)+"/") {
			return true
		}
	}
	return false
}
