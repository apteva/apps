package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Admin REST mirror. Same handlers go through the same DB helpers as
// the MCP tools — the panel and the agent see one source of truth.
//
// All routes go through apteva-server's auth gate (no NoAuth) so the
// dashboard's session cookie is the authn.

func (a *App) handleAdminStreams(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"_project_id": pid}
		if v := r.URL.Query().Get("status"); v != "" {
			args["status"] = v
		}
		if v := r.URL.Query().Get("owner_app"); v != "" {
			args["owner_app"] = v
		}
		if v := r.URL.Query().Get("owner_tag"); v != "" {
			args["owner_tag"] = v
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

// handleAdminStreamItem dispatches /admin/streams/<id>[/<sub>] by
// method + sub-path.
func (a *App) handleAdminStreamItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/streams/")
	parts := strings.SplitN(rest, "/", 3)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	args := map[string]any{"_project_id": pid, "id": id}

	if len(parts) >= 2 {
		switch parts[1] {
		case "metrics":
			out, err := a.toolGetMetrics(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "stop":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST only")
				return
			}
			out, err := a.toolStop(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "rotate-key":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST only")
				return
			}
			out, err := a.toolRotateKey(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "replay":
			out, err := a.toolReplayURL(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "load-test":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST only")
				return
			}
			var body map[string]any
			if r.Body != nil {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
			if body == nil {
				body = map[string]any{}
			}
			body["_project_id"] = pid
			body["id"] = id
			out, err := a.toolLoadTest(globalCtx, body)
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
