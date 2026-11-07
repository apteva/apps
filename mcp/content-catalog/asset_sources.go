package main

import (
	"database/sql"
	"strings"
)

type AssetSource struct {
	AssetID       string `json:"asset_id"`
	Relation      string `json:"relation"`
	SourceOrder   int64  `json:"source_order"`
	MediaRenderID int64  `json:"media_render_id"`
	Name          string `json:"name"`
	SessionID     string `json:"session_id"`
	Kind          string `json:"kind"`
	ContentType   string `json:"content_type"`
}

// Read existing lineage for an entire page with one project-scoped query,
// including display metadata for parents in other sessions. Never infer links.
func loadAssetSources(db *sql.DB, pid string, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	byID := map[string]*Asset{}
	values := []any{pid}
	placeholders := []string{}
	for _, asset := range assets {
		asset.Sources = []AssetSource{}
		byID[asset.ID] = asset
		values = append(values, asset.ID)
		placeholders = append(placeholders, "?")
	}
	rows, err := db.Query(`SELECT s.child_asset_id,s.source_asset_id,s.relation,s.source_order,s.media_render_id,COALESCE(a.name,''),COALESCE(a.session_id,''),COALESCE(a.kind,''),COALESCE(a.content_type,'') FROM asset_sources s LEFT JOIN assets a ON a.id=s.source_asset_id AND a.project_id=s.project_id WHERE s.project_id=? AND s.child_asset_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY s.source_order,s.source_asset_id`, values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var child string
		var source AssetSource
		if err := rows.Scan(&child, &source.AssetID, &source.Relation, &source.SourceOrder, &source.MediaRenderID, &source.Name, &source.SessionID, &source.Kind, &source.ContentType); err != nil {
			return err
		}
		byID[child].Sources = append(byID[child].Sources, source)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return loadAssetAncestors(db, pid, assets)
}
