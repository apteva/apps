// Package web exposes the REST surface the panel reads from.
// Reverse-proxied at /api/apps/robot/* by apteva-server.
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"

	"github.com/apteva/apps/mcp/robot/episode"
	"github.com/apteva/apps/mcp/robot/world"
)

// Build returns the route list to hand sdk.App.HTTPRoutes(). Go's
// stdlib mux refuses overlapping registrations, so each pattern uses
// internal method dispatch where it has to.
func Build(mgr *episode.Manager) []sdk.Route {
	h := &handler{mgr: mgr}
	return []sdk.Route{
		{Pattern: "/scenarios", Handler: h.scenarios},
		{Pattern: "/episodes", Handler: h.episodesCollection},
		{Pattern: "/episodes/", Handler: h.episodesItem},
	}
}

type handler struct{ mgr *episode.Manager }

func (h *handler) scenarios(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	scens := h.mgr.Scenarios()
	out := make([]map[string]any, 0, len(scens))
	for _, s := range scens {
		out = append(out, map[string]any{
			"id":            s.ID,
			"name":          s.Name,
			"description":   s.Description,
			"max_steps":     s.MaxSteps,
			"optimal_steps": s.OptimalSteps,
			"observability": s.Observability,
			"grid":          map[string]int{"width": s.Grid.Width, "height": s.Grid.Height},
			"walls":         s.Walls,
			"agent_start":   s.AgentStart,
			"goal":          s.Goal,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenarios": out})
}

func (h *handler) episodesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		eps, err := h.mgr.RecentEpisodes(limit)
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"episodes": eps})

	case http.MethodPost:
		var body struct {
			ScenarioID string `json:"scenario_id"`
			Model      string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		res, err := h.mgr.Start(body.ScenarioID, body.Model)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, res)

	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// /episodes/{id}            GET   → summary + recent steps
// /episodes/{id}/steps      GET   → just the step rows (paged via limit)
// /episodes/{id}/move       POST  → manual move; body {"direction": "N|E|S|W"}
// /episodes/{id}/pick       POST  → manual pick (inert in v0.1)
// /episodes/{id}/drop       POST  → manual drop (inert in v0.1)
//
// The /move /pick /drop routes hit the same Manager methods the MCP
// tools use, so the harness-decided termination (success / timeout)
// applies whether the agent or a human drives the episode.
func (h *handler) episodesItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/episodes/")
	id, sub, _ := strings.Cut(rest, "/")
	if id == "" {
		http.Error(w, "episode id required", http.StatusBadRequest)
		return
	}
	switch sub {
	case "":
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		summary, err := h.mgr.Status(id)
		if err != nil {
			httpErr(w, err, http.StatusNotFound)
			return
		}
		steps, err := h.mgr.Steps(id, 200)
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"episode": summary, "steps": steps})
	case "steps":
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		steps, err := h.mgr.Steps(id, limit)
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
	case "move":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Direction string `json:"direction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		dir := world.Direction(body.Direction)
		switch dir {
		case world.North, world.East, world.South, world.West:
		default:
			http.Error(w, "direction must be N|E|S|W", http.StatusBadRequest)
			return
		}
		res, err := h.mgr.Move(id, dir)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "pick":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		res, err := h.mgr.Pick(id)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "drop":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		res, err := h.mgr.Drop(id)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, res)
	default:
		http.Error(w, "unknown subresource", http.StatusNotFound)
	}
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error, status int) {
	if errors.Is(err, episode.ErrNoActiveEpisode) || errors.Is(err, episode.ErrEpisodeFinished) {
		status = http.StatusConflict
	}
	http.Error(w, err.Error(), status)
}
