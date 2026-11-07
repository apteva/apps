package main

import (
	"database/sql"
	"strings"
)

// Thin context nodes are provenance, never additional content/search results.
// Source links remain unchanged; archived/intermediate ancestors are readable.
type LineageNode struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	SessionID        string        `json:"session_id"`
	Kind             string        `json:"kind"`
	Role             string        `json:"role"`
	OutputType       string        `json:"output_type"`
	Lifecycle        string        `json:"lifecycle"`
	SessionLifecycle string        `json:"session_lifecycle"`
	Sources          []AssetSource `json:"sources"`
}

func loadAssetAncestors(db *sql.DB, pid string, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	marks := []string{}
	values := []any{pid}
	byID := map[string]*Asset{}
	for _, a := range assets {
		a.Ancestors = []LineageNode{}
		marks = append(marks, "?")
		values = append(values, a.ID)
		byID[a.ID] = a
	}
	values = append(values, pid, pid)
	rows, err := db.Query(`WITH RECURSIVE ancestry(origin,id) AS (
 SELECT child_asset_id,source_asset_id FROM asset_sources WHERE project_id=? AND child_asset_id IN (`+strings.Join(marks, ",")+`)
 UNION SELECT t.origin,s.source_asset_id FROM ancestry t
 JOIN assets parent ON parent.id=t.id AND parent.project_id=?
 JOIN asset_sources s ON s.child_asset_id=parent.id AND s.project_id=parent.project_id
 ) SELECT t.origin,a.id,a.name,a.session_id,a.kind,a.role,a.output_type,a.lifecycle,se.lifecycle,
 COALESCE(s.source_asset_id,''),COALESCE(s.relation,''),COALESCE(s.source_order,0),COALESCE(s.media_render_id,0)
 FROM ancestry t JOIN assets a ON a.id=t.id AND a.project_id=?
 JOIN sessions se ON se.id=a.session_id AND se.project_id=a.project_id
 LEFT JOIN asset_sources s ON s.child_asset_id=a.id AND s.project_id=a.project_id
 WHERE t.id<>t.origin ORDER BY t.origin,a.id,s.source_order,s.source_asset_id`, values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	nodes := map[string]map[string]*LineageNode{}
	orders := map[string][]string{}
	for rows.Next() {
		var origin string
		var n LineageNode
		var source AssetSource
		if err := rows.Scan(&origin, &n.ID, &n.Name, &n.SessionID, &n.Kind, &n.Role, &n.OutputType, &n.Lifecycle, &n.SessionLifecycle, &source.AssetID, &source.Relation, &source.SourceOrder, &source.MediaRenderID); err != nil {
			return err
		}
		if nodes[origin] == nil {
			nodes[origin] = map[string]*LineageNode{}
		}
		node := nodes[origin][n.ID]
		if node == nil {
			n.Sources = []AssetSource{}
			node = &n
			nodes[origin][n.ID] = node
			orders[origin] = append(orders[origin], n.ID)
		}
		if source.AssetID != "" {
			node.Sources = append(node.Sources, source)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for origin, ids := range orders {
		for _, id := range ids {
			byID[origin].Ancestors = append(byID[origin].Ancestors, *nodes[origin][id])
		}
	}
	return nil
}
