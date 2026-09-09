package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"sort"
	"time"
)

type LockedDependency struct {
	Asset   string `json:"asset"`
	Version string `json:"version"`
	Source  string `json:"source"`
	Kind    string `json:"kind"`
}
type ContentManifest struct {
	Schema       string             `json:"schema"`
	Game         string             `json:"game"`
	Assets       []AssetRendition   `json:"assets"`
	Dependencies []LockedDependency `json:"dependencies"`
}

func contentManifestGet(ctx *sdk.AppCtx, s GameScope, digest string) (ContentManifest, error) {
	var m ContentManifest
	var raw string
	e := ctx.AppDB().QueryRow(`SELECT document FROM game_content_manifests WHERE project_id=? AND game_id=? AND digest=?`, s.ProjectID, s.GameID, digest).Scan(&raw)
	if e != nil {
		return m, errors.New("manifest not found for this game")
	}
	if contentSHA([]byte(raw)) != digest {
		return m, errors.New("manifest checksum mismatch")
	}
	e = json.Unmarshal([]byte(raw), &m)
	return m, e
}
func contentFreeze(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	raw, e := json.Marshal(args["rendition_ids"])
	if e != nil {
		return nil, e
	}
	var ids []string
	if e = json.Unmarshal(raw, &ids); e != nil || len(ids) < 1 || len(ids) > 256 {
		return nil, errors.New("select 1-256 exact rendition_ids")
	}
	sort.Strings(ids)
	m := ContentManifest{Schema: "apteva.games.content/v1", Game: s.GameID, Assets: []AssetRendition{}, Dependencies: []LockedDependency{}}
	assets := map[string]bool{}
	versions := map[string]string{}
	deps := map[string]AssetVersion{}
	var visit func(string) error
	visit = func(id string) error {
		if _, ok := deps[id]; ok {
			return nil
		}
		if len(deps) >= 512 {
			return errors.New("manifest dependency limit exceeded")
		}
		v, e := assetVersionGet(ctx, s, id)
		if e != nil {
			return e
		}
		if prev := versions[v.AssetID]; prev != "" && prev != id {
			return errors.New("manifest contains conflicting versions of dependency " + v.AssetID)
		}
		versions[v.AssetID] = id
		deps[id] = v
		for _, d := range v.Spec.Dependencies {
			if e = visit(d.Version); e != nil {
				return e
			}
		}
		return nil
	}
	for _, id := range ids {
		r, e := renditionGet(ctx, s, id)
		if e != nil {
			return nil, e
		}
		key := r.Asset + "/" + r.Target + "/" + r.EngineVersion + "/" + r.Platform
		if assets[key] {
			return nil, errors.New("select one rendition per asset/engine/platform")
		}
		assets[key] = true

		for _, f := range r.Files {
			b, _, e := contentBlob(ctx, s, f.SHA256)
			if e != nil || len(b) != f.Size {
				return nil, errors.New("rendition bytes missing or changed")
			}
		}
		if e = visit(r.Version); e != nil {
			return nil, e
		}
		m.Assets = append(m.Assets, r)
	}
	// Every runtime dependency must have a compatible rendition in the same
	// manifest. Style-only dependencies are consumed at bake time.
	for _, r := range m.Assets {
		v := deps[r.Version]
		for _, d := range v.Spec.Dependencies {
			dependency := deps[d.Version]
			if dependency.Kind == "style" {
				continue
			}
			key := d.Asset + "/" + r.Target + "/" + r.EngineVersion + "/" + r.Platform
			if !assets[key] {
				return nil, fmt.Errorf("bake and select dependency %s for %s %s %s", d.Asset, r.Target, r.EngineVersion, r.Platform)
			}
		}
	}
	keys := []string{}
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := deps[k]
		m.Dependencies = append(m.Dependencies, LockedDependency{Asset: v.AssetID, Version: v.ID, Source: v.Source, Kind: v.Kind})
	}
	document := contentJSON(m)
	if len(document) > 2<<20 {
		return nil, errors.New("manifest exceeds 2 MiB")
	}
	var total int
	seenFiles := map[string]bool{}
	for _, r := range m.Assets {
		for _, f := range r.Files {
			if !seenFiles[f.Path] {
				total += f.Size
				seenFiles[f.Path] = true
			}
		}
	}
	if total > 128<<20 {
		return nil, errors.New("content release exceeds 128 MiB; split the release")
	}
	digest := contentSHA([]byte(document))
	_, e = ctx.AppDB().Exec(`INSERT OR IGNORE INTO game_content_manifests VALUES(?,?,?,?,?)`, s.ProjectID, s.GameID, digest, document, nowRFC())
	return map[string]any{"digest": digest, "manifest": m}, e
}
func contentList(ctx *sdk.AppCtx, s GameScope) (any, error) {
	rows, e := ctx.AppDB().Query(`SELECT digest,created_at,EXISTS(SELECT 1 FROM game_content_reviews r WHERE r.project_id=m.project_id AND r.game_id=m.game_id AND r.digest=m.digest) FROM game_content_manifests m WHERE project_id=? AND game_id=? ORDER BY created_at DESC LIMIT 50`, s.ProjectID, s.GameID)
	if e != nil {
		return nil, e
	}
	items := []map[string]any{}
	for rows.Next() {
		var digest, at string
		var reviewed bool
		if e = rows.Scan(&digest, &at, &reviewed); e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, map[string]any{"digest": digest, "created_at": at, "approved": reviewed})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = ctx.AppDB().Query(`SELECT environment,digest FROM game_content_heads WHERE project_id=? AND game_id=?`, s.ProjectID, s.GameID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	heads := map[string]string{}
	for rows.Next() {
		var env, d string
		if e = rows.Scan(&env, &d); e != nil {
			return nil, e
		}
		heads[env] = d
	}
	return map[string]any{"manifests": items, "heads": heads}, rows.Err()
}
func contentApprove(call context.Context, ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	digest, note := txt(args["digest"]), txt(args["note"])
	if len(note) < 3 || len(note) > 2000 {
		return nil, errors.New("review note required (3-2000 characters)")
	}
	if _, e := contentManifestGet(ctx, s, digest); e != nil {
		return nil, e
	}
	actor := "authenticated-admin"
	if caller := sdk.CallerFrom(call); caller != nil {
		actor = contentJSON(map[string]any{"agent_id": caller.AgentID, "subject_id": caller.SubjectID, "tool_call_id": caller.ToolCallID})
	}
	_, e := ctx.AppDB().Exec(`INSERT OR IGNORE INTO game_content_reviews VALUES(?,?,?,?,?,?)`, s.ProjectID, s.GameID, digest, actor, note, nowRFC())
	return map[string]any{"digest": digest, "approved": e == nil}, e
}
func contentPromote(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	env, digest := txt(args["environment"]), txt(args["digest"])
	expected, ok := args["expected_head"].(string)
	if !ok || (env != "dev" && env != "staging" && env != "prod") {
		return nil, errors.New("environment dev/staging/prod and expected_head required")
	}
	if _, e := contentManifestGet(ctx, s, digest); e != nil {
		return nil, e
	}
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	head := ""
	e = tx.QueryRow(`SELECT digest FROM game_content_heads WHERE project_id=? AND game_id=? AND environment=?`, s.ProjectID, s.GameID, env).Scan(&head)
	if e != nil && e != sql.ErrNoRows {
		return nil, e
	}
	if head != expected {
		return nil, errors.New("environment changed; reload its current head")
	}
	if env == "prod" {
		var count int
		if e = tx.QueryRow(`SELECT count(*) FROM game_content_reviews WHERE project_id=? AND game_id=? AND digest=?`, s.ProjectID, s.GameID, digest).Scan(&count); e != nil || count != 1 {
			return nil, errors.New("production requires a review of this exact manifest")
		}
	}
	_, e = tx.Exec(`INSERT INTO game_content_heads VALUES(?,?,?,?) ON CONFLICT(project_id,game_id,environment) DO UPDATE SET digest=excluded.digest`, s.ProjectID, s.GameID, env, digest)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(`INSERT INTO game_content_moves(project_id,game_id,environment,previous,digest,created_at) VALUES(?,?,?,?,?,?)`, s.ProjectID, s.GameID, env, head, digest, nowRFC())
	if e != nil {
		return nil, e
	}
	return map[string]any{"environment": env, "previous": head, "digest": digest}, tx.Commit()
}
func contentZip(ctx *sdk.AppCtx, s GameScope, digest string) ([]byte, error) {
	m, e := contentManifestGet(ctx, s, digest)
	if e != nil {
		return nil, e
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	var total int
	add := func(path string, b []byte) error {
		total += len(b)
		if total > 128<<20 {
			return errors.New("content export exceeds 128 MiB; split the content release")
		}
		h := &zip.FileHeader{Name: path, Method: zip.Deflate}
		h.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		w, e := z.CreateHeader(h)
		if e != nil {
			return e
		}
		_, e = w.Write(b)
		return e
	}
	if e = add("content.lock.json", []byte(contentJSON(map[string]any{"schema": m.Schema, "digest": digest, "manifest": "manifest.json"}))); e != nil {
		return nil, e
	}
	if e = add("manifest.json", []byte(contentJSON(m))); e != nil {
		return nil, e
	}
	for _, a := range m.Assets {
		if a.Target == "unity" {
			if e = add("bundle.gamescontent", []byte(contentJSON(m))); e != nil {
				return nil, e
			}
			break
		}
	}
	seen := map[string]bool{}
	for _, a := range m.Assets {
		for _, f := range a.Files {
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			b, _, e := contentBlob(ctx, s, f.SHA256)
			if e != nil {
				return nil, e
			}
			if e = add(f.Path, b); e != nil {
				return nil, e
			}
		}
	}
	if e = z.Close(); e != nil {
		return nil, e
	}
	return buf.Bytes(), nil
}
func (a *App) handleContentBytes(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	var s GameScope
	var e error
	if r.PathValue("public") == "true" {
		var ok bool
		ctx, s, _, ok = a.requirePlayer(w, r)
		if !ok {
			return
		}
	} else {
		p, err := resolveProjectFromRequest(r)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		s, e = studioScope(ctx, map[string]any{"_project_id": p, "game_id": r.PathValue("game_id")})
		if e != nil {
			httpErr(w, 404, e.Error())
			return
		}
	}
	digest, sha := r.PathValue("digest"), r.PathValue("sha")
	if r.PathValue("public") == "true" {
		if digest == "head" {
			e = ctx.AppDB().QueryRow(`SELECT digest FROM game_content_heads WHERE project_id=? AND game_id=? AND environment='prod'`, s.ProjectID, s.GameID).Scan(&digest)
		} else {
			var n int
			e = ctx.AppDB().QueryRow(`SELECT count(*) FROM game_content_moves WHERE project_id=? AND game_id=? AND environment='prod' AND digest=?`, s.ProjectID, s.GameID, digest).Scan(&n)
			if n == 0 {
				e = errors.New("content is not published")
			}
		}
		if e != nil {
			httpErr(w, 404, "published content not found")
			return
		}
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if sha != "" {
		if r.PathValue("public") == "true" {
			m, e := contentManifestGet(ctx, s, digest)
			if e != nil {
				httpErr(w, 404, e.Error())
				return
			}
			found := false
			for _, a := range m.Assets {
				for _, f := range a.Files {
					found = found || f.SHA256 == sha
				}
			}
			if !found {
				httpErr(w, 404, "blob is not in this published manifest")
				return
			}
		}
		b, mime, e := contentBlob(ctx, s, sha)
		if e != nil {
			httpErr(w, 404, e.Error())
			return
		}
		if mime != "image/png" && mime != "audio/ogg" && mime != "audio/mpeg" {
			mime = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("ETag", `"`+sha+`"`)
		http.ServeContent(w, r, "asset", time.Time{}, bytes.NewReader(b))
		return
	}
	if r.URL.Query().Get("download") == "zip" {
		b, e := contentZip(ctx, s, digest)
		if e != nil {
			httpErr(w, 400, e.Error())
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="games-content-%s.zip"`, digest))
		w.Header().Set("X-Content-SHA256", contentSHA(b))
		w.Write(b)
		return
	}
	m, e := contentManifestGet(ctx, s, digest)
	if e != nil {
		httpErr(w, 404, e.Error())
		return
	}
	if r.URL.Query().Get("download") == "manifest" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(contentJSON(m)))
		return
	}
	httpJSON(w, map[string]any{"digest": digest, "manifest": m})
}
