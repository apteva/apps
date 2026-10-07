package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (a *App) httpMonitoring(w http.ResponseWriter, r *http.Request, id int64, resource string) {
	ctx := appCtxForRequest(r)
	if ctx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app unavailable")
		return
	}
	if resource == "monitoring" && r.Method == http.MethodPost {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || body.Enabled == nil {
			httpErr(w, http.StatusBadRequest, "enabled boolean required")
			return
		}
		result, err := configureMonitoring(ctx, id, *body.Enabled)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, result)
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	args := map[string]any{"id": id}
	for _, key := range []string{"from", "to", "resolution", "incident_id"} {
		if value := r.URL.Query().Get(key); value != "" {
			args[key] = value
		}
	}
	for _, key := range []string{"max_points", "limit"} {
		if value := r.URL.Query().Get(key); value != "" {
			v, err := strconv.Atoi(value)
			if err != nil {
				httpErr(w, http.StatusBadRequest, "invalid "+key)
				return
			}
			args[key] = v
		}
	}
	var result any
	var err error
	switch resource {
	case "metrics/history":
		result, err = monitoringHistory(ctx, args)
	case "metrics/incidents":
		result, err = monitoringIncidents(ctx, args)
	default:
		result, err = liveMonitoring(ctx, id)
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, result)
}
