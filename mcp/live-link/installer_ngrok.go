// installer_ngrok.go — locate or auto-install the ngrok agent binary.
//
// Mirrors installer.go's cloudflared flow but for ngrok. Resolution
// order (first hit wins):
//
//   1. ngrok_path config field, if set and the file exists.
//   2. ngrok on $PATH (brew install ngrok / manual install).
//   3. <DataDir>/bin/ngrok from a previous auto-install.
//   4. Download the latest stable agent from equinox.io, extract the
//      "ngrok" entry from the zip into <DataDir>/bin/ngrok.
//
// ngrok ships its agent as a zip per (OS, arch) — different from
// cloudflared's mix of raw binary (linux) and tgz (mac). We use Go's
// archive/zip to extract just the one entry we want.
//
// Same trust model as cloudflared: HTTPS to ngrok's official equinox
// download endpoint is the same boundary brew + curl + apt use.

package main

import (
	"archive/zip"
	"bytes"
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

// ngrokInstallMu serialises ngrok auto-installs the same way installMu
// serialises cloudflared's. Distinct mutex so a slow ngrok download
// doesn't block a parallel cloudflared install (and vice versa).
var ngrokInstallMu sync.Mutex

// resolveNgrokBinary returns a path to a usable ngrok, downloading
// one into DataDir if necessary. force=true bypasses the cache and
// always downloads — symmetric with resolveBinary's force flag, used
// by the "Reinstall binary" UI button.
func resolveNgrokBinary(configPath, dataDir string, force bool, log func(string, ...any)) (string, error) {
	if !force {
		// 1. explicit config
		if p := strings.TrimSpace(configPath); p != "" && p != "ngrok" {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
		// 2. PATH lookup
		if p, err := exec.LookPath("ngrok"); err == nil {
			return p, nil
		}
		// 3. previous auto-install
		if dataDir != "" {
			cached := filepath.Join(dataDir, "bin", "ngrok")
			if fi, err := os.Stat(cached); err == nil && fi.Size() > 0 {
				return cached, nil
			}
		}
	}

	if dataDir == "" {
		return "", errors.New("no APTEVA_DATA_DIR available — cannot auto-install ngrok. Install it manually (brew install ngrok) and set the ngrok_path config")
	}
	ngrokInstallMu.Lock()
	defer ngrokInstallMu.Unlock()
	cached := filepath.Join(dataDir, "bin", "ngrok")
	if !force {
		if fi, err := os.Stat(cached); err == nil && fi.Size() > 0 {
			return cached, nil
		}
	}
	if log != nil {
		log("downloading ngrok", "os", runtime.GOOS, "arch", runtime.GOARCH, "dest", cached)
	}
	if err := downloadNgrok(cached); err != nil {
		return "", err
	}
	return cached, nil
}

// downloadNgrok fetches the ngrok v3 stable agent for the host's
// OS/arch from equinox.io and extracts the "ngrok" entry into dest.
// Atomic via temp-file + rename.
func downloadNgrok(dest string) error {
	url, err := ngrokAssetURL(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}

	// ngrok zip is ~20 MB; 90s covers slow links + the in-memory
	// buffer-then-extract path (zip.Reader needs a ReaderAt).
	client := &http.Client{Timeout: 90 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "apteva-live-link/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	// archive/zip needs a ReaderAt; buffer the whole zip in memory.
	// ngrok's release is ~20 MB so this is cheap.
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	tmp := dest + ".part"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	var found bool
	for _, f := range zr.File {
		if filepath.Base(f.Name) != "ngrok" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if err != nil {
			out.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("extract ngrok: %w", err)
		}
		found = true
		break
	}
	if !found {
		out.Close()
		_ = os.Remove(tmp)
		return errors.New("ngrok entry not found in downloaded zip")
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
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

// ngrokAssetURL maps GOOS/GOARCH to ngrok's canonical equinox.io
// download URL. ngrok publishes per-(OS, arch) zips at stable URLs
// that always serve the latest v3 stable agent.
func ngrokAssetURL(goos, goarch string) (string, error) {
	// Channel: bNyj1mQVY4c is ngrok's published v3-stable channel id.
	// Documented at https://ngrok.com/docs/agent/changelog/.
	const base = "https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-v3-stable"
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			return base + "-linux-amd64.zip", nil
		case "arm64":
			return base + "-linux-arm64.zip", nil
		case "arm":
			return base + "-linux-arm.zip", nil
		case "386":
			return base + "-linux-386.zip", nil
		}
	case "darwin":
		switch goarch {
		case "amd64":
			return base + "-darwin-amd64.zip", nil
		case "arm64":
			return base + "-darwin-arm64.zip", nil
		}
	}
	return "", fmt.Errorf("ngrok auto-install unsupported on %s/%s — install manually (brew install ngrok) and set the ngrok_path config", goos, goarch)
}
