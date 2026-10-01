package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// A post owns one destination outcome shared by one or more assets.
type Publication struct {
	ID             string   `json:"id"`
	AssetID        string   `json:"asset_id,omitempty"`
	BrandID        string   `json:"brand_id"`
	Title          string   `json:"title"`
	AssetIDs       []string `json:"asset_ids"`
	Destination    string   `json:"destination"`
	AccountRef     string   `json:"account_ref"`
	Audience       string   `json:"audience"`
	Status         string   `json:"status"`
	PlannedAt      string   `json:"planned_at"`
	ActualAt       string   `json:"actual_at"`
	ExternalPostID string   `json:"external_post_id"`
	ExternalURL    string   `json:"external_url"`
	EvidenceSource string   `json:"evidence_source"`
	FailureDetails string   `json:"failure_details"`
	LegacyTargetID string   `json:"legacy_target_id"`
}

const postColumns = `id,brand_id,title,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details,legacy_target_id`

func scanPost(rows *sql.Rows) (Publication, error) {
	var p Publication
	err := rows.Scan(&p.ID, &p.BrandID, &p.Title, &p.Destination, &p.AccountRef, &p.Audience, &p.Status, &p.PlannedAt, &p.ActualAt, &p.ExternalPostID, &p.ExternalURL, &p.EvidenceSource, &p.FailureDetails, &p.LegacyTargetID)
	p.AssetIDs = []string{}
	return p, err
}

func loadPostAssets(db *sql.DB, pid string, p *Publication) error {
	rows, err := db.Query(`SELECT asset_id FROM post_assets WHERE project_id=? AND post_id=? ORDER BY position`, pid, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	p.AssetIDs = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		p.AssetIDs = append(p.AssetIDs, id)
	}
	return rows.Err()
}

func postByID(db *sql.DB, pid, id string) (*Publication, error) {
	rows, err := db.Query(`SELECT `+postColumns+` FROM posts WHERE project_id=? AND id=?`, pid, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, errors.New("post not found")
	}
	p, err := scanPost(rows)
	if err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return &p, loadPostAssets(db, pid, &p)
}

func publicationsForAsset(db *sql.DB, pid, assetID string) ([]Publication, error) {
	rows, err := db.Query(`SELECT `+postColumns+` FROM posts WHERE project_id=? AND id IN
		(SELECT post_id FROM post_assets WHERE project_id=? AND asset_id=?) ORDER BY created_at DESC,id DESC`, pid, pid, assetID)
	if err != nil {
		return nil, err
	}
	out := []Publication{}
	for rows.Next() {
		p, e := scanPost(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		p.AssetID = assetID
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err = loadPostAssets(db, pid, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *App) assetPublicationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id"); err != nil {
		return nil, err
	}
	if _, err = assetByID(ctx.AppDB(), pid, str(args, "asset_id")); err != nil {
		return nil, err
	}
	items, err := publicationsForAsset(ctx.AppDB(), pid, str(args, "asset_id"))
	return map[string]any{"publications": items}, err
}

func (a *App) postsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + postColumns + ` FROM posts p WHERE p.project_id=?`
	values := []any{pid}
	if id := str(args, "session_id"); id != "" {
		if _, err := sessionByID(ctx.AppDB(), pid, id); err != nil {
			return nil, err
		}
		q += ` AND EXISTS (SELECT 1 FROM post_assets pa JOIN assets a ON a.id=pa.asset_id AND a.project_id=pa.project_id WHERE pa.project_id=p.project_id AND pa.post_id=p.id AND a.session_id=?)`
		values = append(values, id)
	}
	if id := str(args, "asset_id"); id != "" {
		if _, err := assetByID(ctx.AppDB(), pid, id); err != nil {
			return nil, err
		}
		q += ` AND EXISTS (SELECT 1 FROM post_assets pa WHERE pa.project_id=p.project_id AND pa.post_id=p.id AND pa.asset_id=?)`
		values = append(values, id)
	}
	if id := str(args, "brand_id"); id != "" {
		q += ` AND p.brand_id=?`
		values = append(values, id)
	}
	q += ` ORDER BY p.created_at DESC,p.id DESC LIMIT 200`
	rows, err := ctx.AppDB().Query(q, values...)
	if err != nil {
		return nil, err
	}
	out := []Publication{}
	for rows.Next() {
		p, e := scanPost(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err = loadPostAssets(ctx.AppDB(), pid, &out[i]); err != nil {
			return nil, err
		}
	}
	return map[string]any{"posts": out}, nil
}

func (a *App) postsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "id"); err != nil {
		return nil, err
	}
	p, err := postByID(ctx.AppDB(), pid, str(args, "id"))
	return map[string]any{"post": p}, err
}

func assetIDsArg(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	var ids []string
	switch x := v.(type) {
	case []string:
		ids = x
	case []any:
		for _, raw := range x {
			id, ok := raw.(string)
			if !ok {
				return nil, errors.New("asset_ids must contain strings")
			}
			ids = append(ids, id)
		}
	default:
		return nil, errors.New("asset_ids must be an array")
	}
	if len(ids) == 0 {
		return nil, errors.New("a post needs at least one asset")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return nil, errors.New("asset_ids must be distinct nonempty IDs")
		}
		seen[id] = true
	}
	return ids, nil
}

func validatePostAssets(db *sql.DB, pid, brandID string, ids []string) error {
	if len(ids) == 0 {
		return errors.New("a post needs at least one asset")
	}
	for _, id := range ids {
		var assetBrand string
		err := db.QueryRow(`SELECT s.brand_id FROM assets a JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE a.project_id=? AND a.id=?`, pid, id).Scan(&assetBrand)
		if err != nil {
			return fmt.Errorf("asset %s not found", id)
		}
		if assetBrand != brandID {
			return errors.New("all post assets must belong to the same brand")
		}
	}
	return nil
}

func applyPostField(args map[string]any, key string, current *string) {
	if _, ok := args[key]; ok {
		*current = str(args, key)
	}
}

// postsRecord creates or updates a Catalog post and its ordered membership.
// Every edit appends an audit snapshot. It never publishes externally.
func (a *App) postsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	id := str(args, "post_id")
	creating := id == ""
	var p Publication
	if creating {
		if err = required(args, "destination", "status"); err != nil {
			return nil, err
		}
		id = newID()
		p.ID = id
		p.Destination = strings.ToLower(strings.TrimSpace(str(args, "destination")))
	} else {
		old, e := postByID(ctx.AppDB(), pid, id)
		if e != nil {
			return nil, e
		}
		p = *old
	}
	for key, field := range map[string]*string{"title": &p.Title, "account_ref": &p.AccountRef, "audience": &p.Audience, "status": &p.Status, "planned_at": &p.PlannedAt, "actual_at": &p.ActualAt, "external_post_id": &p.ExternalPostID, "external_url": &p.ExternalURL, "evidence_source": &p.EvidenceSource, "failure_details": &p.FailureDetails} {
		applyPostField(args, key, field)
	}
	if !oneOf(p.Status, "planned", "scheduled", "submitted", "provider_reported_published", "verified_published", "failed", "removed", "unknown") {
		return nil, errors.New("invalid post status")
	}
	if (p.Status == "verified_published" || p.Status == "provider_reported_published") && p.ActualAt == "" {
		return nil, errors.New("published status requires actual_at")
	}
	if p.Status == "verified_published" && p.ExternalPostID == "" && p.ExternalURL == "" {
		return nil, errors.New("verified publication requires a post URL or ID")
	}
	if p.Status != "planned" && strings.TrimSpace(p.EvidenceSource) == "" {
		return nil, errors.New("observed status requires evidence_source")
	}
	for _, value := range []string{p.PlannedAt, p.ActualAt} {
		if value != "" {
			if _, e := time.Parse(time.RFC3339, value); e != nil {
				return nil, errors.New("post times must be RFC3339")
			}
		}
	}
	ids := p.AssetIDs
	if raw, ok := args["asset_ids"]; ok {
		ids, err = assetIDsArg(raw)
		if err != nil {
			return nil, err
		}
	}
	if creating {
		if len(ids) == 0 {
			return nil, errors.New("asset_ids required")
		}
		var brandID string
		err = ctx.AppDB().QueryRow(`SELECT s.brand_id FROM assets a JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE a.project_id=? AND a.id=?`, pid, ids[0]).Scan(&brandID)
		if err != nil {
			return nil, errors.New("first asset not found")
		}
		p.BrandID = brandID
	}
	if err = validatePostAssets(ctx.AppDB(), pid, p.BrandID, ids); err != nil {
		return nil, err
	}
	if !creating {
		var verified int
		if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts p WHERE p.project_id=? AND p.id=? AND (p.status='verified_published' OR EXISTS(SELECT 1 FROM post_events e WHERE e.project_id=p.project_id AND e.post_id=p.id AND e.status='verified_published'))`, pid, id).Scan(&verified); err != nil {
			return nil, err
		}
		if verified > 0 && (len(ids) != len(p.AssetIDs) || !sameAssetSet(ids, p.AssetIDs)) {
			return nil, errors.New("assets on a historically verified post cannot be changed")
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if creating {
		_, err = tx.Exec(`INSERT INTO posts(id,project_id,brand_id,title,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, p.BrandID, p.Title, p.Destination, p.AccountRef, p.Audience, p.Status, p.PlannedAt, p.ActualAt, p.ExternalPostID, p.ExternalURL, p.EvidenceSource, p.FailureDetails)
	} else {
		_, err = tx.Exec(`UPDATE posts SET title=?,account_ref=?,audience=?,status=?,planned_at=?,actual_at=?,external_post_id=?,external_url=?,evidence_source=?,failure_details=?,updated_at=? WHERE project_id=? AND id=?`, p.Title, p.AccountRef, p.Audience, p.Status, p.PlannedAt, p.ActualAt, p.ExternalPostID, p.ExternalURL, p.EvidenceSource, p.FailureDetails, now(), pid, id)
	}
	if err != nil {
		return nil, err
	}
	if !creating {
		if _, err = tx.Exec(`DELETE FROM post_assets WHERE project_id=? AND post_id=?`, pid, id); err != nil {
			return nil, err
		}
	}
	for i, assetID := range ids {
		if _, err = tx.Exec(`INSERT INTO post_assets(project_id,post_id,asset_id,position) VALUES(?,?,?,?)`, pid, id, assetID, i); err != nil {
			return nil, err
		}
	}
	assetsJSON, _ := json.Marshal(ids)
	_, err = tx.Exec(`INSERT INTO post_events(id,project_id,post_id,status,external_post_id,external_url,actual_at,evidence_source,failure_details,asset_ids_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, newID(), pid, id, p.Status, p.ExternalPostID, p.ExternalURL, p.ActualAt, p.EvidenceSource, p.FailureDetails, string(assetsJSON))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.publication.observed", pid, map[string]any{"post_id": id, "asset_ids": ids, "status": p.Status})
	return map[string]any{"post_id": id, "publication_id": id, "status": p.Status}, nil
}

func sameAssetSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

// Existing single-asset callers create one-asset posts. Updating an existing
// post ID changes its shared status, including all attached assets.
func (a *App) assetPublicationRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id", "status"); err != nil {
		return nil, err
	}
	assetID := str(args, "asset_id")
	if _, err = assetByID(ctx.AppDB(), pid, assetID); err != nil {
		return nil, err
	}
	copyArgs := map[string]any{}
	for key, value := range args {
		copyArgs[key] = value
	}
	if id := str(args, "publication_id"); id != "" {
		p, e := postByID(ctx.AppDB(), pid, id)
		if e != nil {
			return nil, e
		}
		found := false
		for _, member := range p.AssetIDs {
			if member == assetID {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("asset is not part of that post")
		}
		copyArgs["post_id"] = id
	} else {
		copyArgs["asset_ids"] = []string{assetID}
	}
	return a.postsRecord(ctx, copyArgs)
}
