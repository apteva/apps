// Media — metadata rows in this app's DB; bytes proxied to the bound
// `storage` app at /.media/<uuid>.<ext>. Without a storage binding,
// media_upload returns a structured "binding required" error rather
// than failing silently.

package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type Media struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	Kind        string `json:"kind"`
	StoragePath string `json:"storage_path"`
	Filename    string `json:"filename,omitempty"`
	Mime        string `json:"mime,omitempty"`
	Width       *int   `json:"width,omitempty"`
	Height      *int   `json:"height,omitempty"`
	ByteSize    int64  `json:"byte_size"`
	Alt         string `json:"alt,omitempty"`
	Caption     string `json:"caption,omitempty"`
	Source      string `json:"source"`
	UploadedAt  string `json:"uploaded_at,omitempty"`
}

func dbCreateMedia(db *sql.DB, m Media) (*Media, error) {
	res, err := db.Exec(`INSERT INTO media (project_id, kind, storage_path, filename, mime, byte_size, alt, caption, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProjectID, m.Kind, m.StoragePath, m.Filename, m.Mime, m.ByteSize, m.Alt, m.Caption, m.Source)
	if err != nil {
		return nil, fmt.Errorf("insert media: %w", err)
	}
	id, _ := res.LastInsertId()
	return dbGetMedia(db, m.ProjectID, id)
}

func dbGetMedia(db *sql.DB, projectID string, id int64) (*Media, error) {
	row := db.QueryRow(`SELECT id, project_id, kind, storage_path, filename, mime, width, height, byte_size, alt, caption, source, uploaded_at
		FROM media WHERE project_id=? AND id=?`, projectID, id)
	return scanMedia(row)
}

func scanMedia(row rowScanner) (*Media, error) {
	var m Media
	var width, height sql.NullInt64
	var uploaded sql.NullString
	if err := row.Scan(&m.ID, &m.ProjectID, &m.Kind, &m.StoragePath, &m.Filename, &m.Mime, &width, &height, &m.ByteSize, &m.Alt, &m.Caption, &m.Source, &uploaded); err != nil {
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

func dbListMedia(db *sql.DB, projectID, kind, q string, limit, offset int) ([]Media, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id = ?"}
	args := []any{projectID}
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
	rows, err := db.Query(`SELECT id, project_id, kind, storage_path, filename, mime, width, height, byte_size, alt, caption, source, uploaded_at
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

func dbUpdateMediaMeta(db *sql.DB, projectID string, id int64, alt, caption *string) error {
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
	args = append(args, projectID, id)
	_, err := db.Exec(`UPDATE media SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, args...)
	return err
}

// ── storage app interaction ────────────────────────────────────────

// storagePath generates a unique filename under /.media/.
func storagePath(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".bin"
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "/.media/" + hex.EncodeToString(raw[:]) + ext
}

// storageWrite delegates to the bound storage app's files_upload tool.
// Returns a structured error when no storage binding exists so the
// caller can surface a clear "bind a storage app" message.
func storageWrite(ctx *sdk.AppCtx, path string, bytes []byte, mime string) error {
	bound := ctx.IntegrationFor("storage")
	if bound == nil {
		return errors.New("storage app not bound — install/bind the storage app to enable media uploads")
	}
	in := map[string]any{
		"path":      path,
		"bytes_b64": base64.StdEncoding.EncodeToString(bytes),
		"mime":      mime,
	}
	var out struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	return ctx.PlatformAPI().CallAppResult("storage", "files_upload", in, &out)
}

// storageRead fetches bytes via the storage app's files_read tool.
func storageRead(ctx *sdk.AppCtx, path string) ([]byte, string, error) {
	bound := ctx.IntegrationFor("storage")
	if bound == nil {
		return nil, "", errors.New("storage app not bound")
	}
	var out struct {
		BytesB64 string `json:"bytes_b64"`
		Mime     string `json:"mime"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_read", map[string]any{"path": path}, &out); err != nil {
		return nil, "", err
	}
	data, err := base64.StdEncoding.DecodeString(out.BytesB64)
	return data, out.Mime, err
}

// ── MCP tool handlers ────────────────────────────────────────────

func (a *App) toolMediaUpload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	var data []byte
	filename := asString(args["filename"])
	if b64 := asString(args["bytes_b64"]); b64 != "" {
		data, err = base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("bytes_b64: %w", err)
		}
	} else if url := asString(args["url"]); url != "" {
		resp, err := http.Get(url)
		if err != nil {
			return nil, fmt.Errorf("fetch url: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("fetch url: status %d", resp.StatusCode)
		}
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if filename == "" {
			filename = filepath.Base(url)
		}
	} else {
		return nil, errors.New("bytes_b64 or url required")
	}
	if filename == "" {
		filename = "upload.bin"
	}
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	path := storagePath(filename)
	if err := storageWrite(ctx, path, data, mimeType); err != nil {
		return nil, err
	}
	kind := mediaKindFromMime(mimeType)
	m, err := dbCreateMedia(ctx.AppDB(), Media{
		ProjectID:   pid,
		Kind:        kind,
		StoragePath: path,
		Filename:    filename,
		Mime:        mimeType,
		ByteSize:    int64(len(data)),
		Alt:         asString(args["alt"]),
		Caption:     asString(args["caption"]),
		Source:      asStringDefault(args["source"], "upload"),
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
	limit, offset := 50, 0
	if v, ok := asInt64(args["limit"]); ok {
		limit = int(v)
	}
	if v, ok := asInt64(args["offset"]); ok {
		offset = int(v)
	}
	items, err := dbListMedia(ctx.AppDB(), pid, asString(args["kind"]), asString(args["q"]), limit, offset)
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
	if err := dbUpdateMediaMeta(ctx.AppDB(), pid, id, alt, caption); err != nil {
		return nil, err
	}
	m, err := dbGetMedia(ctx.AppDB(), pid, id)
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
	switch r.Method {
	case http.MethodGet:
		items, err := dbListMedia(ctx.AppDB(), pid,
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
