package main

import (
	"archive/zip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
)

const (
	sourceCapsuleFilename    = "source.zip"
	sourceCapsuleMetadata    = "source-capsule.json"
	sourceCapsuleKeyFilename = ".source-capsule-signing-key"
	sourceCapsuleFormat      = "zip-v1"
	defaultSourceCapsuleTTL  = 2 * time.Hour
	maxSourceCapsuleTTL      = 24 * time.Hour
	maxSourceCapsuleBytes    = int64(1 << 30)
)

type sourceCapsule struct {
	URL     string
	SHA256  string
	Size    int64
	Format  string
	Expires int64
}

type sourceCapsuleMeta struct {
	BuildID int64  `json:"build_id"`
	Project string `json:"project_id"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size_bytes"`
	Format  string `json:"format"`
	Expires int64  `json:"expires_at"`
	Created string `json:"created_at"`
}

func (a *App) prepareSourceCapsule(ctx context.Context, d *Deployment, build *Build, cfg cloudBuildConfig) (*sourceCapsule, error) {
	if d == nil || build == nil {
		return nil, errors.New("deployment and build are required for source capsule")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	buildDir := a.buildDir(build.ID)
	srcDir := filepath.Join(buildDir, "source-capsule-src")
	if err := os.RemoveAll(srcDir); err != nil {
		return nil, fmt.Errorf("reset source capsule scratch: %w", err)
	}
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		return nil, fmt.Errorf("create source capsule scratch: %w", err)
	}
	defer os.RemoveAll(srcDir)

	sourceCfg := a.cfg
	sourceCfg.ProjectID = d.ProjectID
	if err := fetchSource(globalCtx, d, srcDir, sourceCfg); err != nil {
		return nil, fmt.Errorf("fetch source capsule: %w", err)
	}
	if d.TargetKind == "ios" {
		if err := a.snapshotIOSDeviceFamilies(srcDir, d, build); err != nil {
			return nil, fmt.Errorf("detect iOS device families: %w", err)
		}
	}
	if cfg.Preflight != "off" && isMobileDeployment(d, build) {
		if err := validateMobileSource(srcDir, d, cfg); err != nil {
			return nil, fmt.Errorf("mobile source preflight: %w", err)
		}
	}

	capsulePath := filepath.Join(buildDir, sourceCapsuleFilename)
	tmpPath := capsulePath + ".tmp"
	_ = os.Remove(tmpPath)
	sum, size, err := writeSourceCapsule(srcDir, tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := os.Rename(tmpPath, capsulePath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("commit source capsule: %w", err)
	}

	expires := time.Now().Add(sourceCapsuleTTL(cfg)).Unix()
	meta := sourceCapsuleMeta{
		BuildID: build.ID, Project: d.ProjectID, SHA256: sum, Size: size,
		Format: sourceCapsuleFormat, Expires: expires, Created: nowUTC(),
	}
	if err := a.writeSourceCapsuleMeta(build.ID, meta); err != nil {
		a.removeSourceCapsule(build.ID)
		return nil, err
	}
	signedURL, err := a.sourceCapsuleURL(d.ProjectID, build.ID, sum, expires, cfg.SourceBaseURL)
	if err != nil {
		a.removeSourceCapsule(build.ID)
		return nil, err
	}
	if err := dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"source_sha": sum}); err != nil {
		a.removeSourceCapsule(build.ID)
		return nil, err
	}
	return &sourceCapsule{
		URL: signedURL, SHA256: sum, Size: size,
		Format: sourceCapsuleFormat, Expires: expires,
	}, nil
}

func (a *App) snapshotIOSDeviceFamilies(root string, d *Deployment, build *Build) error {
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return err
	}
	if len(target.DeviceFamilies) > 0 {
		return nil
	}
	families, err := detectIOSDeviceFamilies(root)
	if err != nil {
		return err
	}
	if len(families) == 0 {
		return nil
	}
	target.DeviceFamilies = families
	raw, err := json.Marshal(target)
	if err != nil {
		return err
	}
	d.TargetConfigJSON = string(raw)
	build.TargetConfigJSON = string(raw)
	return dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"target_config_json": string(raw)})
}

func sourceCapsuleTTL(cfg cloudBuildConfig) time.Duration {
	if cfg.SourceURLTTLSeconds <= 0 {
		return defaultSourceCapsuleTTL
	}
	return time.Duration(cfg.SourceURLTTLSeconds) * time.Second
}

func writeSourceCapsule(root, dest string) (string, int64, error) {
	paths := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if shouldSkipForBuild(rel) {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("enumerate source capsule: %w", err)
	}
	sort.Strings(paths)

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create source capsule: %w", err)
	}
	h := sha256.New()
	counting := &countingWriter{w: io.MultiWriter(out, h), max: maxSourceCapsuleBytes}
	zw := zip.NewWriter(counting)
	fixedTime := time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, rel := range paths {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			zw.Close()
			out.Close()
			return "", 0, fmt.Errorf("stat source capsule entry %q: %w", rel, err)
		}
		header := &zip.FileHeader{
			Name:     filepath.ToSlash(rel),
			Method:   zip.Deflate,
			Modified: fixedTime,
		}
		header.SetMode(info.Mode().Perm())
		entry, err := zw.CreateHeader(header)
		if err != nil {
			zw.Close()
			out.Close()
			return "", 0, fmt.Errorf("create source capsule entry %q: %w", rel, err)
		}
		in, err := os.Open(path)
		if err != nil {
			zw.Close()
			out.Close()
			return "", 0, fmt.Errorf("open source capsule entry %q: %w", rel, err)
		}
		_, copyErr := io.Copy(entry, in)
		closeErr := in.Close()
		if copyErr != nil {
			zw.Close()
			out.Close()
			return "", 0, fmt.Errorf("write source capsule entry %q: %w", rel, copyErr)
		}
		if closeErr != nil {
			zw.Close()
			out.Close()
			return "", 0, fmt.Errorf("close source capsule entry %q: %w", rel, closeErr)
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return "", 0, fmt.Errorf("finalize source capsule: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", 0, fmt.Errorf("close source capsule: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), counting.n, nil
}

type countingWriter struct {
	w   io.Writer
	n   int64
	max int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.max > 0 && w.n+int64(len(p)) > w.max {
		return 0, fmt.Errorf("source capsule exceeds %d bytes", w.max)
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

func (a *App) writeSourceCapsuleMeta(buildID int64, meta sourceCapsuleMeta) error {
	body, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	path := filepath.Join(a.buildDir(buildID), sourceCapsuleMetadata)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write source capsule metadata: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit source capsule metadata: %w", err)
	}
	return nil
}

func (a *App) readSourceCapsuleMeta(buildID int64) (sourceCapsuleMeta, error) {
	var meta sourceCapsuleMeta
	body, err := os.ReadFile(filepath.Join(a.buildDir(buildID), sourceCapsuleMetadata))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return meta, fmt.Errorf("decode source capsule metadata: %w", err)
	}
	if meta.BuildID != buildID || meta.Project == "" || meta.SHA256 == "" ||
		meta.Size < 0 || meta.Format != sourceCapsuleFormat || meta.Expires <= 0 {
		return meta, errors.New("invalid source capsule metadata")
	}
	return meta, nil
}

func (a *App) sourceCapsuleURL(projectID string, buildID int64, sum string, expires int64, configuredBase string) (string, error) {
	base, installID, err := sourceCapsulePublicBase(configuredBase)
	if err != nil {
		return "", err
	}
	key, err := a.sourceCapsuleSigningKey()
	if err != nil {
		return "", err
	}
	sig := signSourceCapsule(key, buildID, projectID, sum, expires)
	path := "/api/apps/deploy"
	if installID > 0 {
		path += "/_install/" + strconv.FormatInt(installID, 10)
	}
	path += "/source-capsules/" + strconv.FormatInt(buildID, 10) + "/" + sourceCapsuleFilename
	u, err := url.Parse(base + path)
	if err != nil {
		return "", fmt.Errorf("build source capsule URL: %w", err)
	}
	q := u.Query()
	q.Set("exp", strconv.FormatInt(expires, 10))
	q.Set("project_id", projectID)
	q.Set("sig", sig)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func sourceCapsulePublicBase(configured string) (string, int64, error) {
	base := strings.TrimSpace(configured)
	installID := int64(0)
	if globalCtx != nil && globalCtx.PlatformAPI() != nil {
		if identity, err := globalCtx.PlatformAPI().WhoAmI(); err == nil && identity != nil {
			installID = identity.InstallID
			if base == "" {
				base = strings.TrimSpace(identity.PublicURL)
			}
		}
		if base == "" {
			if info, err := globalCtx.PlatformInfo(); err == nil && info != nil {
				base = strings.TrimSpace(info.PublicURL)
			}
		}
	}
	if base == "" {
		base = strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL"))
	}
	base = strings.TrimRight(base, "/")
	if base == "" {
		return "", 0, errors.New("source_mode=bundle requires an externally reachable Apteva public URL; configure the server public URL or source_base_url")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.Scheme == "" {
		return "", 0, errors.New("source capsule public URL must be an absolute URL")
	}
	if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
		return "", 0, errors.New("source capsule public URL must use https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, errors.New("source capsule public URL cannot include a query string or fragment")
	}
	return base, installID, nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func (a *App) sourceCapsuleSigningKey() ([]byte, error) {
	a.sourceCapsuleKeyMu.Lock()
	defer a.sourceCapsuleKeyMu.Unlock()
	if len(a.sourceCapsuleKey) == sha256.Size {
		return append([]byte(nil), a.sourceCapsuleKey...), nil
	}
	if a.dataDir == "" {
		return nil, errors.New("deploy data directory is not configured")
	}
	path := filepath.Join(a.dataDir, sourceCapsuleKeyFilename)
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != sha256.Size {
			return nil, errors.New("invalid source capsule signing key")
		}
		_ = os.Chmod(path, 0o600)
		a.sourceCapsuleKey = append([]byte(nil), key...)
		return append([]byte(nil), key...), nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read source capsule signing key: %w", err)
	}
	key = make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate source capsule signing key: %w", err)
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		return nil, fmt.Errorf("write source capsule signing key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("commit source capsule signing key: %w", err)
	}
	a.sourceCapsuleKey = append([]byte(nil), key...)
	return append([]byte(nil), key...), nil
}

func signSourceCapsule(key []byte, buildID int64, projectID, sum string, expires int64) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "v1\n%d\n%s\n%s\n%d", buildID, projectID, sum, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *App) handleSourceCapsule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	buildID, ok := sourceCapsuleBuildID(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	meta, err := a.readSourceCapsuleMeta(buildID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	expires, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil || expires != meta.Expires {
		http.Error(w, "invalid source capsule signature", http.StatusForbidden)
		return
	}
	projectID := r.URL.Query().Get("project_id")
	key, err := a.sourceCapsuleSigningKey()
	if err != nil {
		http.Error(w, "source capsule unavailable", http.StatusServiceUnavailable)
		return
	}
	want := signSourceCapsule(key, buildID, projectID, meta.SHA256, expires)
	got, err := hex.DecodeString(r.URL.Query().Get("sig"))
	wantBytes, wantErr := hex.DecodeString(want)
	if err != nil || wantErr != nil || !hmac.Equal(got, wantBytes) || projectID != meta.Project {
		http.Error(w, "invalid source capsule signature", http.StatusForbidden)
		return
	}
	if time.Now().Unix() > expires {
		http.Error(w, "source capsule URL expired", http.StatusGone)
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), buildID)
	if err != nil || build == nil {
		http.NotFound(w, r)
		return
	}
	deployment, err := dbGetDeploymentByID(globalCtx.AppDB(), build.DeploymentID)
	if err != nil || deployment == nil || deployment.ProjectID != projectID {
		http.Error(w, "invalid source capsule signature", http.StatusForbidden)
		return
	}
	if build.Status != "pending" && build.Status != "running" {
		http.Error(w, "source capsule no longer available", http.StatusGone)
		return
	}
	path := filepath.Join(a.buildDir(buildID), sourceCapsuleFilename)
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != meta.Size {
		http.Error(w, "source capsule unavailable", http.StatusGone)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="source.zip"`)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("ETag", `"`+meta.SHA256+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, sourceCapsuleFilename, info.ModTime(), file)
}

func sourceCapsuleBuildID(path string) (int64, bool) {
	if !strings.HasPrefix(path, "/source-capsules/") {
		return 0, false
	}
	rest := strings.TrimPrefix(path, "/source-capsules/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != sourceCapsuleFilename {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	return id, err == nil && id > 0
}

func (a *App) removeSourceCapsule(buildID int64) {
	for _, name := range []string{
		sourceCapsuleFilename,
		sourceCapsuleFilename + ".tmp",
		sourceCapsuleMetadata,
		sourceCapsuleMetadata + ".tmp",
	} {
		path := filepath.Join(a.buildDir(buildID), name)
		if a.isSafeBuildPath(path) {
			_ = os.Remove(path)
		}
	}
	scratch := filepath.Join(a.buildDir(buildID), "source-capsule-src")
	if a.isSafeBuildPath(scratch) {
		_ = os.RemoveAll(scratch)
	}
}

func (a *App) cleanupSourceCapsules(dbBuilds []Build, now time.Time) (int, int64) {
	removed := 0
	var bytes int64
	for _, build := range dbBuilds {
		path := filepath.Join(a.buildDir(build.ID), sourceCapsuleFilename)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		remove := build.Status != "pending" && build.Status != "running"
		if !remove {
			if meta, err := a.readSourceCapsuleMeta(build.ID); err != nil || now.Unix() > meta.Expires {
				remove = true
			}
		}
		if remove {
			bytes += info.Size()
			a.removeSourceCapsule(build.ID)
			removed++
		}
	}
	return removed, bytes
}
