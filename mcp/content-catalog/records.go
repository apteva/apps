package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Asset struct {
	ID               string        `json:"id"`
	SessionID        string        `json:"session_id"`
	StorageInstallID int64         `json:"storage_install_id"`
	StorageFileID    string        `json:"storage_file_id"`
	Name             string        `json:"name"`
	Kind             string        `json:"kind"`
	ContentType      string        `json:"content_type"`
	SHA256           string        `json:"sha256"`
	SizeBytes        int64         `json:"size_bytes"`
	ReviewStatus     string        `json:"review_status"`
	MediaStatus      string        `json:"media_status"`
	MediaRating      string        `json:"media_rating"`
	Publications     []Publication `json:"publications"`
}

func assetByID(db *sql.DB, pid, id string) (*Asset, error) {
	a := &Asset{}
	err := db.QueryRow(`SELECT id,session_id,storage_install_id,storage_file_id,name,kind,content_type,sha256,size_bytes,review_status,media_status,media_rating FROM assets WHERE project_id=? AND id=?`, pid, id).Scan(&a.ID, &a.SessionID, &a.StorageInstallID, &a.StorageFileID, &a.Name, &a.Kind, &a.ContentType, &a.SHA256, &a.SizeBytes, &a.ReviewStatus, &a.MediaStatus, &a.MediaRating)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("asset not found")
	}
	return a, err
}

func (a *App) assetAttach(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.attachAsset(ctx, args, false)
}

func (a *App) assetAttachUploaded(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.attachAsset(ctx, args, true)
}

func sessionStorageFolder(root string, s *Session) string {
	return strings.TrimSuffix(root, "/") + "/sessions/" + s.Date + "/" + s.ID + "/"
}

func (a *App) sessionUploadTarget(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	s, err := sessionByID(ctx.AppDB(), pid, str(args, "session_id"))
	if err != nil {
		return nil, err
	}
	b, err := brandByID(ctx.AppDB(), pid, s.BrandID)
	if err != nil {
		return nil, err
	}
	bound := ctx.IntegrationFor("storage")
	if bound == nil || bound.InstallID <= 0 {
		return nil, errors.New("Storage app is not bound")
	}
	return map[string]any{"folder": sessionStorageFolder(b.StorageRoot, s), "storage_install_id": bound.InstallID, "project_id": pid}, nil
}

func (a *App) attachAsset(ctx *sdk.AppCtx, args map[string]any, uploaded bool) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "session_id", "storage_file_id"); err != nil {
		return nil, err
	}
	session, err := sessionByID(ctx.AppDB(), pid, str(args, "session_id"))
	if err != nil {
		return nil, err
	}
	bound := ctx.IntegrationFor("storage")
	if bound == nil || bound.InstallID <= 0 {
		return nil, errors.New("Storage app is not bound")
	}
	fileID, err := strconv.ParseInt(str(args, "storage_file_id"), 10, 64)
	if err != nil || fileID <= 0 {
		return nil, errors.New("storage_file_id must be a positive integer")
	}
	var result struct {
		Found bool `json:"found"`
		File  struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			SHA256      string `json:"sha256"`
			SizeBytes   int64  `json:"size_bytes"`
			ContentType string `json:"content_type"`
			Folder      string `json:"folder"`
			ProjectID   string `json:"project_id"`
		} `json:"file"`
	}
	if err = ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"_project_id": pid, "id": fileID}, &result); err != nil {
		return nil, fmt.Errorf("check Storage file: %w", err)
	}
	if !result.Found || result.File.ID != fileID {
		return nil, errors.New("Storage file not found")
	}
	if result.File.ProjectID != "" && result.File.ProjectID != pid {
		return nil, errors.New("Storage file belongs to another project")
	}
	if uploaded {
		brand, err := brandByID(ctx.AppDB(), pid, session.BrandID)
		if err != nil {
			return nil, err
		}
		if expected := sessionStorageFolder(brand.StorageRoot, session); result.File.Folder != expected {
			return nil, fmt.Errorf("Storage file is in %q; expected this session's folder %q", result.File.Folder, expected)
		}
	}
	kind := str(args, "kind")
	if kind == "" {
		kind = "other"
		ct := result.File.ContentType
		switch {
		case strings.HasPrefix(ct, "video/"):
			kind = "video"
		case strings.HasPrefix(ct, "audio/"):
			kind = "audio"
		case strings.HasPrefix(ct, "image/"):
			kind = "image"
		}
	}
	id := newID()
	attachedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name,kind,content_type,sha256,size_bytes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, str(args, "session_id"), bound.InstallID, str(args, "storage_file_id"), result.File.Name, kind, result.File.ContentType, result.File.SHA256, result.File.SizeBytes, attachedAt, attachedAt)
	if err != nil {
		return nil, err
	}
	var actual string
	err = ctx.AppDB().QueryRow(`SELECT id FROM assets WHERE project_id=? AND session_id=? AND storage_install_id=? AND storage_file_id=?`, pid, str(args, "session_id"), bound.InstallID, str(args, "storage_file_id")).Scan(&actual)
	if err != nil {
		return nil, err
	}
	asset, err := assetByID(ctx.AppDB(), pid, actual)
	if err != nil {
		return nil, err
	}
	if actual == id {
		ctx.EmitWithProject("content-catalog.asset.attached", pid, map[string]any{"asset_id": id, "session_id": asset.SessionID, "storage_file_id": fileID})
	}
	return map[string]any{"asset": asset, "was_existing": actual != id}, nil
}
func (a *App) assetsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "session_id"); err != nil {
		return nil, err
	}
	if _, err = sessionByID(ctx.AppDB(), pid, str(args, "session_id")); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id,session_id,storage_install_id,storage_file_id,name,kind,content_type,sha256,size_bytes,review_status,media_status,media_rating FROM assets WHERE project_id=? AND session_id=? ORDER BY created_at DESC,id DESC LIMIT 200`, pid, str(args, "session_id"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var item Asset
		if err = rows.Scan(&item.ID, &item.SessionID, &item.StorageInstallID, &item.StorageFileID, &item.Name, &item.Kind, &item.ContentType, &item.SHA256, &item.SizeBytes, &item.ReviewStatus, &item.MediaStatus, &item.MediaRating); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Publications, err = publicationsForAsset(ctx.AppDB(), pid, out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"assets": out}, nil
}
func (a *App) assetGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	asset, err := assetByID(ctx.AppDB(), pid, str(args, "id"))
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT source_asset_id,relation,source_order,media_render_id FROM asset_sources WHERE project_id=? AND child_asset_id=? ORDER BY source_order`, pid, asset.ID)
	if err != nil {
		return nil, err
	}
	sources := []map[string]any{}
	for rows.Next() {
		var id, relation string
		var order, renderID int64
		if err = rows.Scan(&id, &relation, &order, &renderID); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, map[string]any{"asset_id": id, "relation": relation, "source_order": order, "media_render_id": renderID})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	hostingsAny, err := a.hostingsList(ctx, map[string]any{"asset_id": asset.ID})
	if err != nil {
		return nil, err
	}
	asset.Publications, err = publicationsForAsset(ctx.AppDB(), pid, asset.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"asset": asset, "sources": sources, "hostings": hostingsAny.(map[string]any)["hostings"], "publications": asset.Publications}
	if ctx.IntegrationFor("media") != nil {
		var media struct {
			Found bool           `json:"found"`
			Media map[string]any `json:"media"`
		}
		if err := ctx.PlatformAPI().CallAppResult("media", "media_get", map[string]any{"_project_id": pid, "file_id": asset.StorageFileID}, &media); err != nil {
			out["media_error"] = err.Error()
		} else if media.Found {
			out["media"] = media.Media
		}
	}
	return out, nil
}
func (a *App) assetLinkSource(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "child_asset_id", "source_asset_id"); err != nil {
		return nil, err
	}
	child, err := assetByID(ctx.AppDB(), pid, str(args, "child_asset_id"))
	if err != nil {
		return nil, err
	}
	source, err := assetByID(ctx.AppDB(), pid, str(args, "source_asset_id"))
	if err != nil {
		return nil, err
	}
	if child.ID == source.ID {
		return nil, errors.New("asset cannot be its own source")
	}
	// A source can itself be derived, but the lineage graph must remain acyclic.
	var cycle int
	err = ctx.AppDB().QueryRow(`WITH RECURSIVE ancestors(id) AS (
	  SELECT source_asset_id FROM asset_sources WHERE project_id=? AND child_asset_id=?
	  UNION SELECT s.source_asset_id FROM asset_sources s JOIN ancestors a ON s.child_asset_id=a.id WHERE s.project_id=?
	) SELECT COUNT(*) FROM ancestors WHERE id=?`, pid, source.ID, pid, child.ID).Scan(&cycle)
	if err != nil {
		return nil, err
	}
	if cycle > 0 {
		return nil, errors.New("asset lineage cycle")
	}
	relation := str(args, "relation")
	if relation == "" {
		relation = "derived"
	}
	_, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO asset_sources(project_id,child_asset_id,source_asset_id,relation,source_order,media_render_id) VALUES(?,?,?,?,?,?)`, pid, child.ID, source.ID, relation, number(args, "source_order"), number(args, "media_render_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"linked": true, "child_asset_id": child.ID, "source_asset_id": source.ID}, nil
}
func (a *App) assetReview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id", "review_status"); err != nil {
		return nil, err
	}
	state := str(args, "review_status")
	if state != "pending" && state != "approved" && state != "rejected" {
		return nil, errors.New("review_status must be pending, approved, or rejected")
	}
	res, err := ctx.AppDB().Exec(`UPDATE assets SET review_status=?,updated_at=? WHERE project_id=? AND id=?`, state, now(), pid, str(args, "asset_id"))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, errors.New("asset not found")
	}
	return map[string]any{"asset_id": str(args, "asset_id"), "review_status": state}, nil
}

type Release struct {
	ID             string          `json:"id"`
	BrandID        string          `json:"brand_id"`
	Title          string          `json:"title"`
	Phase          string          `json:"phase"`
	Audience       string          `json:"audience"`
	PlannedAt      string          `json:"planned_at"`
	ApprovalStatus string          `json:"approval_status"`
	Targets        []ReleaseTarget `json:"targets"`
}
type ReleaseTarget struct {
	ID            string   `json:"id"`
	ReleaseID     string   `json:"release_id"`
	Destination   string   `json:"destination"`
	AccountRef    string   `json:"account_ref"`
	PlannedAt     string   `json:"planned_at"`
	CurrentStatus string   `json:"current_status"`
	AssetIDs      []string `json:"asset_ids"`
}

func releaseByID(db *sql.DB, pid, id string) (*Release, error) {
	r := &Release{}
	err := db.QueryRow(`SELECT id,brand_id,title,phase,audience,planned_at,approval_status FROM releases WHERE project_id=? AND id=?`, pid, id).Scan(&r.ID, &r.BrandID, &r.Title, &r.Phase, &r.Audience, &r.PlannedAt, &r.ApprovalStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("release not found")
	}
	return r, err
}
func (a *App) releaseCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "brand_id", "title"); err != nil {
		return nil, err
	}
	if _, err = brandByID(ctx.AppDB(), pid, str(args, "brand_id")); err != nil {
		return nil, err
	}
	id := newID()
	_, err = ctx.AppDB().Exec(`INSERT INTO releases(id,project_id,brand_id,title,phase,audience,planned_at) VALUES(?,?,?,?,?,?,?)`, id, pid, str(args, "brand_id"), str(args, "title"), str(args, "phase"), str(args, "audience"), str(args, "planned_at"))
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.release.created", pid, map[string]any{"id": id, "brand_id": str(args, "brand_id")})
	r, err := releaseByID(ctx.AppDB(), pid, id)
	return map[string]any{"release": r}, err
}
func (a *App) releasesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT id,brand_id,title,phase,audience,planned_at,approval_status FROM releases WHERE project_id=?`
	params := []any{pid}
	if b := str(args, "brand_id"); b != "" {
		q += " AND brand_id=?"
		params = append(params, b)
	}
	q += " ORDER BY planned_at DESC,id DESC LIMIT 200"
	rows, err := ctx.AppDB().Query(q, params...)
	if err != nil {
		return nil, err
	}
	out := []Release{}
	for rows.Next() {
		var r Release
		if err = rows.Scan(&r.ID, &r.BrandID, &r.Title, &r.Phase, &r.Audience, &r.PlannedAt, &r.ApprovalStatus); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		targets, err := releaseTargets(ctx.AppDB(), pid, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Targets = targets
	}
	return map[string]any{"releases": out}, nil
}
func (a *App) releaseGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	r, err := releaseByID(ctx.AppDB(), pid, str(args, "id"))
	if err != nil {
		return nil, err
	}
	r.Targets, err = releaseTargets(ctx.AppDB(), pid, r.ID)
	if err != nil {
		return nil, err
	}
	observations := map[string][]map[string]any{}
	for _, target := range r.Targets {
		rows, err := ctx.AppDB().Query(`SELECT id,status,external_post_id,external_url,actual_at,evidence_source,failure_details,observed_at FROM publication_observations WHERE project_id=? AND target_id=? ORDER BY observed_at DESC,id DESC`, pid, target.ID)
		if err != nil {
			return nil, err
		}
		items := []map[string]any{}
		for rows.Next() {
			var id, state, postID, url, actual, evidence, failure, observed string
			if err = rows.Scan(&id, &state, &postID, &url, &actual, &evidence, &failure, &observed); err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, map[string]any{"id": id, "status": state, "external_post_id": postID, "external_url": url, "actual_at": actual, "evidence_source": evidence, "failure_details": failure, "observed_at": observed})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		observations[target.ID] = items
	}
	return map[string]any{"release": r, "observations": observations}, nil
}
func releaseTargets(db *sql.DB, pid, releaseID string) ([]ReleaseTarget, error) {
	rows, err := db.Query(`SELECT id,release_id,destination,account_ref,planned_at,current_status FROM release_targets WHERE project_id=? AND release_id=? ORDER BY created_at,id`, pid, releaseID)
	if err != nil {
		return nil, err
	}
	out := []ReleaseTarget{}
	for rows.Next() {
		var t ReleaseTarget
		if err = rows.Scan(&t.ID, &t.ReleaseID, &t.Destination, &t.AccountRef, &t.PlannedAt, &t.CurrentStatus); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		assets, err := db.Query(`SELECT asset_id FROM release_target_assets WHERE project_id=? AND target_id=? ORDER BY position`, pid, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].AssetIDs = []string{}
		for assets.Next() {
			var id string
			if err = assets.Scan(&id); err != nil {
				assets.Close()
				return nil, err
			}
			out[i].AssetIDs = append(out[i].AssetIDs, id)
		}
		err = assets.Err()
		assets.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (a *App) releaseTargetAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "release_id", "destination"); err != nil {
		return nil, err
	}
	r, err := releaseByID(ctx.AppDB(), pid, str(args, "release_id"))
	if err != nil {
		return nil, err
	}
	raw, ok := args["asset_ids"].([]any)
	if !ok {
		if typed, yes := args["asset_ids"].([]string); yes {
			for _, x := range typed {
				raw = append(raw, x)
			}
		} else {
			return nil, errors.New("asset_ids must be an array")
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("asset_ids cannot be empty")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, v := range raw {
		id, ok := v.(string)
		if !ok || id == "" || seen[id] {
			return nil, errors.New("asset_ids must contain distinct IDs")
		}
		seen[id] = true
		asset, err := assetByID(ctx.AppDB(), pid, id)
		if err != nil {
			return nil, err
		}
		session, err := sessionByID(ctx.AppDB(), pid, asset.SessionID)
		if err != nil {
			return nil, err
		}
		if session.BrandID != r.BrandID {
			return nil, errors.New("release assets must belong to the release brand")
		}
		ids = append(ids, id)
	}
	planned := str(args, "planned_at")
	if planned == "" {
		planned = r.PlannedAt
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	targetID := newID()
	_, err = tx.Exec(`INSERT INTO release_targets(id,project_id,release_id,destination,account_ref,planned_at) VALUES(?,?,?,?,?,?)`, targetID, pid, r.ID, str(args, "destination"), str(args, "account_ref"), planned)
	if err != nil {
		return nil, err
	}
	for i, id := range ids {
		if _, err = tx.Exec(`INSERT INTO release_target_assets(project_id,target_id,asset_id,position) VALUES(?,?,?,?)`, pid, targetID, id, i); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`INSERT INTO asset_publications(id,project_id,asset_id,destination,account_ref,audience,status,planned_at,legacy_target_id) VALUES(?,?,?,?,?,?,'planned',?,?)`, targetID+":"+id, pid, id, str(args, "destination"), str(args, "account_ref"), r.Audience, planned, targetID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"target": ReleaseTarget{ID: targetID, ReleaseID: r.ID, Destination: str(args, "destination"), AccountRef: str(args, "account_ref"), PlannedAt: planned, CurrentStatus: "planned", AssetIDs: ids}}, nil
}
func (a *App) publicationRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "target_id", "status", "evidence_source"); err != nil {
		return nil, err
	}
	state := str(args, "status")
	switch state {
	case "scheduled", "submitted", "provider_reported_published", "verified_published", "failed", "removed", "unknown":
	default:
		return nil, errors.New("invalid publication status")
	}
	if state == "verified_published" && str(args, "external_post_id") == "" && str(args, "external_url") == "" {
		return nil, errors.New("verified publication requires an external post ID or URL")
	}
	if (state == "verified_published" || state == "provider_reported_published") && str(args, "actual_at") == "" {
		return nil, errors.New("published observations require the actual publication time")
	}
	if actual := str(args, "actual_at"); actual != "" {
		if _, err := time.Parse(time.RFC3339, actual); err != nil {
			return nil, errors.New("actual_at must be an RFC3339 timestamp")
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE release_targets SET current_status=? WHERE project_id=? AND id=?`, state, pid, str(args, "target_id"))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, errors.New("release target not found")
	}
	id := newID()
	_, err = tx.Exec(`INSERT INTO publication_observations(id,project_id,target_id,status,external_post_id,external_url,actual_at,evidence_source,failure_details) VALUES(?,?,?,?,?,?,?,?,?)`, id, pid, str(args, "target_id"), state, str(args, "external_post_id"), str(args, "external_url"), str(args, "actual_at"), str(args, "evidence_source"), str(args, "failure_details"))
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE asset_publications SET status=?,actual_at=?,external_post_id=?,external_url=?,evidence_source=?,failure_details=?,updated_at=? WHERE project_id=? AND legacy_target_id=?`, state, str(args, "actual_at"), str(args, "external_post_id"), str(args, "external_url"), str(args, "evidence_source"), str(args, "failure_details"), now(), pid, str(args, "target_id"))
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO asset_publication_events(id,project_id,publication_id,status,external_post_id,external_url,actual_at,evidence_source,failure_details)
		SELECT ? || ':' || asset_id,project_id,id,?,?,?,?,?,? FROM asset_publications WHERE project_id=? AND legacy_target_id=?`, id, state, str(args, "external_post_id"), str(args, "external_url"), str(args, "actual_at"), str(args, "evidence_source"), str(args, "failure_details"), pid, str(args, "target_id"))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.publication.observed", pid, map[string]any{"id": id, "target_id": str(args, "target_id"), "status": state})
	return map[string]any{"observation_id": id, "status": state}, nil
}
