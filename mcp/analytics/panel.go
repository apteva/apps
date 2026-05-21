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
// counterpart to filterFromArgs. `where` is a JSON-encoded object of
// "props.X" → value (equality), URL-encoded by the panel; malformed
// JSON is ignored rather than erroring the whole request.
func filterFromQuery(r *http.Request) Filter {
	q := r.URL.Query()
	f := Filter{
		App:       q.Get("app"),
		Topic:     q.Get("topic"),
		ProjectID: q.Get("project_id"),
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

// GET /events — recent raw rows within the filters, newest first. Backs
// the panel's live event feed.
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rows, err := queryRows(globalCtx.AppDB(), filterFromQuery(r), queryLimit(r, 50, 500))
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
	apps, topics, err := distinctDimensions(globalCtx.AppDB())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"apps": apps, "topics": topics})
}
