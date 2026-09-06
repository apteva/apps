package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strings"
	"time"
)

func createLoginTicket(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	scope, err := resolveGameFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	raw := stringArg(args, "custom_id", "")
	if raw == "" || len(raw) > maxIdentityLen {
		return nil, errors.New("custom_id required (max 256 bytes)")
	}
	token := randomID() + randomID()
	expires := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	_, err = ctx.AppDB().Exec(`INSERT INTO game_login_tickets(token_hash,project_id,game_id,subject,expires_at) VALUES(?,?,?,?,?)`, identitySubject(token), scope.ProjectID, scope.GameID, gameIdentity(ctx, scope, raw), expires)
	if err != nil {
		return nil, err
	}
	return map[string]any{"login_ticket": token, "expires_at": expires, "game_id": scope.GameID}, nil
}
func consumeLoginTicket(ctx *sdk.AppCtx, scope GameScope, token string) (string, error) {
	var subject string
	err := ctx.AppDB().QueryRow(`DELETE FROM game_login_tickets WHERE token_hash=? AND project_id=? AND game_id=? AND expires_at>? RETURNING subject`, identitySubject(token), scope.ProjectID, scope.GameID, nowRFC()).Scan(&subject)
	if err != nil {
		return "", errors.New("invalid or expired login ticket")
	}
	return subject, nil
}
func (a *App) handleLoginTicket(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	if err := decodeBody(w, r, &args); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	args["game_id"] = r.PathValue("game_id")
	args["_project_id"] = r.URL.Query().Get("project_id")
	out, err := createLoginTicket(getAppCtx(r), args)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, out)
}
func isV2(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/v2/") }
