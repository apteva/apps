package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// These are release resources, not runtime downloads. Each platform embeds
// only its own compressed libraries; the pinned manifest covers every build.
//
//go:embed native/opus/manifest.json
var rtcOpusManifest []byte

// Keep the upstream notice in every binary and beside the extracted library.
//
//go:embed native/opus/COPYING
var rtcOpusLicense []byte

type rtcOpusBundleEntry struct {
	Asset            string `json:"asset"`
	SHA256           string `json:"sha256"`
	CompressedSHA256 string `json:"compressed_sha256"`
	Bytes            int64  `json:"bytes"`
}
type rtcOpusBundle struct {
	Schema    string                        `json:"schema"`
	Version   string                        `json:"version"`
	Libraries map[string]rtcOpusBundleEntry `json:"libraries"`
}

var rtcOpusCache struct {
	sync.Mutex
	root string
}

func rtcOpusCacheRoot() string {
	rtcOpusCache.Lock()
	defer rtcOpusCache.Unlock()
	if rtcOpusCache.root != "" {
		return rtcOpusCache.root
	}
	// Standalone development/tests also use an app-owned, per-user cache.
	root, err := os.UserCacheDir()
	if err != nil || root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, fmt.Sprintf("apteva-telephony-%d", os.Getuid()))
}

func initRTCOpusBundle(root string) {
	if root != "" {
		rtcOpusCache.Lock()
		rtcOpusCache.root = filepath.Join(root, "native")
		rtcOpusCache.Unlock()
	}
	// Preparation happens at mount, before the installation accepts calls.
	// Library failure is observational: the established portable fallback stays.
	_, _ = loadRTCOpusLibrary()
}

func rtcOpusPlatform() string {
	key := runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "linux" {
		maps, _ := os.ReadFile("/proc/self/maps")
		if bytes.Contains(maps, []byte("ld-musl")) || bytes.Contains(maps, []byte("libc.musl")) {
			return key + "-musl"
		}
		return key + "-gnu"
	}
	return key
}

func prepareBundledRTCOpus(root, platform string) (string, string, error) {
	return prepareRTCOpusBundle(root, platform, rtcOpusManifest, rtcOpusAssets)
}
func prepareRTCOpusBundle(root, platform string, metadata []byte, assets fs.FS) (string, string, error) {
	var manifest rtcOpusBundle
	if err := json.Unmarshal(metadata, &manifest); err != nil || manifest.Schema != "telephony-opus/v1" || manifest.Version != "1.5.2" {
		return "", "", errors.New("invalid bundled Opus manifest")
	}
	entry, ok := manifest.Libraries[platform]
	if !ok || filepath.Base(entry.Asset) != entry.Asset || entry.Bytes <= 0 || entry.Bytes > 8<<20 {
		return "", "", errors.New("no supported bundled Opus library")
	}
	compressed, err := fs.ReadFile(assets, "native/opus/assets/"+entry.Asset)
	if err != nil || !rtcOpusDigest(compressed, entry.CompressedSHA256) {
		return "", "", errors.New("bundled Opus archive verification failed")
	}
	z, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", "", errors.New("invalid bundled Opus archive")
	}
	defer z.Close()
	library, err := io.ReadAll(io.LimitReader(z, entry.Bytes+1))
	if err != nil || int64(len(library)) != entry.Bytes || !rtcOpusDigest(library, entry.SHA256) {
		return "", "", errors.New("bundled Opus library verification failed")
	}
	// Content-addressed, versioned app cache. Refuse links before any writes.
	root, err = rtcOpusPrivateRoot(root)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(root, "opus-"+manifest.Version, entry.SHA256)
	if err := rtcOpusPrivateDir(dir); err != nil {
		return "", "", err
	}
	if err := rtcOpusInstallNotice(dir); err != nil {
		return "", "", err
	}
	filename := "libopus.so.0"
	if strings.HasPrefix(platform, "darwin-") {
		filename = "libopus.0.dylib"
	}
	path := filepath.Join(dir, filename)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", "", errors.New("bundled Opus cache is not a regular file")
		}
		var cached []byte
		if info.Size() == entry.Bytes {
			cached, err = rtcOpusReadCache(path, entry.Bytes)
		}
		if err == nil && info.Mode().Perm() == 0500 && int64(len(cached)) == entry.Bytes && rtcOpusDigest(cached, entry.SHA256) {
			return path, manifest.Version, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	f, err := os.CreateTemp(dir, ".opus-")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(library); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Chmod(0500)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return "", "", err
	}
	return path, manifest.Version, nil
}

func rtcOpusDigest(data []byte, expected string) bool {
	sum := sha256.Sum256(data)
	return len(expected) == 64 && hex.EncodeToString(sum[:]) == expected
}

func rtcOpusPrivateDir(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Existing parents may be shared (e.g. /tmp), but no cache path component
	// may redirect writes through a link. New directories are owner-only.
	var missing []string
	for cursor := path; ; cursor = filepath.Dir(cursor) {
		info, err := os.Lstat(cursor)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("invalid bundled Opus cache directory")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, cursor)
		} else {
			return err
		}
		if filepath.Dir(cursor) == cursor {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(missing[i])
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid bundled Opus cache directory")
		}
	}
	return nil
}

// DataDir/cache ancestors are trusted host paths and may include macOS aliases
// such as /var -> /private/var. Resolve those once; managed descendants must not
// contain links. Never relax the permissions of a shared parent.
func rtcOpusPrivateRoot(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return "", err
	}
	root = filepath.Join(parent, filepath.Base(root))
	if err := rtcOpusPrivateDir(root); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return "", err
	}
	return root, nil
}

func rtcOpusInstallNotice(dir string) error {
	path := filepath.Join(dir, "COPYING")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("invalid Opus license cache")
		}
		if info.Size() == int64(len(rtcOpusLicense)) {
			data, err := rtcOpusReadCache(path, int64(len(rtcOpusLicense)))
			if err == nil && bytes.Equal(data, rtcOpusLicense) {
				return nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".notice-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(rtcOpusLicense); err == nil {
		err = f.Chmod(0400)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}

func rtcOpusReadCache(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maximum+1))
}
