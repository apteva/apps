// Media — metadata rows in this app's DB; bytes proxied to the bound
// `storage` app at /.media/<uuid>.<ext>. Multi-site (v2.0): metadata
// rows are site-scoped so each site has its own media library view.
// Bytes themselves remain in one shared storage namespace.

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Media struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id,omitempty"`
	SiteID        int64  `json:"site_id"`
	Kind          string `json:"kind"`
	StoragePath   string `json:"storage_path"`
	StorageFileID int64  `json:"storage_file_id,omitempty"`
	Filename      string `json:"filename,omitempty"`
	Mime          string `json:"mime,omitempty"`
	Width         *int   `json:"width,omitempty"`
	Height        *int   `json:"height,omitempty"`
	ByteSize      int64  `json:"byte_size"`
	Alt           string `json:"alt,omitempty"`
	Caption       string `json:"caption,omitempty"`
	Source        string `json:"source"`
	UploadedAt    string `json:"uploaded_at,omitempty"`
}

// hydratePostMediaPaths returns a render-only copy of the post with
// numeric media references replaced by tenant-scoped storage paths.
// Theme templates never query the database and cannot resolve an ID
// belonging to another site.
func hydratePostMediaPaths(db *sql.DB, projectID string, siteID int64, post *Post) *Post {
	if post == nil {
		return nil
	}
	copyPost := *post
	copyPost.BodyBlocks = Document{Version: post.BodyBlocks.Version, Blocks: cloneBlocks(post.BodyBlocks.Blocks)}
	ids := map[int64]struct{}{}
	walkBlocks(copyPost.BodyBlocks.Blocks, func(block *Block) {
		for key, value := range block.Attrs {
			switch key {
			case "media_id", "image_media_id":
				if id, ok := asInt64(value); ok && id > 0 {
					ids[id] = struct{}{}
				}
			case "media_ids":
				if values, ok := value.([]any); ok {
					for _, item := range values {
						if id, ok := asInt64(item); ok && id > 0 {
							ids[id] = struct{}{}
						}
					}
				}
			}
		}
	})
	if len(ids) == 0 {
		return &copyPost
	}
	args := []any{projectID, siteID}
	marks := make([]string, 0, len(ids))
	for id := range ids {
		marks = append(marks, "?")
		args = append(args, id)
	}
	rows, err := db.Query(`SELECT id, storage_path FROM media WHERE project_id=? AND site_id=? AND id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return &copyPost
	}
	paths := map[int64]string{}
	for rows.Next() {
		var id int64
		var storagePath string
		if rows.Scan(&id, &storagePath) == nil {
			paths[id] = storagePath
		}
	}
	rows.Close()
	walkBlocks(copyPost.BodyBlocks.Blocks, func(block *Block) {
		for key, value := range block.Attrs {
			switch key {
			case "media_id", "image_media_id":
				if id, ok := asInt64(value); ok {
					block.Attrs[key] = paths[id]
				}
			case "media_ids":
				if values, ok := value.([]any); ok {
					resolved := make([]any, 0, len(values))
					for _, item := range values {
						if id, ok := asInt64(item); ok && paths[id] != "" {
							resolved = append(resolved, paths[id])
						}
					}
					block.Attrs[key] = resolved
				}
			}
		}
	})
	return &copyPost
}

func cloneBlocks(blocks []Block) []Block {
	out := make([]Block, len(blocks))
	for i, block := range blocks {
		out[i] = block
		if block.Attrs != nil {
			out[i].Attrs = make(map[string]any, len(block.Attrs))
			for key, value := range block.Attrs {
				if values, ok := value.([]any); ok {
					out[i].Attrs[key] = append([]any(nil), values...)
				} else {
					out[i].Attrs[key] = value
				}
			}
		}
		out[i].Inner = cloneBlocks(block.Inner)
	}
	return out
}

func walkBlocks(blocks []Block, visit func(*Block)) {
	for i := range blocks {
		visit(&blocks[i])
		walkBlocks(blocks[i].Inner, visit)
	}
}

func dbCreateMedia(db *sql.DB, m Media) (*Media, error) {
	res, err := db.Exec(`INSERT INTO media (project_id, site_id, kind, storage_path, storage_file_id, filename, mime, byte_size, alt, caption, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProjectID, m.SiteID, m.Kind, m.StoragePath, m.StorageFileID, m.Filename, m.Mime, m.ByteSize, m.Alt, m.Caption, m.Source)
	if err != nil {
		return nil, fmt.Errorf("insert media: %w", err)
	}
	id, _ := res.LastInsertId()
	return dbGetMedia(db, m.ProjectID, m.SiteID, id)
}

func dbGetMedia(db *sql.DB, projectID string, siteID int64, id int64) (*Media, error) {
	row := db.QueryRow(`SELECT id, project_id, COALESCE(site_id, 0), kind, storage_path, COALESCE(storage_file_id, 0), filename, mime, width, height, byte_size, alt, caption, source, uploaded_at
		FROM media WHERE project_id=? AND site_id=? AND id=?`, projectID, siteID, id)
	return scanMedia(row)
}

func dbGetMediaByStoragePath(db *sql.DB, projectID string, siteID int64, storagePath string) (*Media, error) {
	row := db.QueryRow(`SELECT id, project_id, COALESCE(site_id, 0), kind, storage_path, COALESCE(storage_file_id, 0), filename, mime, width, height, byte_size, alt, caption, source, uploaded_at
		FROM media WHERE project_id=? AND site_id=? AND storage_path=?`, projectID, siteID, storagePath)
	return scanMedia(row)
}

func scanMedia(row rowScanner) (*Media, error) {
	var m Media
	var width, height sql.NullInt64
	var uploaded sql.NullString
	if err := row.Scan(&m.ID, &m.ProjectID, &m.SiteID, &m.Kind, &m.StoragePath, &m.StorageFileID, &m.Filename, &m.Mime, &width, &height, &m.ByteSize, &m.Alt, &m.Caption, &m.Source, &uploaded); err != nil {
		return nil, err
	}
	if width.Valid {
		v := int(width.Int64)
		m.Width = &v
	}
	if height.Valid {
		v := int(height.Int64)
		m.Height = &v
	}
	if uploaded.Valid {
		m.UploadedAt = uploaded.String
	}
	return &m, nil
}

func dbListMedia(db *sql.DB, projectID string, siteID int64, kind, q string, limit, offset int) ([]Media, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id = ?", "site_id = ?"}
	args := []any{projectID, siteID}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if q != "" {
		where = append(where, "(filename LIKE ? OR alt LIKE ? OR caption LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	args = append(args, limit, offset)
	rows, err := db.Query(`SELECT id, project_id, COALESCE(site_id, 0), kind, storage_path, COALESCE(storage_file_id, 0), filename, mime, width, height, byte_size, alt, caption, source, uploaded_at
		FROM media WHERE `+strings.Join(where, " AND ")+` ORDER BY uploaded_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, nil
}

func dbUpdateMediaMeta(db *sql.DB, projectID string, siteID int64, id int64, alt, caption *string) error {
	sets := []string{}
	args := []any{}
	if alt != nil {
		sets = append(sets, "alt = ?")
		args = append(args, *alt)
	}
	if caption != nil {
		sets = append(sets, "caption = ?")
		args = append(args, *caption)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, projectID, siteID, id)
	_, err := db.Exec(`UPDATE media SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND site_id=? AND id=?`, args...)
	return err
}

// ── storage app interaction ────────────────────────────────────────

func storagePath(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".bin"
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "/.media/" + hex.EncodeToString(raw[:]) + ext
}

func storageWrite(ctx *sdk.AppCtx, projectID, storagePath string, bytes []byte, mimeType string) (int64, error) {
	bound := ctx.IntegrationFor("storage")
	if bound == nil {
		return 0, errors.New("storage app not bound — install/bind the storage app to enable media uploads")
	}
	in := map[string]any{
		"_project_id":    projectID,
		"name":           filepath.Base(storagePath),
		"folder":         "/.media",
		"content_base64": base64.StdEncoding.EncodeToString(bytes),
		"content_type":   mimeType,
		"source":         "content-media",
		"visibility":     "signed",
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", in, &out); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, errors.New("storage upload returned no file id")
	}
	return out.ID, nil
}

func storageReadLegacy(ctx *sdk.AppCtx, projectID, path string) ([]byte, string, error) {
	bound := ctx.IntegrationFor("storage")
	if bound == nil {
		return nil, "", errors.New("storage app not bound")
	}
	var out struct {
		BytesB64 string `json:"bytes_b64"`
		Mime     string `json:"mime"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_read", map[string]any{"_project_id": projectID, "path": path}, &out); err != nil {
		return nil, "", err
	}
	data, err := base64.StdEncoding.DecodeString(out.BytesB64)
	return data, out.Mime, err
}

func storageSignedURL(ctx *sdk.AppCtx, projectID string, fileID int64) (string, error) {
	if ctx.IntegrationFor("storage") == nil {
		return "", errors.New("storage app not bound")
	}
	var out struct {
		URL string `json:"url"`
	}
	err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{
		"_project_id": projectID,
		"id":          fileID,
		"ttl_seconds": 3600,
	}, &out)
	if err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("storage returned an empty signed URL")
	}
	return out.URL, nil
}

const maxMediaUploadBytes int64 = 25 << 20

func fetchMediaURL(rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, "", errors.New("url must be an http(s) URL without credentials")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, candidate := range ips {
				if isPublicIP(candidate.IP) {
					return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
				}
			}
			return nil, fmt.Errorf("url host %q resolves only to private or reserved addresses", host)
		},
	}
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirected to unsupported URL scheme")
			}
			return nil
		},
	}
	resp, err := client.Get(parsed.String())
	if err != nil {
		return nil, "", fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("fetch url: status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxMediaUploadBytes {
		return nil, "", fmt.Errorf("remote file exceeds %d bytes", maxMediaUploadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaUploadBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxMediaUploadBytes {
		return nil, "", fmt.Errorf("remote file exceeds %d bytes", maxMediaUploadBytes)
	}
	return data, filepath.Base(resp.Request.URL.Path), nil
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

// ── MCP tool handlers ────────────────────────────────────────────

func (a *App) toolMediaUpload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	var data []byte
	filename := asString(args["filename"])
	if b64 := asString(args["bytes_b64"]); b64 != "" {
		if int64(len(b64)) > (maxMediaUploadBytes+2)/3*4+4 {
			return nil, fmt.Errorf("upload exceeds %d bytes", maxMediaUploadBytes)
		}
		data, err = base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("bytes_b64: %w", err)
		}
	} else if rawURL := asString(args["url"]); rawURL != "" {
		var remoteName string
		data, remoteName, err = fetchMediaURL(rawURL)
		if err != nil {
			return nil, err
		}
		if filename == "" {
			filename = remoteName
		}
	} else {
		return nil, errors.New("bytes_b64 or url required")
	}
	if filename == "" {
		filename = "upload.bin"
	}
	filename = filepath.Base(filename)
	if int64(len(data)) > maxMediaUploadBytes {
		return nil, fmt.Errorf("upload exceeds %d bytes", maxMediaUploadBytes)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	path := storagePath(filename)
	storageFileID, err := storageWrite(ctx, pid, path, data, mimeType)
	if err != nil {
		return nil, err
	}
	kind := mediaKindFromMime(mimeType)
	m, err := dbCreateMedia(ctx.AppDB(), Media{
		ProjectID:     pid,
		SiteID:        siteID,
		Kind:          kind,
		StoragePath:   path,
		StorageFileID: storageFileID,
		Filename:      filename,
		Mime:          mimeType,
		ByteSize:      int64(len(data)),
		Alt:           asString(args["alt"]),
		Caption:       asString(args["caption"]),
		Source:        asStringDefault(args["source"], "upload"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"media": m}, nil
}

func (a *App) toolMediaList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	limit, offset := 50, 0
	if v, ok := asInt64(args["limit"]); ok {
		limit = int(v)
	}
	if v, ok := asInt64(args["offset"]); ok {
		offset = int(v)
	}
	items, err := dbListMedia(ctx.AppDB(), pid, siteID, asString(args["kind"]), asString(args["q"]), limit, offset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"media": items}, nil
}

func (a *App) toolMediaSetMeta(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	var alt, caption *string
	if v, ok := args["alt"].(string); ok {
		alt = &v
	}
	if v, ok := args["caption"].(string); ok {
		caption = &v
	}
	if err := dbUpdateMediaMeta(ctx.AppDB(), pid, siteID, id, alt, caption); err != nil {
		return nil, err
	}
	m, err := dbGetMedia(ctx.AppDB(), pid, siteID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"media": m}, nil
}

func mediaKindFromMime(m string) string {
	switch {
	case strings.HasPrefix(m, "image/"):
		return "image"
	case strings.HasPrefix(m, "video/"):
		return "video"
	case strings.HasPrefix(m, "audio/"):
		return "audio"
	default:
		return "file"
	}
}

// ── REST handler ─────────────────────────────────────────────────

func (a *App) handleHTTPMedia(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := dbListMedia(ctx.AppDB(), pid, siteID,
			r.URL.Query().Get("kind"), r.URL.Query().Get("q"), 50, 0)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"media": items})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		body["_site_id"] = siteID
		out, err := a.toolMediaUpload(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
