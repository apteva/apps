package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Commands are executable argument arrays; shell scripts are an explicit choice.
// No engine names or publisher-specific behavior belongs in this contract.
type commandStage struct {
	Name           string            `json:"name"`
	Command        []string          `json:"command"`
	Directory      string            `json:"directory,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
}
type commandPipeline struct {
	OS             string         `json:"os,omitempty"`
	Arch           string         `json:"arch,omitempty"`
	Prepare        []commandStage `json:"prepare,omitempty"`
	BuildDirectory string         `json:"build_directory,omitempty"`
	Tests          []commandStage `json:"tests,omitempty"`
	Outputs        []string       `json:"outputs,omitempty"`
}
type pipelineEvidence struct {
	ArtifactSHA256 string   `json:"artifact_sha256"`
	ConfigSHA256   string   `json:"config_sha256"`
	Tests          []string `json:"tests"`
	CompletedAt    string   `json:"completed_at"`
}

func pipelineConfig(raw string) (*commandPipeline, error) {
	var cfg struct {
		Pipeline *commandPipeline `json:"pipeline"`
	}
	if err := json.Unmarshal([]byte(defaultStr(raw, "{}")), &cfg); err != nil {
		return nil, err
	}
	if cfg.Pipeline == nil {
		return nil, nil
	}
	names := map[string]bool{}
	for _, stage := range append(append([]commandStage{}, cfg.Pipeline.Prepare...), cfg.Pipeline.Tests...) {
		if stage.Name == "" || names[stage.Name] || len(stage.Command) == 0 || stage.Command[0] == "" || stage.TimeoutSeconds < 0 || stage.TimeoutSeconds > 86400 {
			return nil, errors.New("pipeline stages require unique names, commands and a timeout up to 86400 seconds")
		}
		names[stage.Name] = true
	}
	return cfg.Pipeline, nil
}
func hasCommandPipeline(raw string) bool { p, e := pipelineConfig(raw); return e == nil && p != nil }
func pipelinePath(root, relative string) (string, error) {
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if relative == "" || relative == "." {
		return root, nil
	}
	if filepath.IsAbs(relative) || strings.Contains(relative, "\\") {
		return "", errors.New("pipeline paths must be relative")
	}
	path := filepath.Join(root, relative)
	rel, e := filepath.Rel(root, path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("pipeline path escapes root")
	}
	// Resolve links only for existing inputs; declared output checks run after execution.
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return "", e
	}
	rr, e := filepath.Rel(root, resolved)
	if e != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(filepath.Separator)) {
		return "", errors.New("pipeline symlink escapes root")
	}
	return resolved, nil
}
func runCommandStage(ctx context.Context, stage commandStage, root, src, artifact string, env map[string]string, log io.Writer) error {
	dir, e := pipelinePath(root, stage.Directory)
	if e != nil {
		return e
	}
	merged := map[string]string{}
	for k, v := range env {
		merged[k] = v
	}
	for k, v := range stage.Env {
		merged[k] = v
	}
	merged["DEPLOY_SOURCE_DIR"] = src
	merged["DEPLOY_ARTIFACT_DIR"] = artifact
	timeout := stage.TimeoutSeconds
	if timeout == 0 {
		timeout = 1800
	}
	child, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	fmt.Fprintf(log, "stage %s started\n", stage.Name)
	cmd := newBuildCommand(child, stage.Command[0], stage.Command[1:]...)
	cmd.Dir = dir
	cmd.Env = workloadEnv(merged)
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("stage %s failed: %w", stage.Name, e)
	}
	for _, output := range stage.Outputs {
		if _, e = pipelinePath(root, output); e != nil {
			return fmt.Errorf("stage %s missing output %s: %w", stage.Name, output, e)
		}
	}
	fmt.Fprintf(log, "stage %s passed\n", stage.Name)
	return nil
}
func buildWithPipeline(builder Builder, src, dist string, ov BuildOverrides, log io.Writer) (string, error) {
	p, e := pipelineConfig(ov.TargetConfigJSON)
	if e != nil {
		return "", e
	}
	if p == nil {
		return builder.Build(src, dist, ov, log)
	}
	if p.OS != "" && p.OS != runtime.GOOS {
		return "", fmt.Errorf("pipeline requires OS %s; runner is %s", p.OS, runtime.GOOS)
	}
	if p.Arch != "" && p.Arch != runtime.GOARCH {
		return "", fmt.Errorf("pipeline requires architecture %s", p.Arch)
	}
	src, _ = filepath.EvalSymlinks(src)
	dist, _ = filepath.EvalSymlinks(dist)
	src, _ = filepath.Abs(src)
	dist, _ = filepath.Abs(dist)
	ctx := buildContext(ov)
	for _, stage := range p.Prepare {
		if e = runCommandStage(ctx, stage, src, src, dist, ov.Env, log); e != nil {
			return "", e
		}
	}
	buildDir, e := pipelinePath(src, p.BuildDirectory)
	if e != nil {
		return "", e
	}
	entry, e := builder.Build(buildDir, dist, ov, log)
	if e != nil {
		return "", e
	}
	if len(p.Outputs) == 0 {
		return "", errors.New("pipeline must declare artifact outputs")
	}
	for _, output := range p.Outputs {
		if _, e = pipelinePath(dist, output); e != nil {
			return "", fmt.Errorf("missing artifact output %s: %w", output, e)
		}
	}
	before, e := artifactTreeDigest(dist)
	if e != nil {
		return "", e
	}
	tests := []string{}
	for _, stage := range p.Tests {
		if e = runCommandStage(ctx, stage, dist, src, dist, ov.Env, log); e != nil {
			return "", e
		}
		tests = append(tests, stage.Name)
	}
	after, e := artifactTreeDigest(dist)
	if e != nil {
		return "", e
	}
	if before != after {
		return "", errors.New("tests modified release artifacts; produce test reports outside DEPLOY_ARTIFACT_DIR")
	}
	var manifest artifactManifest
	if body, e := os.ReadFile(filepath.Join(dist, artifactManifestFilename)); e == nil {
		if e = json.Unmarshal(body, &manifest); e != nil {
			return "", e
		}
	}
	manifest.Pipeline = &pipelineEvidence{ArtifactSHA256: after, ConfigSHA256: jsonDigest(p), Tests: tests, CompletedAt: nowUTC()}
	if e = writeArtifactManifest(dist, manifest); e != nil {
		return "", e
	}
	return entry, nil
}
func jsonDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Include names, permissions, links and bytes; exclude only the root evidence manifest.
func artifactTreeDigest(root string) (string, error) {
	var names []string
	e := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if path != root && path != filepath.Join(root, artifactManifestFilename) {
			names = append(names, path)
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	sort.Strings(names)
	h := sha256.New()
	for _, path := range names {
		info, e := os.Lstat(path)
		if e != nil {
			return "", e
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(path)
			if e != nil {
				return "", e
			}
			if _, e = pipelinePath(root, rel); e != nil {
				return "", e
			}
			fmt.Fprint(h, target)
		} else if info.Mode().IsRegular() {
			f, e := os.Open(path)
			if e != nil {
				return "", e
			}
			_, e = io.Copy(h, f)
			f.Close()
			if e != nil {
				return "", e
			}
		} else if !info.IsDir() {
			return "", errors.New("unsupported artifact file type")
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type commandBuilder struct{}

func (*commandBuilder) Framework() string { return "command" }
func (*commandBuilder) Build(src, dist string, ov BuildOverrides, log io.Writer) (string, error) {
	if strings.TrimSpace(ov.BuildCmd) == "" {
		return "", errors.New("command builder requires build_cmd")
	}
	stage := commandStage{Name: "build", Command: []string{"sh", "-c", ov.BuildCmd}}
	if e := runCommandStage(buildContext(ov), stage, src, src, dist, ov.Env, log); e != nil {
		return "", e
	}
	return "", nil
}
