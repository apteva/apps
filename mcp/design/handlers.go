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
)

func (a *App) handleDesigns(w http.ResponseWriter, r *http.Request) {
	appCtx := a.ctx
	if project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); project != "" && appCtx != nil {
		appCtx = appCtx.WithProject(project)
	}
	service, err := a.service(appCtx)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := service.store.ListDesigns(service.project, r.URL.Query().Get("q"), r.URL.Query().Get("kind"), r.URL.Query().Get("status"), limit)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"designs": items})
	case http.MethodPost:
		var args map[string]any
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		result, err := a.toolDesignCreate(r.Context(), appCtx, args)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (a *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	appCtx := a.ctx
	if project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); project != "" && appCtx != nil {
		appCtx = appCtx.WithProject(project)
	}
	service, err := a.service(appCtx)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/designs/"))
	if len(parts) == 0 {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid design id")
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
		design, err := service.store.GetDesign(service.project, id)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"design": design})
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
		design, err := service.store.ArchiveDesign(service.project, id, archived)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"design": design})
	case "revisions":
		switch r.Method {
		case http.MethodGet:
			revisions, err := service.store.ListRevisions(service.project, id)
			if err != nil {
				writeHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"revisions": revisions})
		case http.MethodPost:
			var args map[string]any
			if err := decodeBody(r, &args); err != nil {
				writeHTTPError(w, err)
				return
			}
			args["design_id"] = id
			result, err := a.toolRevisionCreate(r.Context(), appCtx, args)
			if err != nil {
				writeHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, result)
		default:
			writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
	case "build":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var body struct {
			RevisionID int64    `json:"revision_id"`
			Formats    []string `json:"formats"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeHTTPError(w, err)
			return
		}
		result, err := service.Build(r.Context(), id, body.RevisionID, body.Formats)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "package":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var body struct {
			RevisionID int64 `json:"revision_id"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeHTTPError(w, err)
			return
		}
		artifact, err := service.ManufacturingPackage(r.Context(), id, body.RevisionID)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"artifact": artifact})
	case "artifacts":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		revisionID, _ := strconv.ParseInt(r.URL.Query().Get("revision_id"), 10, 64)
		artifacts, err := service.store.ListArtifacts(service.project, id, revisionID)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifacts": artifacts})
	default:
		writeJSONError(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleRevision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	appCtx := a.ctx
	if project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); project != "" && appCtx != nil {
		appCtx = appCtx.WithProject(project)
	}
	service, err := a.service(appCtx)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/revisions/"), "/"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid revision id")
		return
	}
	revision, err := service.store.GetRevision(service.project, id)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func (a *App) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	appCtx := a.ctx
	if project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); project != "" && appCtx != nil {
		appCtx = appCtx.WithProject(project)
	}
	service, err := a.service(appCtx)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"))
	if len(parts) != 2 || parts[1] != "content" {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid artifact id")
		return
	}
	artifact, err := service.store.GetArtifact(id)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if _, err := service.store.GetDesign(service.project, artifact.DesignID); err != nil {
		writeHTTPError(w, errNotFound)
		return
	}
	file, err := os.Open(artifact.LocalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSONError(w, http.StatusGone, "local artifact is unavailable; use its Storage file id")
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
	writeJSON(w, http.StatusOK, designExamples())
}

func splitPath(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

func decodeBody(r *http.Request, output any) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
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
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errRevisionConflict):
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	}
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}
