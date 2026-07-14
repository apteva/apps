// installer.go — locate or auto-install the cloudflared binary.
//
// Resolution order (first hit wins):
//
//   1. cloudflared_path config field, if set and the file exists.
//   2. cloudflared on $PATH (brew install / package manager / manual).
//   3. <DataDir>/bin/cloudflared from a previous auto-install.
//   4. Download a pinned cloudflared release, verify its SHA-256 digest,
//      cache it under <DataDir>/bin/cloudflared, and return that path.
//
// Download is synchronous from the caller's perspective — the click
// that triggers it is the same click that wants the tunnel up, so the
// "Starting…" button state covers the wait. Asset is ~30MB; takes a
// few seconds on any normal connection.
//
// Downloads are bounded and accepted only when they match the digest shipped
// with this app version. Cached binaries carry a matching version marker.

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const cloudflaredPinnedVersion = "2026.7.1"

// installMu serialises auto-installs. Two simultaneous Start() calls
// (HTTP + MCP race, or two browser tabs) must not both download.
var installMu sync.Mutex

// resolveBinary returns a path to a usable cloudflared, downloading
// one into DataDir if necessary. force=true bypasses the cache and
// always downloads — used by the /install "Reinstall binary" button.
func resolveBinary(configPath, dataDir string, force bool, log func(string, ...any)) (string, error) {
	if !force {
		// 1. explicit config
		if p := strings.TrimSpace(configPath); p != "" && p != "cloudflared" {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
			// configured but missing — fall through to other options
			// rather than erroring; the operator likely typed a path
			// they meant to install at later.
		}
		// 2. PATH lookup
		if p, err := exec.LookPath("cloudflared"); err == nil {
			return p, nil
		}
		// 3. previous auto-install
		if dataDir != "" {
			cached := filepath.Join(dataDir, "bin", "cloudflared")
			if cachedBinaryCurrent(cached, cloudflaredPinnedVersion) {
				return cached, nil
			}
		}
	}

	// 4. fresh download
	if dataDir == "" {
		return "", errors.New("no APTEVA_DATA_DIR available — cannot auto-install cloudflared. Install it manually (brew install cloudflared) and set the cloudflared_path config")
	}
	installMu.Lock()
	defer installMu.Unlock()
	// Re-check after acquiring the lock — a sibling goroutine may
	// have just finished installing while we waited.
	cached := filepath.Join(dataDir, "bin", "cloudflared")
	if !force {
		if cachedBinaryCurrent(cached, cloudflaredPinnedVersion) {
			return cached, nil
		}
	}
	if log != nil {
		log("downloading cloudflared", "os", runtime.GOOS, "arch", runtime.GOARCH, "dest", cached)
	}
	if err := downloadCloudflared(cached); err != nil {
		return "", err
	}
	return cached, nil
}

// downloadCloudflared fetches the pinned release for the host's
// OS/arch and writes it to dest. Atomic via temp-file + rename.
func downloadCloudflared(dest string) error {
	artifact, err := cloudflaredArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := validateArtifactURL(artifact); err != nil {
		return err
	}
	if err := ensureInstallDir(dest); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	archive, tmp, cleanup := prepareInstallPaths(dest)
	cleanup()
	defer cleanup()
	if err := downloadVerified(artifact, archive); err != nil {
		return err
	}
	if artifact.Archived {
		in, err := os.Open(archive)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			in.Close()
			return err
		}
		err = extractFromTGZBounded(in, "cloudflared", out, artifact.MaxExtracted)
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if err != nil {
			return err
		}
		if closeInErr != nil {
			return closeInErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
	} else if err := os.Rename(archive, tmp); err != nil {
		return err
	}
	// Belt + suspenders: O_CREATE perm is umask-affected; explicit
	// chmod ensures the file is executable on every host.
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename into place: %w", err)
	}
	return writeVersionMarker(dest, artifact.Version)
}

func cloudflaredArtifact(goos, goarch string) (binaryArtifact, error) {
	const base = "https://github.com/cloudflare/cloudflared/releases/download/" + cloudflaredPinnedVersion
	allowed := map[string]bool{
		"github.com": true, "release-assets.githubusercontent.com": true,
		"objects.githubusercontent.com": true,
	}
	artifact := binaryArtifact{Version: cloudflaredPinnedVersion, MaxDownload: 48 << 20, MaxExtracted: 64 << 20, AllowedHosts: allowed}
	switch goos + "/" + goarch {
	case "linux/amd64":
		artifact.URL, artifact.SHA256 = base+"/cloudflared-linux-amd64", "79a0ade7fc854f62c1aaef48424d9d979e8c2fcd039189d24db82b84cd146be1"
	case "linux/arm64":
		artifact.URL, artifact.SHA256 = base+"/cloudflared-linux-arm64", "18f2c9bfc7a67a971bd96f1a5a1935def3c1e52aa386626f1566f04e9b5478d6"
	case "linux/arm":
		artifact.URL, artifact.SHA256 = base+"/cloudflared-linux-arm", "17cedcb83d8239c5f81f6d57b7d50a384f0d57fd523af2763f47ac6cade77bf9"
	case "linux/386":
		artifact.URL, artifact.SHA256 = base+"/cloudflared-linux-386", "8452c2b93f2bfa89f1249bceaec128c90424e25a6ef600f57d92b1fbd0cb502f"
	case "darwin/amd64":
		artifact.URL, artifact.SHA256, artifact.Archived = base+"/cloudflared-darwin-amd64.tgz", "05871d772745b0f8398c7be89113a0b178474936ff20638b3b07c0e7262f717e", true
	case "darwin/arm64":
		artifact.URL, artifact.SHA256, artifact.Archived = base+"/cloudflared-darwin-arm64.tgz", "6d4b59383cdad387834d7ae5704fc512882b2d078074bf5770e02b186a0981ed", true
	default:
		return binaryArtifact{}, fmt.Errorf("auto-install unsupported on %s/%s — install cloudflared manually and set the cloudflared_path config", goos, goarch)
	}
	return artifact, nil
}

// assetURL maps GOOS/GOARCH to the canonical cloudflared release
// asset URL. Returns archived=true when the asset is a .tgz that
// needs extraction (mac), false when it's a raw binary (linux).
func assetURL(goos, goarch string) (url string, archived bool, err error) {
	a, err := cloudflaredArtifact(goos, goarch)
	return a.URL, a.Archived, err
}

// extractFromTGZ reads a gzipped tar from r, finds the entry whose
// basename matches name, and copies its contents into dst. Returns
// an error if the entry isn't found.
func extractFromTGZ(r io.Reader, name string, dst io.Writer) error {
	return extractFromTGZBounded(r, name, dst, 64<<20)
}

func extractFromTGZBounded(r io.Reader, name string, dst io.Writer, maxBytes int64) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("entry %q not found in tarball", name)
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if filepath.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			if h.Size < 0 || h.Size > maxBytes {
				return fmt.Errorf("extract %s: entry size %d exceeds limit %d", name, h.Size, maxBytes)
			}
			n, err := io.Copy(dst, io.LimitReader(tr, maxBytes+1))
			if err != nil {
				return fmt.Errorf("extract %s: %w", name, err)
			}
			if n > maxBytes {
				return fmt.Errorf("extract %s: exceeded %d-byte limit", name, maxBytes)
			}
			return nil
		}
	}
}
