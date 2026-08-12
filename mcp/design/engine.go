package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
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

//go:embed runner/dist/runner.mjs runner/dist/replicad_single.wasm
var runnerAssets embed.FS

const bunVersion = "1.2.22"

var bunArchives = map[string]struct {
	Name   string
	SHA256 string
}{
	"darwin/arm64": {"bun-darwin-aarch64.zip", "eb8c7e09cbea572414a0a367848e1acbf05294a946a594405a014b1fb3b3fc76"},
	"darwin/amd64": {"bun-darwin-x64.zip", "a7484721a7ead45887c812e760b124047e663173cf2a3ba7c5aa1992cb22cd3e"},
	"linux/arm64":  {"bun-linux-aarch64.zip", "a97c687fb5e54de4e2fb0869a7ac9a2d9c3af75ac182e2b68138c1dd8f98131b"},
	"linux/amd64":  {"bun-linux-x64.zip", "4c446af1a01d7b40e1e11baebc352f9b2bfd12887e51b97dd3b59879cee2743a"},
}

type Engine struct {
	root        string
	explicitBun string
	timeout     time.Duration
	client      *http.Client
	installMu   sync.Mutex
	buildSlots  chan struct{}
}

func NewEngine(root, explicitBun string, timeout time.Duration) (*Engine, error) {
	if root == "" {
		return nil, errors.New("engine root required")
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	engine := &Engine{
		root:        root,
		explicitBun: strings.TrimSpace(explicitBun),
		timeout:     timeout,
		client:      &http.Client{Timeout: 2 * time.Minute},
		buildSlots:  make(chan struct{}, 2),
	}
	if err := engine.materializeAssets(); err != nil {
		return nil, err
	}
	return engine, nil
}

func (e *Engine) materializeAssets() error {
	dir := filepath.Join(e.root, "runner", engineVersion)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"runner.mjs", "replicad_single.wasm"} {
		body, err := runnerAssets.ReadFile("runner/dist/" + name)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, name)
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, body) {
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, body, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) bun(ctx context.Context) (string, error) {
	if e.explicitBun != "" {
		if _, err := os.Stat(e.explicitBun); err != nil {
			return "", fmt.Errorf("configured bun_path: %w", err)
		}
		return e.explicitBun, nil
	}
	if path, err := exec.LookPath("bun"); err == nil {
		return path, nil
	}
	path := filepath.Join(e.root, "bin", "bun-"+bunVersion)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	e.installMu.Lock()
	defer e.installMu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	archive, ok := bunArchives[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no pinned Bun runtime for %s/%s; configure bun_path", runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://github.com/oven-sh/bun/releases/download/bun-v%s/%s", bunVersion, archive.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download pinned Bun runtime: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("download pinned Bun runtime: HTTP %d", resp.StatusCode)
	}
	compressed, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(compressed)
	if hex.EncodeToString(hash[:]) != archive.SHA256 {
		return "", errors.New("downloaded Bun runtime checksum mismatch")
	}
	zr, err := zip.NewReader(bytes.NewReader(compressed), int64(len(compressed)))
	if err != nil {
		return "", err
	}
	var executable []byte
	for _, file := range zr.File {
		base := filepath.Base(file.Name)
		if base != "bun" && base != "bun.exe" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", err
		}
		executable, err = io.ReadAll(io.LimitReader(rc, 256<<20))
		rc.Close()
		if err != nil {
			return "", err
		}
		break
	}
	if len(executable) == 0 {
		return "", errors.New("Bun archive did not contain its executable")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, executable, 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func (e *Engine) Build(parent context.Context, name string, definition, parameters []byte, formats []string) (*EngineResult, error) {
	select {
	case e.buildSlots <- struct{}{}:
		defer func() { <-e.buildSlots }()
	case <-parent.Done():
		return nil, parent.Err()
	}
	ctx, cancel := context.WithTimeout(parent, e.timeout)
	defer cancel()
	bunPath, err := e.bun(ctx)
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp(filepath.Join(e.root, "tmp"), "build-")
	if err != nil {
		if err := os.MkdirAll(filepath.Join(e.root, "tmp"), 0o700); err != nil {
			return nil, err
		}
		workDir, err = os.MkdirTemp(filepath.Join(e.root, "tmp"), "build-")
		if err != nil {
			return nil, err
		}
	}
	defer os.RemoveAll(workDir)
	var def any
	var params any
	if err := json.Unmarshal(definition, &def); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(parameters, &params); err != nil {
		return nil, err
	}
	request := map[string]any{
		"name": name, "definition": def, "parameters": params, "formats": formats,
		"output_dir": filepath.Join(workDir, "output"), "tolerance": 0.1, "angular_tolerance": 0.1,
	}
	input, _ := json.Marshal(request)
	runnerDir := filepath.Join(e.root, "runner", engineVersion)
	cmd := exec.CommandContext(ctx, bunPath, filepath.Join(runnerDir, "runner.mjs"))
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{
		"HOME=" + workDir,
		"TMPDIR=" + workDir,
		"PATH=" + filepath.Dir(bunPath),
		"NO_COLOR=1",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	runErr := cmd.Run()
	duration := time.Since(started)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("geometry build exceeded %s", e.timeout)
	}
	result, parseErr := parseRunnerOutput(stdout.Bytes())
	if parseErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if len(message) > 4000 {
			message = message[len(message)-4000:]
		}
		return nil, fmt.Errorf("geometry runner failed: %v: %s", runErr, message)
	}
	result.Duration = duration
	if !result.OK {
		return result, errors.New(result.Error)
	}
	if runErr != nil {
		return result, fmt.Errorf("geometry runner: %w", runErr)
	}
	outputRoot := filepath.Join(workDir, "output")
	for index := range result.Artifacts {
		path, err := filepath.Abs(result.Artifacts[index].Path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(outputRoot, path)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return nil, errors.New("runner returned an artifact outside its output directory")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		persisted := filepath.Join(e.root, "staging", sourceHash(definition, parameters), filepath.Base(path))
		if err := os.MkdirAll(filepath.Dir(persisted), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(persisted, body, 0o600); err != nil {
			return nil, err
		}
		result.Artifacts[index].Path = persisted
	}
	return result, nil
}

func parseRunnerOutput(output []byte) (*EngineResult, error) {
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	for index := len(lines) - 1; index >= 0; index-- {
		line := bytes.TrimSpace(lines[index])
		if !bytes.HasPrefix(line, []byte(`{"ok":`)) {
			continue
		}
		var result EngineResult
		if err := json.Unmarshal(line, &result); err == nil {
			return &result, nil
		}
	}
	return nil, errors.New("runner did not return structured JSON")
}
