package main

import (
	"net/http"
	"strconv"
	"strings"
)

type goalListResponse struct {
	Goals []*Goal `json:"goals"`
}

func (a *App) handleGoals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if a.store == nil {
		http.Error(w, "Builder is not ready", http.StatusServiceUnavailable)
		return
	}
	identity, ok := goalIdentityFromRequest(w, r)
	if !ok {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/goals")
	if path == "" || path == "/" {
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 || parsed > 100 {
				http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		statuses := splitQueryList(r.URL.Query().Get("status"))
		goals, err := a.store.ListGoals(identity, statuses, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, goalListResponse{Goals: goals})
		return
	}

	goalID := strings.Trim(strings.TrimSpace(path), "/")
	if goalID == "" || strings.Contains(goalID, "/") {
		http.NotFound(w, r)
		return
	}
	bundle, err := a.store.GetBundle(identity, goalID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}

func goalIdentityFromRequest(w http.ResponseWriter, r *http.Request) (GoalIdentity, bool) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	ownerAgentID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("owner_agent_id")), 10, 64)
	if projectID == "" || err != nil || ownerAgentID <= 0 {
		http.Error(w, "project_id and a positive owner_agent_id are required", http.StatusBadRequest)
		return GoalIdentity{}, false
	}
	return GoalIdentity{ProjectID: projectID, OwnerAgentID: ownerAgentID}, true
}

func splitQueryList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}
