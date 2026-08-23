package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) requestCtx(r *http.Request) *sdk.AppCtx {
	ctx := a.ctx
	project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	if project == "" {
		project = strings.TrimSpace(r.URL.Query().Get("project_id"))
	}
	if project != "" && ctx != nil {
		return ctx.WithProject(project)
	}
	return ctx
}
func (a *App) handleDesigns(w http.ResponseWriter, r *http.Request) {
	app := a.requestCtx(r)
	s, err := a.service(app)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := s.store.ListDesigns(s.project, r.URL.Query().Get("q"), r.URL.Query().Get("status"), limit)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"designs": items})
	case http.MethodPost:
		var args map[string]any
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		result, err := a.toolDesignCreate(r.Context(), app, args)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 201, result)
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}
func (a *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	app := a.requestCtx(r)
	s, err := a.service(app)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/designs/"))
	if len(parts) == 0 {
		writeJSONError(w, 404, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, 400, "invalid design id")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		d, err := s.store.GetDesign(s.project, id)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"design": d})
	case "archive":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var body struct {
			Archived *bool `json:"archived"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeHTTPError(w, err)
			return
		}
		archived := true
		if body.Archived != nil {
			archived = *body.Archived
		}
		d, err := s.store.ArchiveDesign(s.project, id, archived)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"design": d})
	case "revisions":
		if r.Method == http.MethodGet {
			items, err := s.store.ListRevisions(s.project, id)
			if err != nil {
				writeHTTPError(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"revisions": items})
			return
		}
		if r.Method == http.MethodPost {
			var args map[string]any
			if err := decodeBody(r, &args); err != nil {
				writeHTTPError(w, err)
				return
			}
			args["design_id"] = id
			result, err := a.toolRevisionCreate(r.Context(), app, args)
			if err != nil {
				writeHTTPError(w, err)
				return
			}
			writeJSON(w, 201, result)
			return
		}
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	case "operations":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var args map[string]any
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		args["design_id"] = id
		result, err := a.toolOperationsApply(r.Context(), app, args)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 201, result)
	case "validate":
		handleServiceArtifact(w, r, http.MethodPost, func() (any, error) { return s.Validate(id, bodyRevisionID(r)) })
	case "render":
		handleServiceArtifact(w, r, http.MethodPost, func() (any, error) { return s.Render(id, bodyRevisionID(r)) })
	case "bom":
		handleServiceArtifact(w, r, http.MethodPost, func() (any, error) { return s.BOM(id, bodyRevisionID(r)) })
	case "manufacturing":
		handleServiceArtifact(w, r, http.MethodPost, func() (any, error) { return s.Manufacturing(id, bodyRevisionID(r)) })
	case "release":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var body struct {
			RevisionID int64  `json:"revision_id"`
			Note       string `json:"note"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeHTTPError(w, err)
			return
		}
		artifact, err := s.Release(r.Context(), id, body.RevisionID, body.Note)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 201, map[string]any{"artifact": artifact})
	case "artifacts":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		revisionID, _ := strconv.ParseInt(r.URL.Query().Get("revision_id"), 10, 64)
		items, err := s.store.ListArtifacts(s.project, id, revisionID)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"artifacts": items})
	default:
		writeJSONError(w, 404, "not found")
	}
}

func handleServiceArtifact(w http.ResponseWriter, r *http.Request, method string, fn func() (any, error)) {
	if r.Method != method {
		writeMethodNotAllowed(w, method)
		return
	}
	value, err := fn()
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func bodyRevisionID(r *http.Request) int64 {
	var body struct {
		RevisionID int64 `json:"revision_id"`
	}
	_ = decodeBody(r, &body)
	return body.RevisionID
}
func (a *App) handleRevision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	s, err := a.service(a.requestCtx(r))
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/revisions/"), "/"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, 400, "invalid revision id")
		return
	}
	revision, err := s.store.GetRevision(s.project, id)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": revision})
}
func (a *App) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	s, err := a.service(a.requestCtx(r))
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"))
	if len(parts) != 2 || parts[1] != "content" {
		writeJSONError(w, 404, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, 400, "invalid artifact id")
		return
	}
	artifact, err := s.store.GetArtifact(s.project, id)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	file, err := os.Open(artifact.LocalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSONError(w, 410, "local artifact is unavailable; use its Storage file id")
		} else {
			writeHTTPError(w, err)
		}
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(artifact.Name)))
	w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
	http.ServeContent(w, r, artifact.Name, info.ModTime(), file)
}
func (a *App) handleExamples(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, 200, pcbExamples())
}
func (a *App) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	result, err := a.toolProvidersStatus(r.Context(), a.requestCtx(r), nil)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func splitPath(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}
func decodeBody(r *http.Request, out any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func writeHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeJSONError(w, 404, err.Error())
	case errors.Is(err, errRevisionConflict):
		writeJSONError(w, 409, err.Error())
	default:
		writeJSONError(w, 400, err.Error())
	}
}
func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeJSONError(w, 405, "method not allowed")
}
