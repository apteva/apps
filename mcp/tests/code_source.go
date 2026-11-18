package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	sourceDigestMethod     = "workspaces-tree-v1"
	maxSourceDownload      = 64 << 20
	maxSourceExpanded      = 128 << 20
	maxSourceFiles         = 20000
	maxWorkspaceSource     = 8 << 20
	snapshotChunk          = 1 << 20
	sourceDownloadDeadline = 2 * time.Minute
)

// codeProvenance records what a code check tested.
type codeProvenance struct {
	SourceDigest   string            `json:"source_digest"`
	DigestMethod   string            `json:"digest_method"`
	From           string            `json:"from"`
	BuildID        int64             `json:"build_id,omitempty"`
	SourceKind     string            `json:"source_kind,omitempty"`
	Repository     string            `json:"repository,omitempty"`
	Subdir         string            `json:"subdir,omitempty"`
	SnapshotID     string            `json:"snapshot_id,omitempty"`
	SourceRevision string            `json:"source_revision,omitempty"`
	FileCount      int               `json:"file_count"`
	Lockfiles      map[string]string `json:"lockfiles,omitempty"`
	CacheKey       string            `json:"cache_key,omitempty"`
	Image          string            `json:"image,omitempty"`
	WorkspaceID    string            `json:"workspace_id,omitempty"`
}

// codeSource is a fetched source tree ready for workspace_test_create.
type codeSource struct {
	archive    []byte // tar.gz in the Workspaces layout
	provenance codeProvenance
}

type sourceEntry struct {
	path, kind, mode, sum, link string
	data                        []byte
}

var sourceLockfiles = map[string]bool{
	"bun.lock": true, "bun.lockb": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
}

// sourceSkipped mirrors what Deploy and Workspaces leave out of the digest.
func sourceSkipped(p string) bool {
	if p == ".git" || strings.HasPrefix(p, ".git/") {
		return true
	}
	for _, part := range strings.Split(p, "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}

func normalizeEntryPath(name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(name, "\\", "/")), "./")
	if name == "" || name == "." {
		return "", nil
	}
	clean := path.Clean(name)
	if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("source entry %q escapes the archive", name)
	}
	return clean, nil
}

type entryCollector struct {
	entries  map[string]sourceEntry
	expanded int64
}

func (c *entryCollector) add(name string, mode fs.FileMode, symlink bool, r io.Reader, size int64) error {
	clean, err := normalizeEntryPath(name)
	if err != nil || clean == "" {
		return err
	}
	if sourceSkipped(clean) {
		return nil
	}
	if size > maxSourceExpanded {
		return errors.New("source exceeds 128 MiB expanded")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxSourceExpanded-c.expanded+1))
	if err != nil {
		return fmt.Errorf("read source entry %s: %w", clean, err)
	}
	c.expanded += int64(len(data))
	if c.expanded > maxSourceExpanded {
		return errors.New("source exceeds 128 MiB expanded")
	}
	e := sourceEntry{path: clean, kind: "f", mode: "644", data: data}
	if symlink {
		target := string(data)
		resolved := path.Clean(path.Join(path.Dir(clean), target))
		if target == "" || path.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, "../") {
			return fmt.Errorf("source symlink %s points outside the source", clean)
		}
		e.kind, e.mode, e.link, e.data = "l", "777", target, nil
		sum := sha256.Sum256([]byte(target))
		e.sum = hex.EncodeToString(sum[:])
	} else {
		if mode.Perm()&0o111 != 0 {
			e.mode = "755"
		}
		sum := sha256.Sum256(data)
		e.sum = hex.EncodeToString(sum[:])
	}
	c.entries[clean] = e
	if len(c.entries) > maxSourceFiles {
		return fmt.Errorf("source has more than %d files", maxSourceFiles)
	}
	return nil
}

func (c *entryCollector) sorted() ([]sourceEntry, error) {
	if len(c.entries) == 0 {
		return nil, errors.New("source contains no files")
	}
	out := make([]sourceEntry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// entriesFromZip reads a Code zip-v1 snapshot, keeping executable bits and symlinks.
func entriesFromZip(data []byte) ([]sourceEntry, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open Code snapshot: %w", err)
	}
	c := &entryCollector{entries: map[string]sourceEntry{}}
	for _, f := range reader.File {
		mode := f.Mode()
		if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if !mode.IsRegular() && mode&fs.ModeSymlink == 0 {
			return nil, fmt.Errorf("unsupported entry %q in Code snapshot", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read Code snapshot entry %s: %w", f.Name, err)
		}
		err = c.add(f.Name, mode, mode&fs.ModeSymlink != 0, rc, int64(f.UncompressedSize64))
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	return c.sorted()
}

// entriesFromTarGz reads Deploy's kept source copy.
func entriesFromTarGz(data []byte) ([]sourceEntry, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open source archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	c := &entryCollector{entries: map[string]sourceEntry{}}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read source archive: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
			err = c.add(h.Name, fs.FileMode(h.Mode), false, tr, h.Size)
		case tar.TypeSymlink:
			err = c.add(h.Name, 0o777, true, strings.NewReader(h.Linkname), int64(len(h.Linkname)))
		default:
			return nil, fmt.Errorf("unsupported entry %q in source archive", h.Name)
		}
		if err != nil {
			return nil, err
		}
	}
	return c.sorted()
}

// treeDigest is workspaces-tree-v1: sha256 over sorted
// "path NUL type NUL mode NUL sha256 LF" lines. The cache key hashes the lockfiles.
func treeDigest(entries []sourceEntry) (digest string, locks map[string]string) {
	h := sha256.New()
	locks = map[string]string{}
	for _, e := range entries {
		io.WriteString(h, e.path+"\x00"+e.kind+"\x00"+e.mode+"\x00"+e.sum+"\n")
		if e.kind == "f" && sourceLockfiles[path.Base(e.path)] {
			locks[e.path] = e.sum
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), locks
}

// writeTarGz writes a deterministic tar.gz Workspaces imports with the same digest.
func writeTarGz(entries []sourceEntry) ([]byte, error) {
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gz.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gz)
	fixed := time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range entries {
		h := &tar.Header{Name: e.path, ModTime: fixed, Format: tar.FormatPAX}
		if e.kind == "l" {
			h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, e.link, 0o777
		} else {
			h.Typeflag, h.Size, h.Mode = tar.TypeReg, int64(len(e.data)), 0o644
			if e.mode == "755" {
				h.Mode = 0o755
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if e.kind == "f" {
			if _, err := tw.Write(e.data); err != nil {
				return nil, err
			}
		}
		if buf.Len() > maxWorkspaceSource {
			return nil, errors.New("source is larger than the 8 MB compressed archive Workspaces accepts")
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > maxWorkspaceSource {
		return nil, fmt.Errorf("source is %.1f MB compressed; Workspaces accepts 8 MB", float64(buf.Len())/(1<<20))
	}
	return buf.Bytes(), nil
}

// deployBuildSource is deploy_build_source's answer (Deploy 0.33.0+).
type deployBuildSource struct {
	SourceDigest    string            `json:"source_digest"`
	DigestMethod    string            `json:"digest_method"`
	FileCount       int               `json:"file_count"`
	Lockfiles       map[string]string `json:"lockfiles"`
	SourceKind      string            `json:"source_kind"`
	BuildID         int64             `json:"build_id"`
	CandidateDigest string            `json:"candidate_digest"`
	Code            *struct {
		SnapshotID     string `json:"snapshot_id"`
		SourceRevision string `json:"source_revision"`
		Repository     string `json:"repository"`
		Subdir         string `json:"subdir"`
	} `json:"code"`
	Archive *struct {
		Format   string `json:"format"`
		SHA256   string `json:"sha256"`
		Size     int64  `json:"size_bytes"`
		URL      string `json:"url"`
		URLError string `json:"url_error"`
	} `json:"archive"`
	ArchiveNote string `json:"archive_note"`
}

type codeSnapshot struct {
	SnapshotID     string `json:"snapshot_id"`
	SourceRevision string `json:"source_revision"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Format         string `json:"format"`
}

// fetchCandidateSource retrieves the exact source a Deploy build was made from.
func fetchCandidateSource(ctx context.Context, app *sdk.AppCtx, buildID int64) (*codeSource, error) {
	platform := app.PlatformAPI()
	if platform == nil {
		return nil, errors.New("code checks on the candidate need the Deploy app bound to Tests")
	}
	var src deployBuildSource
	if err := sdk.CallAppResultContext(ctx, platform, "deploy", "deploy_build_source",
		map[string]any{"build_id": buildID, "_project_id": app.CurrentProject()}, &src); err != nil {
		return nil, fmt.Errorf("Deploy could not provide the source of build %d (Deploy 0.33.0 or newer records it): %s", buildID, deployReason(err))
	}
	if src.DigestMethod != sourceDigestMethod || !strings.HasPrefix(src.SourceDigest, "sha256:") {
		return nil, fmt.Errorf("Deploy recorded build %d's source with method %q; Tests needs %s", buildID, src.DigestMethod, sourceDigestMethod)
	}
	proof := codeProvenance{From: "candidate", BuildID: buildID, SourceKind: src.SourceKind, DigestMethod: src.DigestMethod, Lockfiles: src.Lockfiles}
	var entries []sourceEntry
	var err error
	switch {
	case src.Code != nil && src.Code.SnapshotID != "":
		proof.Repository, proof.Subdir, proof.SnapshotID, proof.SourceRevision = src.Code.Repository, src.Code.Subdir, src.Code.SnapshotID, src.Code.SourceRevision
		var data []byte
		data, err = readCodeSnapshot(ctx, app, src.Code.Repository, src.Code.Subdir, src.Code.SnapshotID)
		if err == nil {
			entries, err = entriesFromZip(data)
		}
	case src.Archive != nil && src.Archive.URL != "":
		var data []byte
		data, err = downloadSourceArchive(ctx, src.Archive.URL, src.Archive.SHA256)
		if err == nil {
			entries, err = entriesFromTarGz(data)
		}
	case src.Archive != nil:
		err = fmt.Errorf("Deploy kept build %d's source but could not sign a download link: %s", buildID, src.Archive.URLError)
	default:
		note := src.ArchiveNote
		if note == "" {
			note = "no source copy is available"
		}
		err = fmt.Errorf("build %d's source cannot be retrieved: %s", buildID, note)
	}
	if err != nil {
		return nil, err
	}
	return packSource(entries, proof, src.SourceDigest)
}

// fetchRepositorySource exports the current state of a Code repository.
func fetchRepositorySource(ctx context.Context, app *sdk.AppCtx, repository, subdir string) (*codeSource, error) {
	platform := app.PlatformAPI()
	if platform == nil {
		return nil, errors.New("code checks on a repository need the Code app bound to Tests")
	}
	args := map[string]any{"slug": repository, "metadata_only": true}
	if subdir != "" {
		args["subdir"] = subdir
	}
	var snap codeSnapshot
	if err := sdk.CallAppResultContext(ctx, platform, "code", "repos_export", args, &snap); err != nil {
		return nil, fmt.Errorf("Code could not export %s: %s", repository, deployReason(err))
	}
	data, err := readSnapshotChunks(ctx, platform, repository, snap)
	if err != nil {
		return nil, err
	}
	entries, err := entriesFromZip(data)
	if err != nil {
		return nil, err
	}
	proof := codeProvenance{From: "code", Repository: repository, Subdir: subdir, SnapshotID: snap.SnapshotID, SourceRevision: snap.SourceRevision, SourceKind: "code", DigestMethod: sourceDigestMethod}
	return packSource(entries, proof, "")
}

// readCodeSnapshot reopens the snapshot Deploy built from. Code keeps
// snapshots for 24 hours; an unchanged repository re-exports to the same id.
func readCodeSnapshot(ctx context.Context, app *sdk.AppCtx, repository, subdir, snapshotID string) ([]byte, error) {
	platform := app.PlatformAPI()
	var snap codeSnapshot
	err := sdk.CallAppResultContext(ctx, platform, "code", "repos_export", map[string]any{"slug": repository, "snapshot_id": snapshotID, "metadata_only": true}, &snap)
	if err != nil {
		args := map[string]any{"slug": repository, "metadata_only": true}
		if subdir != "" {
			args["subdir"] = subdir
		}
		if again := sdk.CallAppResultContext(ctx, platform, "code", "repos_export", args, &snap); again != nil {
			return nil, fmt.Errorf("Code snapshot %s of %s is no longer available (%s), and the repository could not be exported again: %s", snapshotID, repository, deployReason(err), deployReason(again))
		}
		if snap.SnapshotID != snapshotID {
			return nil, fmt.Errorf("Code snapshot %s of %s expired (snapshots are kept 24 hours) and the repository has changed since; rebuild the candidate to test its source", snapshotID, repository)
		}
	}
	if snap.SnapshotID != snapshotID {
		return nil, fmt.Errorf("Code returned snapshot %s for %s, expected %s", snap.SnapshotID, repository, snapshotID)
	}
	return readSnapshotChunks(ctx, platform, repository, snap)
}

func readSnapshotChunks(ctx context.Context, platform sdk.PlatformClient, repository string, snap codeSnapshot) ([]byte, error) {
	if snap.SnapshotID == "" || snap.Size <= 0 || len(snap.SHA256) != 64 {
		return nil, fmt.Errorf("Code returned an incomplete snapshot for %s", repository)
	}
	if snap.Size > maxSourceDownload {
		return nil, fmt.Errorf("Code snapshot of %s is %d MB; code checks accept 64 MB", repository, snap.Size>>20)
	}
	var buf bytes.Buffer
	var offset int64
	for {
		var chunk struct {
			DataB64    string `json:"data_b64"`
			NextOffset int64  `json:"next_offset"`
			EOF        bool   `json:"eof"`
		}
		if err := sdk.CallAppResultContext(ctx, platform, "code", "repos_snapshot_read",
			map[string]any{"slug": repository, "snapshot_id": snap.SnapshotID, "offset": offset, "limit": snapshotChunk}, &chunk); err != nil {
			return nil, fmt.Errorf("read Code snapshot %s: %s", snap.SnapshotID, deployReason(err))
		}
		data, err := base64.StdEncoding.DecodeString(chunk.DataB64)
		if err != nil {
			return nil, errors.New("Code returned an undecodable snapshot chunk")
		}
		buf.Write(data)
		if int64(buf.Len()) > snap.Size {
			return nil, errors.New("Code snapshot is larger than its recorded size")
		}
		if chunk.EOF {
			break
		}
		if chunk.NextOffset <= offset || len(data) == 0 {
			return nil, errors.New("Code snapshot read made no progress")
		}
		offset = chunk.NextOffset
	}
	sum := sha256.Sum256(buf.Bytes())
	if int64(buf.Len()) != snap.Size || hex.EncodeToString(sum[:]) != snap.SHA256 {
		return nil, fmt.Errorf("Code snapshot %s failed its sha256 check", snap.SnapshotID)
	}
	return buf.Bytes(), nil
}

// downloadSourceArchive fetches Deploy's signed source link. The URL comes
// from the bound Deploy app, so it may point at the platform's own host.
func downloadSourceArchive(ctx context.Context, url, want string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, sourceDownloadDeadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid source link from Deploy: %v", err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download the build's source from Deploy: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download the build's source from Deploy: HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxSourceDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download the build's source from Deploy: %v", err)
	}
	if len(data) > maxSourceDownload {
		return nil, errors.New("the build's source archive exceeds 64 MB")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return nil, errors.New("the build's source archive failed its sha256 check")
	}
	return data, nil
}

// packSource computes the digest, checks it against the expected one, and
// writes the Workspaces archive.
func packSource(entries []sourceEntry, proof codeProvenance, expected string) (*codeSource, error) {
	digest, locks := treeDigest(entries)
	if expected != "" && digest != expected {
		return nil, fmt.Errorf("the retrieved source has digest %s, but Deploy recorded %s for build %d", short(strings.TrimPrefix(digest, "sha256:")), short(strings.TrimPrefix(expected, "sha256:")), proof.BuildID)
	}
	archive, err := writeTarGz(entries)
	if err != nil {
		return nil, err
	}
	proof.SourceDigest, proof.FileCount = digest, len(entries)
	if len(locks) > 0 {
		proof.Lockfiles = locks
	}
	return &codeSource{archive: archive, provenance: proof}, nil
}
