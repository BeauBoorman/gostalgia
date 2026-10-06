package pkg

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"gostalgia/sdk"
)

func testManifest() sdk.Manifest {
	return sdk.Manifest{
		ID: "com.test.package", Name: "Package test", Version: "1.0.0",
		Mode: sdk.ModeExternal, Isolation: sdk.IsolationSandbox,
		Executable: "bin/app", ProtocolVersion: sdk.ProtocolVersion,
	}
}

func payload(t testing.TB, man sdk.Manifest, binary []byte) map[string][]byte {
	t.Helper()
	raw, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"manifest.json": raw, man.Executable: binary}
}

func metadata(man sdk.Manifest, files map[string][]byte) Metadata {
	m := Metadata{FormatVersion: FormatVersion, ID: man.ID, Version: man.Version, Checksums: make(map[string]string)}
	for name, b := range files {
		sum := sha256.Sum256(b)
		m.Checksums[name] = hex.EncodeToString(sum[:])
	}
	return m
}

func zipFiles(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func archive(t testing.TB, man sdk.Manifest, binary []byte, change func(*Metadata, map[string][]byte)) []byte {
	t.Helper()
	files := payload(t, man, binary)
	meta := metadata(man, files)
	if change != nil {
		change(&meta, files)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	files["package.json"] = raw
	return zipFiles(t, files)
}

func TestArchiveVerification(t *testing.T) {
	man := testManifest()
	b := archive(t, man, []byte("binary"), nil)
	v, err := verify(bytes.NewReader(b), nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Manifest.ID != man.ID || v.Provenance.Verified || v.Provenance.Signed {
		t.Fatalf("unexpected verification: %+v", v)
	}
}

func TestPublisherProvenance(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(m *Metadata, _ map[string][]byte) {
		m.Publisher, m.KeyID = "Test publisher", "test-key"
		b, err := m.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, b))
	}
	man := testManifest()
	b := archive(t, man, []byte("binary"), sign)
	trust := TrustStore{"test-key": public}
	v, err := verify(bytes.NewReader(b), trust)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Provenance.Verified || v.Manifest.Isolation != sdk.IsolationSandbox || len(v.Manifest.Permissions) != 0 {
		t.Fatal("provenance changed runtime isolation or permissions")
	}
	if _, err := verify(bytes.NewReader(b), nil); err == nil {
		t.Fatal("accepted an untrusted key")
	}
	b = archive(t, man, []byte("binary"), func(m *Metadata, files map[string][]byte) {
		sign(m, files)
		m.Publisher = "Impostor"
	})
	if _, err := verify(bytes.NewReader(b), trust); err == nil {
		t.Fatal("accepted modified signed metadata")
	}
	man.Isolation = sdk.IsolationTrusted
	if _, err := verify(bytes.NewReader(archive(t, man, []byte("binary"), sign)), trust); err == nil {
		t.Fatal("signed package bypassed sandbox requirement")
	}
}

func TestAdversarialArchives(t *testing.T) {
	man := testManifest()
	for _, name := range []string{"../escape", "/escape", "a/../../escape", `a\escape`, "bad\x00name", "C:/escape", "CON", "bin/app.", "bin/app ", "BIN/APP"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			b := archive(t, man, []byte("binary"), func(m *Metadata, files map[string][]byte) { files[name] = []byte("evil") })
			if _, err := verify(bytes.NewReader(b), nil); err == nil {
				t.Fatal("accepted unsafe path")
			}
		})
	}
	for _, mode := range []fs.FileMode{fs.ModeSymlink | 0o777, fs.ModeNamedPipe | 0o600, fs.ModeDevice | 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			var buf bytes.Buffer
			w := zip.NewWriter(&buf)
			h := &zip.FileHeader{Name: "breakout"}
			h.SetMode(mode)
			f, err := w.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write([]byte("../../outside"))
			f, _ = w.Create("manifest.json")
			_, _ = f.Write([]byte("{}"))
			_ = w.Close()
			if _, err := verify(bytes.NewReader(buf.Bytes()), nil); err == nil {
				t.Fatal("accepted a link or special file")
			}
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		for range 2 {
			f, _ := w.Create("manifest.json")
			_, _ = f.Write([]byte("{}"))
		}
		_ = w.Close()
		if _, err := verify(bytes.NewReader(buf.Bytes()), nil); err == nil {
			t.Fatal("accepted duplicate")
		}
	})
	t.Run("parent case alias", func(t *testing.T) {
		b := archive(t, man, []byte("binary"), func(_ *Metadata, files map[string][]byte) {
			files["BIN/resource"] = []byte("alias")
		})
		if _, err := verify(bytes.NewReader(b), nil); err == nil {
			t.Fatal("accepted aliased parent directory")
		}
	})
	for name, change := range map[string]func(*Metadata, map[string][]byte){
		"identity":         func(m *Metadata, _ map[string][]byte) { m.ID = "com.test.other" },
		"version":          func(m *Metadata, _ map[string][]byte) { m.Version = "2.0.0" },
		"format":           func(m *Metadata, _ map[string][]byte) { m.FormatVersion = 99 },
		"checksum":         func(m *Metadata, _ map[string][]byte) { m.Checksums["bin/app"] = strings.Repeat("0", 64) },
		"missing checksum": func(m *Metadata, _ map[string][]byte) { delete(m.Checksums, "bin/app") },
		"extra checksum":   func(m *Metadata, _ map[string][]byte) { m.Checksums["missing"] = strings.Repeat("0", 64) },
		"missing manifest": func(_ *Metadata, f map[string][]byte) { delete(f, "manifest.json") },
		"missing binary":   func(_ *Metadata, f map[string][]byte) { delete(f, "bin/app") },
		"untrusted signature": func(m *Metadata, _ map[string][]byte) {
			m.KeyID = "evil"
			m.Publisher = "evil"
			m.Signature = "invalid"
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verify(bytes.NewReader(archive(t, man, []byte("binary"), change)), nil); err == nil {
				t.Fatal("accepted malformed package")
			}
		})
	}
	t.Run("local header", func(t *testing.T) {
		b := archive(t, man, []byte("binary"), nil)
		b[0] = 0
		if _, err := verify(bytes.NewReader(b), nil); err == nil {
			t.Fatal("accepted corrupted header")
		}
	})
	t.Run("CRC", func(t *testing.T) {
		b := archive(t, man, []byte("binary"), nil)
		z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		offset, err := z.File[0].DataOffset()
		if err != nil {
			t.Fatal(err)
		}
		b[offset] ^= 0xff
		if _, err := verify(bytes.NewReader(b), nil); err == nil {
			t.Fatal("accepted corrupted data")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		b := archive(t, man, []byte("binary"), nil)
		if _, err := verify(bytes.NewReader(b[:len(b)-10]), nil); err == nil {
			t.Fatal("accepted truncated ZIP")
		}
	})
}

func TestExtractionBudgets(t *testing.T) {
	t.Run("bomb", func(t *testing.T) {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		for _, name := range []string{"manifest.json", "bomb"} {
			f, _ := w.Create(name)
			_, _ = f.Write(bytes.Repeat([]byte("0"), 1<<20))
		}
		_ = w.Close()
		if _, err := verify(bytes.NewReader(buf.Bytes()), nil); err == nil {
			t.Fatal("accepted decompression bomb")
		}
	})
	t.Run("entry count", func(t *testing.T) {
		files := make(map[string][]byte)
		for n := range MaxFiles + 1 {
			files[fmt.Sprintf("file-%d", n)] = nil
		}
		if _, err := verify(bytes.NewReader(zipFiles(t, files)), nil); err == nil {
			t.Fatal("accepted excessive file count")
		}
	})
	t.Run("implicit directories", func(t *testing.T) {
		files := map[string][]byte{strings.Repeat("a/", MaxFiles) + "file": nil, "package.json": nil}
		if _, err := verify(bytes.NewReader(zipFiles(t, files)), nil); err == nil {
			t.Fatal("accepted excessive directory count")
		}
	})
	t.Run("claimed size", func(t *testing.T) {
		b := archive(t, testManifest(), []byte("binary"), nil)
		offset := bytes.Index(b, []byte{0x50, 0x4b, 0x01, 0x02})
		binary.LittleEndian.PutUint32(b[offset+24:offset+28], MaxExtractBytes+1)
		if _, err := verify(bytes.NewReader(b), nil); err == nil {
			t.Fatal("accepted oversized file")
		}
	})
	t.Run("oversized input", func(t *testing.T) {
		if _, err := verify(bytes.NewReader(make([]byte, MaxArchiveBytes+1)), nil); err == nil {
			t.Fatal("accepted oversized archive")
		}
	})
}

func TestPermissionDiff(t *testing.T) {
	old := testManifest()
	old.Permissions = []string{sdk.CapIPC, sdk.CapFileRead}
	old.PathGrants = []sdk.PathGrant{{Path: "/users/guest/documents", Access: "read", Recursive: true}}
	next := old
	next.PathGrants = []sdk.PathGrant{{Path: "/users/guest/documents/sub/file", Access: "read"}}
	if d := DiffPermissions(old, next); d.Expansion {
		t.Fatalf("narrower grant expands: %+v", d)
	}
	next.PathGrants[0].Access = "read-write"
	next.Permissions = []string{sdk.CapIPC, sdk.CapNetEgress}
	d := DiffPermissions(old, next)
	if !d.Expansion || len(d.AddedCapabilities) != 1 || len(d.RemovedCapabilities) != 1 || len(d.AddedPathGrants) != 1 {
		t.Fatalf("missing expansion: %+v", d)
	}
	old.PathGrants[0].Recursive = false
	next = old
	next.PathGrants = []sdk.PathGrant{{Path: "/users/guest/documents", Access: "read", Recursive: true}}
	if !DiffPermissions(old, next).Expansion {
		t.Fatal("recursive expansion not detected")
	}
	old.Isolation = sdk.IsolationStrict
	next.Isolation = sdk.IsolationSandbox
	if !DiffPermissions(old, next).IsolationWeakened {
		t.Fatal("isolation weakening not detected")
	}
}

func FuzzArchiveVerification(f *testing.F) {
	f.Add([]byte("not a zip"))
	f.Add([]byte{0x50, 0x4b, 0x05, 0x06})
	f.Add(archive(f, testManifest(), []byte("binary"), nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}
		_, _ = verify(bytes.NewReader(b), nil)
	})
}
