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
			ts, err := listTemplateSummaries(app.AppDB())
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
		ts, err := listTemplateSummaries(app.AppDB())
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"templates": ts})
	case http.MethodPost:
		var t Template
		if err := decodeJSONBody(w, r, &t); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := validateTemplatePayload(app, t.Body, t.Stylesheet); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
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
			if err := decodeJSONBody(w, r, &fields); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
				return
			}
			if settings, ok := fields["settings"]; ok {
				encoded, err := json.Marshal(settings)
				if err != nil {
					httpErr(w, http.StatusBadRequest, "settings must be a JSON object")
					return
				}
				delete(fields, "settings")
				fields["settings_json"] = string(encoded)
			}
			current, err := getTemplate(app.AppDB(), id, "")
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if current == nil {
				httpErr(w, http.StatusNotFound, "template not found")
				return
			}
			body := current.Body
			stylesheet := current.Stylesheet
			if value, ok := fields["body"].(string); ok {
				body = value
			}
			if value, ok := fields["stylesheet"].(string); ok {
				stylesheet = value
			}
			if err := validateTemplatePayload(app, body, stylesheet); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
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
			PageSize     string         `json:"page_size"`
		}
		if err := decodeJSONBody(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if body.Data == nil {
			httpErr(w, http.StatusBadRequest, "data must be a JSON object")
			return
		}
		args := map[string]any{
			"template_id":   id,
			"data":          body.Data,
			"output_name":   body.OutputName,
			"output_folder": body.OutputFolder,
			"page_size":     body.PageSize,
		}
		out, err := a.renderDocument(app, args, "dashboard")
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "preview":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Data         map[string]any `json:"data"`
			Body         string         `json:"body"` // optional inline override
			Stylesheet   string         `json:"stylesheet"`
			SourceFormat string         `json:"source_format"`
			Settings     map[string]any `json:"settings"`
			PageSize     string         `json:"page_size"`
		}
		if err := decodeJSONBody(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if body.Data == nil {
			httpErr(w, http.StatusBadRequest, "data must be a JSON object")
			return
		}
		args := map[string]any{
			"template_id":   id,
			"data":          body.Data,
			"body":          body.Body,
			"stylesheet":    body.Stylesheet,
			"source_format": body.SourceFormat,
			"settings":      body.Settings,
			"page_size":     body.PageSize,
		}
		out, err := a.previewDocument(app, args)
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
	offset, _ := strconv.Atoi(q.Get("offset"))
	rs, err := listRenders(app.AppDB(), RenderFilters{
		TemplateID: tID,
		Since:      q.Get("since"),
		Limit:      limit,
		Offset:     offset,
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

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON object")
		}
		return err
	}
	return nil
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
