package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// SourceFetcher unpacks a Deployment's source spec into a working
// directory the builder can operate on. Each kind plugs in here; the
// builder above never knows where the bytes came from.
type SourceFetcher interface {
	Kind() string
	Fetch(d *Deployment, destDir string) error
}

// fetchSource is the dispatch point. Adds new kinds here as they ship.
func fetchSource(ctx *sdk.AppCtx, d *Deployment, destDir string, cfg sourceConfig) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	switch d.SourceKind {
	case "code":
		return newCodeFetcher(ctx).Fetch(d, destDir)
	case "local":
		return (&localFetcher{}).Fetch(d, destDir)
	default:
		return fmt.Errorf("unsupported source_kind %q (v0.1 supports: code, local)", d.SourceKind)
	}
}

type sourceConfig struct {
	ProjectID string
	InstallID string
}

// ─── code source ──────────────────────────────────────────────────

type codeFetcher struct {
	platform      sdk.PlatformClient
	httpClient    *http.Client
	gatewayURL    string
	appToken      string
	codeInstallID int64
}

func (f *codeFetcher) Kind() string { return "code" }

func newCodeFetcher(ctx *sdk.AppCtx) *codeFetcher {
	f := &codeFetcher{
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		gatewayURL: strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"),
		appToken:   os.Getenv("APTEVA_APP_TOKEN"),
	}
	if ctx != nil {
		f.platform = ctx.PlatformAPI()
		if bound := ctx.IntegrationFor("code"); bound != nil && bound.Kind == "app" {
			f.codeInstallID = bound.InstallID
		}
	}
	return f
}

// Fetch streams the Code app's REST zip export through the platform
// proxy. The older app-to-app MCP path returned the whole zip as
// base64 JSON and hit the callback response cap for medium repos.
// Keep that path only as a no-gateway fallback for tests/legacy envs.
func (f *codeFetcher) Fetch(d *Deployment, destDir string) error {
	if d.SourceRef == "" {
		return errors.New("source_ref (repo slug) required for kind=code")
	}
	if f.canStreamExport() {
		return f.fetchViaHTTPExport(d, destDir)
	}
	return f.fetchViaMCPExport(d, destDir)
}

func (f *codeFetcher) canStreamExport() bool {
	return strings.TrimSpace(f.gatewayURL) != "" && strings.TrimSpace(f.appToken) != ""
}

func (f *codeFetcher) fetchViaHTTPExport(d *Deployment, destDir string) error {
	tmp, err := os.CreateTemp("", "apteva-deploy-code-*.zip")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	req, err := http.NewRequest(http.MethodGet, f.exportURL(d), nil)
	if err != nil {
		tmp.Close()
		return err
	}
	req.Header.Set("Authorization", "Bearer "+f.appToken)
	resp, err := f.httpClient.Do(req)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("stream code export: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		tmp.Close()
		return fmt.Errorf("stream code export: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("stream code export: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return fmt.Errorf("code export: not a valid zip: %w", err)
	}
	defer zr.Close()
	return unpackZip(&zr.Reader, destDir)
}

func (f *codeFetcher) exportURL(d *Deployment) string {
	base := strings.TrimRight(f.gatewayURL, "/")
	q := url.Values{}
	if d.ProjectID != "" {
		q.Set("project_id", d.ProjectID)
	}
	if f.codeInstallID > 0 {
		q.Set("install_id", strconv.FormatInt(f.codeInstallID, 10))
	}
	u := base + "/api/apps/code/api/repos/" + url.PathEscape(d.SourceRef) + "/export"
	if encoded := q.Encode(); encoded != "" {
		u += "?" + encoded
	}
	return u
}

func (f *codeFetcher) fetchViaMCPExport(d *Deployment, destDir string) error {
	if f.platform == nil {
		return errors.New("platform client unavailable; deploy app not fully mounted")
	}
	args := map[string]any{
		"slug":        d.SourceRef,
		"_project_id": d.ProjectID,
	}
	raw, err := f.platform.CallApp("code", "repos_export", args)
	if err != nil {
		return fmt.Errorf("call code.repos_export: %w", err)
	}
	zipBytes, err := decodeRepoExport(raw)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return fmt.Errorf("code export: not a valid zip: %w", err)
	}
	return unpackZip(zr, destDir)
}

// decodeRepoExport unwraps the JSON-RPC envelope CallApp returned and
// pulls the repo zip out of the tool's `zip_b64` field. The full path
// is: JSON-RPC response → result.content[0].text → JSON object with
// {slug, size, sha256, zip_b64}.
func decodeRepoExport(raw json.RawMessage) ([]byte, error) {
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode mcp envelope: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("code.repos_export error %d: %s", env.Error.Code, env.Error.Message)
	}
	content, _ := env.Result["content"].([]any)
	if len(content) == 0 {
		return nil, errors.New("code.repos_export returned empty content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" {
		return nil, errors.New("code.repos_export returned no text payload")
	}
	var payload struct {
		ZipB64 string `json:"zip_b64"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil, fmt.Errorf("decode export payload: %w", err)
	}
	if payload.ZipB64 == "" {
		return nil, errors.New("code.repos_export returned no zip_b64")
	}
	zipBytes, err := base64.StdEncoding.DecodeString(payload.ZipB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode zip: %w", err)
	}
	if payload.SHA256 != "" {
		got := sha256.Sum256(zipBytes)
		if hex.EncodeToString(got[:]) != payload.SHA256 {
			return nil, errors.New("code.repos_export sha256 mismatch")
		}
	}
	return zipBytes, nil
}

// ─── local source ─────────────────────────────────────────────────

type localFetcher struct{}

func (f *localFetcher) Kind() string { return "local" }

// Fetch copies the on-disk directory at SourceRef into destDir. Used
// when Apteva is running on a developer machine and wants to build
// straight from a local checkout.
func (f *localFetcher) Fetch(d *Deployment, destDir string) error {
	src := strings.TrimSpace(d.SourceRef)
	if src == "" {
		return errors.New("source_ref (path) required for kind=local")
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("local source %q: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local source %q: not a directory", src)
	}
	return copyTree(src, destDir)
}

// ─── shared helpers ───────────────────────────────────────────────

// unpackZip extracts zr into destDir. Rejects path traversal and
// preserves file modes the zip carried.
func unpackZip(zr *zip.Reader, destDir string) error {
	for _, f := range zr.File {
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("zip entry escapes root: %q", f.Name)
		}
		out := filepath.Join(destDir, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
			w.Close()
			return err
		}
		rc.Close()
		w.Close()
	}
	return nil
}

// copyTree mirrors src into dst. Plain file copy; symlinks become
// the file they point to (good enough for build inputs).
func copyTree(src, dst string) error {
	src = filepath.Clean(src)
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode())
		}
		// Skip common build-output / VCS noise to keep the source
		// hash stable.
		if shouldSkipForBuild(rel) {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, in); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	})
}

func shouldSkipForBuild(rel string) bool {
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts {
		switch p {
		case ".git", "node_modules", ".next", "dist", "build", ".cache":
			return true
		}
	}
	return false
}

// hashTree returns a deterministic hex digest of the file contents
// + paths under root. Used to short-circuit redundant rebuilds.
func hashTree(root string) (string, error) {
	type entry struct {
		path string
		sum  []byte
	}
	var entries []entry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h := sha256.Sum256(body)
		entries = append(entries, entry{path: rel, sum: h[:]})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\n", e.path, hex.EncodeToString(e.sum))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
