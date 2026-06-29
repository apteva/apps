package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: entitlements
display_name: Entitlements
version: 0.1.0
description: Shared access-control and usage layer.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/entitlements
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/entitlements.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("entitlements requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("entitlements mounted", "version", "0.1.0", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/grants", Handler: a.handleGrants},
		{Pattern: "/grants/", Handler: a.handleGrantItem},
		{Pattern: "/check", Handler: a.handleCheck},
		{Pattern: "/limits", Handler: a.handleLimits},
		{Pattern: "/usage", Handler: a.handleUsage},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "entitlement_grants_create", Description: "Grant a feature/key to a subject.", InputSchema: subjectSchema(map[string]any{"source_type": map[string]any{"type": "string"}, "source_id": map[string]any{"type": "string"}, "starts_at": map[string]any{"type": "string"}, "expires_at": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"}}), Handler: a.toolGrantCreate},
		{Name: "entitlement_grants_revoke", Description: "Revoke a grant.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "reason": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolGrantRevoke},
		{Name: "entitlements_check", Description: "Check subject access to a feature/key.", InputSchema: subjectSchema(nil), Handler: a.toolCheck},
		{Name: "entitlement_grants_list", Description: "List grants for a subject.", InputSchema: schemaObject(map[string]any{"subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"}, "feature_key": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, []string{"subject_id"}), Handler: a.toolGrantsList},
		{Name: "entitlement_limits_set", Description: "Set subject/feature limit.", InputSchema: subjectSchema(map[string]any{"limit_type": map[string]any{"type": "string"}, "limit_value": map[string]any{"type": "integer"}, "reset_interval": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"}}), Handler: a.toolLimitSet},
		{Name: "usage_record", Description: "Record usage.", InputSchema: subjectSchema(map[string]any{"quantity": map[string]any{"type": "integer"}, "idempotency_key": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"}}), Handler: a.toolUsageRecord},
		{Name: "usage_get", Description: "Get usage and limits.", InputSchema: subjectSchema(nil), Handler: a.toolUsageGet},
	}
}

func main() { sdk.Run(&App{}) }

type Grant struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id"`
	SubjectType   string          `json:"subject_type"`
	SubjectID     string          `json:"subject_id"`
	FeatureKey    string          `json:"feature_key"`
	Status        string          `json:"status"`
	SourceType    string          `json:"source_type"`
	SourceID      string          `json:"source_id,omitempty"`
	StartsAt      string          `json:"starts_at,omitempty"`
	ExpiresAt     string          `json:"expires_at,omitempty"`
	RevokedAt     string          `json:"revoked_at,omitempty"`
	RevokedReason string          `json:"revoked_reason,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

type Limit struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id"`
	SubjectType   string          `json:"subject_type"`
	SubjectID     string          `json:"subject_id"`
	FeatureKey    string          `json:"feature_key"`
	LimitType     string          `json:"limit_type"`
	LimitValue    int64           `json:"limit_value"`
	ResetInterval string          `json:"reset_interval,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

func (a *App) toolGrantCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	g, err := dbGrantCreate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	ctx.Emit("entitlement.granted", map[string]any{"grant_id": g.ID, "subject_id": g.SubjectID, "feature_key": g.FeatureKey})
	return map[string]any{"grant": g}, nil
}

func (a *App) toolGrantRevoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	g, err := dbGrantRevoke(ctx.AppDB(), pid, int64Arg(args, "id"), strArg(args, "reason"))
	if err != nil {
		return nil, err
	}
	ctx.Emit("entitlement.revoked", map[string]any{"grant_id": g.ID})
	return map[string]any{"grant": g}, nil
}

func (a *App) toolCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ok, grants, usage, limit, err := dbCheck(ctx.AppDB(), pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"allowed": ok, "grants": grants, "usage": usage, "limit": limit}, nil
}

func (a *App) toolGrantsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbGrantsList(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"grants": out, "count": len(out)}, nil
}

func (a *App) toolLimitSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	lim, err := dbLimitSet(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"limit": lim}, nil
}

func (a *App) toolUsageRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	usage, err := dbUsageRecord(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage": usage}, nil
}

func (a *App) toolUsageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	usage, limit, err := dbUsageGet(ctx.AppDB(), pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage": usage, "limit": limit}, nil
}

func dbGrantCreate(db *sql.DB, pid string, args map[string]any) (*Grant, error) {
	if strArg(args, "subject_id") == "" || strArg(args, "feature_key") == "" {
		return nil, errors.New("subject_id and feature_key required")
	}
	var id int64
	err := db.QueryRow(
		`INSERT INTO entitlement_grants
		   (project_id, subject_type, subject_id, feature_key, status, source_type, source_id, starts_at, expires_at, metadata)
		 VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)
		 RETURNING id`,
		pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"),
		firstNonEmpty(strArg(args, "source_type"), "manual"), nullStr(strArg(args, "source_id")),
		nullStr(strArg(args, "starts_at")), nullStr(strArg(args, "expires_at")), jsonOrEmpty(args["metadata"], "{}"),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return dbGrantGet(db, pid, id)
}

func dbGrantRevoke(db *sql.DB, pid string, id int64, reason string) (*Grant, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	res, err := db.Exec(`UPDATE entitlement_grants SET status='revoked', revoked_at=CURRENT_TIMESTAMP, revoked_reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, nullStr(reason), id, pid)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("grant not found")
	}
	return dbGrantGet(db, pid, id)
}

func dbGrantGet(db *sql.DB, pid string, id int64) (*Grant, error) {
	g, err := scanGrant(db.QueryRow(grantSelect()+` WHERE id=? AND project_id=?`, id, pid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return g, err
}

func dbGrantsList(db *sql.DB, pid string, args map[string]any) ([]*Grant, error) {
	where := []string{"project_id=?", "subject_type=?", "subject_id=?"}
	qargs := []any{pid, subjectType(args), strArg(args, "subject_id")}
	if strArg(args, "feature_key") != "" {
		where = append(where, "feature_key=?")
		qargs = append(qargs, strArg(args, "feature_key"))
	}
	if strArg(args, "status") != "" {
		where = append(where, "status=?")
		qargs = append(qargs, strArg(args, "status"))
	}
	qargs = append(qargs, clampLimit(int(int64Arg(args, "limit")), 200))
	rows, err := db.Query(grantSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func dbCheck(db *sql.DB, pid, subjType, subjID, feature string) (bool, []*Grant, int64, *Limit, error) {
	if subjID == "" || feature == "" {
		return false, nil, 0, nil, errors.New("subject_id and feature_key required")
	}
	args := map[string]any{"subject_type": subjType, "subject_id": subjID, "feature_key": feature, "status": "active", "limit": 50}
	grants, err := dbGrantsList(db, pid, args)
	if err != nil {
		return false, nil, 0, nil, err
	}
	active := make([]*Grant, 0, len(grants))
	nowClause := `SELECT 1`
	_ = nowClause
	for _, g := range grants {
		if grantIsCurrent(g) {
			active = append(active, g)
		}
	}
	usage, limit, err := dbUsageGet(db, pid, subjType, subjID, feature)
	if err != nil {
		return false, nil, 0, nil, err
	}
	allowed := len(active) > 0
	if allowed && limit != nil && limit.LimitValue > 0 && usage >= limit.LimitValue {
		allowed = false
	}
	return allowed, active, usage, limit, nil
}

func dbLimitSet(db *sql.DB, pid string, args map[string]any) (*Limit, error) {
	if strArg(args, "subject_id") == "" || strArg(args, "feature_key") == "" {
		return nil, errors.New("subject_id and feature_key required")
	}
	_, err := db.Exec(
		`INSERT INTO entitlement_limits
		   (project_id, subject_type, subject_id, feature_key, limit_type, limit_value, reset_interval, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, subject_type, subject_id, feature_key) DO UPDATE SET
		   limit_type=excluded.limit_type, limit_value=excluded.limit_value, reset_interval=excluded.reset_interval,
		   metadata=excluded.metadata, updated_at=CURRENT_TIMESTAMP`,
		pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"),
		firstNonEmpty(strArg(args, "limit_type"), "quota"), int64Arg(args, "limit_value"), nullStr(strArg(args, "reset_interval")), jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	return dbLimitGet(db, pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
}

func dbLimitGet(db *sql.DB, pid, subjType, subjID, feature string) (*Limit, error) {
	var l Limit
	var reset sql.NullString
	var meta string
	err := db.QueryRow(`SELECT id, project_id, subject_type, subject_id, feature_key, limit_type, limit_value, reset_interval, metadata FROM entitlement_limits WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=?`, pid, subjType, subjID, feature).
		Scan(&l.ID, &l.ProjectID, &l.SubjectType, &l.SubjectID, &l.FeatureKey, &l.LimitType, &l.LimitValue, &reset, &meta)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if reset.Valid {
		l.ResetInterval = reset.String
	}
	l.Metadata = json.RawMessage(meta)
	return &l, nil
}

func dbUsageRecord(db *sql.DB, pid string, args map[string]any) (map[string]any, error) {
	if strArg(args, "subject_id") == "" || strArg(args, "feature_key") == "" {
		return nil, errors.New("subject_id and feature_key required")
	}
	qty := firstNonZero(int64Arg(args, "quantity"), 1)
	_, err := db.Exec(`INSERT INTO usage_events (project_id, subject_type, subject_id, feature_key, quantity, idempotency_key, metadata) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"), qty, nullStr(strArg(args, "idempotency_key")), jsonOrEmpty(args["metadata"], "{}"))
	if err != nil && strings.Contains(err.Error(), "ux_usage_idempotency") {
		usage, limit, getErr := dbUsageGet(db, pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
		return map[string]any{"quantity": usage, "limit": limit, "deduped": true}, getErr
	}
	if err != nil {
		return nil, err
	}
	usage, limit, err := dbUsageGet(db, pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
	return map[string]any{"quantity": usage, "limit": limit, "deduped": false}, err
}

func dbUsageGet(db *sql.DB, pid, subjType, subjID, feature string) (int64, *Limit, error) {
	var total int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM usage_events WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=?`, pid, subjType, subjID, feature).Scan(&total); err != nil {
		return 0, nil, err
	}
	lim, err := dbLimitGet(db, pid, subjType, subjID, feature)
	return total, lim, err
}

func grantSelect() string {
	return `SELECT id, project_id, subject_type, subject_id, feature_key, status, source_type, COALESCE(source_id,''), starts_at, expires_at, revoked_at, COALESCE(revoked_reason,''), metadata, created_at, updated_at FROM entitlement_grants`
}

func scanGrant(row rowScanner) (*Grant, error) {
	var g Grant
	var starts, expires, revoked sql.NullString
	var meta string
	err := row.Scan(&g.ID, &g.ProjectID, &g.SubjectType, &g.SubjectID, &g.FeatureKey, &g.Status, &g.SourceType, &g.SourceID, &starts, &expires, &revoked, &g.RevokedReason, &meta, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if starts.Valid {
		g.StartsAt = starts.String
	}
	if expires.Valid {
		g.ExpiresAt = expires.String
	}
	if revoked.Valid {
		g.RevokedAt = revoked.String
	}
	g.Metadata = json.RawMessage(meta)
	return &g, nil
}

func grantIsCurrent(g *Grant) bool {
	if g == nil || g.Status != "active" || g.RevokedAt != "" {
		return false
	}
	now := time.Now().UTC()
	if g.StartsAt != "" {
		if t, err := time.Parse(time.RFC3339, g.StartsAt); err == nil && now.Before(t) {
			return false
		}
	}
	if g.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, g.ExpiresAt); err == nil && !now.Before(t) {
			return false
		}
	}
	return true
}

func (a *App) handleGrants(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		args := map[string]any{"subject_type": r.URL.Query().Get("subject_type"), "subject_id": r.URL.Query().Get("subject_id"), "feature_key": r.URL.Query().Get("feature_key"), "status": r.URL.Query().Get("status"), "limit": r.URL.Query().Get("limit")}
		out, err := dbGrantsList(ctx.AppDB(), pid, args)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, map[string]any{"grants": out, "count": len(out)})
		return
	}
	if r.Method == http.MethodPost {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid JSON body")
			return
		}
		g, err := dbGrantCreate(ctx.AppDB(), pid, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"grant": g})
		return
	}
	httpErr(w, 405, "method not allowed")
}

func (a *App) handleGrantItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/revoke") || r.Method != http.MethodPost {
		httpErr(w, 405, "method not allowed")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	g, err := dbGrantRevoke(ctx.AppDB(), pid, pathInt(r.URL.Path, "/grants/"), strArg(body, "reason"))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, map[string]any{"grant": g})
}

func (a *App) handleCheck(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	args := map[string]any{"subject_type": r.URL.Query().Get("subject_type"), "subject_id": r.URL.Query().Get("subject_id"), "feature_key": r.URL.Query().Get("feature_key")}
	ok, grants, usage, limit, err := dbCheck(ctx.AppDB(), pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, map[string]any{"allowed": ok, "grants": grants, "usage": usage, "limit": limit})
}

func (a *App) handleLimits(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method != http.MethodPost {
		httpErr(w, 405, "method not allowed")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, 400, "invalid JSON body")
		return
	}
	lim, err := dbLimitSet(ctx.AppDB(), pid, body)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, map[string]any{"limit": lim})
}

func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		args := map[string]any{"subject_type": r.URL.Query().Get("subject_type"), "subject_id": r.URL.Query().Get("subject_id"), "feature_key": r.URL.Query().Get("feature_key")}
		usage, limit, err := dbUsageGet(ctx.AppDB(), pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, map[string]any{"usage": usage, "limit": limit})
		return
	}
	if r.Method == http.MethodPost {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid JSON body")
			return
		}
		usage, err := dbUsageRecord(ctx.AppDB(), pid, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"usage": usage})
		return
	}
	httpErr(w, 405, "method not allowed")
}

type rowScanner interface{ Scan(dest ...any) error }

func subjectSchema(extra map[string]any) map[string]any {
	props := map[string]any{"subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"}, "feature_key": map[string]any{"type": "string"}}
	for k, v := range extra {
		props[k] = v
	}
	return schemaObject(props, []string{"subject_id", "feature_key"})
}
func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func subjectType(m map[string]any) string {
	return firstNonEmpty(strArg(m, "subject_type"), "customer")
}
func resolveProjectFromArgs(args map[string]any) (string, error) {
	pid := strings.TrimSpace(strArg(args, "_project_id"))
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id required")
	}
	return pid, nil
}
func resolveProjectFromRequest(r *http.Request) (string, error) {
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id query parameter required")
	}
	return pid, nil
}
func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
func int64Arg(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}
func jsonOrEmpty(v any, sentinel string) string {
	if v == nil {
		return sentinel
	}
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return sentinel
		}
		return t
	case []byte:
		if len(t) == 0 {
			return sentinel
		}
		return string(t)
	case json.RawMessage:
		if len(t) == 0 {
			return sentinel
		}
		return string(t)
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return sentinel
	}
	return string(raw)
}
func clampLimit(n, max int) int {
	if n <= 0 {
		return 50
	}
	if n > max {
		return max
	}
	return n
}
func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.SplitN(rest, "/", 2)[0]
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}
func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }
