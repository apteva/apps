package main

// Build export / import (v0.32.0).
//
// A sealed build can be exported as a manifest plus one archive, and imported
// into another Deploy install (for example a Tests runtime) where it is
// re-hashed against its candidate digest before it can be released. Exports
// never carry environment values: env is described as a template of keys, and
// the export refuses when a secret env value appears in the artifact bytes.

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	buildExportFormat      = "apteva-deploy-build/v1"
	buildExportRoutePrefix = "/build-exports/"
	buildExportArchiveName = "build.zip"
	defaultExportTTL       = time.Hour
	maxExportTTL           = 24 * time.Hour
	testsVerdictPinTTL     = 14 * 24 * time.Hour

	pinReasonManual = "manual"
	pinReasonExport = "export"
	pinReasonTests  = "tests_verdict"
)

// ─── Build resolution ─────────────────────────────────────────────

// resolveBuildRef finds one succeeded, sealed build in the caller's project by
// build_id, candidate digest, or build:"latest_succeeded" (with deployment id
// or name and optional environment).
func (a *App) resolveBuildRef(db *sql.DB, args map[string]any) (*Build, *Deployment, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	ref := strings.TrimSpace(strArg(args, "build"))
	if n, ok := args["build"].(float64); ok && n > 0 && n == float64(int64(n)) {
		ref = strconv.FormatInt(int64(n), 10)
	}
	digest := strings.ToLower(strings.TrimSpace(defaultStr(strArg(args, "candidate_digest"), strArg(args, "digest"))))
	var build *Build
	switch {
	case int64(intArg(args, "build_id")) > 0:
		build, err = dbGetBuild(db, int64(intArg(args, "build_id")))
	case digest != "":
		if !testsDigest.MatchString(digest) {
			return nil, nil, errors.New("candidate_digest must be a 64-character hex SHA-256")
		}
		build, err = dbBuildByDigest(db, project, digest)
	case ref == "latest_succeeded":
		d, lookupErr := a.lookupDeployment(args)
		if lookupErr != nil {
			return nil, nil, lookupErr
		}
		build, err = dbLatestSealedBuild(db, d.ID, d.EnvironmentID)
	case ref != "":
		id, parseErr := strconv.ParseInt(ref, 10, 64)
		if parseErr != nil || id < 1 {
			return nil, nil, errors.New(`build must be a build id or "latest_succeeded"`)
		}
		build, err = dbGetBuild(db, id)
	default:
		return nil, nil, errors.New(`build_id, build (a build id or "latest_succeeded"), or candidate_digest required`)
	}
	if err != nil || build == nil || build.Status != "succeeded" {
		return nil, nil, errors.New("succeeded build not found")
	}
	d, err := dbGetDeployment(db, project, build.DeploymentID)
	if err != nil || d == nil {
		return nil, nil, errors.New("succeeded build not found in this project")
	}
	return build, d, nil
}

func dbBuildByDigest(db *sql.DB, project, digest string) (*Build, error) {
	var id int64
	err := db.QueryRow(`
		SELECT b.id FROM builds b
		  JOIN build_attestations a ON a.build_id = b.id
		  JOIN deployments d ON d.id = b.deployment_id
		 WHERE a.artifact_sha256 = ? AND b.status = 'succeeded' AND d.project_id = ?
		 ORDER BY b.id DESC LIMIT 1`, digest, project).Scan(&id)
	if err != nil {
		return nil, err
	}
	return dbGetBuild(db, id)
}

func dbLatestSealedBuild(db *sql.DB, deploymentID, environmentID int64) (*Build, error) {
	var id int64
	err := db.QueryRow(`
		SELECT b.id FROM builds b
		  JOIN build_attestations a ON a.build_id = b.id
		 WHERE b.deployment_id = ? AND COALESCE(b.environment_id,0) = ? AND b.status = 'succeeded'
		   AND b.artifact_path != ''
		 ORDER BY b.id DESC LIMIT 1`, deploymentID, environmentID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return dbGetBuild(db, id)
}

// ─── Retention pins ───────────────────────────────────────────────

type buildPin struct {
	BuildID   int64  `json:"build_id"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

// pinBuild keeps a build's artifact out of retention until expires (zero =
// until unpinned). Re-pinning only ever extends an existing expiry.
func pinBuild(db *sql.DB, buildID int64, reason string, expires time.Time) error {
	exp := ""
	if !expires.IsZero() {
		exp = expires.UTC().Format(time.RFC3339)
	}
	_, err := db.Exec(`
		INSERT INTO build_pins(build_id, reason, expires_at, created_at) VALUES(?,?,?,?)
		ON CONFLICT(build_id, reason) DO UPDATE SET expires_at = CASE
			WHEN build_pins.expires_at = '' OR excluded.expires_at = '' THEN ''
			WHEN excluded.expires_at > build_pins.expires_at THEN excluded.expires_at
			ELSE build_pins.expires_at END`, buildID, reason, exp, nowUTC())
	return err
}

func unpinBuild(db *sql.DB, buildID int64, reason string) error {
	_, err := db.Exec(`DELETE FROM build_pins WHERE build_id = ? AND reason = ?`, buildID, reason)
	return err
}

func dbBuildPins(db *sql.DB, buildID int64) ([]buildPin, error) {
	rows, err := db.Query(`SELECT build_id, reason, expires_at, created_at FROM build_pins
		WHERE build_id = ? AND (expires_at = '' OR expires_at > ?) ORDER BY reason`, buildID, nowUTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pins := []buildPin{}
	for rows.Next() {
		var p buildPin
		if err := rows.Scan(&p.BuildID, &p.Reason, &p.ExpiresAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		pins = append(pins, p)
	}
	return pins, rows.Err()
}

func (a *App) toolBuildPin(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	build, _, err := a.resolveBuildRef(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	pinned := true
	if _, ok := args["pinned"]; ok {
		pinned = boolArg(args, "pinned")
	}
	a.artifactMu.RLock()
	available := buildArtifactAvailable(build)
	a.artifactMu.RUnlock()
	if pinned {
		if !available {
			return nil, errBuildArtifactPruned
		}
		if err = pinBuild(ctx.AppDB(), build.ID, pinReasonManual, time.Time{}); err != nil {
			return nil, err
		}
	} else if err = unpinBuild(ctx.AppDB(), build.ID, pinReasonManual); err != nil {
		return nil, err
	}
	pins, err := dbBuildPins(ctx.AppDB(), build.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"build_id": build.ID, "pinned": len(pins) > 0, "pins": pins}, nil
}

// ─── Export ───────────────────────────────────────────────────────

type exportFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
	Link   string `json:"link,omitempty"`
}

type exportEnvVar struct {
	Key         string `json:"key"`
	Secret      bool   `json:"secret"`
	Placeholder string `json:"placeholder"`
}

var (
	secretEnvKey = regexp.MustCompile(`(?i)(SECRET|TOKEN|PASSWORD|PASSWD|PASSPHRASE|PRIVATE|CREDENTIAL|API_?KEY|ACCESS_?KEY|SIGNING|DSN|DATABASE_URL|_KEY$|^KEY$|AUTH)`)
	publicEnvKey = regexp.MustCompile(`^(VITE_|NEXT_PUBLIC_|PUBLIC_|EXPO_PUBLIC_|REACT_APP_)`)
)

// minSecretScanLength keeps short, common values (e.g. "true", "8080") from
// producing false leak reports.
const minSecretScanLength = 8

func envKeyIsSecret(key string) bool {
	return !publicEnvKey.MatchString(key) && secretEnvKey.MatchString(key)
}

// exportEnvTemplate describes the build environment's variables without any
// values. A runtime supplies its own (test) values for each placeholder.
func exportEnvTemplate(env map[string]string) ([]exportEnvVar, [][]byte) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	template := make([]exportEnvVar, 0, len(keys))
	var secrets [][]byte
	for _, k := range keys {
		secret := envKeyIsSecret(k)
		template = append(template, exportEnvVar{Key: k, Secret: secret, Placeholder: "${" + k + "}"})
		if secret && len(env[k]) >= minSecretScanLength {
			secrets = append(secrets, []byte(env[k]))
		}
	}
	return template, secrets
}

// scanArtifactFiles lists every file the candidate digest covers and refuses
// when any secret value appears in artifact bytes.
func scanArtifactFiles(root string, secrets [][]byte) ([]exportFile, error) {
	overlap := 0
	for _, s := range secrets {
		if len(s) > overlap {
			overlap = len(s)
		}
	}
	files := []exportFile{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || path == filepath.Join(root, artifactManifestFilename) {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		entry := exportFile{Path: filepath.ToSlash(rel), Mode: info.Mode().String()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry.Link = target
		case info.Mode().IsRegular():
			sum, leaked, err := hashAndScanFile(path, secrets, overlap)
			if err != nil {
				return err
			}
			if leaked {
				return fmt.Errorf("artifact file %s contains a secret environment value; rebuild without baking secrets into the artifact", entry.Path)
			}
			entry.Size = info.Size()
			entry.SHA256 = sum
		case info.IsDir():
		default:
			return fmt.Errorf("unsupported artifact entry %s", entry.Path)
		}
		files = append(files, entry)
		return nil
	})
	return files, err
}

func hashAndScanFile(path string, secrets [][]byte, overlap int) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 256<<10)
	var tail []byte
	leaked := false
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if len(secrets) > 0 && !leaked {
				window := append(tail, buf[:n]...)
				for _, s := range secrets {
					if bytes.Contains(window, s) {
						leaked = true
						break
					}
				}
				keep := overlap - 1
				if keep > len(window) {
					keep = len(window)
				}
				tail = append([]byte(nil), window[len(window)-keep:]...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", false, readErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), leaked, nil
}

// buildExportEnvironment returns the deployment config the build was made for.
func buildExportEnvironment(db *sql.DB, d *Deployment, b *Build) (*Deployment, error) {
	if b.EnvironmentID == 0 {
		return d, nil
	}
	env, err := dbGetEnvironment(db, b.EnvironmentID)
	if err != nil || env == nil {
		return d, err
	}
	return effectiveDeploymentForEnvironment(d, env), nil
}

func (a *App) toolBuildExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	build, base, err := a.resolveBuildRef(db, args)
	if err != nil {
		return nil, err
	}
	d, err := buildExportEnvironment(db, base, build)
	if err != nil {
		return nil, err
	}
	ttl := defaultExportTTL
	if s := intArg(args, "ttl_seconds"); s > 0 {
		ttl = time.Duration(s) * time.Second
	}
	if ttl > maxExportTTL {
		ttl = maxExportTTL
	}

	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	digest, _, err := verifiedBuildEvidence(build)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(build.ArtifactPath)
	if err != nil || !info.IsDir() {
		return nil, errors.New("build artifact is not a directory export")
	}
	envTemplate, secrets := exportEnvTemplate(parseEnvJSON(d.EnvJSON))
	files, err := scanArtifactFiles(build.ArtifactPath, secrets)
	if err != nil {
		return nil, err
	}
	var size int64
	for _, f := range files {
		size += f.Size
	}

	expires := time.Now().Add(ttl)
	if err = pinBuild(db, build.ID, pinReasonExport, expires); err != nil {
		return nil, err
	}
	out := map[string]any{
		"format":           buildExportFormat,
		"build_id":         build.ID,
		"candidate_digest": digest,
		"deployment":       map[string]any{"id": d.ID, "name": d.Name, "environment": d.EnvironmentName},
		"framework":        build.Framework,
		"source_sha":       build.SourceSHA,
		"static": map[string]any{
			"files": files, "file_count": len(files), "size": size,
			"base_path": staticBasePath(d.TargetConfigJSON),
		},
		"env":             envTemplate,
		"archive_format":  "zip",
		"archive_expires": expires.UTC().Format(time.RFC3339),
	}
	if raw := strings.TrimSpace(build.ArtifactManifestJSON); raw != "" && raw != "{}" && json.Valid([]byte(raw)) {
		out["artifact_manifest"] = json.RawMessage(raw)
	}
	if src, _ := dbBuildSource(db, build.ID); src != nil {
		out["source"] = sourceSummary(src)
	}
	if body, err := os.ReadFile(filepath.Join(build.ArtifactPath, "backend", "release.json")); err == nil && json.Valid(body) {
		out["backend"] = json.RawMessage(body)
	}
	if archiveURL, err := a.buildExportURL(base.ProjectID, build.ID, digest, expires.Unix(), strArg(args, "public_base_url")); err == nil {
		out["archive_url"] = archiveURL
	} else {
		out["archive_url_error"] = err.Error()
	}
	return out, nil
}

func signBuildExport(key []byte, buildID int64, projectID, digest string, expires int64) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "export-v1\n%d\n%s\n%s\n%d", buildID, projectID, digest, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *App) buildExportURL(projectID string, buildID int64, digest string, expires int64, configuredBase string) (string, error) {
	base, installID, err := sourceCapsulePublicBase(configuredBase)
	if err != nil {
		return "", err
	}
	key, err := a.sourceCapsuleSigningKey()
	if err != nil {
		return "", err
	}
	path := "/api/apps/deploy"
	if installID > 0 {
		path += "/_install/" + strconv.FormatInt(installID, 10)
	}
	path += buildExportRoutePrefix + strconv.FormatInt(buildID, 10) + "/" + buildExportArchiveName
	q := url.Values{}
	q.Set("digest", digest)
	q.Set("exp", strconv.FormatInt(expires, 10))
	q.Set("project_id", projectID)
	q.Set("sig", signBuildExport(key, buildID, projectID, digest, expires))
	return base + path + "?" + q.Encode(), nil
}

// handleBuildExport serves a signed export archive. The signature binds the
// build, project, digest and expiry; the artifact is re-verified against the
// digest before any byte is sent.
func (a *App) handleBuildExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, buildExportRoutePrefix)
	idPart, name, ok := strings.Cut(rest, "/")
	buildID, err := strconv.ParseInt(idPart, 10, 64)
	if !ok || name != buildExportArchiveName || err != nil || buildID < 1 {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	expires, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		http.Error(w, "invalid export signature", http.StatusForbidden)
		return
	}
	key, err := a.sourceCapsuleSigningKey()
	if err != nil {
		http.Error(w, "build export unavailable", http.StatusServiceUnavailable)
		return
	}
	projectID, digest := q.Get("project_id"), q.Get("digest")
	got, gotErr := hex.DecodeString(q.Get("sig"))
	want, _ := hex.DecodeString(signBuildExport(key, buildID, projectID, digest, expires))
	if gotErr != nil || !hmac.Equal(got, want) {
		http.Error(w, "invalid export signature", http.StatusForbidden)
		return
	}
	if time.Now().Unix() > expires {
		http.Error(w, "build export URL expired", http.StatusGone)
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), buildID)
	if err != nil || build == nil || build.Status != "succeeded" {
		http.NotFound(w, r)
		return
	}
	if d, err := dbGetDeployment(globalCtx.AppDB(), projectID, build.DeploymentID); err != nil || d == nil {
		http.NotFound(w, r)
		return
	}
	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	actual, _, err := verifiedBuildEvidence(build)
	if err != nil || actual != digest {
		http.Error(w, "build artifact no longer matches the exported digest", http.StatusGone)
		return
	}
	filename := fmt.Sprintf("build-%d-%s.zip", buildID, digest[:12])
	if err := serveBuildArtifact(w, r, buildArtifactDownload{Path: build.ArtifactPath, Filename: filename, Directory: true}); err != nil {
		log.Printf("build export %d: %v", buildID, err)
	}
}

// ─── Import ───────────────────────────────────────────────────────

const buildBackendImport = "import"

var importHTTPClient = &http.Client{Timeout: 5 * time.Minute}

func validateImportURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, errors.New("archive_url must be an absolute URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, errors.New("archive_url must use https (http is allowed only for loopback hosts)")
	}
	if u.User != nil {
		return nil, errors.New("archive_url cannot carry credentials")
	}
	return u, nil
}

func (a *App) toolBuildImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	if d.Framework != "static" {
		return nil, errors.New("deploy_build_import supports framework=static deployments")
	}
	digest := strings.ToLower(strings.TrimSpace(strArg(args, "candidate_digest")))
	if !testsDigest.MatchString(digest) {
		return nil, errors.New("candidate_digest must be the 64-character hex digest from deploy_build_export")
	}
	src, err := validateImportURL(strArg(args, "archive_url"))
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if existing, err := dbBuildByDigest(db, d.ProjectID, digest); err == nil && existing != nil &&
		existing.DeploymentID == d.ID && existing.EnvironmentID == d.EnvironmentID && buildArtifactAvailable(existing) {
		return map[string]any{"build": buildWithArtifactDownloadURL(existing, d.ProjectID), "candidate_digest": digest, "already_imported": true}, nil
	}
	build, err := dbCreateBuildForEnvBackend(db, d.ID, d.EnvironmentID, "static", "", buildBackendImport, mustJSON(map[string]any{"candidate_digest": digest, "source_host": src.Host}))
	if err != nil {
		return nil, err
	}
	_ = dbUpdateBuild(db, build.ID, map[string]any{"status": "running", "started_at": nowUTC()})
	started := time.Now()
	distDir, size, manifestJSON, err := a.fetchAndExtractImport(build.ID, src.String(), digest)
	if err != nil {
		_ = removeArtifactTree(a.buildDir(build.ID))
		failed := a.failBuild(build, "import: "+err.Error())
		return map[string]any{"build": failed, "error": failed.Error}, nil
	}
	candidate := *build
	candidate.ArtifactPath = distDir
	candidate.ArtifactManifestJSON = manifestJSON
	candidate.TargetConfigJSON = d.TargetConfigJSON
	a.artifactMu.Lock()
	// Seal under the candidate digest so Tests verdicts still match. Only a
	// v2 candidate survives the read-only chmod; older ones stay writable.
	v2, _ := artifactTreeDigestV2(distDir)
	sealErr := sealProtected(&candidate, digest, v2 == digest)
	a.artifactMu.Unlock()
	if sealErr != nil {
		_ = removeArtifactTree(a.buildDir(build.ID))
		failed := a.failBuild(build, "import: "+sealErr.Error())
		return map[string]any{"build": failed, "error": failed.Error}, nil
	}
	if err := dbUpdateBuild(db, build.ID, map[string]any{
		"status": "succeeded", "finished_at": nowUTC(), "duration_ms": time.Since(started).Milliseconds(),
		"exit_code": 0, "artifact_path": distDir, "artifact_size": size, "artifact_manifest_json": manifestJSON,
		"target_config_json": d.TargetConfigJSON,
	}); err != nil {
		return nil, err
	}
	emit("deploy.build.succeeded", map[string]any{
		"deployment_id": d.ID, "environment": d.EnvironmentName, "build_id": build.ID,
		"framework": "static", "size": size, "imported": true, "candidate_digest": digest,
	})
	out, _ := dbGetBuild(db, build.ID)
	return map[string]any{"build": buildWithArtifactDownloadURL(out, d.ProjectID), "candidate_digest": digest}, nil
}

// fetchAndExtractImport downloads the archive into the build directory,
// extracts it with the bounded extractor and checks the tree digest.
func (a *App) fetchAndExtractImport(buildID int64, archiveURL, digest string) (string, int64, string, error) {
	buildDir := a.buildDir(buildID)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return "", 0, "", err
	}
	archivePath := filepath.Join(buildDir, "import.zip")
	limit := sourceTransferLimit()
	resp, err := importHTTPClient.Get(archiveURL)
	if err != nil {
		return "", 0, "", fmt.Errorf("download archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, "", fmt.Errorf("download archive: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, "", err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	defer os.Remove(archivePath)
	if copyErr != nil {
		return "", 0, "", fmt.Errorf("download archive: %w", copyErr)
	}
	if closeErr != nil {
		return "", 0, "", closeErr
	}
	if n > limit {
		return "", 0, "", fmt.Errorf("archive exceeds %d bytes (DEPLOY_SOURCE_MAX_BYTES)", limit)
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", 0, "", fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()
	distDir := filepath.Join(buildDir, "dist")
	if err := extractBoundedZip(&zr.Reader, distDir, sourceExpandedLimit(), true); err != nil {
		return "", 0, "", fmt.Errorf("extract archive: %w", err)
	}
	same, err := artifactDigestMatches(distDir, digest)
	if err != nil {
		return "", 0, "", err
	}
	if !same {
		actual, _ := artifactTreeDigestV2(distDir)
		return "", 0, "", fmt.Errorf("imported artifact digest %s does not match candidate_digest %s", actual, digest)
	}
	manifestJSON := "{}"
	if body, err := os.ReadFile(filepath.Join(distDir, artifactManifestFilename)); err == nil && json.Valid(body) {
		manifestJSON = string(body)
	}
	size, _ := dirSize(distDir)
	return distDir, size, manifestJSON, nil
}

// ─── Static base path ─────────────────────────────────────────────

var staticBasePathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)

func validateStaticBasePath(p string) error {
	if p == "" {
		return nil
	}
	if !staticBasePathPattern.MatchString(p) {
		return errors.New("static_base_path must look like /segment[/segment], without a trailing slash")
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." {
			return errors.New("static_base_path cannot contain . or .. segments")
		}
	}
	return nil
}

func staticBasePath(targetConfigJSON string) string {
	t, err := genericTarget(targetConfigJSON)
	if err != nil {
		return ""
	}
	return t.StaticBasePath
}
