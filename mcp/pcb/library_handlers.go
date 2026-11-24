package main

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// handleLibrary serves GET /api/library?q=&category=&limit=.
func (a *App) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	args := map[string]any{"query": r.URL.Query().Get("q"), "category": r.URL.Query().Get("category")}
	if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 {
		args["limit"] = float64(limit)
	}
	result, err := a.toolLibraryList(r.Context(), a.requestCtx(r), args)
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// handleLibraryItem serves GET /api/library/{part_id} and
// POST /api/library/import-kicad (body {content, name?, ...}).
func (a *App) handleLibraryItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/library/"), "/")
	app := a.requestCtx(r)
	if rest == "import-kicad" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		args := map[string]any{}
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		result, err := a.toolFootprintImportKiCad(r.Context(), app, args)
		if err != nil {
			writeLibraryError(w, err)
			return
		}
		writeJSON(w, 201, result)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	partID, err := url.PathUnescape(rest)
	if err != nil || partID == "" {
		writeJSONError(w, 404, "not found")
		return
	}
	result, err := a.toolLibraryGet(r.Context(), app, map[string]any{"part_id": partID})
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// handleDesignLibraryPlace serves POST /api/designs/{id}/library-place with
// the same arguments as pcb_component_place_from_library.
func (a *App) handleDesignLibraryPlace(w http.ResponseWriter, r *http.Request, app *sdk.AppCtx, id int64) {
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
	args["design_id"] = float64(id)
	result, err := a.toolComponentPlaceFromLibrary(r.Context(), app, args)
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, 201, result)
}

func writeLibraryError(w http.ResponseWriter, err error) {
	if errors.Is(err, errLibraryPartNotFound) {
		writeJSONError(w, 404, err.Error())
		return
	}
	writeHTTPError(w, err)
}
