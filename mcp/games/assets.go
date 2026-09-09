package main

// Content metadata and retained bytes are game-scoped. The bounded archive is
// deliberately independent of deletable provider/Storage files and expiring URLs.
import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const contentMaxBlob = 25 << 20
const contentMaxArchive = 512 << 20
const contentSchema = `
CREATE TABLE IF NOT EXISTS game_assets(project_id TEXT NOT NULL,game_id TEXT NOT NULL,id TEXT NOT NULL,name TEXT NOT NULL,kind TEXT NOT NULL,head TEXT NOT NULL DEFAULT '',PRIMARY KEY(project_id,game_id,id),FOREIGN KEY(project_id,game_id) REFERENCES games(project_id,id));
CREATE TABLE IF NOT EXISTS game_content_blobs(project_id TEXT NOT NULL,game_id TEXT NOT NULL,sha TEXT NOT NULL,mime TEXT NOT NULL,bytes BLOB NOT NULL,PRIMARY KEY(project_id,game_id,sha));
CREATE TABLE IF NOT EXISTS game_asset_versions(project_id TEXT NOT NULL,game_id TEXT NOT NULL,id TEXT NOT NULL,asset_id TEXT NOT NULL,name TEXT NOT NULL,parent TEXT NOT NULL,spec TEXT NOT NULL,source TEXT NOT NULL,provenance TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(project_id,game_id,id),FOREIGN KEY(project_id,game_id,asset_id) REFERENCES game_assets(project_id,game_id,id));
CREATE INDEX IF NOT EXISTS game_asset_history ON game_asset_versions(project_id,game_id,asset_id,created_at);
CREATE TABLE IF NOT EXISTS game_renditions(project_id TEXT NOT NULL,game_id TEXT NOT NULL,id TEXT NOT NULL,version_id TEXT NOT NULL,document TEXT NOT NULL,PRIMARY KEY(project_id,game_id,id));
CREATE TABLE IF NOT EXISTS game_content_manifests(project_id TEXT NOT NULL,game_id TEXT NOT NULL,digest TEXT NOT NULL,document TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(project_id,game_id,digest));
CREATE TABLE IF NOT EXISTS game_content_heads(project_id TEXT NOT NULL,game_id TEXT NOT NULL,environment TEXT NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(project_id,game_id,environment));
CREATE TABLE IF NOT EXISTS game_content_reviews(project_id TEXT NOT NULL,game_id TEXT NOT NULL,digest TEXT NOT NULL,actor TEXT NOT NULL,note TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(project_id,game_id,digest));
CREATE TABLE IF NOT EXISTS game_content_moves(id INTEGER PRIMARY KEY AUTOINCREMENT,project_id TEXT NOT NULL,game_id TEXT NOT NULL,environment TEXT NOT NULL,previous TEXT NOT NULL,digest TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS game_content_jobs(project_id TEXT NOT NULL,game_id TEXT NOT NULL,request_key TEXT NOT NULL,fingerprint TEXT NOT NULL,media_install INTEGER NOT NULL,asset_id TEXT NOT NULL,parent TEXT NOT NULL,spec TEXT NOT NULL,status TEXT NOT NULL,result TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(project_id,game_id,request_key));
`

var contentSlug = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var contentDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type AssetDependency struct {
	Asset   string `json:"asset"`
	Version string `json:"version"`
}
type AssetFrame struct {
	Name       string  `json:"name"`
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	PivotX     float64 `json:"pivot_x"`
	PivotY     float64 `json:"pivot_y"`
	DurationMS int     `json:"duration_ms"`
}
type AssetAnimation struct {
	Name   string   `json:"name"`
	Frames []string `json:"frames"`
	Loop   bool     `json:"loop"`
}
type AssetSpec struct {
	Rig      *RigSpec      `json:"rig,omitempty"`
	Material *MaterialSpec `json:"material,omitempty"`
	Font     *FontSpec     `json:"font,omitempty"`
	Stream   *StreamSpec   `json:"stream,omitempty"`
	Locale   *LocaleSpec   `json:"locale,omitempty"`
	Table    *TableSpec    `json:"table,omitempty"`

	License       string            `json:"license"`
	Tags          []string          `json:"tags,omitempty"`
	Description   string            `json:"description,omitempty"`
	Dependencies  []AssetDependency `json:"dependencies,omitempty"`
	Palette       []string          `json:"palette,omitempty"`
	Rules         string            `json:"rules,omitempty"`
	Width         int               `json:"width,omitempty"`
	Height        int               `json:"height,omitempty"`
	RequireAlpha  bool              `json:"require_alpha,omitempty"`
	PixelsPerUnit float64           `json:"pixels_per_unit,omitempty"`
	Frames        []AssetFrame      `json:"frames,omitempty"`
	Animations    []AssetAnimation  `json:"animations,omitempty"`
	Recipe        map[string]any    `json:"recipe,omitempty"`
}
type AssetVersion struct {
	ID         string         `json:"id"`
	AssetID    string         `json:"asset_id"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Parent     string         `json:"parent"`
	Spec       AssetSpec      `json:"spec"`
	Source     string         `json:"source"`
	Provenance map[string]any `json:"provenance"`
	CreatedAt  string         `json:"created_at"`
}
type contentDB interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}

func initializeContent(ctx *sdk.AppCtx) error {
	if _, e := ctx.AppDB().Exec(contentSchema); e != nil {
		return e
	}
	for _, table := range []string{"game_asset_versions", "game_renditions", "game_content_manifests", "game_content_blobs"} {
		for _, op := range []string{"UPDATE", "DELETE"} {
			query := "CREATE TRIGGER IF NOT EXISTS " + table + "_immutable_" + op + " BEFORE " + op + " ON " + table + " BEGIN SELECT RAISE(ABORT, 'immutable content cannot be modified'); END"
			if _, e := ctx.AppDB().Exec(query); e != nil {
				return e
			}
		}
	}
	return nil
}
func contentJSON(v any) string   { b, _ := json.Marshal(v); return string(b) }
func contentSHA(b []byte) string { return hashBytes(b) }
func contentBlobPut(db contentDB, s GameScope, b []byte, mime string) (string, error) {
	if len(b) == 0 || len(b) > contentMaxBlob {
		return "", errors.New("asset bytes must be between 1 byte and 25 MiB")
	}
	sha := contentSHA(b)
	var exists int
	if e := db.QueryRow(`SELECT count(*) FROM game_content_blobs WHERE project_id=? AND game_id=? AND sha=?`, s.ProjectID, s.GameID, sha).Scan(&exists); e != nil {
		return "", e
	}
	if exists > 0 {
		return sha, nil
	}
	var size int64
	if e := db.QueryRow(`SELECT coalesce(sum(length(bytes)),0) FROM game_content_blobs WHERE project_id=? AND game_id=?`, s.ProjectID, s.GameID).Scan(&size); e != nil {
		return "", e
	}
	if size+int64(len(b)) > contentMaxArchive {
		return "", errors.New("game content archive exceeds 512 MiB; published bytes are never automatically evicted")
	}
	_, e := db.Exec(`INSERT INTO game_content_blobs VALUES(?,?,?,?,?)`, s.ProjectID, s.GameID, sha, mime, b)
	return sha, e
}
func contentBlob(ctx *sdk.AppCtx, s GameScope, sha string) ([]byte, string, error) {
	var b []byte
	var mime string
	e := ctx.AppDB().QueryRow(`SELECT bytes,mime FROM game_content_blobs WHERE project_id=? AND game_id=? AND sha=?`, s.ProjectID, s.GameID, sha).Scan(&b, &mime)
	if e != nil {
		return nil, "", errors.New("content bytes not found for this game")
	}
	if contentSHA(b) != sha {
		return nil, "", errors.New("content checksum mismatch")
	}
	return b, mime, nil
}
func assetVersionGet(ctx *sdk.AppCtx, s GameScope, id string) (AssetVersion, error) {
	v := AssetVersion{}
	var spec, provenance string
	e := ctx.AppDB().QueryRow(`SELECT v.id,v.asset_id,a.kind,v.name,v.parent,v.spec,v.source,v.provenance,v.created_at FROM game_asset_versions v JOIN game_assets a ON a.project_id=v.project_id AND a.game_id=v.game_id AND a.id=v.asset_id WHERE v.project_id=? AND v.game_id=? AND v.id=?`, s.ProjectID, s.GameID, id).Scan(&v.ID, &v.AssetID, &v.Kind, &v.Name, &v.Parent, &spec, &v.Source, &provenance, &v.CreatedAt)
	if e != nil {
		return v, errors.New("asset version not found for this game")
	}
	if e = json.Unmarshal([]byte(spec), &v.Spec); e != nil {
		return v, e
	}
	e = json.Unmarshal([]byte(provenance), &v.Provenance)
	return v, e
}
func assetSpecParse(v any) (AssetSpec, error) {
	var s AssetSpec
	b, e := json.Marshal(v)
	if e != nil {
		return s, e
	}
	if len(b) > 256<<10 {
		return s, errors.New("spec exceeds 256 KiB")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&s); e != nil {
		return s, e
	}
	if len(s.License) == 0 || len(s.License) > 1024 {
		return s, errors.New("license or rights status required (up to 1024 characters)")
	}
	if len(s.Tags) > 32 || len(s.Dependencies) > 32 || len(s.Frames) > 256 || len(s.Animations) > 64 || len(s.Palette) > 256 {
		return s, errors.New("asset specification exceeds collection limits")
	}
	if s.Width < 0 || s.Height < 0 || s.Width > 4096 || s.Height > 4096 {
		return s, errors.New("canvas dimensions must be at most 4096")
	}
	if s.PixelsPerUnit < 0 || s.PixelsPerUnit > 10000 {
		return s, errors.New("invalid pixels_per_unit")
	}
	colors := map[string]bool{}
	for _, c := range s.Palette {
		if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(c) || colors[strings.ToLower(c)] {
			return s, errors.New("palette requires unique #RRGGBB colors")
		}
		colors[strings.ToLower(c)] = true
	}
	frames := map[string]bool{}
	for _, f := range s.Frames {
		if !contentSlug.MatchString(f.Name) || frames[f.Name] || f.X < 0 || f.Y < 0 || f.Width < 1 || f.Height < 1 || f.Width > 4096 || f.Height > 4096 || f.X > 4096-f.Width || f.Y > 4096-f.Height || f.PivotX < 0 || f.PivotX > 1 || f.PivotY < 0 || f.PivotY > 1 || f.DurationMS < 1 || f.DurationMS > 60000 {
			return s, errors.New("invalid or duplicate frame; use normalized top-left pivots and positive duration_ms")
		}
		frames[f.Name] = true
	}
	anims := map[string]bool{}
	for _, a := range s.Animations {
		if !contentSlug.MatchString(a.Name) || anims[a.Name] || len(a.Frames) == 0 || len(a.Frames) > 1024 {
			return s, errors.New("invalid animation")
		}
		anims[a.Name] = true
		for _, f := range a.Frames {
			if !frames[f] {
				return s, errors.New("animation refers to unknown frame")
			}
		}
	}
	return s, nil
}
func assetSave(ctx *sdk.AppCtx, s GameScope, args map[string]any, source []byte, mime string, provenance map[string]any) (any, error) {
	id := txt(args["asset_id"])
	kind := txt(args["kind"])
	name := strings.TrimSpace(txt(args["name"]))
	parent, hasParent := args["expected_parent"].(string)
	if !contentSlug.MatchString(id) || name == "" || len(name) > 200 || !hasParent {
		return nil, errors.New("asset_id slug, name and expected_parent (empty for new asset) required")
	}
	switch kind {
	case "sprite", "spriteset", "tileset", "style", "sfx", "music", "blob", "rig", "material", "font", "stream", "locale", "table":
	default:
		return nil, errors.New("unsupported asset kind")
	}
	spec, e := assetSpecParse(args["spec"])
	if e != nil {
		return nil, e
	}
	if e = validateKind(kind, spec); e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	for _, d := range spec.Dependencies {
		if d.Asset == id || seen[d.Asset] {
			return nil, errors.New("self or duplicate dependency")
		}
		seen[d.Asset] = true
		v, e := assetVersionGet(ctx, s, d.Version)
		if e != nil || v.AssetID != d.Asset {
			return nil, errors.New("dependency must reference an exact version in this game")
		}
	}
	if e = validateAssetDependencies(ctx, s, kind, spec); e != nil {
		return nil, e
	}
	// A recipe-only version is a draft; it cannot be baked until a source is attached.
	if len(source) > 0 && (kind == "sprite" || kind == "spriteset" || kind == "tileset") {
		if _, e = validateSprite(source, spec); e != nil {
			return nil, e
		}
		mime = "image/png"
	}
	if (kind == "style" || kind == "rig" || kind == "material" || kind == "stream" || kind == "locale" || kind == "table") && len(source) > 0 {
		return nil, errors.New("structured asset kinds use their typed specification, not source bytes")
	}
	if len(source) > 0 && (kind == "sfx" || kind == "music") {
		if !validAudio(source) {
			return nil, errors.New("audio source must be WAV, OGG or MP3")
		}
	}
	if kind == "font" && len(source) > 0 {
		if _, e = fontFormat(source); e != nil {
			return nil, e
		}
	}
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var versionCount int
	if e = tx.QueryRow(`SELECT count(*) FROM game_asset_versions WHERE project_id=? AND game_id=?`, s.ProjectID, s.GameID).Scan(&versionCount); e != nil {
		return nil, e
	}
	if versionCount >= 10000 {
		return nil, errors.New("game has reached its 10,000 asset version limit")
	}
	var head, oldKind string
	e = tx.QueryRow(`SELECT head,kind FROM game_assets WHERE project_id=? AND game_id=? AND id=?`, s.ProjectID, s.GameID, id).Scan(&head, &oldKind)
	if e != nil && e != sql.ErrNoRows {
		return nil, e
	}
	if head != parent {
		return nil, errors.New("asset changed; reload its head before saving")
	}
	if oldKind != "" && oldKind != kind {
		return nil, errors.New("asset kind is immutable")
	}
	if e == sql.ErrNoRows {
		if _, e = tx.Exec(`INSERT INTO game_assets VALUES(?,?,?,?,?,?)`, s.ProjectID, s.GameID, id, name, kind, ""); e != nil {
			return nil, e
		}
	}
	sha := ""
	if len(source) > 0 {
		sha, e = contentBlobPut(tx, s, source, mime)
		if e != nil {
			return nil, e
		}
	} else if existing := txt(args["source"]); existing != "" {
		var n int
		if e = tx.QueryRow(`SELECT count(*) FROM game_content_blobs WHERE project_id=? AND game_id=? AND sha=?`, s.ProjectID, s.GameID, existing).Scan(&n); e != nil || n != 1 {
			return nil, errors.New("source not found")
		}
		sha = existing
	}
	vid := studioHash(map[string]any{"asset": id, "parent": parent, "spec": spec, "source": sha, "provenance": provenance, "name": name, "kind": kind})
	_, e = tx.Exec(`INSERT INTO game_asset_versions VALUES(?,?,?,?,?,?,?,?,?,?)`, s.ProjectID, s.GameID, vid, id, name, parent, contentJSON(spec), sha, contentJSON(provenance), nowRFC())
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(`UPDATE game_assets SET head=?,name=? WHERE project_id=? AND game_id=? AND id=? AND head=?`, vid, name, s.ProjectID, s.GameID, id, parent)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return assetVersionGet(ctx, s, vid)
}
func assetImport(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	var b []byte
	mime := "application/octet-stream"
	provenance := map[string]any{"method": "authored"}
	sources := 0
	for _, set := range []bool{number(args["storage_id"]) > 0, txt(args["source"]) != "", txt(args["content_base64"]) != ""} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return nil, errors.New("choose exactly one source")
	}
	if encoded := txt(args["content_base64"]); encoded != "" {
		if len(encoded) > base64.StdEncoding.EncodedLen(contentMaxBlob) {
			return nil, errors.New("source exceeds 25 MiB")
		}
		var e error
		b, e = base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			return nil, e
		}
		mime = http.DetectContentType(b)
		provenance = map[string]any{"method": "upload", "sha256": contentSHA(b)}
	}
	if id := number(args["storage_id"]); id > 0 {
		install, e := studioBinding(ctx, "storage")
		if e != nil {
			return nil, e
		}
		out, e := studioCall(ctx, s, "storage", "files_get_content", map[string]any{"id": id})
		if e != nil {
			return nil, e
		}
		if number(out["id"]) != id {
			return nil, errors.New("Storage returned a different file")
		}
		raw := txt(out["content_base64"])
		if len(raw) > base64.StdEncoding.EncodedLen(contentMaxBlob) {
			return nil, errors.New("source exceeds 25 MiB")
		}
		b, e = base64.StdEncoding.DecodeString(raw)
		if e != nil {
			return nil, e
		}
		if int64(len(b)) != number(out["size_bytes"]) {
			return nil, errors.New("Storage byte length mismatch")
		}
		mime = txt(out["content_type"])
		provenance = map[string]any{"method": "storage", "install_id": install, "file_id": id, "sha256": contentSHA(b)}
	}
	if txt(args["source"]) != "" {
		var e error
		b, mime, e = contentBlob(ctx, s, txt(args["source"]))
		if e != nil {
			return nil, e
		}
		provenance = map[string]any{"method": "retained", "sha256": contentSHA(b)}
	}
	return assetSave(ctx, s, args, b, mime, provenance)
}
func assetList(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	limit := boundedArg(args, "limit", 25, 1, 100)
	offset := boundedArg(args, "offset", 0, 0, 100000)
	rows, e := ctx.AppDB().Query(`SELECT id,name,kind,head FROM game_assets WHERE project_id=? AND game_id=? ORDER BY id LIMIT ? OFFSET ?`, s.ProjectID, s.GameID, limit+1, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, kind, head string
		if e = rows.Scan(&id, &name, &kind, &head); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "name": name, "kind": kind, "head": head})
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return map[string]any{"assets": out, "has_more": more, "offset": offset}, rows.Err()
}
func contentAction(call context.Context, ctx *sdk.AppCtx, action string, args map[string]any) (any, error) {
	s, e := studioScope(ctx, args)
	if e != nil {
		return nil, e
	}
	if action != "versions" && action != "renditions" && action != "assets" && action != "version" && action != "content" && action != "jobs" && action != "manifest" {
		if e = checkActiveGame(ctx.AppDB(), s); e != nil {
			return nil, e
		}
	}
	switch action {
	case "versions":
		rows, e := ctx.AppDB().Query(`SELECT id,parent,name,created_at FROM game_asset_versions WHERE project_id=? AND game_id=? AND asset_id=? ORDER BY rowid DESC LIMIT 50`, s.ProjectID, s.GameID, txt(args["asset_id"]))
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, parent, name, at string
			if e = rows.Scan(&id, &parent, &name, &at); e != nil {
				return nil, e
			}
			out = append(out, map[string]any{"id": id, "parent": parent, "name": name, "created_at": at})
		}
		return out, rows.Err()
	case "renditions":
		rows, e := ctx.AppDB().Query(`SELECT document FROM game_renditions WHERE project_id=? AND game_id=? ORDER BY rowid DESC LIMIT 100`, s.ProjectID, s.GameID)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []json.RawMessage{}
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				return nil, e
			}
			out = append(out, json.RawMessage(raw))
		}
		return out, rows.Err()
	case "assets":
		return assetList(ctx, s, args)
	case "version":
		return assetVersionGet(ctx, s, txt(args["version_id"]))
	case "save":
		return assetImport(ctx, s, args)
	case "bake":
		return assetBake(ctx, s, args)
	case "freeze":
		return contentFreeze(ctx, s, args)
	case "manifest":
		return contentManifestGet(ctx, s, txt(args["digest"]))
	case "content":
		return contentList(ctx, s)
	case "approve":
		return contentApprove(call, ctx, s, args)
	case "promote":
		return contentPromote(ctx, s, args)
	case "generate":
		return contentGenerate(ctx, s, args)
	case "generation_sync":
		return contentGenerationSync(ctx, s, args)
	case "jobs":
		return contentJobs(ctx, s)
	default:
		return nil, errors.New("unknown content action")
	}
}
func (a *App) handleContent(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	if r.Method == "POST" {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 35<<20))
		e := dec.Decode(&args)
		if e == nil && dec.Decode(new(any)) != io.EOF {
			e = errors.New("one JSON object required")
		}
		if e != nil || args == nil {
			httpErr(w, 400, "JSON object required")
			return
		}
	}
	p, e := resolveProjectFromRequest(r)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	args["_project_id"] = p
	args["game_id"] = r.PathValue("game_id")
	for _, k := range []string{"version_id", "digest", "asset_id"} {
		if v := r.URL.Query().Get(k); v != "" {
			args[k] = v
		}
	}
	action := r.PathValue("action")
	if r.Method == "GET" && action != "renditions" && action != "versions" && action != "assets" && action != "version" && action != "content" && action != "manifest" && action != "jobs" {
		httpErr(w, 405, "use POST")
		return
	}
	out, e := contentAction(r.Context(), getAppCtx(r), action, args)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	httpJSON(w, map[string]any{"data": out})
}
func contentTools() []sdk.Tool {
	specs := []struct{ name, action, desc string }{
		{"games_asset_versions", "versions", "List the most recent 50 immutable versions for an asset_id."},
		{"games_renditions_list", "renditions", "List the 100 most recent baked renditions for this game."},
		{"games_assets_list", "assets", "List game assets with pagination."}, {"games_asset_save", "save", "Save an immutable asset version. Requires asset_id, name, kind, spec, expected_parent; optional storage_id or retained source SHA. Empty expected_parent creates an asset."}, {"games_asset_version", "version", "Get an exact asset version including its recipe and dependency versions."}, {"games_asset_bake", "bake", "Bake an exact version for target generic, unity or godot, engine_version, platform, optional scale (1-4)."}, {"games_content_freeze", "freeze", "Freeze exact rendition_ids and dependencies into a content manifest."}, {"games_content_manifest", "manifest", "Read an immutable manifest by digest."}, {"games_content_list", "content", "List recent manifests and environment heads."}, {"games_content_approve", "approve", "Approve the exact manifest digest with a review note before production promotion."}, {"games_content_promote", "promote", "Atomically move dev/staging/prod to a digest with expected_head; production requires a review."}, {"games_asset_generate", "generate", "Generate a candidate for a recipe version via Media Studio once per request_key. Paid provider call; retries do not repeat uncertain dispatch."}, {"games_asset_generation_sync", "generation_sync", "Attach a completed generation to a new version; optional generation_id reconciles uncertain dispatch after verifying the saved request marker."}, {"games_asset_jobs", "jobs", "Inspect recent generation jobs and retained outcomes."},
	}
	out := []sdk.Tool{}
	for _, sp := range specs {
		sp := sp
		props := map[string]any{}
		for _, k := range []string{"game_id", "asset_id", "name", "kind", "expected_parent", "source", "content_base64", "version_id", "target", "engine_version", "platform", "digest", "environment", "expected_head", "note", "request_key"} {
			props[k] = map[string]any{"type": "string"}
		}
		for _, k := range []string{"storage_id", "scale", "generation_id", "limit", "offset"} {
			props[k] = map[string]any{"type": "integer"}
		}
		props["spec"] = map[string]any{"type": "object"}
		props["rendition_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		out = append(out, sdk.Tool{Name: sp.name, Description: sp.desc, InputSchema: schemaObject(props, []string{"game_id"}), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			return contentAction(call, ctx, sp.action, args)
		}})
	}
	return out
}
