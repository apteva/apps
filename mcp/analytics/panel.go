package main

// HTTP routes backing the Analytics dashboard panel. Read-only views
// over the events table: headline counts (/summary), a daily time
// series (/series), and top-N values for a props key (/top). The panel
// (ui/AnalyticsPanel.mjs) is the only caller — agents use the MCP tools.

import (
	"encoding/json"
	"net/http"
	"strconv"

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
// counterpart to filterFromArgs. Only the fields the panel sends.
func filterFromQuery(r *http.Request) Filter {
	q := r.URL.Query()
	return Filter{
		App:       q.Get("app"),
		Topic:     q.Get("topic"),
		ProjectID: q.Get("project_id"),
		Since:     parseInt64(q.Get("since")),
		Until:     parseInt64(q.Get("until")),
	}
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
	db := globalCtx.AppDB()
	f := filterFromQuery(r)
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

// GET /series — event counts bucketed by UTC day within the window.
func (a *App) handleSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	series, err := dailySeries(globalCtx.AppDB(), filterFromQuery(r))
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
	rows, err := topByPropsKey(globalCtx.AppDB(), filterFromQuery(r), by, queryLimit(r, 10, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"top": rows, "by": by})
}
