package main

// admin.go — dashboard REST surface under /admin, behind the platform
// bearer token. The Games panel is the only client; agents use the MCP
// tools, game builds use /v2/games/{game_id} (or legacy /v1).

import (
	"net/http"
	"strings"
	"time"
)

func (a *App) adminCtx(w http.ResponseWriter, r *http.Request) (GameScope, bool) {
	scope, err := resolveGameFromRequest(getAppCtx(r), r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return GameScope{}, false
	}
	return scope, true
}

func (a *App) adminPlayer(w http.ResponseWriter, r *http.Request) (GameScope, *Player, bool) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return GameScope{}, nil, false
	}
	id, ok := pathInt(r, "id")
	if !ok {
		httpErr(w, http.StatusBadRequest, "invalid player id")
		return GameScope{}, nil, false
	}
	p, err := dbGetPlayer(getAppCtx(r).AppDB(), scope, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return GameScope{}, nil, false
	}
	if p == nil {
		httpErr(w, http.StatusNotFound, "player not found")
		return GameScope{}, nil, false
	}
	return scope, p, true
}

func (a *App) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	db := getAppCtx(r).AppDB()
	counts, err := dbPlayerCounts(db, scope)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defs, _ := dbListStatDefs(db, scope)
	boards, _ := dbListLeaderboards(db, scope)
	achs, _ := dbListAchievementDefs(db, scope)
	httpJSON(w, map[string]any{
		"players": counts, "stat_defs": len(defs), "leaderboards": len(boards), "achievements": len(achs),
	})
}

func (a *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	clientID, _ := dbGetSetting(ctx.AppDB(), scope, settingAuthClient)
	httpJSON(w, map[string]any{
		"auth_client_id":         clientID,
		"auth_organization_slug": authOrg(ctx, scope),
		"analytics_enabled":      cfgBool(ctx, "analytics_enabled", true),
		"project_id":             scope.ProjectID,
		"game_id":                scope.GameID,
		"player_api_base":        "/api/apps/games/v2/games/" + scope.GameID,
	})
}

func (a *App) handleAdminPlayersList(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	players, total, err := dbSearchPlayers(getAppCtx(r).AppDB(), scope, q.Get("q"), q.Get("status"),
		queryInt(r, "limit", 25, 1, 100), queryInt(r, "offset", 0, 0, 1_000_000))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"players": players, "total": total})
}

func (a *App) handleAdminPlayerGet(w http.ResponseWriter, r *http.Request) {
	scope, p, ok := a.adminPlayer(w, r)
	if !ok {
		return
	}
	out, err := playerContext(getAppCtx(r), scope, p)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleAdminPlayerPatch(w http.ResponseWriter, r *http.Request) {
	scope, p, ok := a.adminPlayer(w, r)
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := applyProfilePatch(getAppCtx(r), scope, p, body, "dashboard")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"player": updated})
}

func (a *App) handleAdminPlayerBan(w http.ResponseWriter, r *http.Request) {
	scope, p, ok := a.adminPlayer(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason    string `json:"reason"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ban, err := banPlayer(getAppCtx(r), scope, p, body.Reason, body.ExpiresAt, "dashboard")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"ban": ban, "player": p})
}

func (a *App) handleAdminPlayerUnban(w http.ResponseWriter, r *http.Request) {
	scope, p, ok := a.adminPlayer(w, r)
	if !ok {
		return
	}
	n, err := unbanPlayer(getAppCtx(r), scope, p, "dashboard")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"lifted": n, "player": p})
}

func (a *App) handleAdminPlayerStats(w http.ResponseWriter, r *http.Request) {
	scope, p, ok := a.adminPlayer(w, r)
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
	out, err := applyStatUpdates(getAppCtx(r), scope, p, body.Updates, "dashboard", false, r.Header.Get("Idempotency-Key"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleAdminStatDefsList(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	defs, err := dbListStatDefs(getAppCtx(r).AppDB(), scope)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"stats": defs})
}

func (a *App) handleAdminStatDefsUpsert(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	var body struct {
		Name           string `json:"name"`
		Aggregation    string `json:"aggregation"`
		ClientWritable bool   `json:"client_writable"`
		Description    string `json:"description"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	def, err := defineStat(getAppCtx(r), scope, body.Name, body.Aggregation, body.ClientWritable, body.Description)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"stat": def})
}

func (a *App) handleAdminLeaderboardsList(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	boards, err := dbListLeaderboards(ctx.AppDB(), scope)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range boards {
		if err := ensureCurrentPeriod(ctx, scope, &boards[i], time.Now()); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
	}
	httpJSON(w, map[string]any{"leaderboards": boards})
}

func (a *App) handleAdminLeaderboardsCreate(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Stat        string `json:"stat"`
		Sort        string `json:"sort"`
		Reset       string `json:"reset"`
		SeasonDays  int    `json:"season_days"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	lb, err := createLeaderboard(getAppCtx(r), scope, body.Name, body.DisplayName, body.Stat, body.Sort, body.Reset, body.SeasonDays)
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") {
			code = http.StatusConflict
		}
		httpErr(w, code, err.Error())
		return
	}
	httpStatus(w, http.StatusCreated, map[string]any{"leaderboard": lb})
}

func (a *App) adminLeaderboard(w http.ResponseWriter, r *http.Request) (GameScope, *Leaderboard, bool) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return GameScope{}, nil, false
	}
	lb, err := dbGetLeaderboard(getAppCtx(r).AppDB(), scope, strings.ToLower(r.PathValue("name")))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return GameScope{}, nil, false
	}
	if lb == nil {
		httpErr(w, http.StatusNotFound, "leaderboard not found")
		return GameScope{}, nil, false
	}
	return scope, lb, true
}

func (a *App) handleAdminLeaderboardEntries(w http.ResponseWriter, r *http.Request) {
	scope, lb, ok := a.adminLeaderboard(w, r)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	page, err := leaderboardPageFor(ctx, scope, lb, r.URL.Query().Get("period"),
		queryInt(r, "limit", 50, 1, 200), queryInt(r, "offset", 0, 0, 1_000_000), 0)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	periods, _ := dbListPeriods(ctx.AppDB(), scope, lb.ID, 24)
	httpJSON(w, map[string]any{"page": page, "periods": periods})
}

func (a *App) handleAdminLeaderboardReset(w http.ResponseWriter, r *http.Request) {
	scope, lb, ok := a.adminLeaderboard(w, r)
	if !ok {
		return
	}
	if err := resetLeaderboardNow(getAppCtx(r), scope, lb, time.Now(), r.Header.Get("Idempotency-Key")); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"leaderboard": lb, "previous_period": lb.PreviousPeriod})
}

func (a *App) handleAdminAchievementsList(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	defs, err := dbListAchievementDefs(getAppCtx(r).AppDB(), scope)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"achievements": defs})
}

func (a *App) handleAdminAchievementsUpsert(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.adminCtx(w, r)
	if !ok {
		return
	}
	var body AchievementDef
	if err := decodeBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	def, err := defineAchievement(getAppCtx(r), scope, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"achievement": def})
}
