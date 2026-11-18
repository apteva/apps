package main

// Source provenance (v0.33.0).
//
// Every build records what it was built from, before any build command
// runs, so a release can be traced back to exact source:
//
//   - source_digest uses the Workspaces test-source method
//     ("workspaces-tree-v1"), so a digest Deploy records for a build equals
//     the digest Workspaces computes for the same files. Tests can compare
//     the two without trusting either side's archive.
//   - lockfile hashes are recorded per package folder, not only at the root.
//   - Code sources record the snapshot id and revision they came from.
//     Local sources keep a deterministic tar.gz copy next to the build, which
//     follows the build's retention (pin the build to keep it longer).
//
// The record is written once per build and cannot change after the build's
// artifact is sealed.

import (
	"archive/tar"
	"compress/gzip"
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
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	sourceDigestMethod      = "workspaces-tree-v1"
	sourceArchiveFilename   = "source-archive.tar.gz"
	sourceArchiveFormat     = "tar.gz"
	sourceArchiveRoutePath  = "/build-sources/"
	sourceArchiveRouteName  = "source.tar.gz"
	sourceArchiveMaxBytes   = 256 << 20
	defaultSourceArchiveTTL = time.Hour
)

var sourceLockfiles = map[string]bool{
	"bun.lock": true, "bun.lockb": true, "package-lock.json": true,
	"yarn.lock": true, "pnpm-lock.yaml": true,
}

type buildSource struct {
	BuildID        int64             `json:"build_id"`
	SourceDigest   string            `json:"source_digest"`
	DigestMethod   string            `json:"digest_method"`
	FileCount      int               `json:"file_count"`
	Lockfiles      map[string]string `json:"lockfiles"`
	SourceKind     string            `json:"source_kind"`
	SnapshotID     string            `json:"snapshot_id,omitempty"`
	SourceRevision string            `json:"source_revision,omitempty"`
	Repository     string            `json:"repository,omitempty"`
	Subdir         string            `json:"subdir,omitempty"`
	ArchiveSHA256  string            `json:"archive_sha256,omitempty"`
	ArchiveSize    int64             `json:"archive_size,omitempty"`
	ArchiveNote    string            `json:"archive_note,omitempty"`
	CreatedAt      string            `json:"created_at"`
}

// sourceSkipped reports whether a slash path is left out of the source
// digest: anything under node_modules (as in Workspaces) and the top-level
// .git folder (which Workspaces refuses outright).
func sourceSkipped(rel string, isDir bool) bool {
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}

type sourceEntry struct {
	rel, kind, mode, sum, abs, link string
}

// walkSourceTree lists the files and symlinks the source digest covers.
func walkSourceTree(root string) ([]sourceEntry, error) {
	entries := []sourceEntry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if sourceSkipped(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256([]byte(target))
			entries = append(entries, sourceEntry{rel: rel, kind: "l", mode: "777", sum: hex.EncodeToString(sum[:]), abs: p, link: target})
		case info.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			mode := "644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "755"
			}
			entries = append(entries, sourceEntry{rel: rel, kind: "f", mode: mode, sum: hex.EncodeToString(h.Sum(nil)), abs: p})
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	return entries, err
}

// sourceTreeDigest computes the workspaces-tree-v1 digest of root and the
// sha256 of every lockfile, keyed by its slash path.
func sourceTreeDigest(root string) (string, int, map[string]string, error) {
	entries, err := walkSourceTree(root)
	if err != nil {
		return "", 0, nil, err
	}
	digest, locks := digestSourceEntries(entries)
	return digest, len(entries), locks, nil
}

func digestSourceEntries(entries []sourceEntry) (string, map[string]string) {
	h := sha256.New()
	locks := map[string]string{}
	for _, e := range entries {
		io.WriteString(h, e.rel+"\x00"+e.kind+"\x00"+e.mode+"\x00"+e.sum+"\n")
		if e.kind == "f" && sourceLockfiles[path.Base(e.rel)] {
			locks[e.rel] = e.sum
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), locks
}

// writeSourceArchive writes a deterministic tar.gz of the digest's entries:
// sorted paths, fixed times and owners, modes 644/755/777. Importing it into
// Workspaces yields the same source digest.
func writeSourceArchive(entries []sourceEntry, dest string, limit int64) (string, int64, error) {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	counter := &countingWriter{w: io.MultiWriter(out, h), max: limit}
	gz, _ := gzip.NewWriterLevel(counter, gzip.DefaultCompression)
	gz.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gz)
	fixed := time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
	fail := func(e error) (string, int64, error) {
		tw.Close()
		gz.Close()
		out.Close()
		_ = os.Remove(dest)
		return "", 0, e
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.rel, ModTime: fixed, Format: tar.FormatPAX, Uname: "", Gname: ""}
		if e.kind == "l" {
			hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, e.link, 0o777
			if err := tw.WriteHeader(hdr); err != nil {
				return fail(err)
			}
			continue
		}
		info, err := os.Lstat(e.abs)
		if err != nil {
			return fail(err)
		}
		hdr.Typeflag, hdr.Size, hdr.Mode = tar.TypeReg, info.Size(), 0o644
		if e.mode == "755" {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fail(err)
		}
		f, err := os.Open(e.abs)
		if err != nil {
			return fail(err)
		}
		fh := sha256.New()
		_, err = io.Copy(io.MultiWriter(tw, fh), io.LimitReader(f, info.Size()))
		f.Close()
		if err != nil {
			return fail(err)
		}
		if hex.EncodeToString(fh.Sum(nil)) != e.sum {
			return fail(fmt.Errorf("source file %s changed while it was being archived", e.rel))
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return "", 0, err
	}
	_ = os.Chmod(dest, 0o444)
	return hex.EncodeToString(h.Sum(nil)), counter.n, nil
}

// captureBuildSource records the provenance of the source at root before any
// build command runs. buildDir holds the Code source receipt, when there is
// one, and receives the local source archive.
func captureBuildSource(db *sql.DB, d *Deployment, buildID int64, buildDir, root string) (*buildSource, error) {
	entries, err := walkSourceTree(root)
	if err != nil {
		return nil, fmt.Errorf("source provenance: %w", err)
	}
	digest, locks := digestSourceEntries(entries)
	src := &buildSource{
		BuildID: buildID, SourceDigest: digest, DigestMethod: sourceDigestMethod,
		FileCount: len(entries), Lockfiles: locks, SourceKind: d.SourceKind, CreatedAt: nowUTC(),
	}
	if body, err := os.ReadFile(filepath.Join(buildDir, "source-receipt.json")); err == nil {
		var r sourceReceipt
		if json.Unmarshal(body, &r) == nil {
			src.SnapshotID, src.SourceRevision = r.SnapshotID, r.SourceRevision
			src.Repository, src.Subdir = r.Repository, r.Subdir
		}
	}
	if src.Repository == "" && d.SourceKind == "code" {
		src.Repository = d.SourceRef
	}
	if d.SourceKind != "code" {
		archive := filepath.Join(buildDir, sourceArchiveFilename)
		_ = removeArtifactTree(archive)
		sum, size, err := writeSourceArchive(entries, archive, sourceArchiveMaxBytes)
		if err != nil {
			src.ArchiveNote = "source copy not kept: " + err.Error()
		} else {
			src.ArchiveSHA256, src.ArchiveSize = sum, size
		}
	}
	if err := dbRecordBuildSource(db, src); err != nil {
		return nil, err
	}
	return src, nil
}

func dbRecordBuildSource(db *sql.DB, s *buildSource) error {
	var sealed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM build_attestations WHERE build_id=?`, s.BuildID).Scan(&sealed); err != nil {
		return err
	}
	if sealed > 0 {
		return errors.New("source provenance cannot change after the build is sealed")
	}
	locks, _ := json.Marshal(s.Lockfiles)
	_, err := db.Exec(`INSERT INTO build_sources(build_id,source_digest,digest_method,file_count,lockfiles_json,source_kind,snapshot_id,source_revision,repository,subdir,archive_sha256,archive_size,archive_note,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(build_id) DO UPDATE SET source_digest=excluded.source_digest,digest_method=excluded.digest_method,file_count=excluded.file_count,
		lockfiles_json=excluded.lockfiles_json,source_kind=excluded.source_kind,snapshot_id=excluded.snapshot_id,source_revision=excluded.source_revision,
		repository=excluded.repository,subdir=excluded.subdir,archive_sha256=excluded.archive_sha256,archive_size=excluded.archive_size,
		archive_note=excluded.archive_note,created_at=excluded.created_at`,
		s.BuildID, s.SourceDigest, s.DigestMethod, s.FileCount, string(locks), s.SourceKind, s.SnapshotID, s.SourceRevision,
		s.Repository, s.Subdir, s.ArchiveSHA256, s.ArchiveSize, s.ArchiveNote, s.CreatedAt)
	return err
}

// dbBuildSource returns the build's source record, or nil for builds made
// before Deploy 0.33.0 and imported builds.
func dbBuildSource(db *sql.DB, buildID int64) (*buildSource, error) {
	var s buildSource
	var locks string
	err := db.QueryRow(`SELECT build_id,source_digest,digest_method,file_count,lockfiles_json,source_kind,snapshot_id,source_revision,repository,subdir,archive_sha256,archive_size,archive_note,created_at FROM build_sources WHERE build_id=?`, buildID).
		Scan(&s.BuildID, &s.SourceDigest, &s.DigestMethod, &s.FileCount, &locks, &s.SourceKind, &s.SnapshotID, &s.SourceRevision, &s.Repository, &s.Subdir, &s.ArchiveSHA256, &s.ArchiveSize, &s.ArchiveNote, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(locks), &s.Lockfiles)
	if s.Lockfiles == nil {
		s.Lockfiles = map[string]string{}
	}
	return &s, nil
}

func buildSourceDigest(buildID int64) string {
	if globalCtx == nil {
		return ""
	}
	s, err := dbBuildSource(globalCtx.AppDB(), buildID)
	if err != nil || s == nil {
		return ""
	}
	return s.SourceDigest
}

// sourceSummary is the provenance block shown by export and candidate tools.
func sourceSummary(s *buildSource) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{
		"source_digest": s.SourceDigest, "digest_method": s.DigestMethod,
		"file_count": s.FileCount, "lockfiles": s.Lockfiles, "source_kind": s.SourceKind,
	}
	if s.SnapshotID != "" || s.SourceRevision != "" {
		out["code"] = map[string]any{"snapshot_id": s.SnapshotID, "source_revision": s.SourceRevision, "repository": s.Repository, "subdir": s.Subdir}
	}
	return out
}

func (a *App) sourceArchivePath(buildID int64) string {
	return filepath.Join(a.buildDir(buildID), sourceArchiveFilename)
}

// toolBuildSource returns where a build's source can be retrieved: the Code
// snapshot it came from, or a signed link to the copy Deploy kept.
func (a *App) toolBuildSource(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	build, base, err := a.resolveBuildRef(db, args)
	if err != nil {
		return nil, err
	}
	src, err := dbBuildSource(db, build.ID)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, fmt.Errorf("build %d has no recorded source (built before Deploy 0.33.0, or imported); rebuild to record it", build.ID)
	}
	out := sourceSummary(src)
	out["build_id"] = build.ID
	if digest := buildSourceSealedDigest(db, build.ID); digest != "" {
		out["candidate_digest"] = digest
	}
	if src.SourceKind == "code" {
		out["retrieve"] = "Fetch the Code snapshot or source_revision listed under code; Deploy does not keep a second copy of Code sources."
		return out, nil
	}
	if src.ArchiveSHA256 == "" {
		out["archive_note"] = defaultStr(src.ArchiveNote, "no source copy was kept for this build")
		return out, nil
	}
	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	sum, err := fileSHA256(a.sourceArchivePath(build.ID))
	if err != nil {
		out["archive_note"] = "the source copy was removed with the build's artifact by retention; pin builds with deploy_build_pin to keep them"
		return out, nil
	}
	if sum != src.ArchiveSHA256 {
		return nil, errors.New("the kept source copy no longer matches its recorded sha256")
	}
	ttl := defaultSourceArchiveTTL
	if s := intArg(args, "ttl_seconds"); s > 0 {
		ttl = time.Duration(s) * time.Second
	}
	if ttl > maxExportTTL {
		ttl = maxExportTTL
	}
	expires := time.Now().Add(ttl)
	if err = pinBuild(db, build.ID, pinReasonExport, expires); err != nil {
		return nil, err
	}
	archive := map[string]any{"format": sourceArchiveFormat, "sha256": src.ArchiveSHA256, "size_bytes": src.ArchiveSize, "expires_at": expires.UTC().Format(time.RFC3339)}
	if u, err := a.sourceArchiveURL(base.ProjectID, build.ID, src.ArchiveSHA256, expires.Unix(), strArg(args, "public_base_url")); err == nil {
		archive["url"] = u
	} else {
		archive["url_error"] = err.Error()
	}
	out["archive"] = archive
	return out, nil
}

func buildSourceSealedDigest(db *sql.DB, buildID int64) string {
	var digest string
	_ = db.QueryRow(`SELECT artifact_sha256 FROM build_attestations WHERE build_id=?`, buildID).Scan(&digest)
	return digest
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func signSourceArchive(key []byte, buildID int64, projectID, sum string, expires int64) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "source-v1\n%d\n%s\n%s\n%d", buildID, projectID, sum, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *App) sourceArchiveURL(projectID string, buildID int64, sum string, expires int64, configuredBase string) (string, error) {
	base, installID, err := sourceCapsulePublicBase(configuredBase)
	if err != nil {
		return "", err
	}
	key, err := a.sourceCapsuleSigningKey()
	if err != nil {
		return "", err
	}
	p := "/api/apps/deploy"
	if installID > 0 {
		p += "/_install/" + strconv.FormatInt(installID, 10)
	}
	p += sourceArchiveRoutePath + strconv.FormatInt(buildID, 10) + "/" + sourceArchiveRouteName
	q := url.Values{}
	q.Set("sha256", sum)
	q.Set("exp", strconv.FormatInt(expires, 10))
	q.Set("project_id", projectID)
	q.Set("sig", signSourceArchive(key, buildID, projectID, sum, expires))
	return base + p + "?" + q.Encode(), nil
}

// handleSourceArchive serves a signed source copy after re-checking its sha256.
func (a *App) handleSourceArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idPart, name, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, sourceArchiveRoutePath), "/")
	buildID, err := strconv.ParseInt(idPart, 10, 64)
	if !ok || name != sourceArchiveRouteName || err != nil || buildID < 1 {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	expires, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		http.Error(w, "invalid source signature", http.StatusForbidden)
		return
	}
	key, err := a.sourceCapsuleSigningKey()
	if err != nil {
		http.Error(w, "build source unavailable", http.StatusServiceUnavailable)
		return
	}
	projectID, sum := q.Get("project_id"), q.Get("sha256")
	got, gotErr := hex.DecodeString(q.Get("sig"))
	want, _ := hex.DecodeString(signSourceArchive(key, buildID, projectID, sum, expires))
	if gotErr != nil || !hmac.Equal(got, want) {
		http.Error(w, "invalid source signature", http.StatusForbidden)
		return
	}
	if time.Now().Unix() > expires {
		http.Error(w, "build source URL expired", http.StatusGone)
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), buildID)
	if err != nil || build == nil {
		http.NotFound(w, r)
		return
	}
	if d, err := dbGetDeployment(globalCtx.AppDB(), projectID, build.DeploymentID); err != nil || d == nil {
		http.NotFound(w, r)
		return
	}
	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	p := a.sourceArchivePath(buildID)
	if actual, err := fileSHA256(p); err != nil || actual != sum {
		http.Error(w, "build source no longer matches the signed sha256", http.StatusGone)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="build-%d-source.tar.gz"`, buildID))
	w.Header().Set("X-Content-SHA256", sum)
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func logSourceCapture(buildID int64, s *buildSource, logF io.Writer) {
	if s == nil || logF == nil {
		return
	}
	fmt.Fprintf(logF, "source provenance: %s (%d files, %d lockfiles)\n", s.SourceDigest, s.FileCount, len(s.Lockfiles))
	if s.ArchiveNote != "" {
		fmt.Fprintf(logF, "source provenance: %s\n", s.ArchiveNote)
		log.Printf("build %d: %s", buildID, s.ArchiveNote)
	}
}
