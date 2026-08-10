package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const zrokPinnedVersion = "2.0.4"

func resolveZrokBinary(configPath, dataDir string, force bool, log func(string, ...any)) (string, error) {
	if !force {
		if p := strings.TrimSpace(configPath); p != "" && p != "zrok2" {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
		if p, err := exec.LookPath("zrok2"); err == nil {
			return p, nil
		}
		if dataDir != "" {
			cached := filepath.Join(dataDir, "bin", "zrok2")
			if cachedBinaryCurrent(cached, zrokPinnedVersion) {
				return cached, nil
			}
		}
	}
	if dataDir == "" {
		return "", errors.New("no APTEVA_DATA_DIR available — cannot auto-install zrok2; install it manually and set zrok2_path")
	}

	installMu.Lock()
	defer installMu.Unlock()
	cached := filepath.Join(dataDir, "bin", "zrok2")
	if !force && cachedBinaryCurrent(cached, zrokPinnedVersion) {
		return cached, nil
	}
	if log != nil {
		log("downloading zrok2", "version", zrokPinnedVersion, "os", runtime.GOOS, "arch", runtime.GOARCH, "dest", cached)
	}
	if err := downloadZrok(cached); err != nil {
		return "", err
	}
	return cached, nil
}

func downloadZrok(dest string) error {
	artifact, err := zrokArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
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
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		in.Close()
		return err
	}
	extractErr := extractFromTGZBounded(in, "zrok2", out, artifact.MaxExtracted)
	closeInErr := in.Close()
	closeOutErr := out.Close()
	if extractErr != nil {
		return extractErr
	}
	if closeInErr != nil {
		return closeInErr
	}
	if closeOutErr != nil {
		return closeOutErr
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return writeVersionMarker(dest, artifact.Version)
}

func zrokArtifact(goos, goarch string) (binaryArtifact, error) {
	const base = "https://github.com/openziti/zrok/releases/download/v2.0.4/"
	artifact := binaryArtifact{
		Version: zrokPinnedVersion, Archived: true,
		// zrok2 v2.0.4 is 87-92 MiB depending on platform. Keep the
		// extraction bounded while leaving headroom for every pinned asset.
		MaxDownload: 40 << 20, MaxExtracted: 128 << 20,
		AllowedHosts: map[string]bool{
			"github.com": true, "release-assets.githubusercontent.com": true,
			"objects.githubusercontent.com": true,
		},
	}
	switch goos + "/" + goarch {
	case "linux/amd64":
		artifact.URL, artifact.SHA256 = base+"zrok_2.0.4_linux_amd64.tar.gz", "1877981b9050c9d69c61bc12c0b92c2da7330e3fbb374faa78ffdbfa37f8a8e3"
	case "linux/arm64":
		artifact.URL, artifact.SHA256 = base+"zrok_2.0.4_linux_arm64.tar.gz", "71a08d11058959a0b90e8f59d4a33612b5fc010fced8c65883995cf64e5502cc"
	case "darwin/amd64":
		artifact.URL, artifact.SHA256 = base+"zrok_2.0.4_darwin_amd64.tar.gz", "d0d0882d84768081c7cbd45c03490bd13e305d19861eaf4811a11e6eb1db5924"
	case "darwin/arm64":
		artifact.URL, artifact.SHA256 = base+"zrok_2.0.4_darwin_arm64.tar.gz", "ad90ee0730bdd066a0a95c1f57bb250bac1b2d5c474ba662043820ea0d2b7e86"
	default:
		return binaryArtifact{}, fmt.Errorf("zrok2 auto-install unsupported on %s/%s — install it manually and set zrok2_path", goos, goarch)
	}
	return artifact, nil
}
