package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fixed releases, not moving latest URLs. Render dependencies are prepared on
// the worker; the parent contains no host installation implementation.
const ffmpegRelease = "autobuild-2026-10-25-13-00"
const chromeRelease = "156.0.8078.80"
const nodeRelease = "v24.22.1"

type artifact struct{ Name, SHA string }

var ffmpegArtifacts = map[string]artifact{
	"amd64": {"ffmpeg-n8.1.3-24-g92611900bb-linux64-gpl-8.1.tar.xz", "3300aa8c1d68e79e20d22117d3748a6121a49fe09b7eece3dcc38b84a1104e71"},
	"arm64": {"ffmpeg-n8.1.3-24-g92611900bb-linuxarm64-gpl-8.1.tar.xz", "e9e98391369a863e4e391ca7c1e280fddc51d84604c4e9a842a39c133fa9c6cb"},
}

// Chrome for Testing publishes the headless shell per platform directory.
var chromeArtifacts = map[string]artifact{
	"amd64": {"linux64/chrome-headless-shell-linux64.zip", "547dcd374e02961bf998f6046a267644f8502225c36bb78ca307dab32a0b3eb4"},
	"arm64": {"linux-arm64/chrome-headless-shell-linux-arm64.zip", "5bb6a00a4285c13303d3f72c5afdb70f144ea7a93eed462365a52b69631c9607"},
}

// Checksums from https://nodejs.org/dist/v24.22.1/SHASUMS256.txt.
var nodeArtifacts = map[string]artifact{
	"amd64": {"node-v24.22.1-linux-x64.tar.gz", "eb6728e77c576a35162c122c45ccadf224d89ff00abc8dae46cc2897c6002d7c"},
	"arm64": {"node-v24.22.1-linux-arm64.tar.gz", "33c37be234ba8bee041961047b19160f4d7d0f164e8e246f78bdd10feff148b3"},
}

func (a *RendererApp) prepareFFmpeg(ctx context.Context) error {
	if a.ffmpeg != "" && a.ffprobe != "" {
		return nil
	}
	source, ok := ffmpegArtifacts[runtime.GOARCH]
	if runtime.GOOS != "linux" || !ok {
		return errors.New("install FFmpeg/FFprobe on this worker target or configure their executable paths")
	}
	target := filepath.Join(a.root, "dependencies", "ffmpeg-"+ffmpegRelease)
	ff, probe := filepath.Join(target, "bin", "ffmpeg"), filepath.Join(target, "bin", "ffprobe")
	if executable(ff, "") != "" && executable(probe, "") != "" {
		a.ffmpeg, a.ffprobe = ff, probe
		return nil
	}
	err := installDependency(ctx, target, "https://github.com/BtbN/FFmpeg-Builds/releases/download/"+ffmpegRelease+"/"+source.Name, source.SHA, 300<<20,
		func(archive, unpack string) error {
			// Only the two tools the renderer uses; ffplay and docs are not unpacked.
			cmd := exec.CommandContext(ctx, "tar", "-xJf", archive, "--strip-components=1", "-C", unpack, "--wildcards", "*/bin/ffmpeg", "*/bin/ffprobe")
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("FFmpeg dependency extraction failed: %s", strings.TrimSpace(string(out)))
			}
			if executable(filepath.Join(unpack, "bin", "ffmpeg"), "") == "" || executable(filepath.Join(unpack, "bin", "ffprobe"), "") == "" {
				return errors.New("FFmpeg dependency binaries missing")
			}
			return nil
		})
	if err != nil {
		return err
	}
	a.ffmpeg, a.ffprobe = ff, probe
	return nil
}

// installDependency downloads a pinned artifact, verifies its checksum, unpacks
// it into a staging directory and renames that into place.
func installDependency(ctx context.Context, target, source, sha string, limit int64, unpack func(archive, dir string) error) error {
	root := filepath.Dir(target)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("dependency download unavailable: %s", filepath.Base(source))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("dependency %s HTTP %d", filepath.Base(source), resp.StatusCode)
	}
	archive := filepath.Join(staging, "archive")
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, limit))
	ce := file.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if n >= limit || hex.EncodeToString(hash.Sum(nil)) != sha {
		return fmt.Errorf("dependency %s checksum mismatch", filepath.Base(source))
	}
	dir := filepath.Join(staging, "unpack")
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	if err = unpack(archive, dir); err != nil {
		return err
	}
	_ = os.Remove(archive)
	return os.Rename(dir, target)
}

type browserPaths struct {
	Chrome    string   `json:"chrome"`
	Node      string   `json:"node"`
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// browserDeps resolves Chrome and Node once per worker process. Concurrent
// browser jobs share one provisioning attempt; a failure is retried by the next.
type browserDeps struct {
	prepare      sync.Mutex // serializes provisioning; mu guards reported state
	mu           sync.Mutex
	chromeConfig string
	nodeConfig   string
	ready        *browserPaths
	lastError    string
	missingLibs  []string
}

func (b *browserDeps) status() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]any{"chrome_release": chromeRelease, "node_release": nodeRelease, "ready": b.ready != nil}
	if b.ready != nil {
		out["chrome"], out["node"] = b.ready.Chrome, b.ready.Node
	}
	if b.lastError != "" {
		out["error"] = b.lastError
	}
	if len(b.missingLibs) > 0 {
		out["missing_libraries"] = b.missingLibs
	}
	return out
}

func (b *browserDeps) ensure(ctx context.Context, root string) (browserPaths, error) {
	b.prepare.Lock()
	defer b.prepare.Unlock()
	b.mu.Lock()
	ready := b.ready
	b.mu.Unlock()
	if ready != nil {
		return *ready, nil
	}
	paths, missing, err := resolveBrowser(ctx, root, b.chromeConfig, b.nodeConfig)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.missingLibs = missing
	if err != nil {
		b.lastError = err.Error()
		return browserPaths{}, err
	}
	b.lastError = ""
	b.ready = &paths
	return paths, nil
}

func resolveBrowser(ctx context.Context, root, chromeConfig, nodeConfig string) (browserPaths, []string, error) {
	var paths browserPaths
	node, err := resolveNode(ctx, root, nodeConfig)
	if err != nil {
		return paths, nil, err
	}
	chrome, err := resolveChrome(ctx, root, chromeConfig)
	if err != nil {
		return paths, nil, err
	}
	if missing := missingLibraries(ctx, chrome); len(missing) > 0 {
		return paths, missing, fmt.Errorf("Chrome is missing system libraries on this target: %s", strings.Join(missing, ", "))
	}
	paths.Chrome, paths.Node = chrome, node
	if os.Geteuid() == 0 {
		// Chrome refuses to run its sandbox as root; service users keep it.
		paths.ExtraArgs = append(paths.ExtraArgs, "--no-sandbox")
	}
	if runtime.GOOS == "linux" {
		// Small hosts mount a tiny /dev/shm; render through /tmp instead.
		paths.ExtraArgs = append(paths.ExtraArgs, "--disable-dev-shm-usage")
	}
	return paths, nil, nil
}

var nodeVersion = regexp.MustCompile(`^v(\d+)\.`)

func nodeUsable(ctx context.Context, path string) bool {
	if path == "" {
		return false
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return false
	}
	m := nodeVersion.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	return major >= 22 // global WebSocket and fetch drive the CDP capture.
}

func resolveNode(ctx context.Context, root, configured string) (string, error) {
	if configured != "" {
		if path := executable(configured, ""); nodeUsable(ctx, path) {
			return path, nil
		}
		return "", fmt.Errorf("configured node_path %q is not a Node.js 22+ executable", configured)
	}
	if path := executable("node", ""); nodeUsable(ctx, path) {
		return path, nil
	}
	source, ok := nodeArtifacts[runtime.GOARCH]
	if runtime.GOOS != "linux" || !ok {
		return "", errors.New("install Node.js 22+ on this worker target or configure node_path")
	}
	target := filepath.Join(root, "dependencies", "node-"+nodeRelease)
	bin := filepath.Join(target, "bin", "node")
	if nodeUsable(ctx, bin) {
		return bin, nil
	}
	err := installDependency(ctx, target, "https://nodejs.org/dist/"+nodeRelease+"/"+source.Name, source.SHA, 200<<20,
		func(archive, dir string) error {
			member := strings.TrimSuffix(source.Name, ".tar.gz") + "/bin/node"
			return extractTarGzMember(archive, member, filepath.Join(dir, "bin", "node"))
		})
	if err != nil {
		return "", err
	}
	if !nodeUsable(ctx, bin) {
		return "", errors.New("provisioned Node.js does not run on this target")
	}
	return bin, nil
}

func resolveChrome(ctx context.Context, root, configured string) (string, error) {
	if configured != "" {
		if path := executable(configured, ""); path != "" {
			return path, nil
		}
		return "", fmt.Errorf("configured chrome_path %q is not executable", configured)
	}
	candidates := []string{"chrome-headless-shell", "chromium", "chromium-browser", "google-chrome-stable", "google-chrome"}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"}, candidates...)
	}
	for _, c := range candidates {
		if path := executable(c, ""); path != "" {
			return path, nil
		}
	}
	source, ok := chromeArtifacts[runtime.GOARCH]
	if runtime.GOOS != "linux" || !ok {
		return "", errors.New("install Chrome/Chromium on this worker target or configure chrome_path")
	}
	target := filepath.Join(root, "dependencies", "chrome-headless-shell-"+chromeRelease)
	bin := filepath.Join(target, strings.TrimSuffix(filepath.Base(source.Name), ".zip"), "chrome-headless-shell")
	if executable(bin, "") != "" {
		return bin, nil
	}
	err := installDependency(ctx, target, "https://storage.googleapis.com/chrome-for-testing-public/"+chromeRelease+"/"+source.Name, source.SHA, 400<<20,
		func(archive, dir string) error { return extractZip(archive, dir, 1<<30) })
	if err != nil {
		return "", err
	}
	if executable(bin, "") == "" {
		return "", errors.New("Chrome headless shell binary missing after provisioning")
	}
	return bin, nil
}

// missingLibraries lists shared objects the dynamic loader cannot resolve. The
// native backend does not install host packages; operators get exact names.
func missingLibraries(ctx context.Context, binary string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	out, err := exec.CommandContext(ctx, "ldd", binary).CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "not found") {
			if name := strings.TrimSpace(strings.SplitN(line, "=>", 2)[0]); name != "" {
				seen[name] = true
			}
		}
	}
	missing := make([]string, 0, len(seen))
	for name := range seen {
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

func extractTarGzMember(archive, member, destination string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in archive", member)
		}
		if err != nil {
			return err
		}
		if h.Name != member || h.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, 512<<20))
		if ce := out.Close(); err == nil {
			err = ce
		}
		return err
	}
}

// extractZip unpacks regular files and directories, rejecting paths that escape
// dir. Chrome's executables keep their mode bits from the archive.
func extractZip(archive, dir string, limit int64) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	var total int64
	for _, f := range r.File {
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path %q", f.Name)
		}
		path := filepath.Join(dir, name)
		mode := f.Mode()
		if mode.IsDir() {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > limit {
			return errors.New("archive exceeds expanded size limit")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		perm := os.FileMode(0600)
		if mode&0o111 != 0 {
			perm = 0700
		}
		dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			src.Close()
			return err
		}
		_, err = io.Copy(dst, io.LimitReader(src, int64(f.UncompressedSize64)+1))
		src.Close()
		if ce := dst.Close(); err == nil {
			err = ce
		}
		if err != nil {
			return err
		}
	}
	return nil
}
