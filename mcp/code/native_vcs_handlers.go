package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
)

func (a *App) httpRepoVersion(w http.ResponseWriter, r *http.Request, slug, action string) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := requireRepoSlug(globalCtx, pid, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	if a.native == nil {
		httpErr(w, http.StatusServiceUnavailable, "native version control is not initialized")
		return
	}
	n := a.native
	switch action {
	case "", "status":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		value, err := n.status(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, value)
	case "checkpoint":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Message string `json:"message"`
			Actor   string `json:"actor"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Message == "" {
			body.Message = "Checkpoint"
		}
		value, err := n.checkpoint(repo, body.Message, body.Actor)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"revision": value})
	case "history":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		values, err := n.history(repo, limit)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"revisions": values, "count": len(values)})
	case "branches":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		values, err := n.branches(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"branches": values, "count": len(values)})
	case "branches/create":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name          string `json:"name"`
			StartRevision string `json:"start_revision"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := n.createBranch(repo, body.Name, body.StartRevision); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"ok": true})
	case "switch":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := n.switchBranch(repo, body.Name); err != nil {
			httpErr(w, http.StatusConflict, err.Error())
			return
		}
		value, _ := n.status(repo)
		httpJSON(w, value)
	case "tags":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		values, err := n.tags(repo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"tags": values, "count": len(values)})
	case "tags/create":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name     string `json:"name"`
			Revision string `json:"revision"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := n.createTag(repo, body.Name, body.Revision); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"ok": true})
	case "restore":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Revision        string   `json:"revision"`
			Paths           []string `json:"paths"`
			WorkingTreeOnly bool     `json:"working_tree_only"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := n.restoreRevision(repo, body.Revision, body.Paths); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if body.WorkingTreeOnly {
			value, _ := n.status(repo)
			httpJSON(w, value)
			return
		}
		value, err := n.checkpoint(repo, "Restore "+body.Revision, httpActor(r))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"revision": value})
	case "export":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		revision := r.URL.Query().Get("revision")
		body, sha, err := n.exportRef(repo, revision)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(body) > 32<<20 {
			httpErr(w, http.StatusRequestEntityTooLarge, "native revision export exceeds 32 MiB")
			return
		}
		httpJSON(w, map[string]any{"revision": revision, "sha256": sha, "size": len(body), "zip_b64": base64.StdEncoding.EncodeToString(body)})
	default:
		httpErr(w, http.StatusNotFound, "no such native version-control action")
	}
}
