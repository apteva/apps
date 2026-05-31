package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Dashboard struct {
	ID          int64             `json:"id"`
	ProjectID   string            `json:"project_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	CreatedAt   int64             `json:"created_at"`
	UpdatedAt   int64             `json:"updated_at"`
	Widgets     []DashboardWidget `json:"widgets,omitempty"`
}

type DashboardWidget struct {
	ID          int64          `json:"id"`
	DashboardID int64          `json:"dashboard_id,omitempty"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Position    int            `json:"position"`
	Config      map[string]any `json:"config"`
	CreatedAt   int64          `json:"created_at,omitempty"`
	UpdatedAt   int64          `json:"updated_at,omitempty"`
}

func (a *App) handleDashboards(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleDashboardsList(w, r)
	case http.MethodPost:
		a.handleDashboardsCreate(w, r)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDashboardItem(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/dashboards/"))
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid dashboard id", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 && parts[1] == "widgets" {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		a.handleWidgetCreate(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleDashboardGet(w, r, id)
	case http.MethodPut:
		a.handleDashboardUpdate(w, r, id)
	case http.MethodDelete:
		a.handleDashboardDelete(w, r, id)
	default:
		http.Error(w, "GET, PUT or DELETE only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleWidgetItem(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/widgets/"))
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid widget id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		a.handleWidgetUpdate(w, r, id)
	case http.MethodDelete:
		a.handleWidgetDelete(w, r, id)
	default:
		http.Error(w, "PUT or DELETE only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleWidgetQuery(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ProjectID string          `json:"project_id"`
		Widget    DashboardWidget `json:"widget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" {
		body.ProjectID = globalCtx.CurrentProject()
	}
	result, err := evaluateWidget(globalCtx.AppDB(), body.ProjectID, body.Widget)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (a *App) handleDashboardsList(w http.ResponseWriter, r *http.Request) {
	projectID := projectFromRequest(r)
	rows, err := globalCtx.AppDB().Query(
		`SELECT id, project_id, name, description, created_at, updated_at
		 FROM dashboards WHERE project_id = ? ORDER BY updated_at DESC, id DESC`,
		projectID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var out []Dashboard
	for rows.Next() {
		var d Dashboard
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Description, &d.CreatedAt, &d.UpdatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, d)
	}
	writeJSON(w, map[string]any{"dashboards": out})
}

func (a *App) handleDashboardsCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID   string `json:"project_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Template    string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" {
		body.ProjectID = globalCtx.CurrentProject()
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		if body.Template == "website_traffic" {
			body.Name = "Website Traffic"
		} else {
			body.Name = "Analytics Dashboard"
		}
	}
	d, err := createDashboard(globalCtx.AppDB(), body.ProjectID, body.Name, body.Description, templateWidgets(body.Template))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (a *App) handleDashboardGet(w http.ResponseWriter, r *http.Request, id int64) {
	d, err := getDashboard(globalCtx.AppDB(), id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (a *App) handleDashboardUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	_, err := globalCtx.AppDB().Exec(
		`UPDATE dashboards SET name=?, description=?, updated_at=? WHERE id=?`,
		body.Name, body.Description, time.Now().UnixMilli(), id,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.handleDashboardGet(w, r, id)
}

func (a *App) handleDashboardDelete(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := globalCtx.AppDB().Exec(`DELETE FROM dashboards WHERE id=?`, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleWidgetCreate(w http.ResponseWriter, r *http.Request, dashboardID int64) {
	var body DashboardWidget
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Type == "" {
		http.Error(w, "type required", http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		body.Title = defaultWidgetTitle(body.Type)
	}
	widget, err := insertWidget(globalCtx.AppDB(), dashboardID, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, widget)
}

func (a *App) handleWidgetUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	var body DashboardWidget
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Type == "" {
		http.Error(w, "type required", http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		body.Title = defaultWidgetTitle(body.Type)
	}
	cfg, _ := json.Marshal(nonNilConfig(body.Config))
	_, err := globalCtx.AppDB().Exec(
		`UPDATE dashboard_widgets SET type=?, title=?, position=?, config_json=?, updated_at=? WHERE id=?`,
		body.Type, body.Title, body.Position, string(cfg), time.Now().UnixMilli(), id,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleWidgetDelete(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := globalCtx.AppDB().Exec(`DELETE FROM dashboard_widgets WHERE id=?`, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func createDashboard(db *sql.DB, projectID, name, description string, widgets []DashboardWidget) (*Dashboard, error) {
	now := time.Now().UnixMilli()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO dashboards (project_id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		projectID, name, description, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for i, w := range widgets {
		w.Position = i
		if err := insertWidgetTx(tx, id, w, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getDashboard(db, id)
}

func getDashboard(db *sql.DB, id int64) (*Dashboard, error) {
	var d Dashboard
	err := db.QueryRow(
		`SELECT id, project_id, name, description, created_at, updated_at FROM dashboards WHERE id=?`,
		id,
	).Scan(&d.ID, &d.ProjectID, &d.Name, &d.Description, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	widgets, err := listWidgets(db, id)
	if err != nil {
		return nil, err
	}
	d.Widgets = widgets
	return &d, nil
}

func insertWidget(db *sql.DB, dashboardID int64, w DashboardWidget) (*DashboardWidget, error) {
	now := time.Now().UnixMilli()
	res, err := db.Exec(
		`INSERT INTO dashboard_widgets (dashboard_id, type, title, position, config_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		dashboardID, w.Type, w.Title, w.Position, mustConfigJSON(w.Config), now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	w.ID = id
	w.DashboardID = dashboardID
	w.CreatedAt = now
	w.UpdatedAt = now
	w.Config = nonNilConfig(w.Config)
	return &w, nil
}

func insertWidgetTx(tx *sql.Tx, dashboardID int64, w DashboardWidget, now int64) error {
	_, err := tx.Exec(
		`INSERT INTO dashboard_widgets (dashboard_id, type, title, position, config_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		dashboardID, w.Type, w.Title, w.Position, mustConfigJSON(w.Config), now, now,
	)
	return err
}

func listWidgets(db *sql.DB, dashboardID int64) ([]DashboardWidget, error) {
	rows, err := db.Query(
		`SELECT id, dashboard_id, type, title, position, config_json, created_at, updated_at
		 FROM dashboard_widgets WHERE dashboard_id=? ORDER BY position, id`,
		dashboardID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DashboardWidget
	for rows.Next() {
		var w DashboardWidget
		var cfg string
		if err := rows.Scan(&w.ID, &w.DashboardID, &w.Type, &w.Title, &w.Position, &cfg, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		if cfg != "" {
			_ = json.Unmarshal([]byte(cfg), &w.Config)
		}
		w.Config = nonNilConfig(w.Config)
		out = append(out, w)
	}
	return out, rows.Err()
}

func evaluateWidget(db *sql.DB, projectID string, w DashboardWidget) (map[string]any, error) {
	f := filterFromWidget(projectID, w.Config)
	limit := intConfig(w.Config, "limit", 10)
	switch w.Type {
	case "stat":
		var (
			n   int64
			err error
		)
		if by := stringConfig(w.Config, "by", ""); by != "" {
			n, err = distinctCount(db, f, by)
		} else {
			n, err = countEvents(db, f)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": w.Type, "value": n}, nil
	case "timeseries":
		rows, err := seriesForWidget(db, f, stringConfig(w.Config, "interval", "minute"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": w.Type, "series": rows}, nil
	case "top", "breakdown":
		by := stringConfig(w.Config, "by", "props.path")
		rows, err := topByPropsKey(db, f, by, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": w.Type, "top": rows, "by": by}, nil
	case "feed":
		rows, err := queryRows(db, f, intConfig(w.Config, "limit", 25))
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": w.Type, "events": rows}, nil
	default:
		return nil, fmt.Errorf("unsupported widget type %q", w.Type)
	}
}

func filterFromWidget(projectID string, cfg map[string]any) Filter {
	window := windowMillis(stringConfig(cfg, "window", "24h"))
	f := Filter{
		App:       stringConfig(cfg, "app", ""),
		Topic:     stringConfig(cfg, "topic", ""),
		ProjectID: projectID,
		Source:    stringConfig(cfg, "source", ""),
	}
	if window > 0 {
		f.Since = time.Now().UnixMilli() - window
	}
	if raw, ok := cfg["where"].(map[string]any); ok {
		f.Where = raw
	}
	return f
}

func seriesForWidget(db *sql.DB, f Filter, interval string) ([]map[string]any, error) {
	where, args := f.buildWhere()
	expr := "strftime('%Y-%m-%dT%H:%M:00Z', ts / 1000, 'unixepoch')"
	switch interval {
	case "hour":
		expr = "strftime('%Y-%m-%dT%H:00:00Z', ts / 1000, 'unixepoch')"
	case "day":
		expr = "strftime('%Y-%m-%d', ts / 1000, 'unixepoch')"
	}
	q := "SELECT " + expr + " AS bucket, COUNT(*) AS count FROM events"
	if where != "" {
		q += " WHERE " + where
	}
	q += " GROUP BY bucket ORDER BY bucket"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var bucket string
		var count int64
		if err := rows.Scan(&bucket, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"bucket": bucket, "count": count})
	}
	return out, rows.Err()
}

func distinctCount(db *sql.DB, f Filter, by string) (int64, error) {
	expr, ok := dashboardGroupExpr(by)
	if !ok {
		return 0, fmt.Errorf("by must be session_id, user_id, app, topic, source or props.X")
	}
	where, args := f.buildWhere()
	q := "SELECT COUNT(DISTINCT " + expr + ") FROM events"
	if where != "" {
		q += " WHERE " + where + " AND " + expr + " IS NOT NULL"
	} else {
		q += " WHERE " + expr + " IS NOT NULL"
	}
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func dashboardGroupExpr(by string) (string, bool) {
	switch by {
	case "session_id", "user_id", "app", "topic", "source", "project_id":
		return by, true
	default:
		return propsExtract(by)
	}
}

func templateWidgets(name string) []DashboardWidget {
	if name != "website_traffic" {
		return nil
	}
	return []DashboardWidget{
		{Type: "stat", Title: "Page Views Today", Config: map[string]any{"topic": "page_view", "window": "24h"}},
		{Type: "stat", Title: "Active Sessions", Config: map[string]any{"topic": "page_view", "window": "5m", "by": "session_id"}},
		{Type: "timeseries", Title: "Live Page Views", Config: map[string]any{"topic": "page_view", "window": "30m", "interval": "minute"}},
		{Type: "top", Title: "Top Pages", Config: map[string]any{"topic": "page_view", "window": "24h", "by": "props.path", "limit": 8}},
		{Type: "breakdown", Title: "Devices", Config: map[string]any{"topic": "page_view", "window": "24h", "by": "props.device", "limit": 5}},
		{Type: "feed", Title: "Live Activity", Config: map[string]any{"topic": "page_view", "window": "30m", "limit": 20}},
	}
}

func defaultWidgetTitle(typ string) string {
	switch typ {
	case "stat":
		return "Metric"
	case "timeseries":
		return "Trend"
	case "top":
		return "Top Values"
	case "breakdown":
		return "Breakdown"
	case "feed":
		return "Live Feed"
	default:
		return "Widget"
	}
}

func projectFromRequest(r *http.Request) string {
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		return pid
	}
	return globalCtx.CurrentProject()
}

func splitPath(s string) []string {
	raw := strings.Split(strings.Trim(s, "/"), "/")
	out := raw[:0]
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func mustConfigJSON(cfg map[string]any) string {
	b, _ := json.Marshal(nonNilConfig(cfg))
	return string(b)
}

func nonNilConfig(cfg map[string]any) map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	return cfg
}

func stringConfig(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intConfig(cfg map[string]any, key string, def int) int {
	if v := int64Config(cfg, key, int64(def)); v > 0 {
		return int(v)
	}
	return def
}

func int64Config(cfg map[string]any, key string, def int64) int64 {
	switch v := cfg[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return def
}

func windowMillis(s string) int64 {
	if s == "" || s == "all" {
		return 0
	}
	unit := s[len(s)-1]
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil || n <= 0 {
		return 24 * 3600 * 1000
	}
	switch unit {
	case 'm':
		return n * 60 * 1000
	case 'h':
		return n * 3600 * 1000
	case 'd':
		return n * 24 * 3600 * 1000
	default:
		return 24 * 3600 * 1000
	}
}
