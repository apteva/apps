package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strings"
	"time"
)

var validPatreonIntents = map[string]bool{"unset": true, "free": true, "paid": true}

type AssetLabelFields struct {
	Favorite      bool     `json:"favorite"`
	PatreonIntent string   `json:"patreon_intent"`
	Tags          []string `json:"tags"`
}

func normalizeTags(raw any) ([]string, error) {
	var values []string
	switch v := raw.(type) {
	case []string:
		values = v
	case []any:
		for _, item := range v {
			values = append(values, strings.TrimSpace(fmt.Sprint(item)))
		}
	case string:
		for _, item := range strings.Split(v, ",") {
			values = append(values, strings.TrimSpace(item))
		}
	default:
		return nil, errors.New("tags must be an array of strings or comma-separated text")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, tag := range values {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if len(tag) > 64 || strings.ContainsAny(tag, " ,\t\r\n") {
			return nil, errors.New("tags must be non-empty tokens up to 64 characters")
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	if len(out) > 25 {
		return nil, errors.New("at most 25 tags per asset")
	}
	sort.Strings(out)
	return out, nil
}
func loadAssetLabels(db *sql.DB, pid string, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	marks := make([]string, len(assets))
	vals := []any{pid}
	byID := map[string]*Asset{}
	for i, a := range assets {
		marks[i] = "?"
		vals = append(vals, a.ID)
		byID[a.ID] = a
		a.Tags = []string{}
		a.PatreonIntent = "unset"
	}
	rows, err := db.Query(`SELECT id,favorite,patreon_intent FROM assets WHERE project_id=? AND id IN (`+strings.Join(marks, ",")+`)`, vals...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, intent string
		var favorite int
		if err := rows.Scan(&id, &favorite, &intent); err != nil {
			rows.Close()
			return err
		}
		if a := byID[id]; a != nil {
			a.Favorite = favorite != 0
			a.PatreonIntent = intent
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = db.Query(`SELECT asset_id,tag FROM asset_tags WHERE project_id=? AND asset_id IN (`+strings.Join(marks, ",")+`) ORDER BY tag`, vals...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return err
		}
		if a := byID[id]; a != nil {
			a.Tags = append(a.Tags, tag)
		}
	}
	return rows.Err()
}
func (a *App) assetLabelsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := assetIDsArg(args["asset_ids"])
	if err != nil {
		return nil, err
	}
	if len(ids) > 100 {
		return nil, errors.New("at most 100 assets per update")
	}
	hasTags := args["tags"] != nil
	hasFav := args["favorite"] != nil
	hasIntent := args["patreon_intent"] != nil
	if !hasTags && !hasFav && !hasIntent {
		return nil, errors.New("provide tags, favorite, or patreon_intent")
	}
	var tags []string
	if hasTags {
		tags, err = normalizeTags(args["tags"])
		if err != nil {
			return nil, err
		}
	}
	intent := ""
	if hasIntent {
		intent = str(args, "patreon_intent")
		if !validPatreonIntents[intent] {
			return nil, errors.New("patreon_intent must be unset, free, or paid")
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	updated := []*Asset{}
	for _, id := range ids {
		var rev int64
		var current int
		var currentIntent string
		if err = tx.QueryRow(`SELECT revision,favorite,patreon_intent FROM assets WHERE project_id=? AND id=?`, pid, id).Scan(&rev, &current, &currentIntent); err != nil {
			return nil, err
		}
		if raw := args["expected_revisions"]; raw != nil {
			expected := map[string]int64{}
			if e := decodeJSON(raw, &expected); e != nil {
				return nil, errors.New("expected_revisions must map asset IDs to revisions")
			}
			if expected[id] != rev {
				return nil, errors.New("revision conflict; reload the assets")
			}
		}
		f := current
		if hasFav {
			if v, ok := args["favorite"].(bool); ok {
				if v {
					f = 1
				} else {
					f = 0
				}
			} else {
				return nil, errors.New("favorite must be boolean")
			}
		}
		in := currentIntent
		if hasIntent {
			in = intent
		}
		_, err = tx.Exec(`UPDATE assets SET favorite=?,patreon_intent=?,revision=revision+1,updated_at=? WHERE project_id=? AND id=? AND revision=?`, f, in, time.Now().UTC().Format(time.RFC3339Nano), pid, id, rev)
		if err != nil {
			return nil, err
		}
		asset := &Asset{ID: id, AssetLabelFields: AssetLabelFields{Favorite: f != 0, PatreonIntent: in, Tags: append([]string(nil), tags...)}}
		updated = append(updated, asset)
		if hasTags {
			if _, err = tx.Exec(`DELETE FROM asset_tags WHERE project_id=? AND asset_id=?`, pid, id); err != nil {
				return nil, err
			}
			for _, tag := range tags {
				if _, err = tx.Exec(`INSERT INTO asset_tags(project_id,asset_id,tag) VALUES(?,?,?)`, pid, id, tag); err != nil {
					return nil, err
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"assets": updated}, nil
}
func decodeJSON(raw any, out any) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func assetLabelsSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"asset_ids"}, "properties": map[string]any{
		"asset_ids":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 100},
		"expected_revisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer", "minimum": 1}},
		"tags":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 25},
		"favorite":           map[string]any{"type": "boolean"},
		"patreon_intent":     map[string]any{"type": "string", "enum": []string{"unset", "free", "paid"}},
	}}
}
