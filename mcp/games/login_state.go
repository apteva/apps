package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

var errPlayerErased = errors.New("player erased")

type loginBanError struct{ ban *Ban }

func (e *loginBanError) Error() string { return "banned" }

func finishLoginState(ctx *sdk.AppCtx, scope GameScope, provider string, res *authLoginResult) (*Player, bool, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if err = checkActiveGame(tx, scope); err != nil {
		return nil, false, err
	}
	var erased int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM game_tombstones WHERE project_id=? AND game_id=? AND auth_user_id=?`, scope.ProjectID, scope.GameID, res.User.ID).Scan(&erased); err != nil {
		return nil, false, err
	}
	if erased > 0 {
		return nil, false, errPlayerErased
	}
	p, err := dbGetPlayerByAuthUser(tx, scope, res.User.ID)
	if err != nil {
		return nil, false, err
	}
	created := p == nil
	kind := res.User.Kind
	if kind == "" {
		kind = "guest"
	}
	if created {
		name := strings.TrimSpace(res.User.DisplayName)
		if len(name) > 64 {
			name = name[:64]
		}
		id, err := dbCreatePlayer(tx, scope, res.User.ID, name, kind)
		if err != nil {
			return nil, false, err
		}
		if name == "" {
			name = fmt.Sprintf("%s %d", cfgStr(ctx, "default_display_name_prefix", "Player"), id)
			if err = dbUpdatePlayer(tx, scope, id, playerPatch{DisplayName: &name}); err != nil {
				return nil, false, err
			}
		}
		p, err = dbGetPlayer(tx, scope, id)
		if err != nil {
			return nil, false, err
		}
	}
	ban, err := dbActiveBan(tx, scope, p.ID)
	if err != nil {
		return nil, false, err
	}
	if ban != nil {
		return nil, false, &loginBanError{ban}
	}
	if p.Status == "banned" {
		if _, err = dbLiftBans(tx, scope, p.ID); err != nil {
			return nil, false, err
		}
		if err = dbSetPlayerStatus(tx, scope, p.ID, "active"); err != nil {
			return nil, false, err
		}
		if err = dbAudit(tx, scope, p.ID, "player.unbanned", "expiry", nil); err != nil {
			return nil, false, err
		}
		if err = queueEvent(tx, scope, "player.unbanned", map[string]any{"player_id": p.ID, "source": "expiry"}, false); err != nil {
			return nil, false, err
		}
	}
	if err = dbTouchLogin(tx, scope, p.ID, kind); err != nil {
		return nil, false, err
	}
	p, err = dbGetPlayer(tx, scope, p.ID)
	if err != nil {
		return nil, false, err
	}
	if created {
		if err = dbAudit(tx, scope, p.ID, "player.created", "login:"+provider, map[string]any{"auth_user_id": res.User.ID}); err != nil {
			return nil, false, err
		}
		if err = queueEvent(tx, scope, "player.created", map[string]any{"player_id": p.ID, "auth_user_id": res.User.ID, "provider": provider, "kind": kind, "display_name": p.DisplayName}, false); err != nil {
			return nil, false, err
		}
	}
	if cfgBool(ctx, "analytics_enabled", true) {
		if err = queueEvent(tx, scope, "games.session_start", map[string]any{"player_id": p.ID, "provider": provider, "created": created}, true); err != nil {
			return nil, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return p, created, nil
}
