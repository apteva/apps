// installer.go — locate or auto-install the cloudflared binary.
//
// Resolution order (first hit wins):
//
//   1. cloudflared_path config field, if set and the file exists.
//   2. cloudflared on $PATH (brew install / package manager / manual).
//   3. <DataDir>/bin/cloudflared from a previous auto-install.
//   4. Download from github.com/cloudflare/cloudflared/releases/latest,
//      cache under <DataDir>/bin/cloudflared, return that path.
//
// Download is synchronous from the caller's perspective — the click
// that triggers it is the same click that wants the tunnel up, so the
// "Starting…" button state covers the wait. Asset is ~30MB; takes a
// few seconds on any normal connection.
//
// We deliberately do NOT verify SHA256 in v0.1: HTTPS to a github.com
// release URL is the same trust boundary brew/curl/apt use, and the
// publish locations for cloudflare's checksum file have shifted enough
// across releases that hard-coding the URL would be a bigger
// reliability risk than the integrity it protects against. v0.2 can
// add it once we settle on the canonical checksum URL.

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

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
			if fi, err := os.Stat(cached); err == nil && fi.Size() > 0 {
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
		if fi, err := os.Stat(cached); err == nil && fi.Size() > 0 {
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

// downloadCloudflared fetches the latest release for the host's
// OS/arch and writes it to dest. Atomic via temp-file + rename.
func downloadCloudflared(dest string) error {
	url, archived, err := assetURL(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}

	// 60s is generous for a 30MB asset on any non-pathological link.
	// Slow links would still benefit from a streamed write; we do
	// that, so the timeout only catches genuine stalls.
	client := &http.Client{Timeout: 90 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// GitHub's raw release-download URL serves a 302 to a CDN. The
	// default client follows redirects. UA is just polite.
	req.Header.Set("User-Agent", "apteva-live-link/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Most common reason: an unsupported os/arch combination
		// (we shouldn't reach here because assetURL filters, but a
		// future GitHub URL change would land us here).
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	tmp := dest + ".part"
	// Clean up any half-finished previous attempt.
	_ = os.Remove(tmp)

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	if archived {
		// Mac releases are gzipped tarballs containing a single
		// `cloudflared` entry. Stream-extract it.
		if err := extractFromTGZ(resp.Body, "cloudflared", out); err != nil {
			out.Close()
			_ = os.Remove(tmp)
			return err
		}
	} else {
		// Linux releases are the raw binary.
		if _, err := io.Copy(out, resp.Body); err != nil {
			out.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("write %s: %w", tmp, err)
		}
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
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
	return nil
}

// assetURL maps GOOS/GOARCH to the canonical cloudflared release
// asset URL. Returns archived=true when the asset is a .tgz that
// needs extraction (mac), false when it's a raw binary (linux).
func assetURL(goos, goarch string) (url string, archived bool, err error) {
	const base = "https://github.com/cloudflare/cloudflared/releases/latest/download"
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			return base + "/cloudflared-linux-amd64", false, nil
		case "arm64":
			return base + "/cloudflared-linux-arm64", false, nil
		case "arm":
			return base + "/cloudflared-linux-arm", false, nil
		case "386":
			return base + "/cloudflared-linux-386", false, nil
		}
	case "darwin":
		switch goarch {
		case "amd64":
			return base + "/cloudflared-darwin-amd64.tgz", true, nil
		case "arm64":
			return base + "/cloudflared-darwin-arm64.tgz", true, nil
		}
	}
	return "", false, fmt.Errorf("auto-install unsupported on %s/%s — install cloudflared manually and set the cloudflared_path config", goos, goarch)
}

// extractFromTGZ reads a gzipped tar from r, finds the entry whose
// basename matches name, and copies its contents into dst. Returns
// an error if the entry isn't found.
func extractFromTGZ(r io.Reader, name string, dst io.Writer) error {
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
			if _, err := io.Copy(dst, tr); err != nil {
				return fmt.Errorf("extract %s: %w", name, err)
			}
			return nil
		}
	}
}
