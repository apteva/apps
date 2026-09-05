package main

// public.go — the /v1 player API game clients call.
//
// Every route is NoAuth at the SDK gate (the platform token never
// reaches a game build). Login routes talk to Auth and return an Auth
// session; every other route requires `Authorization: Bearer <access>`
// with an Auth-issued JWT, verified locally against Auth's JWKS.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxIdentityLen = 256

func bearerToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	}
	return ""
}

// requirePlayer verifies the bearer token, resolves the player row, and
// enforces bans. It writes the error response itself on failure.
func (a *App) requirePlayer(w http.ResponseWriter, r *http.Request) (*sdk.AppCtx, string, *Player, bool) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return nil, "", nil, false
	}
	token := bearerToken(r)
	if token == "" {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "missing_bearer"})
		return nil, "", nil, false
	}
	claims, err := verifyPlayerToken(ctx, pid, token)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "detail": err.Error()})
		return nil, "", nil, false
	}
	player, err := dbGetPlayerByAuthUser(ctx.AppDB(), pid, claims.AuthUserID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return nil, "", nil, false
	}
	if player == nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{
			"error": "player_not_found", "hint": "call /v1/login/device or /v1/login/custom first",
		})
		return nil, "", nil, false
	}
	ban, err := activeBanFor(ctx, pid, player)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return nil, "", nil, false
	}
	if ban != nil {
		httpStatus(w, http.StatusForbidden, map[string]any{"error": "banned", "reason": ban.Reason, "expires_at": ban.ExpiresAt})
		return nil, "", nil, false
	}
	return ctx, pid, player, true
}

// ─── login ───────────────────────────────────────────────────────────

type loginBody struct {
	DeviceID    string `json:"device_id"`
	CustomID    string `json:"custom_id"`
	DisplayName string `json:"display_name"`
}

func (a *App) handleLoginDevice(w http.ResponseWriter, r *http.Request) { a.login(w, r, "device") }
func (a *App) handleLoginCustom(w http.ResponseWriter, r *http.Request) { a.login(w, r, "custom") }

func (a *App) login(w http.ResponseWriter, r *http.Request, provider string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body loginBody
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	raw := body.DeviceID
	if provider == "custom" {
		raw = body.CustomID
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxIdentityLen {
		httpErr(w, http.StatusBadRequest, provider+"_id required (max 256 characters)")
		return
	}
	subject := identitySubject(raw)
	res, err := authLoginIdentity(ctx, pid, provider, subject, strings.TrimSpace(body.DisplayName), clientIP(r), r.UserAgent())
	if err != nil {
		a.loginFailed(w, ctx, pid, provider, subject, err)
		return
	}
	a.finishLogin(w, ctx, pid, provider, res)
}

func (a *App) loginFailed(w http.ResponseWriter, ctx *sdk.AppCtx, pid, provider, subject string, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "rate_limited"):
		httpStatus(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited", "detail": msg})
	case strings.Contains(msg, "user_inactive"), strings.Contains(msg, "account_locked"):
		// Disabled in Auth — usually one of our bans. Surface the reason.
		if uid, rerr := authResolveIdentity(ctx, pid, provider, subject); rerr == nil && uid > 0 {
			if p, _ := dbGetPlayerByAuthUser(ctx.AppDB(), pid, uid); p != nil {
				if ban, _ := dbActiveBan(ctx.AppDB(), pid, p.ID); ban != nil {
					httpStatus(w, http.StatusForbidden, map[string]any{"error": "banned", "reason": ban.Reason, "expires_at": ban.ExpiresAt})
					return
				}
			}
		}
		httpStatus(w, http.StatusForbidden, map[string]string{"error": "account_disabled"})
	default:
		httpStatus(w, http.StatusBadGateway, map[string]string{"error": "auth_unavailable", "detail": msg})
	}
}

func (a *App) finishLogin(w http.ResponseWriter, ctx *sdk.AppCtx, pid, provider string, res *authLoginResult) {
	db := ctx.AppDB()
	kind := res.User.Kind
	if kind == "" {
		kind = "guest"
	}
	player, err := dbGetPlayerByAuthUser(db, pid, res.User.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	created := false
	if player == nil {
		name := strings.TrimSpace(res.User.DisplayName)
		id, err := dbCreatePlayer(db, pid, res.User.ID, name, kind)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if name == "" {
			name = fmt.Sprintf("%s %d", cfgStr(ctx, "default_display_name_prefix", "Player"), id)
			_ = dbUpdatePlayer(db, pid, id, playerPatch{DisplayName: &name})
		}
		player, err = dbGetPlayer(db, pid, id)
		if err != nil || player == nil {
			httpErr(w, http.StatusInternalServerError, "player row missing after create")
			return
		}
		created = true
	}
	ban, err := activeBanFor(ctx, pid, player)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ban != nil {
		httpStatus(w, http.StatusForbidden, map[string]any{"error": "banned", "reason": ban.Reason, "expires_at": ban.ExpiresAt})
		return
	}
	if err := dbTouchLogin(db, pid, player.ID, kind); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if refreshed, err := dbGetPlayer(db, pid, player.ID); err == nil && refreshed != nil {
		player = refreshed
	}
	if created {
		dbAudit(db, pid, player.ID, "player.created", "login:"+provider, map[string]any{"auth_user_id": res.User.ID})
		ctx.Emit("player.created", map[string]any{
			"player_id": player.ID, "auth_user_id": res.User.ID, "provider": provider, "kind": kind, "display_name": player.DisplayName,
		})
	}
	trackEvent(ctx, pid, "session_start", player, map[string]any{"provider": provider, "created": created})
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	httpStatus(w, code, map[string]any{
		"player":        player,
		"created":       created,
		"access_token":  res.AccessToken,
		"refresh_token": res.RefreshToken,
		"expires_in":    res.ExpiresIn,
		"token_type":    "Bearer",
		"auth": map[string]any{
			"client_id":         res.ClientID,
			"organization_slug": res.OrganizationSlug,
			"refresh_path":      "/api/apps/auth/refresh",
		},
	})
}

func (a *App) handleLoginLink(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	var body struct {
		Provider       string `json:"provider"`
		ProviderUserID string `json:"provider_user_id"`
		DeviceID       string `json:"device_id"`
		CustomID       string `json:"custom_id"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	provider := strings.ToLower(strings.TrimSpace(body.Provider))
	raw := body.ProviderUserID
	if provider == "" {
		switch {
		case strings.TrimSpace(body.DeviceID) != "":
			provider, raw = "device", body.DeviceID
		case strings.TrimSpace(body.CustomID) != "":
			provider, raw = "custom", body.CustomID
		}
	}
	if provider != "device" && provider != "custom" {
		httpErr(w, http.StatusBadRequest, "provider must be device or custom (pass device_id or custom_id)")
		return
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxIdentityLen {
		httpErr(w, http.StatusBadRequest, "identity required (max 256 characters)")
		return
	}
	if err := authLinkIdentity(ctx, pid, player.AuthUserID, provider, identitySubject(raw)); err != nil {
		if strings.Contains(err.Error(), "identity_already_linked") {
			httpStatus(w, http.StatusConflict, map[string]string{"error": "identity_already_linked"})
			return
		}
		httpStatus(w, http.StatusBadGateway, map[string]string{"error": "auth_unavailable", "detail": err.Error()})
		return
	}
	dbAudit(ctx.AppDB(), pid, player.ID, "player.linked", "client", map[string]any{"provider": provider})
	ctx.Emit("player.linked", map[string]any{"player_id": player.ID, "provider": provider})
	httpJSON(w, map[string]any{"ok": true, "provider": provider})
}

// handleSessionRefresh proxies to Auth's /refresh so a game build only
// needs one base URL. Sessions are Auth's; games adds nothing here.
func (a *App) handleSessionRefresh(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeBody(w, r, &body); err != nil || strings.TrimSpace(body.RefreshToken) == "" {
		httpErr(w, http.StatusBadRequest, "refresh_token required")
		return
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("APTEVA_GATEWAY_URL")), "/")
	if base == "" {
		httpStatus(w, http.StatusServiceUnavailable, map[string]string{
			"error": "refresh_unavailable",
			"hint":  "POST refresh_token and client_id to the Auth app's /refresh route",
		})
		return
	}
	clientID, err := ensureAuthClient(ctx, pid)
	if err != nil {
		httpStatus(w, http.StatusBadGateway, map[string]string{"error": "auth_unavailable", "detail": err.Error()})
		return
	}
	payload, _ := json.Marshal(map[string]string{"refresh_token": body.RefreshToken, "client_id": clientID})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		base+"/api/apps/auth/refresh?project_id="+url.QueryEscape(pid), bytes.NewReader(payload))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		httpStatus(w, http.StatusBadGateway, map[string]string{"error": "auth_unavailable", "detail": err.Error()})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// ─── me / profiles ───────────────────────────────────────────────────

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	_, _, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	httpJSON(w, map[string]any{"player": player})
}

func (a *App) handleMePatch(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	allowed := map[string]any{}
	for _, k := range []string{"display_name", "avatar_url", "region", "locale", "metadata"} {
		if v, ok := body[k]; ok {
			allowed[k] = v
		}
	}
	updated, err := applyProfilePatch(ctx, pid, player, allowed, "client")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"player": updated})
}

func (a *App) handlePublicPlayer(w http.ResponseWriter, r *http.Request) {
	ctx, pid, _, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		httpErr(w, http.StatusBadRequest, "invalid player id")
		return
	}
	p, err := dbGetPlayer(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		httpErr(w, http.StatusNotFound, "player not found")
		return
	}
	data, err := dbListData(ctx.AppDB(), pid, p.ID, []string{"public"})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"player": publicProfile(p), "data": data})
}

// ─── player data ─────────────────────────────────────────────────────

func (a *App) handleDataList(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	entries, err := dbListData(ctx.AppDB(), pid, player.ID, []string{"public", "private"})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"data": entries})
}

func (a *App) handleDataGet(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	e, err := dbGetData(ctx.AppDB(), pid, player.ID, key)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if e == nil || e.Visibility == "server" {
		httpStatus(w, http.StatusNotFound, map[string]string{"error": "not_found", "key": key})
		return
	}
	httpJSON(w, map[string]any{"data": e})
}

func (a *App) handleDataPut(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !dataKeyRe.MatchString(key) {
		httpErr(w, http.StatusBadRequest, "key must match [A-Za-z0-9_.:-]{1,128}")
		return
	}
	var body struct {
		Value      json.RawMessage `json:"value"`
		Visibility string          `json:"visibility"`
		Version    int64           `json:"version"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	value := bytes.TrimSpace(body.Value)
	if len(value) == 0 || !json.Valid(value) {
		httpErr(w, http.StatusBadRequest, "value must be a JSON value")
		return
	}
	if len(value) > maxDataValueBytes {
		httpErr(w, http.StatusRequestEntityTooLarge, "value must be 256 KB or less")
		return
	}
	switch body.Visibility {
	case "", "public", "private":
	case "server":
		httpErr(w, http.StatusForbidden, "server-only keys are written by tools, not clients")
		return
	default:
		httpErr(w, http.StatusBadRequest, "visibility must be public or private")
		return
	}
	existing, err := dbGetData(ctx.AppDB(), pid, player.ID, key)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing != nil && existing.Visibility == "server" {
		httpErr(w, http.StatusForbidden, "this key is server-only")
		return
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, value)
	e, err := dbSetData(ctx.AppDB(), pid, player.ID, key, compact.String(), body.Visibility, body.Version)
	if errors.Is(err, errVersionConflict) {
		httpStatus(w, http.StatusConflict, map[string]any{"error": "version_conflict", "current": existing})
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"data": e})
}

func (a *App) handleDataDelete(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	existing, err := dbGetData(ctx.AppDB(), pid, player.ID, key)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		httpStatus(w, http.StatusNotFound, map[string]string{"error": "not_found", "key": key})
		return
	}
	if existing.Visibility == "server" {
		httpErr(w, http.StatusForbidden, "this key is server-only")
		return
	}
	if _, err := dbDeleteData(ctx.AppDB(), pid, player.ID, key); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── statistics ──────────────────────────────────────────────────────

func (a *App) handleStatsGet(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	stats, err := dbGetPlayerStats(ctx.AppDB(), pid, player.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"stats": stats})
}

func (a *App) handleStatsPost(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	var body struct {
		Updates []statUpdate `json:"updates"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Updates) == 0 || len(body.Updates) > 50 {
		httpErr(w, http.StatusBadRequest, "updates must hold 1-50 entries of {stat, value}")
		return
	}
	out, err := applyStatUpdates(ctx, pid, player, body.Updates, "client", true)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, out)
}

// ─── leaderboards ────────────────────────────────────────────────────

func (a *App) handleLeaderboardGet(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	lb, err := dbGetLeaderboard(ctx.AppDB(), pid, strings.ToLower(r.PathValue("name")))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if lb == nil {
		httpErr(w, http.StatusNotFound, "leaderboard not found")
		return
	}
	page, err := leaderboardPageFor(ctx, pid, lb, r.URL.Query().Get("period"),
		queryInt(r, "limit", 50, 1, 200), queryInt(r, "offset", 0, 0, 1_000_000), player.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, page)
}

func (a *App) handleLeaderboardAround(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	lb, err := dbGetLeaderboard(ctx.AppDB(), pid, strings.ToLower(r.PathValue("name")))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if lb == nil {
		httpErr(w, http.StatusNotFound, "leaderboard not found")
		return
	}
	page, err := leaderboardAround(ctx, pid, lb, r.URL.Query().Get("period"), player.ID, queryInt(r, "radius", 5, 1, 50))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, page)
}

// ─── achievements ────────────────────────────────────────────────────

func (a *App) handleAchievementsGet(w http.ResponseWriter, r *http.Request) {
	ctx, pid, player, ok := a.requirePlayer(w, r)
	if !ok {
		return
	}
	defs, err := dbListAchievementDefs(ctx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	mine, err := dbPlayerAchievements(ctx.AppDB(), pid, player.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	unlockedAt := map[string]string{}
	for _, m := range mine {
		unlockedAt[m.Key] = m.UnlockedAt
	}
	items := []map[string]any{}
	for _, d := range defs {
		at, unlocked := unlockedAt[d.Key]
		if d.Hidden && !unlocked {
			continue
		}
		item := map[string]any{
			"key": d.Key, "name": d.Name, "description": d.Description, "stat": d.Stat,
			"threshold": d.Threshold, "op": d.Op, "unlocked": unlocked,
		}
		if unlocked {
			item["unlocked_at"] = at
		}
		items = append(items, item)
	}
	httpJSON(w, map[string]any{"achievements": items})
}
