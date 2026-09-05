package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	Config      map[string]any    `json:"config"`
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
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleDashboardsList(w, r, projectID)
	case http.MethodPost:
		a.handleDashboardsCreate(w, r, projectID)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDashboardItem(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
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
		a.handleWidgetCreate(w, r, projectID, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleDashboardGet(w, r, projectID, id)
	case http.MethodPut:
		a.handleDashboardUpdate(w, r, projectID, id)
	case http.MethodDelete:
		a.handleDashboardDelete(w, r, projectID, id)
	default:
		http.Error(w, "GET, PUT or DELETE only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleWidgetItem(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
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
		a.handleWidgetUpdate(w, r, projectID, id)
	case http.MethodDelete:
		a.handleWidgetDelete(w, r, projectID, id)
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
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	var body struct {
		ProjectID string          `json:"project_id"`
		Widget    DashboardWidget `json:"widget"`
		Filters   map[string]any  `json:"filters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if _, err := assignRequestProject(body.ProjectID, projectID); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	result, err := evaluateWidget(requestReadDB(r), projectID, body.Widget, body.Filters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

type dashboardWidgetResult struct {
	WidgetID int64          `json:"widget_id"`
	Data     map[string]any `json:"data,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func (a *App) handleDashboardQuery(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	var body struct {
		ProjectID    string         `json:"project_id"`
		DashboardID  int64          `json:"dashboard_id"`
		Filters      map[string]any `json:"filters"`
		IncludeGoals bool           `json:"include_goals"`
		WidgetIDs    []int64        `json:"widget_ids"`
	}
	if r.Method == http.MethodGet {
		body.ProjectID = r.URL.Query().Get("project_id")
		body.DashboardID = parseInt64(r.URL.Query().Get("dashboard_id"))
		body.IncludeGoals = r.URL.Query().Get("include_goals") == "true"
		if raw := r.URL.Query().Get("widget_ids"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &body.WidgetIDs); err != nil {
				http.Error(w, "invalid widget_ids", 400)
				return
			}
		}
		if raw := r.URL.Query().Get("filters"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &body.Filters); err != nil {
				http.Error(w, "invalid filters", http.StatusBadRequest)
				return
			}
		}
	} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if _, err := assignRequestProject(body.ProjectID, projectID); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	tx, err := readPool(globalCtx).BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer tx.Rollback()
	plan := newEvaluationPlan(contextualDB{db: tx, ctx: r.Context()})
	dashboard, err := getDashboardForProject(plan, body.DashboardID, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(body.WidgetIDs) > 0 {
		requested := map[int64]bool{}
		for _, id := range body.WidgetIDs {
			requested[id] = true
		}
		filtered := []DashboardWidget{}
		for _, widget := range dashboard.Widgets {
			if requested[widget.ID] {
				filtered = append(filtered, widget)
				delete(requested, widget.ID)
			}
		}
		if len(requested) > 0 {
			http.Error(w, "widget is not in this dashboard", 400)
			return
		}
		dashboard.Widgets = filtered
	}
	goalsByWidget := map[int64][]DashboardGoalProgress{}
	goalErrors := map[int64]string{}
	if body.IncludeGoals {
		goalsByWidget, goalErrors, err = dashboardGoalsForWidgets(plan, projectID, dashboard.Widgets)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	results := make([]dashboardWidgetResult, 0, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		data, err := evaluateWidget(plan, projectID, widget, body.Filters)
		result := dashboardWidgetResult{WidgetID: widget.ID, Data: data}
		if err != nil {
			result.Data = nil
			result.Error = err.Error()
		} else if body.IncludeGoals {
			if goals := goalsByWidget[widget.ID]; len(goals) > 0 {
				result.Data["goals"] = goals
			}
			if goalError := goalErrors[widget.ID]; goalError != "" {
				result.Data["goal_error"] = goalError
			}
		}
		results = append(results, result)
	}
	writeJSON(w, map[string]any{"dashboard_id": dashboard.ID, "widgets": results})
}

func (a *App) handleDashboardFilterOptions(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	var body struct {
		ProjectID string         `json:"project_id"`
		Filter    map[string]any `json:"filter"`
	}
	if r.Method == http.MethodGet {
		body.ProjectID = r.URL.Query().Get("project_id")
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filter")), &body.Filter); err != nil {
			http.Error(w, "invalid filter", http.StatusBadRequest)
			return
		}
	} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if _, err := assignRequestProject(body.ProjectID, projectID); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	options, err := dashboardFilterOptions(requestReadDB(r), projectID, body.Filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"options": options})
}

func (a *App) handleDashboardsList(w http.ResponseWriter, r *http.Request, projectID string) {
	rows, err := requestReadDB(r).Query(
		`SELECT id, project_id, name, description, COALESCE(config_json, '{}'), created_at, updated_at
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
		var cfg string
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Description, &cfg, &d.CreatedAt, &d.UpdatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg != "" {
			_ = json.Unmarshal([]byte(cfg), &d.Config)
		}
		d.Config = nonNilConfig(d.Config)
		out = append(out, d)
	}
	writeJSON(w, map[string]any{"dashboards": out})
}

func (a *App) handleDashboardsCreate(w http.ResponseWriter, r *http.Request, projectID string) {
	var body struct {
		ProjectID   string         `json:"project_id"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Template    string         `json:"template"`
		Config      map[string]any `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if _, err := assignRequestProject(body.ProjectID, projectID); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		if body.Template == "website_traffic" {
			body.Name = "Website Traffic"
		} else if body.Template == "patreon_overview" {
			body.Name = "Patreon Overview"
		} else {
			body.Name = "Analytics Dashboard"
		}
	}
	cfg := nonNilConfig(body.Config)
	if body.Template != "" && len(cfg) == 0 {
		cfg = templateDashboardConfig(body.Template)
	}
	d, err := createDashboard(requestWriteDB(r), projectID, body.Name, body.Description, cfg, templateWidgets(body.Template))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (a *App) handleDashboardGet(w http.ResponseWriter, r *http.Request, projectID string, id int64) {
	d, err := getDashboardForProject(requestReadDB(r), id, projectID)
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

func (a *App) handleDashboardUpdate(w http.ResponseWriter, r *http.Request, projectID string, id int64) {
	var body struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Config      map[string]any `json:"config"`
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
	cfg, _ := json.Marshal(nonNilConfig(body.Config))
	result, err := requestWriteDB(r).Exec(
		`UPDATE dashboards SET name=?, description=?, config_json=?, updated_at=? WHERE id=? AND project_id=?`,
		body.Name, body.Description, string(cfg), time.Now().UnixMilli(), id, projectID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		http.NotFound(w, r)
		return
	}
	a.handleDashboardGet(w, r, projectID, id)
}

func (a *App) handleDashboardDelete(w http.ResponseWriter, r *http.Request, projectID string, id int64) {
	result, err := requestWriteDB(r).Exec(`DELETE FROM dashboards WHERE id=? AND project_id=?`, id, projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleWidgetCreate(w http.ResponseWriter, r *http.Request, projectID string, dashboardID int64) {
	if _, err := getDashboardForProject(requestReadDB(r), dashboardID, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
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
	if err := validateDashboardGoalLinks(requestWriteDB(r), projectID, body.Config); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	widget, err := insertWidget(requestWriteDB(r), dashboardID, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, widget)
}

func (a *App) handleWidgetUpdate(w http.ResponseWriter, r *http.Request, projectID string, id int64) {
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
	if err := validateDashboardGoalLinks(requestWriteDB(r), projectID, body.Config); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, _ := json.Marshal(nonNilConfig(body.Config))
	result, err := requestWriteDB(r).Exec(
		`UPDATE dashboard_widgets SET type=?, title=?, position=?, config_json=?, updated_at=?
		 WHERE id=? AND dashboard_id IN (SELECT id FROM dashboards WHERE project_id=?)`,
		body.Type, body.Title, body.Position, string(cfg), time.Now().UnixMilli(), id, projectID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleWidgetDelete(w http.ResponseWriter, r *http.Request, projectID string, id int64) {
	result, err := requestWriteDB(r).Exec(
		`DELETE FROM dashboard_widgets WHERE id=? AND dashboard_id IN (SELECT id FROM dashboards WHERE project_id=?)`,
		id, projectID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func createDashboard(db sqlDatabase, projectID, name, description string, config map[string]any, widgets []DashboardWidget) (*Dashboard, error) {
	now := time.Now().UnixMilli()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cfg, _ := json.Marshal(nonNilConfig(config))
	res, err := tx.Exec(
		`INSERT INTO dashboards (project_id, name, description, config_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, name, description, string(cfg), now, now,
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

func getDashboard(db sqlRunner, id int64) (*Dashboard, error) {
	var d Dashboard
	var cfg string
	err := db.QueryRow(
		`SELECT id, project_id, name, description, COALESCE(config_json, '{}'), created_at, updated_at FROM dashboards WHERE id=?`,
		id,
	).Scan(&d.ID, &d.ProjectID, &d.Name, &d.Description, &cfg, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if cfg != "" {
		_ = json.Unmarshal([]byte(cfg), &d.Config)
	}
	d.Config = nonNilConfig(d.Config)
	widgets, err := listWidgets(db, id)
	if err != nil {
		return nil, err
	}
	d.Widgets = widgets
	return &d, nil
}

func getDashboardForProject(db sqlRunner, id int64, projectID string) (*Dashboard, error) {
	var found int
	if err := db.QueryRow(`SELECT 1 FROM dashboards WHERE id=? AND project_id=?`, id, projectID).Scan(&found); err != nil {
		return nil, err
	}
	return getDashboard(db, id)
}

func insertWidget(db sqlRunner, dashboardID int64, w DashboardWidget) (*DashboardWidget, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dashboard_widgets WHERE dashboard_id=?`, dashboardID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= 50 {
		return nil, errors.New("dashboard limit is 50 widgets")
	}
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

func listWidgets(db sqlRunner, dashboardID int64) ([]DashboardWidget, error) {
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

func evaluateWidget(db sqlRunner, projectID string, w DashboardWidget, selectedFilters map[string]any) (map[string]any, error) {
	if plan, ok := db.(*evaluationPlan); ok {
		key := widgetCacheKey(projectID, w, selectedFilters)
		if cached, found := plan.widgets[key]; found {
			return copyResult(cached.data), cached.err
		}
		data, err := evaluateWidgetUncached(db, projectID, w, selectedFilters)
		plan.widgets[key] = cachedWidget{copyResult(data), err}
		return data, err
	}
	return evaluateWidgetUncached(db, projectID, w, selectedFilters)
}
func evaluateWidgetUncached(db sqlRunner, projectID string, w DashboardWidget, selectedFilters map[string]any) (map[string]any, error) {
	cfg := resolveDashboardFilters(w.Config, selectedFilters)
	f := filterFromWidget(projectID, cfg)
	if plan, ok := db.(*evaluationPlan); ok {
		if ms := windowMillis(stringConfig(cfg, "window", "24h")); ms > 0 {
			f.Since = plan.now - ms
			f.Until = plan.now
		}
	}
	limit := intConfig(cfg, "limit", 10)
	switch w.Type {
	case "stat":
		aggregation, err := widgetAggregation(cfg)
		if err != nil {
			return nil, err
		}
		result, err := evaluateStatWidget(db, f, w.Type, cfg, aggregation)
		if err != nil {
			return nil, err
		}
		if err := addPreviousPeriodComparison(db, result, f, cfg, aggregation); err != nil {
			return nil, err
		}
		return result, nil
	case "timeseries":
		aggregation, err := widgetAggregation(cfg)
		if err != nil {
			return nil, err
		}
		if aggregation == "sum_money" {
			rows, metadata, err := moneySeriesForWidget(db, f, stringConfig(cfg, "interval", "minute"), cfg)
			if err != nil {
				return nil, err
			}
			metadata["type"] = w.Type
			metadata["series"] = rows
			return metadata, nil
		}
		rows, err := seriesForWidget(
			db,
			f,
			stringConfig(cfg, "interval", "minute"),
			stringConfig(cfg, "value", ""),
			stringConfig(cfg, "by", ""),
			aggregation,
		)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"type": w.Type, "series": rows, "aggregation": aggregation}
		if aggregation != "count" && aggregation != "distinct" {
			result["metric"] = aggregation
			result["field"] = stringConfig(cfg, "value", "")
		}
		return result, nil
	case "top", "breakdown":
		by := stringConfig(cfg, "by", "props.path")
		rows, err := topByPropsKey(db, f, by, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": w.Type, "top": rows, "by": by}, nil
	case "feed":
		rows, err := queryRows(db, f, intConfig(cfg, "limit", 25))
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": w.Type, "events": rows}, nil
	default:
		return nil, fmt.Errorf("unsupported widget type %q", w.Type)
	}
}

// addPreviousPeriodComparison enriches a stat without changing its primary
// aggregation. It is intentionally opt-in because counts, sums, snapshots, and
// distinct values can all use the same comparison, while dashboards that do
// not want it keep their existing payload unchanged.
func addPreviousPeriodComparison(db sqlRunner, result map[string]any, current Filter, cfg map[string]any, aggregation string) error {
	if result["value"] == nil {
		return nil
	}
	if stringConfig(cfg, "comparison", "") != "previous_period" {
		return nil
	}
	windowName := stringConfig(cfg, "window", "")
	period := windowMillis(windowName)
	if period <= 0 || current.Since <= 0 {
		return nil
	}
	previous := current
	previous.Until = current.Since
	previous.Since = current.Since - period
	previousResult, err := evaluateStatWidget(db, previous, "stat", cfg, aggregation)
	if err != nil {
		return err
	}
	currentCount, currentCountOK := numericMapValue(result["count"])
	previousCount, previousCountOK := numericMapValue(previousResult["count"])
	if !currentCountOK {
		currentCount, _ = numericMapValue(result["value"])
	}
	if !previousCountOK {
		previousCount, _ = numericMapValue(previousResult["value"])
	}
	if currentCount <= 0 || previousCount <= 0 {
		return nil
	}
	currentValue, currentOK := numericMapValue(result["value"])
	previousValue, previousOK := numericMapValue(previousResult["value"])
	if !currentOK || !previousOK {
		return nil
	}
	change := currentValue - previousValue
	comparison := map[string]any{
		"kind":           "previous_period",
		"window":         windowName,
		"previous_value": previousValue,
		"change":         change,
	}
	if previousValue != 0 {
		comparison["change_percent"] = change / previousValue * 100
	}
	result["comparison"] = comparison
	return nil
}

func numericMapValue(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func evaluateStatWidget(db sqlRunner, f Filter, widgetType string, cfg map[string]any, aggregation string) (map[string]any, error) {
	valueKey := stringConfig(cfg, "value", "")
	by := stringConfig(cfg, "by", "")
	if aggregation == "count" {
		n, err := countEvents(db, f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": widgetType, "value": n, "aggregation": aggregation}, nil
	}
	if aggregation == "distinct" {
		if by == "" {
			by = valueKey
		}
		if by == "" {
			return nil, fmt.Errorf("distinct aggregation requires by or value")
		}
		n, err := distinctCount(db, f, by)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": widgetType, "value": n, "aggregation": aggregation, "field": by}, nil
	}
	if aggregation == "sum_money" {
		result, err := moneyScalarForWidget(db, f, cfg)
		if err != nil {
			return nil, err
		}
		result["type"] = widgetType
		return result, nil
	}
	if valueKey == "" {
		return nil, fmt.Errorf("%s aggregation requires value", aggregation)
	}
	result, err := numericScalarForWidget(db, f, valueKey, aggregation)
	if err != nil {
		return nil, err
	}
	result["type"] = widgetType
	return result, nil
}

// widgetAggregation makes aggregation explicit without changing saved v0.9
// dashboards: value implied sum, by implied distinct, and a bare widget
// implied event count.
func widgetAggregation(cfg map[string]any) (string, error) {
	aggregation := strings.ToLower(strings.TrimSpace(stringConfig(cfg, "aggregation", "")))
	if aggregation == "avg" {
		aggregation = "average"
	}
	if aggregation == "" {
		if stringConfig(cfg, "by", "") != "" && stringConfig(cfg, "value", "") == "" {
			return "distinct", nil
		}
		if stringConfig(cfg, "value", "") != "" {
			return "sum", nil
		}
		return "count", nil
	}
	switch aggregation {
	case "count", "distinct", "sum", "sum_money", "average", "min", "max", "latest", "change":
		return aggregation, nil
	default:
		return "", fmt.Errorf("unsupported aggregation %q", aggregation)
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
		f.Until = time.Now().UnixMilli() + 1
		f.Since = f.Until - window
	}
	if raw, ok := cfg["where"].(map[string]any); ok {
		f.Where = raw
	}
	return f
}

func seriesForWidget(db sqlRunner, f Filter, interval, valueKey, by, aggregation string) ([]map[string]any, error) {
	start, end, step, err := seriesGrid(db, f, interval)
	if err != nil {
		return nil, err
	}
	rows, err := seriesForWidgetRaw(db, f, fmt.Sprintf("grid:%d", step), valueKey, by, aggregation)
	if err != nil {
		return nil, err
	}
	return fillSeries(rows, start, end, step, aggregation), nil
}
func seriesForWidgetRaw(db sqlRunner, f Filter, interval, valueKey, by, aggregation string) ([]map[string]any, error) {
	where, args, filterErr := f.buildWhere()
	if filterErr != nil {
		return nil, filterErr
	}
	bucketExpr := dashboardBucketExpr(interval)
	if aggregation == "latest" || aggregation == "change" {
		return orderedSeriesForWidget(db, f, bucketExpr, valueKey, aggregation)
	}
	valueExpr := ""
	numericPredicate := ""
	selectValue := "COUNT(*) AS count"
	if aggregation == "distinct" {
		field := by
		if field == "" {
			field = valueKey
		}
		var ok bool
		valueExpr, ok = dashboardGroupExpr(field)
		if !ok || field == "" {
			return nil, fmt.Errorf("distinct aggregation requires a valid by or value field")
		}
		selectValue = "COUNT(DISTINCT " + valueExpr + ") AS value, COUNT(*) AS count"
	} else if aggregation != "count" {
		var ok bool
		valueExpr, numericPredicate, ok = numericValueExtract(valueKey)
		if !ok || valueKey == "" {
			return nil, fmt.Errorf("%s aggregation requires a numeric event field or props.X", aggregation)
		}
		sqlAggregation := map[string]string{"sum": "SUM", "average": "AVG", "min": "MIN", "max": "MAX"}[aggregation]
		selectValue = sqlAggregation + "(CASE WHEN " + numericPredicate + " THEN CAST(" + valueExpr + " AS REAL) END) AS value, COUNT(*) AS count, SUM(CASE WHEN " + numericPredicate + " THEN 0 ELSE 1 END) AS invalid_count"
	}
	q := "SELECT " + bucketExpr + " AS bucket, " + selectValue + " FROM events"
	if where != "" {
		q += " WHERE " + where
		if valueExpr != "" && aggregation != "count" {
			q += " AND " + valueExpr + " IS NOT NULL"
		}
	} else if valueExpr != "" && aggregation != "count" {
		q += " WHERE " + valueExpr + " IS NOT NULL"
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
		if aggregation == "distinct" {
			var value int64
			if err := rows.Scan(&bucket, &value, &count); err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"bucket": bucket, "count": count, "value": value, "aggregation": aggregation})
		} else if aggregation != "count" {
			var value sql.NullFloat64
			var invalid int64
			if err := rows.Scan(&bucket, &value, &count, &invalid); err != nil {
				return nil, err
			}
			if invalid > 0 {
				return nil, fmt.Errorf("value %q contains %d non-numeric row(s) in bucket %s", valueKey, invalid, bucket)
			}
			row := map[string]any{"bucket": bucket, "count": count, "value": 0.0, "aggregation": aggregation}
			if value.Valid {
				row["value"] = value.Float64
			}
			out = append(out, row)
		} else {
			if err := rows.Scan(&bucket, &count); err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"bucket": bucket, "count": count})
		}
	}
	return out, rows.Err()
}

func dashboardBucketExpr(interval string) string {
	if step, ok := parseGridInterval(interval); ok {
		return gridBucketExpr(step)
	}
	switch interval {
	case "hour":
		return "strftime('%Y-%m-%dT%H:00:00Z', ts / 1000, 'unixepoch')"
	case "day":
		return "strftime('%Y-%m-%d', ts / 1000, 'unixepoch')"
	default:
		return "strftime('%Y-%m-%dT%H:%M:00Z', ts / 1000, 'unixepoch')"
	}
}

func numericScalarForWidget(db sqlRunner, f Filter, valueKey, aggregation string) (map[string]any, error) {
	expr, numericPredicate, ok := numericValueExtract(valueKey)
	if !ok {
		return nil, fmt.Errorf("value must be a numeric event field or props.X")
	}
	where, args, filterErr := f.buildWhere()
	if filterErr != nil {
		return nil, filterErr
	}
	valueWhere := expr + " IS NOT NULL"
	if where != "" {
		valueWhere = where + " AND " + valueWhere
	}
	if aggregation == "latest" || aggregation == "change" {
		return orderedScalarForWidget(db, expr, numericPredicate, valueWhere, args, valueKey, aggregation)
	}
	sqlAggregation := map[string]string{"sum": "SUM", "average": "AVG", "min": "MIN", "max": "MAX"}[aggregation]
	if sqlAggregation == "" {
		return nil, fmt.Errorf("unsupported numeric aggregation %q", aggregation)
	}
	q := "SELECT " + sqlAggregation + "(CASE WHEN " + numericPredicate + " THEN CAST(" + expr + " AS REAL) END), COUNT(*), COALESCE(SUM(CASE WHEN " + numericPredicate + " THEN 0 ELSE 1 END), 0) FROM events WHERE " + valueWhere
	var value sql.NullFloat64
	var count int64
	var invalid int64
	if err := db.QueryRow(q, args...).Scan(&value, &count, &invalid); err != nil {
		return nil, err
	}
	if invalid > 0 {
		return nil, fmt.Errorf("value %q contains %d non-numeric row(s)", valueKey, invalid)
	}
	var resultValue any = float64(0)
	if !value.Valid && aggregation != "sum" {
		resultValue = nil
	}
	if value.Valid && (math.IsNaN(value.Float64) || math.IsInf(value.Float64, 0)) {
		return nil, errors.New("numeric aggregate overflow")
	}
	if value.Valid {
		resultValue = value.Float64
	}
	return map[string]any{"value": resultValue, "count": count, "aggregation": aggregation, "metric": aggregation, "field": valueKey}, nil
}

// sumScalarForWidget remains the shared strict-sum primitive used by v0.9
// objective targets as well as legacy dashboard callers.
func sumScalarForWidget(db sqlRunner, f Filter, valueKey string) (float64, int64, error) {
	result, err := numericScalarForWidget(db, f, valueKey, "sum")
	if err != nil {
		return 0, 0, err
	}
	value, _ := result["value"].(float64)
	count, _ := result["count"].(int64)
	return value, count, nil
}

func orderedScalarForWidget(db sqlRunner, expr, numericPredicate, where string, args []any, valueKey, aggregation string) (map[string]any, error) {
	var invalid int64
	if err := db.QueryRow("SELECT COALESCE(SUM(CASE WHEN "+numericPredicate+" THEN 0 ELSE 1 END), 0) FROM events WHERE "+where, args...).Scan(&invalid); err != nil {
		return nil, err
	}
	if invalid > 0 {
		return nil, fmt.Errorf("value %q contains %d non-numeric row(s)", valueKey, invalid)
	}
	qBase := "SELECT CAST(" + expr + " AS REAL) FROM events WHERE " + where
	var first, latest sql.NullFloat64
	if err := db.QueryRow(qBase+" ORDER BY ts, id LIMIT 1", args...).Scan(&first); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err := db.QueryRow(qBase+" ORDER BY ts DESC, id DESC LIMIT 1", args...).Scan(&latest); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE "+where, args...).Scan(&count); err != nil {
		return nil, err
	}
	latestValue := 0.0
	if latest.Valid {
		latestValue = latest.Float64
	}
	result := map[string]any{"value": latestValue, "count": count, "aggregation": aggregation, "metric": aggregation, "field": valueKey}
	if aggregation == "change" {
		previous := 0.0
		if first.Valid {
			previous = first.Float64
		}
		change := latestValue - previous
		result["value"] = change
		result["previous"] = previous
		result["current"] = latestValue
		result["change"] = change
		if first.Valid && previous != 0 {
			result["change_percent"] = change / previous * 100
		}
	}
	if count == 0 || (aggregation == "change" && count < 2) {
		result["value"] = nil
		result["status"] = "no_data"
	}
	return result, nil
}

func orderedSeriesForWidget(db sqlRunner, f Filter, bucketExpr, valueKey, aggregation string) ([]map[string]any, error) {
	expr, numericPredicate, ok := numericValueExtract(valueKey)
	if !ok || valueKey == "" {
		return nil, fmt.Errorf("%s aggregation requires a numeric event field or props.X", aggregation)
	}
	where, args, filterErr := f.buildWhere()
	if filterErr != nil {
		return nil, filterErr
	}
	if where != "" {
		where += " AND " + expr + " IS NOT NULL"
	} else {
		where = expr + " IS NOT NULL"
	}
	q := "SELECT " + bucketExpr + " AS bucket, CAST(" + expr + " AS REAL), CASE WHEN " + numericPredicate + " THEN 1 ELSE 0 END, ts, id FROM events WHERE " + where + " ORDER BY bucket, ts, id"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type bucketValues struct {
		bucket      string
		first, last float64
		count       int64
	}
	var current bucketValues
	var out []map[string]any
	flush := func() {
		if current.count == 0 {
			return
		}
		value := current.last
		row := map[string]any{"bucket": current.bucket, "count": current.count, "value": value, "aggregation": aggregation}
		if aggregation == "change" {
			value = current.last - current.first
			row["value"] = value
			row["previous"] = current.first
			row["current"] = current.last
			row["change"] = value
			if current.first != 0 {
				row["change_percent"] = value / current.first * 100
			}
		}
		out = append(out, row)
	}
	for rows.Next() {
		var bucket string
		var value float64
		var numeric int
		var ts, id int64
		if err := rows.Scan(&bucket, &value, &numeric, &ts, &id); err != nil {
			return nil, err
		}
		if numeric == 0 {
			return nil, fmt.Errorf("value %q contains a non-numeric row in bucket %s", valueKey, bucket)
		}
		if current.count > 0 && bucket != current.bucket {
			flush()
			current = bucketValues{}
		}
		if current.count == 0 {
			current.bucket = bucket
			current.first = value
		}
		current.last = value
		current.count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

func distinctCount(db sqlRunner, f Filter, by string) (int64, error) {
	expr, ok := dashboardGroupExpr(by)
	if !ok {
		return 0, fmt.Errorf("by must be session_id, user_id, app, topic, source or props.X")
	}
	where, args, filterErr := f.buildWhere()
	if filterErr != nil {
		return 0, filterErr
	}
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

func resolveDashboardFilters(cfg map[string]any, selected map[string]any) map[string]any {
	if len(cfg) == 0 {
		return map[string]any{}
	}
	b, _ := json.Marshal(cfg)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	out = nonNilConfig(out)
	for k, v := range out {
		if s, ok := v.(string); ok {
			if resolved, found := filterPlaceholderValue(s, selected); found {
				if k != "window" && isEmptyDashboardFilterValue(resolved) {
					delete(out, k)
				} else {
					out[k] = unwrapFilterValue(resolved)
				}
			}
		}
	}
	if raw, ok := out["where"].(map[string]any); ok {
		where := map[string]any{}
		for k, v := range raw {
			if s, ok := v.(string); ok {
				resolved, found := filterPlaceholderValue(s, selected)
				if found {
					if !isEmptyDashboardFilterValue(resolved) {
						where[k] = unwrapFilterValue(resolved)
					}
					continue
				}
			}
			where[k] = v
		}
		if len(where) == 0 {
			delete(out, "where")
		} else {
			out["where"] = where
		}
	}
	return out
}

func filterPlaceholderValue(s string, selected map[string]any) (any, bool) {
	const prefix = "$filters."
	if !strings.HasPrefix(s, prefix) {
		return nil, false
	}
	if selected == nil {
		return nil, true
	}
	value, ok := selected[strings.TrimPrefix(s, prefix)]
	if !ok {
		return nil, true
	}
	if wrapped, ok := value.(map[string]any); ok {
		if typed, found := wrapped["value"]; found {
			return typedFilterValue{typed}, true
		}
	}
	return value, true
}

func isEmptyDashboardFilterValue(v any) bool {
	if v == nil {
		return true
	}
	switch raw := v.(type) {
	case string:
		return raw == "" || raw == "all" // Legacy placeholder sentinel; typed selections use a value wrapper.
	case []any:
		if len(raw) == 0 {
			return true
		}
		for _, item := range raw {
			if !isEmptyDashboardFilterValue(item) {
				return false
			}
		}
		return true
	case []string:
		if len(raw) == 0 {
			return true
		}
		for _, item := range raw {
			if item != "" && item != "all" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func dashboardFilterOptions(db sqlRunner, projectID string, filter map[string]any) ([]map[string]any, error) {
	source, ok := filter["source"].(map[string]any)
	if !ok {
		if options, ok := filter["options"].([]any); ok {
			out := make([]map[string]any, 0, len(options))
			for _, item := range options {
				switch v := item.(type) {
				case string:
					out = append(out, map[string]any{"value": v, "label": v})
				case map[string]any:
					out = append(out, v)
				}
			}
			return out, nil
		}
		return nil, fmt.Errorf("filter source required")
	}
	valueField := stringConfig(source, "value_field", "")
	if valueField == "" {
		return nil, fmt.Errorf("filter source value_field required")
	}
	valueExpr, ok := dashboardGroupExpr(valueField)
	if !ok {
		return nil, fmt.Errorf("filter value_field must be app, topic, source, project_id, session_id, user_id or props.X")
	}
	labelField := stringConfig(source, "label_field", "")
	labelExpr := valueExpr
	if labelField != "" {
		var labelOK bool
		labelExpr, labelOK = dashboardGroupExpr(labelField)
		if !labelOK {
			return nil, fmt.Errorf("filter label_field must be app, topic, source, project_id, session_id, user_id or props.X")
		}
	}
	f := Filter{
		App:       stringConfig(source, "app", ""),
		Topic:     stringConfig(source, "topic", ""),
		ProjectID: projectID,
		Source:    stringConfig(source, "source", ""),
	}
	if window := stringConfig(source, "window", ""); window != "" {
		if ms := windowMillis(window); ms > 0 {
			f.Since = time.Now().UnixMilli() - ms
		}
	}
	where, args, filterErr := f.buildWhere()
	if filterErr != nil {
		return nil, filterErr
	}
	typedExpr := "json_quote(" + valueExpr + ")"
	if path, ok := propsJSONPath(valueField); ok {
		typedExpr = "CASE json_type(props, '" + path + "') WHEN 'true' THEN 'true' WHEN 'false' THEN 'false' ELSE json_quote(" + valueExpr + ") END"
	}
	presentPredicate := valueExpr + " IS NOT NULL"
	if path, ok := propsJSONPath(valueField); ok {
		presentPredicate = "json_type(props, '" + path + "') IS NOT NULL"
	}
	q := "SELECT " + typedExpr + " AS value, MAX(CAST(" + labelExpr + " AS TEXT)) AS label, COUNT(*) AS count FROM events"
	if where != "" {
		q += " WHERE " + where + " AND " + presentPredicate
	} else {
		q += " WHERE " + presentPredicate
	}
	q += " GROUP BY value ORDER BY label COLLATE NOCASE LIMIT 200"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var value, label sql.NullString
		var count int64
		if err := rows.Scan(&value, &label, &count); err != nil {
			return nil, err
		}
		if !value.Valid || value.String == "" {
			continue
		}
		var typed any
		if err := json.Unmarshal([]byte(value.String), &typed); err != nil {
			return nil, err
		}
		display := fmt.Sprint(typed)
		if label.Valid && label.String != "" {
			display = label.String
		}
		out = append(out, map[string]any{"value": typed, "label": display, "count": count})
	}
	return out, rows.Err()
}

func templateWidgets(name string) []DashboardWidget {
	if name == "patreon_overview" {
		filtered := map[string]any{
			"props.page_id": "$filters.page_id",
		}
		return []DashboardWidget{
			{Type: "timeseries", Title: "Net Earnings", Config: map[string]any{"app": "patreon", "topic": "daily_earnings_snapshot", "window": "$filters.window", "interval": "day", "value": "props.net_earnings_total", "aggregation": "sum", "format": "currency", "currency": "USD", "where": map[string]any{"props.page_id": "$filters.page_id", "props.currency": "$filters.currency"}}},
			{Type: "timeseries", Title: "Paid Members", Config: map[string]any{"app": "patreon", "topic": "daily_membership_snapshot", "window": "$filters.window", "interval": "day", "value": "props.paid_members", "aggregation": "sum", "format": "number", "where": filtered}},
			{Type: "timeseries", Title: "Traffic Views", Config: map[string]any{"app": "patreon", "topic": "daily_traffic_snapshot", "window": "$filters.window", "interval": "day", "value": "props.total_views", "aggregation": "sum", "format": "number", "where": filtered}},
			{Type: "feed", Title: "Latest Patreon Snapshots", Config: map[string]any{"app": "patreon", "window": "$filters.window", "limit": 25, "where": filtered}},
		}
	}
	if name != "website_traffic" {
		return nil
	}
	return []DashboardWidget{
		{Type: "stat", Title: "Page Views Today", Config: map[string]any{"topic": "page_view", "window": "24h", "aggregation": "count", "format": "number"}},
		{Type: "stat", Title: "Active Sessions", Config: map[string]any{"topic": "page_view", "window": "5m", "by": "session_id", "aggregation": "distinct", "format": "number"}},
		{Type: "timeseries", Title: "Live Page Views", Config: map[string]any{"topic": "page_view", "window": "30m", "interval": "minute", "aggregation": "count", "format": "number"}},
		{Type: "top", Title: "Top Pages", Config: map[string]any{"topic": "page_view", "window": "24h", "by": "props.path", "limit": 8}},
		{Type: "breakdown", Title: "Devices", Config: map[string]any{"topic": "page_view", "window": "24h", "by": "props.device", "limit": 5}},
		{Type: "feed", Title: "Live Activity", Config: map[string]any{"topic": "page_view", "window": "30m", "limit": 20}},
	}
}

func templateDashboardConfig(name string) map[string]any {
	if name != "patreon_overview" {
		return map[string]any{}
	}
	return map[string]any{
		"filters": []map[string]any{
			{
				"key":     "page_id",
				"label":   "Page",
				"type":    "select",
				"default": "all",
				"source": map[string]any{
					"app":         "patreon",
					"topic":       "daily_traffic_snapshot",
					"value_field": "props.page_id",
					"label_field": "props.page_name",
				},
			},
			{
				"key":     "currency",
				"label":   "Currency",
				"type":    "select",
				"default": "all",
				"source": map[string]any{
					"app":         "patreon",
					"topic":       "daily_earnings_snapshot",
					"value_field": "props.currency",
				},
			},
			{
				"key":     "window",
				"label":   "Window",
				"type":    "date_window",
				"default": "90d",
				"options": []map[string]any{
					{"value": "7d", "label": "7d"},
					{"value": "30d", "label": "30d"},
					{"value": "90d", "label": "90d"},
					{"value": "all", "label": "All"},
				},
			},
		},
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

type typedFilterValue struct{ Value any }

func unwrapFilterValue(v any) any {
	if w, ok := v.(typedFilterValue); ok {
		return w.Value
	}
	return v
}
