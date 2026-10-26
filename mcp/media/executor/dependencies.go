package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// A fixed release, not a moving latest URL. Processing dependencies are prepared
// on the worker; the parent contains no host installation implementation.
const ffmpegRelease = "autobuild-2026-10-25-13-00"

var ffmpegArtifacts = map[string]struct{ Name, SHA string }{
	"amd64": {"ffmpeg-n8.1.3-24-g92611900bb-linux64-gpl-8.1.tar.xz", "3300aa8c1d68e79e20d22117d3748a6121a49fe09b7eece3dcc38b84a1104e71"},
	"arm64": {"ffmpeg-n8.1.3-24-g92611900bb-linuxarm64-gpl-8.1.tar.xz", "e9e98391369a863e4e391ca7c1e280fddc51d84604c4e9a842a39c133fa9c6cb"},
}

func (a *ExecutorApp) prepareFFmpeg(ctx context.Context) error {
	if a.ffmpeg != "" && a.ffprobe != "" {
		return nil
	}
	source, ok := ffmpegArtifacts[runtime.GOARCH]
	if runtime.GOOS != "linux" || !ok {
		return errors.New("install FFmpeg/FFprobe on this worker target or configure their executable paths")
	}
	target := filepath.Join(a.root, "dependencies", ffmpegRelease)
	ff, probe := filepath.Join(target, "bin", "ffmpeg"), filepath.Join(target, "bin", "ffprobe")
	if executable(ff, "") != "" && executable(probe, "") != "" {
		a.ffmpeg = ff
		a.ffprobe = probe
		return nil
	}
	root := filepath.Dir(target)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".ffmpeg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/BtbN/FFmpeg-Builds/releases/download/"+ffmpegRelease+"/"+source.Name, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 110 * time.Second}).Do(req)
	if err != nil {
		return errors.New("FFmpeg dependency download unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("FFmpeg dependency HTTP %d", resp.StatusCode)
	}
	archive := filepath.Join(staging, "ffmpeg.tar.xz")
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, 300<<20))
	ce := file.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if n >= 300<<20 || hex.EncodeToString(hash.Sum(nil)) != source.SHA {
		return errors.New("FFmpeg dependency checksum mismatch")
	}
	unpack := filepath.Join(staging, "unpack")
	if err = os.Mkdir(unpack, 0700); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "tar", "-xJf", archive, "--strip-components=1", "-C", unpack)
	if err = cmd.Run(); err != nil {
		return errors.New("FFmpeg dependency extraction failed")
	}
	if executable(filepath.Join(unpack, "bin", "ffmpeg"), "") == "" || executable(filepath.Join(unpack, "bin", "ffprobe"), "") == "" {
		return errors.New("FFmpeg dependency binaries missing")
	}
	if err = os.Rename(unpack, target); err != nil {
		return err
	}
	a.ffmpeg = ff
	a.ffprobe = probe
	return nil
}
