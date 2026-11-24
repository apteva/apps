package main

import (
	"net/http"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

// handleDesignV011 serves POST /api/designs/{id}/{copy|template-apply|lcsc-match}.
func (a *App) handleDesignV011(w http.ResponseWriter, r *http.Request, app *sdk.AppCtx, id int64, action string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	args := map[string]any{}
	if err := decodeBody(r, &args); err != nil {
		writeHTTPError(w, err)
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	var (
		value any
		err   error
	)
	switch action {
	case "copy":
		args["source_design_id"] = float64(id)
		value, err = a.toolDesignsCopy(r.Context(), app, args)
	case "template-apply":
		args["design_id"] = float64(id)
		value, err = a.toolTemplateApply(r.Context(), app, args)
	case "lcsc-match":
		args["design_id"] = float64(id)
		value, err = a.toolLCSCMatch(r.Context(), app, args)
	}
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

// handleTemplates serves GET /api/templates and POST /api/templates/{id}/sync.
func (a *App) handleTemplates(w http.ResponseWriter, r *http.Request) {
	app := a.requestCtx(r)
	if r.URL.Path == "/api/templates" {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		value, err := a.toolTemplatesList(r.Context(), app, nil)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, value)
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	var idText string
	if n, _ := sscanTemplateSync(r.URL.Path, &idText); n != 1 {
		writeJSONError(w, 404, "not found")
		return
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, 400, "invalid template id")
		return
	}
	args := map[string]any{}
	if err := decodeBody(r, &args); err != nil {
		writeHTTPError(w, err)
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	args["template_design_id"] = float64(id)
	value, err := a.toolTemplateSync(r.Context(), app, args)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func sscanTemplateSync(path string, id *string) (int, error) {
	const prefix, suffix = "/api/templates/", "/sync"
	if len(path) <= len(prefix)+len(suffix) || path[:len(prefix)] != prefix || path[len(path)-len(suffix):] != suffix {
		return 0, nil
	}
	*id = path[len(prefix) : len(path)-len(suffix)]
	return 1, nil
}

// handleLCSCSearch serves GET /api/lcsc/search?q=&package=&limit=.
func (a *App) handleLCSCSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	value, err := a.toolLCSCSearch(r.Context(), a.requestCtx(r), map[string]any{"query": q.Get("q"), "package": q.Get("package"), "limit": float64(limit)})
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

// handleI2CBusCheck serves POST /api/i2c/bus-check {design_ids}.
func (a *App) handleI2CBusCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	args := map[string]any{}
	if err := decodeBody(r, &args); err != nil {
		writeHTTPError(w, err)
		return
	}
	value, err := a.toolI2CBusCheck(r.Context(), a.requestCtx(r), args)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
