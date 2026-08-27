package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleGitImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		RemoteURL    string `json:"remote_url"`
		Ref          string `json:"ref"`
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		Description  string `json:"description"`
		Framework    string `json:"framework"`
		ConnectionID int64  `json:"connection_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	service, err := a.requireGit()
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	result, err := service.Import(globalCtx, GitImportInput{
		RemoteURL: body.RemoteURL, Ref: body.Ref, Name: body.Name, Slug: body.Slug,
		Description: body.Description, Framework: body.Framework, ProjectID: pid,
		ConnectionID: body.ConnectionID,
	})
	if err != nil {
		writeGitHTTPError(w, err)
		return
	}
	httpJSON(w, result)
}

func (a *App) handleGitConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	type connection struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		Default bool   `json:"default,omitempty"`
	}
	out := []connection{}
	for _, bound := range boundGitIntegrations(globalCtx) {
		item := connection{ID: bound.ConnectionID, Slug: bound.AppSlug, Default: bound.IsDefault}
		if platformConnection, err := globalCtx.PlatformAPI().GetConnection(bound.ConnectionID); err == nil && platformConnection != nil {
			item.Slug = platformConnection.AppSlug
			item.Name = platformConnection.Name
			item.Status = platformConnection.Status
		}
		out = append(out, item)
	}
	httpJSON(w, map[string]any{"connections": out})
}

func (a *App) httpRepoGit(w http.ResponseWriter, r *http.Request, slug, action string) {
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
	service, err := a.requireGit()
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	switch action {
	case "", "status":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		status, err := service.Status(globalCtx, repo)
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, status)
	case "connect":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			RemoteURL    string `json:"remote_url"`
			Branch       string `json:"branch"`
			ConnectionID int64  `json:"connection_id"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		result, err := service.Connect(globalCtx, repo, GitConnectInput{RemoteURL: body.RemoteURL, Branch: body.Branch, ConnectionID: body.ConnectionID})
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, result)
	case "fetch", "pull", "push":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Actor       string `json:"actor"`
			SetUpstream bool   `json:"set_upstream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var status *GitStatus
		switch action {
		case "fetch":
			status, err = service.Fetch(globalCtx, repo, body.Actor)
		case "pull":
			status, err = service.Pull(globalCtx, repo, body.Actor)
		case "push":
			status, err = service.Push(globalCtx, repo, body.Actor, body.SetUpstream)
		}
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, status)
	case "commit":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Message     string   `json:"message"`
			Paths       []string `json:"paths"`
			AuthorName  string   `json:"author_name"`
			AuthorEmail string   `json:"author_email"`
			Actor       string   `json:"actor"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		status, err := service.Commit(globalCtx, repo, body.Message, body.Paths, body.AuthorName, body.AuthorEmail, body.Actor)
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, status)
	case "diff":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		maxBytes, _ := strconv.Atoi(r.URL.Query().Get("max_bytes"))
		diff, truncated, err := service.Diff(globalCtx, repo, r.URL.Query().Get("base"), maxBytes)
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, map[string]any{"diff": diff, "truncated": truncated})
	case "log":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		commits, err := service.Log(globalCtx, repo, limit)
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, map[string]any{"commits": commits, "count": len(commits)})
	case "branches":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		branches, err := service.Branches(globalCtx, repo)
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, map[string]any{"branches": branches, "count": len(branches)})
	case "branches/create":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name       string `json:"name"`
			StartPoint string `json:"start_point"`
			Actor      string `json:"actor"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		status, err := service.CreateBranch(globalCtx, repo, body.Name, body.StartPoint, body.Actor)
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, status)
	case "switch":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Name  string `json:"name"`
			Actor string `json:"actor"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		status, err := service.Switch(globalCtx, repo, body.Name, body.Actor)
		if err != nil {
			writeGitHTTPError(w, err)
			return
		}
		httpJSON(w, status)
	default:
		httpErr(w, http.StatusNotFound, "no such Git action")
	}
}

func writeGitHTTPError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	status := http.StatusBadRequest
	message := err.Error()
	switch {
	case strings.Contains(message, "not Git-backed"):
		status = http.StatusConflict
	case strings.Contains(message, "uncommitted"), strings.Contains(message, "conflict"), strings.Contains(message, "already Git-backed"):
		status = http.StatusConflict
	case strings.Contains(message, "not bound"), strings.Contains(message, "credentials"):
		status = http.StatusFailedDependency
	}
	httpErr(w, status, message)
}
