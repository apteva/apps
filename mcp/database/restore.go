package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/database/engine"
)

// OnRestore moves databases restored from a snapshot to the new project and
// the new install ids of the apps that own them. Only scopes of the restored
// project move; a global install's other projects stay as they are.
func (a *App) OnRestore(ctx context.Context, app *sdk.AppCtx, _ *sql.Tx, rc sdk.RestoreContext) error {
	if rc.OldProjectID == "" && len(rc.InstallIDs) == 0 {
		return nil
	}
	moved, err := engine.RemapScopes(ctx, filepath.Join(app.DataDir(), "database"), func(scope string) (string, bool) {
		return restoredScope(scope, rc)
	})
	if err != nil {
		return err
	}
	app.Logger().Info("database scopes restored", "scopes", moved)
	return nil
}

// restoredScope maps one scope key, the JSON array [project, owner install id]
// built by scope(). The owner keeps its id when the snapshot has no mapping
// for it.
func restoredScope(raw string, rc sdk.RestoreContext) (string, bool) {
	var parts []string
	if err := json.Unmarshal([]byte(raw), &parts); err != nil || len(parts) != 2 {
		return raw, false
	}
	project, owner := parts[0], parts[1]
	if rc.OldProjectID != "" {
		if project != rc.OldProjectID {
			return raw, false
		}
		project = rc.NewProjectID
	}
	if owner != "" {
		if old, err := strconv.ParseInt(owner, 10, 64); err == nil {
			if next, ok := rc.InstallID(old); ok {
				owner = strconv.FormatInt(next, 10)
			}
		}
	}
	out, _ := json.Marshal([]string{project, owner})
	return string(out), string(out) != raw
}
