package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Admin REST mirror — same handlers go through the same DB helpers as
// the MCP tools. The (future) panel + the agent both see one source of
// truth.

func (a *App) handleAdminCollection(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"_project_id": pid}
		for _, k := range []string{"status", "kind", "scheduled_at_after", "scheduled_at_before"} {
			if v := r.URL.Query().Get(k); v != "" {
				args[k] = v
			}
		}
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				args["limit"] = n
			}
		}
		out, err := a.toolList(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolCreate(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/webinars/")
	parts := strings.SplitN(rest, "/", 3)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	args := map[string]any{"_project_id": pid, "id": id}

	if len(parts) >= 2 {
		switch parts[1] {
		case "registrants":
			args["webinar_id"] = id
			delete(args, "id")
			out, err := a.toolListRegistrants(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "register":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST")
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body == nil {
				body = map[string]any{}
			}
			body["_project_id"] = pid
			body["webinar_id"] = id
			out, err := a.toolRegister(globalCtx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "engagement":
			out, err := a.toolGetEngagement(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "close":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST")
				return
			}
			out, err := a.toolClose(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "publish-replay":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST")
				return
			}
			out, err := a.toolPublishReplay(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "send-reminder":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST")
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body == nil {
				body = map[string]any{}
			}
			body["_project_id"] = pid
			body["id"] = id
			out, err := a.toolSendReminder(globalCtx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		out, err := a.toolGet(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPatch:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		args["patch"] = body
		out, err := a.toolUpdate(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodDelete:
		out, err := a.toolDelete(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
