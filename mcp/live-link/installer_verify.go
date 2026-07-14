package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type binaryArtifact struct {
	URL          string
	SHA256       string
	Version      string
	Archived     bool
	MaxDownload  int64
	MaxExtracted int64
	AllowedHosts map[string]bool
}

func downloadVerified(artifact binaryArtifact, archivePath string) error {
	if err := validateArtifactURL(artifact); err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many download redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("download redirect refused non-HTTPS URL")
			}
			if !artifact.AllowedHosts[strings.ToLower(req.URL.Hostname())] {
				return fmt.Errorf("download redirected to untrusted host %q", req.URL.Hostname())
			}
			return nil
		},
	}
	req, err := http.NewRequest(http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "apteva-live-link/0.5")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifact.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", artifact.URL, resp.StatusCode)
	}
	if resp.ContentLength > artifact.MaxDownload {
		return fmt.Errorf("download is too large: %d bytes (limit %d)", resp.ContentLength, artifact.MaxDownload)
	}

	out, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, artifact.MaxDownload+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(archivePath)
		return fmt.Errorf("download asset: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(archivePath)
		return closeErr
	}
	if n > artifact.MaxDownload {
		_ = os.Remove(archivePath)
		return fmt.Errorf("download exceeded %d-byte limit", artifact.MaxDownload)
	}
	got := fmt.Sprintf("%x", h.Sum(nil))
	if !strings.EqualFold(got, artifact.SHA256) {
		_ = os.Remove(archivePath)
		return fmt.Errorf("download checksum mismatch: got %s, want %s", got, artifact.SHA256)
	}
	return nil
}

func validateArtifactURL(artifact binaryArtifact) error {
	u, err := url.Parse(artifact.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("invalid pinned artifact URL %q", artifact.URL)
	}
	if !artifact.AllowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("artifact host %q is not allowlisted", u.Hostname())
	}
	checksum, err := hex.DecodeString(artifact.SHA256)
	if err != nil || len(checksum) != sha256.Size {
		return fmt.Errorf("artifact checksum must be 64 hexadecimal characters")
	}
	if artifact.MaxDownload <= 0 {
		return fmt.Errorf("artifact download limit must be positive")
	}
	if artifact.Archived && artifact.MaxExtracted <= 0 {
		return fmt.Errorf("archived artifact extraction limit must be positive")
	}
	return nil
}

func cachedBinaryCurrent(path, version string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Mode().Perm()&0o111 == 0 {
		return false
	}
	b, err := os.ReadFile(path + ".version")
	return err == nil && strings.TrimSpace(string(b)) == version
}

func writeVersionMarker(path, version string) error {
	tmp := path + ".version.part"
	if err := os.WriteFile(tmp, []byte(version+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path+".version")
}

func prepareInstallPaths(dest string) (archive, output string, cleanup func()) {
	archive = dest + ".download"
	output = dest + ".part"
	cleanup = func() {
		_ = os.Remove(archive)
		_ = os.Remove(output)
	}
	return
}

func ensureInstallDir(dest string) error {
	return os.MkdirAll(filepath.Dir(dest), 0o700)
}
