// installer_ngrok.go — locate or auto-install the ngrok agent binary.
//
// Mirrors installer.go's cloudflared flow but for ngrok. Resolution
// order (first hit wins):
//
//   1. ngrok_path config field, if set and the file exists.
//   2. ngrok on $PATH (brew install ngrok / manual install).
//   3. <DataDir>/bin/ngrok from a previous auto-install.
//   4. Download the pinned stable agent from equinox.io, verify its shipped
//      digest, and extract the "ngrok" entry into <DataDir>/bin/ngrok.
//
// ngrok ships its agent as a zip per (OS, arch) — different from
// cloudflared's mix of raw binary (linux) and tgz (mac). We use Go's
// archive/zip to extract just the one entry we want.
//
// The stable upstream URL is mutable, so any upstream update fails closed
// until a Live Link release ships the matching version and digest.

package main

import (
	"archive/zip"
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

const ngrokPinnedVersion = "3.39.9"

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
			if cachedBinaryCurrent(cached, ngrokPinnedVersion) {
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
		if cachedBinaryCurrent(cached, ngrokPinnedVersion) {
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
	artifact, err := ngrokArtifact(runtime.GOOS, runtime.GOARCH)
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
	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	fi, err := in.Stat()
	if err != nil {
		in.Close()
		return err
	}
	zr, err := zip.NewReader(in, fi.Size())
	if err != nil {
		in.Close()
		return fmt.Errorf("open zip: %w", err)
	}
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		in.Close()
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	var found bool
	for _, f := range zr.File {
		if filepath.Base(f.Name) != "ngrok" {
			continue
		}
		if int64(f.UncompressedSize64) > artifact.MaxExtracted {
			out.Close()
			in.Close()
			return fmt.Errorf("ngrok zip entry exceeds %d-byte limit", artifact.MaxExtracted)
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		n, copyErr := io.Copy(out, io.LimitReader(rc, artifact.MaxExtracted+1))
		rc.Close()
		if copyErr != nil || n > artifact.MaxExtracted {
			out.Close()
			_ = os.Remove(tmp)
			in.Close()
			if copyErr != nil {
				return fmt.Errorf("extract ngrok: %w", copyErr)
			}
			return fmt.Errorf("extract ngrok: exceeded %d-byte limit", artifact.MaxExtracted)
		}
		found = true
		break
	}
	if !found {
		out.Close()
		in.Close()
		_ = os.Remove(tmp)
		return errors.New("ngrok entry not found in downloaded zip")
	}

	if err := out.Close(); err != nil {
		in.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := in.Close(); err != nil {
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
	return writeVersionMarker(dest, artifact.Version)
}

func ngrokArtifact(goos, goarch string) (binaryArtifact, error) {
	const base = "https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-v3-stable"
	artifact := binaryArtifact{
		Version: ngrokPinnedVersion, MaxDownload: 32 << 20, MaxExtracted: 48 << 20,
		Archived: true, AllowedHosts: map[string]bool{"bin.equinox.io": true},
	}
	switch goos + "/" + goarch {
	case "linux/amd64":
		artifact.URL, artifact.SHA256 = base+"-linux-amd64.zip", "7f51243ef837f22b971daff05978430598da038294a02994c1dbb7291b35a309"
	case "linux/arm64":
		artifact.URL, artifact.SHA256 = base+"-linux-arm64.zip", "a5db1a260b5e8b058c0842e5bf9269c6471a75522426b22c55550620741dc30b"
	case "linux/arm":
		artifact.URL, artifact.SHA256 = base+"-linux-arm.zip", "2bd5971851267ecdcf010e8fcd9d7e82ad6983036966a44c6b7c0ca4f16c83ab"
	case "linux/386":
		artifact.URL, artifact.SHA256 = base+"-linux-386.zip", "6ff2637f8a169a0119465dba56662c8179d5cf86f43f9fc6db6f4d96d855262d"
	case "darwin/amd64":
		artifact.URL, artifact.SHA256 = base+"-darwin-amd64.zip", "94f5ee996cda6cec8b179c5dab816c790a640232844cd65dd9e5f830ea528f8e"
	case "darwin/arm64":
		artifact.URL, artifact.SHA256 = base+"-darwin-arm64.zip", "840a06680a0a68cdf295012bd5244d645a4d4235c451c0354293ac24db096e8c"
	default:
		return binaryArtifact{}, fmt.Errorf("ngrok auto-install unsupported on %s/%s — install manually and set the ngrok_path config", goos, goarch)
	}
	return artifact, nil
}

// ngrokAssetURL maps GOOS/GOARCH to ngrok's canonical equinox.io
// download URL. ngrok publishes per-(OS, arch) zips at stable URLs
// that always serve the latest v3 stable agent.
func ngrokAssetURL(goos, goarch string) (string, error) {
	a, err := ngrokArtifact(goos, goarch)
	return a.URL, err
}
