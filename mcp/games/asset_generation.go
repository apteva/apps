package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

func contentJobs(ctx *sdk.AppCtx, s GameScope) (any, error) {
	rows, e := ctx.AppDB().Query(`SELECT request_key,asset_id,parent,status,result,created_at FROM game_content_jobs WHERE project_id=? AND game_id=? ORDER BY created_at DESC LIMIT 50`, s.ProjectID, s.GameID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, asset, parent, status, raw, at string
		if e = rows.Scan(&key, &asset, &parent, &status, &raw, &at); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"request_key": key, "asset_id": asset, "version_id": parent, "status": status, "result": json.RawMessage(raw), "created_at": at})
	}
	return out, rows.Err()
}
func contentGenerate(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	key := txt(args["request_key"])
	if !contentSlug.MatchString(key) {
		return nil, errors.New("request_key must be a stable slug (max 64 characters)")
	}
	v, e := assetVersionGet(ctx, s, txt(args["version_id"]))
	if e != nil {
		return nil, e
	}
	if len(v.Spec.Recipe) == 0 {
		return nil, errors.New("asset version has no generation recipe")
	}
	install, e := studioBinding(ctx, "media-studio")
	if e != nil {
		return nil, e
	}
	fingerprint := studioHash(map[string]any{"version": v.ID, "install": install})
	var old, status, raw string
	e = ctx.AppDB().QueryRow(`SELECT fingerprint,status,result FROM game_content_jobs WHERE project_id=? AND game_id=? AND request_key=?`, s.ProjectID, s.GameID, key).Scan(&old, &status, &raw)
	if e == nil {
		if old != fingerprint {
			return nil, errors.New("request_key reused with different generation inputs")
		}
		return map[string]any{"status": status, "result": json.RawMessage(raw)}, nil
	}
	kind := "image"
	switch v.Kind {
	case "sprite", "spriteset", "tileset":
	case "sfx":
		kind = "audio_sfx"
	case "music":
		kind = "music"
	default:
		return nil, errors.New("this asset kind cannot be generated")
	}
	in := map[string]any{}
	for _, k := range []string{"prompt", "provider", "model", "size", "duration", "source_images", "options"} {
		if val, ok := v.Spec.Recipe[k]; ok {
			in[k] = val
		}
	}
	if txt(in["prompt"]) == "" {
		return nil, errors.New("recipe prompt required")
	}
	for _, dep := range v.Spec.Dependencies {
		style, e := assetVersionGet(ctx, s, dep.Version)
		if e != nil {
			return nil, e
		}
		if style.Kind == "style" {
			in["prompt"] = txt(in["prompt"]) + "\nPinned style: " + contentJSON(style.Spec)
		}
	}
	namespace := ""
	if e = ctx.AppDB().QueryRow(`SELECT namespace FROM game_studio_identity WHERE id=1`).Scan(&namespace); e != nil {
		return nil, e
	}
	marker := studioHash([]string{namespace, s.ProjectID, s.GameID, key, v.ID})
	in["kind"] = kind
	in["n"] = 1
	in["cache_key"] = marker
	in["cache_policy"] = "reuse"
	in["storage_folder"] = "/.games/" + s.GameID + "/" + marker
	if kind == "image" {
		opts := object(in["options"])
		if opts == nil {
			opts = map[string]any{}
		}
		opts["output_format"] = "png"
		in["options"] = opts
		in["output_format"] = "png"
	}
	receipt := map[string]any{"marker": marker, "request": in}
	_, e = ctx.AppDB().Exec(`INSERT INTO game_content_jobs VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.ProjectID, s.GameID, key, fingerprint, install, v.AssetID, v.ID, contentJSON(v.Spec), "dispatching", contentJSON(receipt), nowRFC())
	if e != nil {
		return nil, errors.New("generation already dispatched; inspect jobs before retrying")
	}
	out, dispatchErr := studioCall(ctx, s, "media-studio", "media_generate", in)
	status = "unknown"
	if dispatchErr != nil {
		receipt["error"] = dispatchErr.Error()
	} else {
		meta := object(out["_meta"])
		if id := number(meta["generation_id"]); id > 0 {
			receipt["generation_id"] = id
			status = "generated"
		} else {
			receipt["error"] = "Media Studio returned no generation receipt; reconcile the exact generation ID"
		}
	}
	if _, e = ctx.AppDB().Exec(`UPDATE game_content_jobs SET status=?,result=? WHERE project_id=? AND game_id=? AND request_key=?`, status, contentJSON(receipt), s.ProjectID, s.GameID, key); e != nil {
		return nil, e
	}
	return map[string]any{"status": status, "result": receipt}, nil
}
func contentGenerationSync(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	key := txt(args["request_key"])
	var install int64
	var parent, status, raw string
	if e := ctx.AppDB().QueryRow(`SELECT media_install,parent,status,result FROM game_content_jobs WHERE project_id=? AND game_id=? AND request_key=?`, s.ProjectID, s.GameID, key).Scan(&install, &parent, &status, &raw); e != nil {
		return nil, errors.New("generation request not found")
	}
	var receipt map[string]any
	if e := json.Unmarshal([]byte(raw), &receipt); e != nil {
		return nil, e
	}
	if status == "attached" {
		return receipt, nil
	}
	current, e := studioBinding(ctx, "media-studio")
	if e != nil || current != install {
		return nil, errors.New("Media Studio binding changed since generation")
	}
	id := number(receipt["generation_id"])
	if supplied := number(args["generation_id"]); supplied > 0 {
		if id > 0 && id != supplied {
			return nil, errors.New("generation_id conflicts with retained receipt")
		}
		id = supplied
	}
	if id <= 0 {
		return nil, errors.New("inspect Media Studio and supply the exact generation_id to reconcile; generation will not be repeated")
	}
	out, e := studioCall(ctx, s, "media-studio", "media_asset_source", map[string]any{"id": id, "index": 0})
	if e != nil {
		return nil, e
	}
	if number(out["generation_id"]) != id {
		return nil, errors.New("generation identity mismatch")
	}
	var request map[string]any
	if e = json.Unmarshal([]byte(txt(out["request_json"])), &request); e != nil {
		return nil, e
	}
	if txt(request["cache_key"]) != txt(receipt["marker"]) {
		return nil, errors.New("generation does not match this game's saved request")
	}
	encoded := txt(out["content_base64"])
	if len(encoded) > base64.StdEncoding.EncodedLen(contentMaxBlob) {
		return nil, errors.New("generated asset exceeds 25 MiB")
	}
	b, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		return nil, e
	}
	if contentSHA(b) != txt(out["sha256"]) || int64(len(b)) != number(out["size_bytes"]) {
		return nil, errors.New("generated bytes failed integrity verification")
	}
	v, e := assetVersionGet(ctx, s, parent)
	if e != nil {
		return nil, e
	}
	provenance := map[string]any{"method": "media-studio", "install_id": install, "generation_id": id, "provider": out["provider"], "model": out["model"], "cost_usd": out["cost_usd"], "sha256": out["sha256"], "request": request}
	// Serialize attach via a transaction-independent claim. A restart does not
	// need to regenerate: the deterministic child version can be found below.
	expectedID := studioHash(map[string]any{"asset": v.AssetID, "parent": v.ID, "spec": v.Spec, "source": contentSHA(b), "provenance": provenance, "name": v.Name, "kind": v.Kind})
	var saved any
	if child, err := assetVersionGet(ctx, s, expectedID); err == nil {
		saved = child
	} else {
		saved, e = assetSave(ctx, s, map[string]any{"asset_id": v.AssetID, "name": v.Name, "kind": v.Kind, "expected_parent": v.ID, "spec": v.Spec}, b, txt(out["content_type"]), provenance)
		if e != nil {
			return nil, fmt.Errorf("source retained in Media Studio generation %d; attach failed: %w", id, e)
		}
	}
	receipt["generation_id"] = id
	receipt["version"] = saved
	_, e = ctx.AppDB().Exec(`UPDATE game_content_jobs SET status='attached',result=? WHERE project_id=? AND game_id=? AND request_key=?`, contentJSON(receipt), s.ProjectID, s.GameID, key)
	return receipt, e
}
