package main

// HTTP routes backing the Analytics dashboard panel. Read-only views
// over the events table: headline counts (/summary), a daily time
// series (/series), and top-N values for a props key (/top). The panel
// (ui/AnalyticsPanel.mjs) is the only caller — agents use the MCP tools.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// globalCtx is captured in OnMount so HTTP handlers — which receive only
// the *http.Request — can reach the app DB. Same pattern as the health app.
var globalCtx *sdk.AppCtx

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// filterFromQuery builds a Filter from URL query params — the HTTP
// counterpart to filterFromArgs. `where` is a JSON-encoded object of
// "props.X" → value (equality), URL-encoded by the panel; malformed
// JSON is ignored rather than erroring the whole request.
func filterFromQuery(r *http.Request) Filter {
	q := r.URL.Query()
	f := Filter{
		App:       q.Get("app"),
		Topic:     q.Get("topic"),
		ProjectID: q.Get("project_id"),
		Source:    q.Get("source"),
		Since:     parseInt64(q.Get("since")),
		Until:     parseInt64(q.Get("until")),
	}
	if ws := q.Get("where"); ws != "" {
		var w map[string]any
		if json.Unmarshal([]byte(ws), &w) == nil {
			f.Where = w
		}
	}
	return f
}

func scopedFilterFromRequest(r *http.Request) (Filter, error) {
	projectID, err := requestProjectID(r)
	if err != nil {
		return Filter{}, err
	}
	f := filterFromQuery(r)
	f.ProjectID = projectID
	return f, nil
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func queryLimit(r *http.Request, def, max int) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// GET /summary — headline counts + the topics list within the window.
func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	db := requestReadDB(r)
	f, err := scopedFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	ov, err := overview(db, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	topics, err := topicsWindowed(db, f, queryLimit(r, 50, 500))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ov["topics_list"] = topics
	writeJSON(w, ov)
}

// handleGlobalSummary is the read-only data path for the global Home widget.
// The platform supplies only projects visible to this install; a requested
// project selector is accepted only from that allowlist.
func (a *App) handleGlobalSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil || globalCtx.PlatformAPI() == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return
	}
	projects, err := globalCtx.PlatformAPI().ListProjects()
	if err != nil {
		http.Error(w, "unable to list projects", http.StatusInternalServerError)
		return
	}
	allowed := make(map[string]sdk.PlatformProject, len(projects))
	ids := make([]string, 0, len(projects))
	for _, project := range projects {
		id := strings.TrimSpace(project.ID)
		if id == "" {
			continue
		}
		allowed[id] = project
		ids = append(ids, id)
	}
	selected := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if selected != "" {
		if _, ok := allowed[selected]; !ok {
			http.Error(w, "project is not visible to this install", http.StatusForbidden)
			return
		}
		ids = []string{selected}
	}
	since := parseInt64(r.URL.Query().Get("since"))
	until := parseInt64(r.URL.Query().Get("until"))
	whereArgs := make([]any, 0, len(ids)+2)
	where := ""
	if len(ids) > 0 {
		marks := make([]string, len(ids))
		for i, id := range ids {
			marks[i] = "?"
			whereArgs = append(whereArgs, id)
		}
		where = "project_id IN (" + strings.Join(marks, ",") + ")"
	}
	if since > 0 {
		where = addSQLCondition(where, "ts >= ?")
		whereArgs = append(whereArgs, since)
	}
	if until > 0 {
		where = addSQLCondition(where, "ts < ?")
		whereArgs = append(whereArgs, until)
	}
	if where == "" {
		where = "1 = 0"
	}
	db := requestReadDB(r)
	var total, apps, topics int64
	query := "SELECT COUNT(*), COUNT(DISTINCT app), COUNT(DISTINCT app || char(31) || topic) FROM events WHERE " + where
	if err := db.QueryRow(query, whereArgs...).Scan(&total, &apps, &topics); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	topicRows, err := globalTopics(db, where, whereArgs, queryLimit(r, 6, 50))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projectRows := make([]map[string]string, 0, len(projects))
	for _, project := range projects {
		if _, ok := allowed[project.ID]; ok {
			projectRows = append(projectRows, map[string]string{"id": project.ID, "name": project.Name})
		}
	}
	writeJSON(w, map[string]any{"total": total, "apps": apps, "topics": topics, "topics_list": topicRows, "projects": projectRows, "selected_project_id": selected})
}

func addSQLCondition(where, condition string) string {
	if where == "" {
		return condition
	}
	return where + " AND " + condition
}

func globalTopics(db sqlRunner, where string, args []any, limit int) ([]map[string]any, error) {
	query := "SELECT app, topic, MAX(ts), COUNT(*) FROM events WHERE " + where + " GROUP BY app, topic ORDER BY COUNT(*) DESC LIMIT ?"
	queryArgs := append(append([]any(nil), args...), limit)
	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var app, topic string
		var lastTS, count int64
		if err := rows.Scan(&app, &topic, &lastTS, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"app": app, "topic": topic, "last_ts": lastTS, "count": count})
	}
	return out, rows.Err()
}

// GET /series — event counts bucketed by UTC day within the window.
func (a *App) handleSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	f, err := scopedFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	series, err := dailySeries(requestReadDB(r), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"series": series})
}

// GET /top?by=props.X — top-N values for a props key within the window.
func (a *App) handleTop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "props.platform"
	}
	f, err := scopedFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	rows, err := topByPropsKey(requestReadDB(r), f, by, queryLimit(r, 10, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"top": rows, "by": by})
}

// GET /feed — recent raw rows within the filters, newest first. Backs
// the panel's live event feed. (Not /events — that path is reserved by
// the app-sdk for platform event ingestion.)
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	f, err := scopedFilterFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	rows, err := queryRows(requestReadDB(r), f, queryLimit(r, 50, 500))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": rows})
}

// GET /dimensions — distinct apps + topics across the whole store, for
// the panel's filter dropdowns. Unfiltered on purpose so the option set
// stays stable as the operator narrows other filters.
func (a *App) handleDimensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	projectID, ok := requireRequestProject(w, r)
	if !ok {
		return
	}
	apps, topics, err := distinctDimensions(requestReadDB(r), projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"apps": apps, "topics": topics})
}
