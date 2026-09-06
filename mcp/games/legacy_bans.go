package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
)

// v0.1 disabled Auth accounts when banning a player. Only reverse a migrated
// disable when Auth's latest disable audit attributes it to that exact ban.
// Missing audit evidence or a later independent suspension requires review.
func recoverLegacyBan(ctx *sdk.AppCtx, scope GameScope, userID int64, explicitUnban bool) (bool, error) {
	var reason string
	err := ctx.AppDB().QueryRow(`SELECT reason FROM game_legacy_auth_bans WHERE project_id=? AND game_id=? AND auth_user_id=?`, scope.ProjectID, scope.GameID, userID).Scan(&reason)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	p, err := dbGetPlayerByAuthUser(ctx.AppDB(), scope, userID)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, nil
	}
	if !explicitUnban {
		ban, err := dbActiveBan(ctx.AppDB(), scope, p.ID)
		if err != nil {
			return false, err
		}
		if ban != nil {
			return false, nil
		}
	}
	var audit struct {
		Events []struct {
			Metadata string `json:"metadata"`
		} `json:"events"`
	}
	if err = callAuth(ctx, scope, "auth_audit_search", map[string]any{"user_id": userID, "event": "user_disabled", "limit": 1}, &audit); err != nil {
		return false, err
	}
	var metadata struct {
		Reason string `json:"reason"`
	}
	if len(audit.Events) != 1 || json.Unmarshal([]byte(audit.Events[0].Metadata), &metadata) != nil || metadata.Reason != "games ban: "+reason {
		return false, errors.New("legacy Auth suspension needs review: latest disable is not attributable to this game ban")
	}
	if err = authEnableUser(ctx, scope, userID); err != nil {
		return false, err
	}
	_, err = ctx.AppDB().Exec(`DELETE FROM game_legacy_auth_bans WHERE project_id=? AND game_id=? AND auth_user_id=?`, scope.ProjectID, scope.GameID, userID)
	return err == nil, err
}

func recoverLegacyBans(ctx *sdk.AppCtx, scope GameScope) {
	rows, err := ctx.AppDB().Query(`SELECT auth_user_id FROM game_legacy_auth_bans WHERE project_id=? AND game_id=? LIMIT 100`, scope.ProjectID, scope.GameID)
	if err != nil {
		ctx.Logger().Warn("legacy ban lookup failed", "error", err)
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err = recoverLegacyBan(ctx, scope, id, false); err != nil {
			ctx.Logger().Warn("legacy Auth ban recovery pending", "game_id", scope.GameID, "auth_user_id", id, "error", err)
		}
	}
}
