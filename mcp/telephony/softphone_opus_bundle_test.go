//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestRTCOpusBundlePlatformAndIntegrity(t *testing.T) {
	var m rtcOpusBundle
	if err := json.Unmarshal(rtcOpusManifest, &m); err != nil {
		t.Fatal(err)
	}
	count := 0
	for key, entry := range m.Libraries {
		compressed, err := rtcOpusAssets.ReadFile("native/opus/assets/" + entry.Asset)
		if os.IsNotExist(err) {
			continue
		} // each binary embeds only its platform
		if err != nil || !rtcOpusDigest(compressed, entry.CompressedSHA256) {
			t.Fatal(key, err)
		}
		path, version, err := prepareBundledRTCOpus(filepath.Join(t.TempDir(), "native"), key)
		if err != nil || version != "1.5.2" {
			t.Fatal(key, version, err)
		}
		notice, err := os.ReadFile(filepath.Join(filepath.Dir(path), "COPYING"))
		if err != nil || !bytes.Equal(notice, rtcOpusLicense) {
			t.Fatal("missing upstream notice", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || int64(len(data)) != entry.Bytes || !rtcOpusDigest(data, entry.SHA256) {
			t.Fatal(key, err)
		}
		if runtime.GOOS == "linux" {
			f, err := elf.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			expected := elf.EM_AARCH64
			if runtime.GOARCH == "amd64" {
				expected = elf.EM_X86_64
			}
			if f.Type != elf.ET_DYN || f.Machine != expected {
				t.Fatal(key, f.Type, f.Machine)
			}
		} else {
			f, err := macho.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			expected := macho.CpuArm64
			if runtime.GOARCH == "amd64" {
				expected = macho.CpuAmd64
			}
			if f.Type != macho.TypeDylib || f.Cpu != expected {
				t.Fatal(key, f.Type, f.Cpu)
			}
		}
		count++
	}
	expected := 1
	if runtime.GOOS == "linux" {
		expected = 2
	}
	if count != expected {
		t.Fatal("missing packaged variants", count, expected)
	}
}

func TestRTCOpusBundleCacheReuseRepairAndConcurrency(t *testing.T) {
	root := filepath.Join(t.TempDir(), "native")
	path, _, err := prepareBundledRTCOpus(root, rtcOpusPlatform())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0500 {
		t.Fatal("unsafe library permissions", info.Mode())
	}
	if _, _, err = prepareBundledRTCOpus(root, rtcOpusPlatform()); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if !info.ModTime().Equal(old) {
		t.Fatal("valid library rewritten")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("corruption"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _, err := prepareBundledRTCOpus(root, rtcOpusPlatform())
			if err != nil || p != path {
				t.Error(p, err)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 10000 {
		t.Fatal("cache repair failed", err)
	}
	if info, _ = os.Stat(root); info.Mode().Perm() != 0700 {
		t.Fatal("unsafe root", info.Mode())
	}
}

func TestRTCOpusBundleRejectsManagedSymlinks(t *testing.T) {
	for _, kind := range []string{"root", "directory", "file"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "native")
			target := t.TempDir()
			if kind == "root" {
				if err := os.Symlink(target, root); err != nil {
					t.Fatal(err)
				}
			} else {
				path, _, err := prepareBundledRTCOpus(root, rtcOpusPlatform())
				if err != nil {
					t.Fatal(err)
				}
				replace := path
				if kind == "directory" {
					replace = filepath.Dir(path)
				}
				if err := os.RemoveAll(replace); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, replace); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := prepareBundledRTCOpus(root, rtcOpusPlatform()); err == nil {
				t.Fatal("accepted managed symlink")
			}
		})
	}
}

func TestRTCOpusBundleRejectsInvalidResources(t *testing.T) {
	var m rtcOpusBundle
	if err := json.Unmarshal(rtcOpusManifest, &m); err != nil {
		t.Fatal(err)
	}
	platform := rtcOpusPlatform()
	original := m.Libraries[platform]
	for _, kind := range []string{"metadata", "platform", "archive_hash", "library_hash", "size", "path", "gzip"} {
		t.Run(kind, func(t *testing.T) {
			entry := original
			metadata := rtcOpusManifest
			assets := rtcOpusAssets
			switch kind {
			case "platform":
				platform = "unsupported"
			case "archive_hash":
				entry.CompressedSHA256 = "bad"
			case "library_hash":
				entry.SHA256 = "bad"
			case "size":
				entry.Bytes = 1
			case "path":
				entry.Asset = "../escape"
			}
			mm := m
			mm.Libraries = map[string]rtcOpusBundleEntry{rtcOpusPlatform(): entry}
			metadata, _ = json.Marshal(mm)
			if kind == "metadata" {
				metadata = []byte("{")
			}
			if kind == "gzip" {
				var b bytes.Buffer
				z := gzip.NewWriter(&b)
				_, _ = z.Write([]byte("not a library"))
				_ = z.Close()
				entry.CompressedSHA256 = rtcTestSHA(b.Bytes())
				mm.Libraries[rtcOpusPlatform()] = entry
				metadata, _ = json.Marshal(mm)
				if _, _, err := prepareRTCOpusBundle(filepath.Join(t.TempDir(), "native"), rtcOpusPlatform(), metadata, fstest.MapFS{"native/opus/assets/" + entry.Asset: &fstest.MapFile{Data: b.Bytes()}}); err == nil {
					t.Fatal("accepted wrong decompressed data")
				}
				return
			}
			if _, _, err := prepareRTCOpusBundle(filepath.Join(t.TempDir(), "native"), platform, metadata, assets); err == nil {
				t.Fatal("accepted invalid resource")
			}
			platform = rtcOpusPlatform()
		})
	}
}

func rtcTestSHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
