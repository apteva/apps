// Site settings — KV table. Bootstraps from the install's config
// (apteva.yaml::config_schema) on first read so the defaults
// (site_title, posts_per_page, render_mode, …) are usable even before
// any settings_set call.

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

func dbGetSetting(db *sql.DB, projectID, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE project_id=? AND key=?`, projectID, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func dbSetSetting(db *sql.DB, projectID, key, value string) error {
	_, err := db.Exec(`INSERT INTO settings (project_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(project_id, key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		projectID, key, value)
	return err
}

func dbListSettings(db *sql.DB, projectID string) (map[string]string, error) {
	rows, err := db.Query(`SELECT key, value FROM settings WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// effectiveSettings merges install-time config (from apteva.yaml) with
// per-project DB overrides. DB wins.
func effectiveSettings(ctx *sdk.AppCtx, projectID string) (map[string]string, error) {
	out := map[string]string{}
	if cfg := ctx.Config(); cfg != nil {
		for k, v := range cfg {
			out[k] = v
		}
	}
	dbVals, err := dbListSettings(ctx.AppDB(), projectID)
	if err != nil {
		return out, err
	}
	for k, v := range dbVals {
		out[k] = v
	}
	return out, nil
}

func (a *App) toolSettingsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	all, err := effectiveSettings(ctx, pid)
	if err != nil {
		return nil, err
	}
	if keys, ok := args["keys"].([]any); ok && len(keys) > 0 {
		picked := map[string]string{}
		for _, k := range keys {
			if s, ok := k.(string); ok {
				picked[s] = all[s]
			}
		}
		return map[string]any{"settings": picked}, nil
	}
	return map[string]any{"settings": all}, nil
}

func (a *App) toolSettingsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	key := asString(args["key"])
	if key == "" {
		return nil, errors.New("key required")
	}
	val := ""
	switch v := args["value"].(type) {
	case string:
		val = v
	case nil:
		val = ""
	default:
		b, _ := json.Marshal(v)
		val = string(b)
	}
	if err := dbSetSetting(ctx.AppDB(), pid, key, val); err != nil {
		return nil, err
	}
	invalidatePageCache()
	return map[string]any{"ok": true, "key": key, "value": val}, nil
}

func (a *App) handleHTTPSettings(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		all, err := effectiveSettings(ctx, pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"settings": all})
	case http.MethodPost, http.MethodPut:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolSettingsSet(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
