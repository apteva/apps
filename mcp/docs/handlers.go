package main

// HTTP handlers — the panel-facing twin of the MCP tools. Same
// behavior, plain HTTP envelope. Auth is the platform's session
// cookie (forwarded by authMiddleware → app proxy with bearer to
// our /mcp + HTTP routes).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// handleTemplatesCollection — GET /templates lists; POST /templates
// creates.
func (a *App) handleTemplatesCollection(w http.ResponseWriter, r *http.Request) {
	app := globalCtx
	switch r.Method {
	case http.MethodGet:
		// Format=picker returns the {items:[{id,label}]} shape the
		// dashboard's permission-picker expects (per the
		// list_endpoint convention in app-sdk's ResourceDecl).
		// Without the param, returns the full template list for
		// the panel.
		if r.URL.Query().Get("format") == "picker" {
			ts, err := listTemplates(app.AppDB())
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			items := make([]map[string]any, 0, len(ts))
			for _, t := range ts {
				items = append(items, map[string]any{
					"id":    strconv.FormatInt(t.ID, 10),
					"label": t.Name,
				})
			}
			httpJSON(w, map[string]any{"items": items})
			return
		}
		ts, err := listTemplates(app.AppDB())
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"templates": ts})
	case http.MethodPost:
		var t Template
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&t); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		id, err := createTemplate(app.AppDB(), &t)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				httpErr(w, http.StatusConflict, "slug already exists")
				return
			}
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		t.ID = id
		w.WriteHeader(http.StatusCreated)
		httpJSON(w, map[string]any{"template": t})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTemplatesItem — GET /templates/:id, PATCH /templates/:id,
// DELETE /templates/:id, POST /templates/:id/render or /preview.
func (a *App) handleTemplatesItem(w http.ResponseWriter, r *http.Request) {
	app := globalCtx
	rest := strings.TrimPrefix(r.URL.Path, "/templates/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch tail {
	case "":
		switch r.Method {
		case http.MethodGet:
			t, err := getTemplate(app.AppDB(), id, "")
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if t == nil {
				httpErr(w, http.StatusNotFound, "template not found")
				return
			}
			httpJSON(w, map[string]any{"template": t})
		case http.MethodPatch:
			var fields map[string]any
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&fields); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			if err := updateTemplate(app.AppDB(), id, fields); err != nil {
				if errors.Is(err, errNoRows()) {
					httpErr(w, http.StatusNotFound, "template not found")
					return
				}
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, map[string]any{"updated": true, "id": id})
		case http.MethodDelete:
			if err := deleteTemplate(app.AppDB(), id); err != nil {
				if errors.Is(err, errNoRows()) {
					httpErr(w, http.StatusNotFound, "template not found")
					return
				}
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, map[string]any{"deleted": true})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "render":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Data         map[string]any `json:"data"`
			OutputName   string         `json:"output_name"`
			OutputFolder string         `json:"output_folder"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		args := map[string]any{
			"template_id":   id,
			"data":          body.Data,
			"output_name":   body.OutputName,
			"output_folder": body.OutputFolder,
		}
		out, err := a.toolRender(app, args)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, out)
	case "preview":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Data map[string]any `json:"data"`
			Body string         `json:"body"` // optional inline override
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		args := map[string]any{
			"template_id": id,
			"data":        body.Data,
			"body":        body.Body,
		}
		out, err := a.toolPreview(app, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

// handleRendersCollection — GET /renders lists with filters.
func (a *App) handleRendersCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	app := globalCtx
	q := r.URL.Query()
	tID, _ := strconv.ParseInt(q.Get("template_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	rs, err := listRenders(app.AppDB(), RenderFilters{
		TemplateID: tID,
		Since:      q.Get("since"),
		Limit:      limit,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"renders": rs})
}

// handleRendersItem — GET /renders/:id.
func (a *App) handleRendersItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	app := globalCtx
	rest := strings.TrimPrefix(r.URL.Path, "/renders/")
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	rRow, err := getRender(app.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rRow == nil {
		httpErr(w, http.StatusNotFound, "render not found")
		return
	}
	httpJSON(w, map[string]any{"render": rRow})
}

// ─── helpers ──────────────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":` + jsonString(msg) + `}`))
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// errNoRows isolates the database/sql import dependency from this
// file's view (handlers.go doesn't import database/sql elsewhere).
func errNoRows() error {
	// store.go imports database/sql; reference its sentinel via
	// a small indirection.
	return errSqlNoRows
}
