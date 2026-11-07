package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Only these four values constitute a saved edge; display metadata is not a
// concurrency token. Compare edge sets canonically, independent of input order.
type SourceEdge struct {
	AssetID       string `json:"asset_id"`
	Relation      string `json:"relation"`
	SourceOrder   int64  `json:"source_order"`
	MediaRenderID int64  `json:"media_render_id"`
}

func sourceEdges(sources []AssetSource) []SourceEdge {
	out := make([]SourceEdge, 0, len(sources))
	for _, s := range sources {
		out = append(out, SourceEdge{s.AssetID, s.Relation, s.SourceOrder, s.MediaRenderID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssetID < out[j].AssetID })
	return out
}
func parseSourceEdges(raw any) ([]SourceEdge, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var entries []map[string]json.RawMessage
	if err = json.Unmarshal(b, &entries); err != nil || entries == nil {
		return nil, errors.New("sources must be an explicit array (empty array removes all sources)")
	}
	if len(entries) > 100 {
		return nil, errors.New("at most 100 sources")
	}
	out := make([]SourceEdge, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		for _, key := range []string{"asset_id", "relation", "source_order", "media_render_id"} {
			if len(entry[key]) == 0 || string(entry[key]) == "null" {
				return nil, fmt.Errorf("source edge requires %s", key)
			}
		}
		b, _ = json.Marshal(entry)
		var edge SourceEdge
		if err = json.Unmarshal(b, &edge); err != nil {
			return nil, errors.New("source fields must be text and nonnegative integers")
		}
		if err = validateSourceEdge(edge); err != nil {
			return nil, err
		}
		if seen[edge.AssetID] {
			return nil, errors.New("duplicate source asset ID")
		}
		seen[edge.AssetID] = true
		out = append(out, edge)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssetID < out[j].AssetID })
	return out, nil
}
func validateSourceEdge(edge SourceEdge) error {
	if strings.TrimSpace(edge.AssetID) == "" || edge.AssetID != strings.TrimSpace(edge.AssetID) {
		return errors.New("invalid source asset ID")
	}
	if strings.TrimSpace(edge.Relation) == "" || edge.Relation != strings.TrimSpace(edge.Relation) || len(edge.Relation) > 128 {
		return errors.New("relation must be nonempty text up to 128 characters")
	}
	if edge.SourceOrder < 0 || edge.MediaRenderID < 0 {
		return errors.New("source_order and media_render_id must be nonnegative integers")
	}
	return nil
}
func sourceInteger(raw any, fallback int64) (int64, error) {
	if raw == nil {
		return fallback, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return 0, err
	}
	var v int64
	if err = json.Unmarshal(b, &v); err != nil || v < 0 {
		return 0, errors.New("expected a nonnegative integer")
	}
	return v, nil
}
func savedSources(tx *sql.Tx, pid, child string) ([]AssetSource, error) {
	rows, err := tx.Query(`SELECT s.source_asset_id,s.relation,s.source_order,s.media_render_id,COALESCE(a.name,''),COALESCE(a.session_id,''),COALESCE(a.kind,''),COALESCE(a.content_type,'') FROM asset_sources s LEFT JOIN assets a ON a.id=s.source_asset_id AND a.project_id=s.project_id WHERE s.project_id=? AND s.child_asset_id=? ORDER BY s.source_order,s.source_asset_id`, pid, child)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetSource{}
	for rows.Next() {
		var s AssetSource
		if err = rows.Scan(&s.AssetID, &s.Relation, &s.SourceOrder, &s.MediaRenderID, &s.Name, &s.SessionID, &s.Kind, &s.ContentType); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func lockSourceChild(tx *sql.Tx, pid, child string) (int64, error) {
	// Acquire SQLite's writer lock before reading ANY graph state. All supported
	// lineage writes use this lock so concurrent edits cannot create a cycle.
	res, err := tx.Exec(`UPDATE assets SET revision=revision WHERE project_id=? AND id=?`, pid, child)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, errors.New("child asset not found in current project")
	}
	var revision int64
	err = tx.QueryRow(`SELECT revision FROM assets WHERE project_id=? AND id=?`, pid, child).Scan(&revision)
	return revision, err
}
func validateSourceParents(tx *sql.Tx, pid, child string, edges []SourceEdge) error {
	for _, edge := range edges {
		if edge.AssetID == child {
			return errors.New("asset cannot be its own source")
		}
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM assets WHERE project_id=? AND id=?`, pid, edge.AssetID).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return errors.New("source asset not found in current project")
		}
		var cycle int
		if err := tx.QueryRow(`WITH RECURSIVE ancestors(id) AS (
   SELECT source_asset_id FROM asset_sources WHERE project_id=? AND child_asset_id=?
   UNION SELECT s.source_asset_id FROM asset_sources s JOIN ancestors t ON s.child_asset_id=t.id JOIN assets parent ON parent.id=t.id AND parent.project_id=s.project_id WHERE s.project_id=?
  ) SELECT COUNT(*) FROM ancestors WHERE id=?`, pid, edge.AssetID, pid, child).Scan(&cycle); err != nil {
			return err
		}
		if cycle > 0 {
			return errors.New("asset lineage cycle")
		}
	}
	return nil
}
func saveSourceEvent(tx *sql.Tx, pid, child, operation, key, hash, reason string, previous, revision int64, before, after []SourceEdge) (string, error) {
	id := newID()
	b, _ := json.Marshal(before)
	d, _ := json.Marshal(after)
	_, err := tx.Exec(`INSERT INTO asset_source_events(id,project_id,asset_id,operation,idempotency_key,request_hash,previous_revision,revision,before_json,after_json,reason,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, child, operation, key, hash, previous, revision, string(b), string(d), reason, now())
	return id, err
}
func advanceSourceRevision(tx *sql.Tx, pid, child string, revision int64) error {
	res, err := tx.Exec(`UPDATE assets SET revision=revision+1,updated_at=? WHERE project_id=? AND id=? AND revision=?`, now(), pid, child, revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("revision conflict; reload the asset")
	}
	return nil
}
func sourceResult(child, status string, revision int64, sources []AssetSource, event string, replayed bool) map[string]any {
	return map[string]any{"child_asset_id": child, "status": status, "revision": revision, "sources": sources, "audit_event_id": event, "replayed": replayed}
}
func (a *App) assetLinkSource(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "child_asset_id", "source_asset_id"); err != nil {
		return nil, err
	}
	child := str(args, "child_asset_id")
	edge := SourceEdge{AssetID: str(args, "source_asset_id"), Relation: str(args, "relation")}
	if edge.Relation == "" {
		edge.Relation = "derived"
	}
	edge.SourceOrder, err = sourceInteger(args["source_order"], 0)
	if err != nil {
		return nil, err
	}
	edge.MediaRenderID, err = sourceInteger(args["media_render_id"], 0)
	if err != nil {
		return nil, err
	}
	if err = validateSourceEdge(edge); err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	revision, err := lockSourceChild(tx, pid, child)
	if err != nil {
		return nil, err
	}
	before, err := savedSources(tx, pid, child)
	if err != nil {
		return nil, err
	}
	if err = validateSourceParents(tx, pid, child, []SourceEdge{edge}); err != nil {
		return nil, err
	}
	// An existing edge is either exactly identical or a conflict, never a
	// successful correction. Conflicts make no writes and require replacement.
	for _, old := range sourceEdges(before) {
		if old.AssetID != edge.AssetID {
			continue
		}
		if old != edge {
			out := sourceResult(child, "conflict", revision, before, "", false)
			out["linked"] = false
			out["source_asset_id"] = edge.AssetID
			out["existing_source"] = old
			out["requested_source"] = edge
			out["error"] = "existing source values conflict; use content_catalog_assets_sources_replace with expected_revision and expected_sources"
			return out, nil
		}
		out := sourceResult(child, "unchanged", revision, before, "", false)
		out["linked"] = true
		out["source_asset_id"] = edge.AssetID
		return out, nil
	}

	if _, err = tx.Exec(`INSERT INTO asset_sources(project_id,child_asset_id,source_asset_id,relation,source_order,media_render_id) VALUES(?,?,?,?,?,?)`, pid, child, edge.AssetID, edge.Relation, edge.SourceOrder, edge.MediaRenderID); err != nil {
		return nil, err
	}
	if err = advanceSourceRevision(tx, pid, child, revision); err != nil {
		return nil, err
	}
	after, err := savedSources(tx, pid, child)
	if err != nil {
		return nil, err
	}
	event, err := saveSourceEvent(tx, pid, child, "link", "", "", str(args, "reason"), revision, revision+1, sourceEdges(before), sourceEdges(after))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	out := sourceResult(child, "inserted", revision+1, after, event, false)
	out["linked"] = true
	out["source_asset_id"] = edge.AssetID
	return out, nil
}
func (a *App) assetSourcesReplace(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "child_asset_id", "idempotency_key", "reason"); err != nil {
		return nil, err
	}
	if args["expected_revision"] == nil {
		return nil, errors.New("expected_revision is required")
	}
	expectedRevision, err := sourceInteger(args["expected_revision"], 0)
	if err != nil || expectedRevision < 1 {
		return nil, errors.New("expected_revision must be a positive integer")
	}
	expected, err := parseSourceEdges(args["expected_sources"])
	if err != nil {
		return nil, fmt.Errorf("expected_sources: %w", err)
	}
	desired, err := parseSourceEdges(args["sources"])
	if err != nil {
		return nil, err
	}
	child, key, reason := str(args, "child_asset_id"), str(args, "idempotency_key"), str(args, "reason")
	if len(key) > 160 || len(reason) > 2000 {
		return nil, errors.New("idempotency_key max 160 characters; reason max 2000")
	}
	b, _ := json.Marshal([]any{child, expectedRevision, expected, desired, reason})
	digest := sha256.Sum256(b)
	hash := hex.EncodeToString(digest[:])
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	revision, err := lockSourceChild(tx, pid, child)
	if err != nil {
		return nil, err
	}
	before, err := savedSources(tx, pid, child)
	if err != nil {
		return nil, err
	}
	actual := sourceEdges(before)
	var event, previousHash, afterJSON string
	err = tx.QueryRow(`SELECT id,request_hash,after_json FROM asset_source_events WHERE project_id=? AND idempotency_key=?`, pid, key).Scan(&event, &previousHash, &afterJSON)
	if err == nil {
		if previousHash != hash {
			return nil, errors.New("idempotency_key reused with different request")
		}
		var saved []SourceEdge
		if err = json.Unmarshal([]byte(afterJSON), &saved); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(actual, saved) {
			return nil, errors.New("retry conflict: sources changed after this operation; reload and use a new key")
		}
		return sourceResult(child, "unchanged", revision, before, event, true), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if revision != expectedRevision {
		return nil, errors.New("revision conflict; reload the asset and its sources")
	}
	if !reflect.DeepEqual(actual, expected) {
		return nil, errors.New("source list conflict; reload the asset and its sources")
	}
	if err = validateSourceParents(tx, pid, child, desired); err != nil {
		return nil, err
	}
	status := "unchanged"
	nextRevision := revision
	if !reflect.DeepEqual(actual, desired) {
		if _, err = tx.Exec(`DELETE FROM asset_sources WHERE project_id=? AND child_asset_id=?`, pid, child); err != nil {
			return nil, err
		}
		for _, edge := range desired {
			if _, err = tx.Exec(`INSERT INTO asset_sources(project_id,child_asset_id,source_asset_id,relation,source_order,media_render_id) VALUES(?,?,?,?,?,?)`, pid, child, edge.AssetID, edge.Relation, edge.SourceOrder, edge.MediaRenderID); err != nil {
				return nil, err
			}
		}
		if err = advanceSourceRevision(tx, pid, child, revision); err != nil {
			return nil, err
		}
		status = "replaced"
		nextRevision++
	}
	after, err := savedSources(tx, pid, child)
	if err != nil {
		return nil, err
	}
	event, err = saveSourceEvent(tx, pid, child, "replace", key, hash, reason, revision, nextRevision, actual, sourceEdges(after))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return sourceResult(child, status, nextRevision, after, event, false), nil
}
func sourceHistory(db *sql.DB, pid, child string) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT id,operation,idempotency_key,previous_revision,revision,before_json,after_json,reason,created_at FROM asset_source_events WHERE project_id=? AND asset_id=? ORDER BY revision DESC,rowid DESC LIMIT 100`, pid, child)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, operation, key, beforeJSON, afterJSON, reason, at string
		var previous, revision int64
		if err = rows.Scan(&id, &operation, &key, &previous, &revision, &beforeJSON, &afterJSON, &reason, &at); err != nil {
			return nil, err
		}
		var before, after []SourceEdge
		if err = json.Unmarshal([]byte(beforeJSON), &before); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(afterJSON), &after); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "operation": operation, "idempotency_key": key, "previous_revision": previous, "revision": revision, "before": before, "after": after, "reason": reason, "created_at": at})
	}
	return out, rows.Err()
}
func sourceLinkSchema(replace bool) map[string]any {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": description}
	}
	edgeProps := map[string]any{"asset_id": text("Exact Catalog parent ID"), "relation": text("Explicit immediate relationship, e.g. cropped"), "source_order": map[string]any{"type": "integer", "minimum": 0}, "media_render_id": map[string]any{"type": "integer", "minimum": 0, "description": "Media render ID; 0 means not recorded"}}
	edges := map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "object", "properties": edgeProps, "required": []string{"asset_id", "relation", "source_order", "media_render_id"}, "additionalProperties": true}}
	props := map[string]any{"child_asset_id": text("Exact Catalog child ID"), "reason": text("Reason retained in the source correction audit")}
	required := []string{"child_asset_id"}
	if replace {
		props["expected_revision"] = map[string]any{"type": "integer", "minimum": 1}
		props["expected_sources"] = edges
		props["sources"] = edges
		props["idempotency_key"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 160, "description": "Stable key for this exact correction; reuse identical arguments on retry"}
		required = append(required, "expected_revision", "expected_sources", "sources", "idempotency_key", "reason")
	} else {
		props["source_asset_id"] = text("Exact Catalog parent ID")
		props["relation"] = edgeProps["relation"]
		props["source_order"] = edgeProps["source_order"]
		props["media_render_id"] = edgeProps["media_render_id"]
		required = append(required, "source_asset_id")
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": true}
}
