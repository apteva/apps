package main

import (
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleHTTPReferenceData(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		httpErr(w, http.StatusServiceUnavailable, "reference data unavailable")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/reference/"), "/")
	switch {
	case path == "status" && r.Method == http.MethodGet:
		httpJSON(w, 200, referenceDataStatus(globalCtx.AppDB()))
	case path == "securities" && r.Method == http.MethodGet:
		rows, err := dbListSecurities(globalCtx.AppDB(), r.URL.Query().Get("q"), r.URL.Query().Get("as_of"), queryLimit(r, 250))
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"securities": rows})
	case path == "corporate-actions" && r.Method == http.MethodGet:
		rows, err := dbListCorporateActions(globalCtx.AppDB(), r.URL.Query().Get("symbol"), r.URL.Query().Get("type"), r.URL.Query().Get("since"), r.URL.Query().Get("until"), queryLimit(r, 200))
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"corporate_actions": rows})
	case path == "sessions" && r.Method == http.MethodGet:
		rows, err := dbListExchangeSessions(globalCtx.AppDB(), r.URL.Query().Get("venue"), r.URL.Query().Get("start"), r.URL.Query().Get("end"), queryLimit(r, 500))
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"sessions": rows})
	case path == "quality" && r.Method == http.MethodGet:
		rows, err := dbListReferenceIssues(globalCtx.AppDB(), r.URL.Query().Get("status"), queryLimit(r, 100))
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"issues": rows})
	case path == "postings" && r.Method == http.MethodGet:
		portfolioID, _ := strconv.ParseInt(r.URL.Query().Get("portfolio_id"), 10, 64)
		if portfolioID <= 0 {
			httpErr(w, 400, "portfolio_id required")
			return
		}
		pid, err := resolveProjectFromRequest(r)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		if _, err = dbGetPortfolio(globalCtx.AppDB(), pid, portfolioID); err != nil {
			httpErr(w, 404, "portfolio not found")
			return
		}
		rows, err := dbListCorporateActionPostings(globalCtx.AppDB(), portfolioID, queryLimit(r, 200))
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"postings": rows})
	case path == "sync" && r.Method == http.MethodPost:
		if err := syncReferenceData(r.Context(), globalCtx); err != nil {
			httpErr(w, 502, err.Error())
			return
		}
		httpJSON(w, 200, referenceDataStatus(globalCtx.AppDB()))
	default:
		httpErr(w, http.StatusNotFound, "reference-data endpoint not found")
	}
}

func queryLimit(r *http.Request, def int) int {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return def
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return def
	}
	return limit
}
