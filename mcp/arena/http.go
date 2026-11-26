package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/arena/engine"
)

// HTTP API (same operations as the MCP tools, for the panel and browser
// clients). Seats are identified by the X-Seat-Token header or ?token=.
//
//	GET  /games
//	GET  /matches?status=&game=
//	POST /matches                      {game, seats, options, turn_seconds, title}
//	GET  /matches/:id                  seat view (spectator without token)
//	POST /matches/:id/join             {seat?, name?}   -> {your_seat, seat_token}
//	POST /matches/:id/act              {type, data}
//	POST /matches/:id/chat             {text}
//	GET  /matches/:id/wait?since=N     long-poll until version > N (25s)
//	GET  /matches/:id/events?after_id=
//	POST /matches/:id/cancel
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/games", Handler: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"games": engine.List()})
		}},
		{Pattern: "/matches", Handler: handleMatches},
		{Pattern: "/matches/", Handler: handleMatch},
	}
}

func requestProject(r *http.Request) string {
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return pid
	}
	if globalCtx != nil {
		return globalCtx.CurrentProject()
	}
	return ""
}

func seatToken(r *http.Request) string {
	if t := r.Header.Get("X-Seat-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}

func handleMatches(w http.ResponseWriter, r *http.Request) {
	pid := requestProject(r)
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		ms, err := rt.List(pid, ListFilter{Status: q.Get("status"), Game: q.Get("game"), Limit: limit})
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"matches": ms})
	case http.MethodPost:
		var body struct {
			Game        string         `json:"game"`
			Seats       []string       `json:"seats"`
			Options     map[string]any `json:"options"`
			TurnSeconds *int           `json:"turn_seconds"`
			Title       string         `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res, err := rt.Create(CreateRequest{ProjectID: pid, Game: body.Game, Seats: body.Seats, Options: body.Options,
			TurnSeconds: body.TurnSeconds, Title: body.Title, CreatedBy: "dashboard", AgentName: agentName(globalCtx)})
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, res)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func handleMatch(w http.ResponseWriter, r *http.Request) {
	pid := requestProject(r)
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/matches/"), "/"), "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id <= 0 {
		http.Error(w, "match id required", http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	seatFor := func(required bool) (int, bool) {
		tok := seatToken(r)
		if tok == "" {
			if required {
				http.Error(w, "seat token required", http.StatusUnauthorized)
				return 0, false
			}
			return engine.Spectator, true
		}
		seat, err := rt.ResolveSeat(pid, id, 0, tok)
		if err != nil {
			if required {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return 0, false
			}
			return engine.Spectator, true
		}
		return seat, true
	}

	switch {
	case r.Method == http.MethodGet && action == "":
		seat, _ := seatFor(false)
		st, err := rt.State(pid, id, seat, 20)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, st)
	case r.Method == http.MethodGet && action == "wait":
		seat, _ := seatFor(false)
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		ctx, cancel := context.WithTimeout(r.Context(), 26*time.Second)
		defer cancel()
		st, ready, err := rt.Wait(ctx, pid, id, seat, since, false, 25*time.Second)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			httpError(w, err)
			return
		}
		if st != nil && ready {
			writeJSON(w, st)
			return
		}
		writeJSON(w, map[string]any{"waiting": true})
	case r.Method == http.MethodGet && action == "events":
		seat, _ := seatFor(false)
		after, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		evs, err := rt.Events(pid, id, seat, after, limit)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"events": evs})
	case r.Method == http.MethodPost && action == "join":
		var body struct {
			Seat *int   `json:"seat"`
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res, err := rt.Join(JoinRequest{ProjectID: pid, MatchID: id, Seat: body.Seat, Name: body.Name})
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, res)
	case r.Method == http.MethodPost && action == "act":
		seat, ok := seatFor(true)
		if !ok {
			return
		}
		var act engine.Action
		if err := json.NewDecoder(r.Body).Decode(&act); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := rt.Act(pid, id, seat, act); err != nil {
			httpError(w, err)
			return
		}
		st, err := rt.State(pid, id, seat, 20)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, st)
	case r.Method == http.MethodPost && action == "chat":
		seat, ok := seatFor(true)
		if !ok {
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := rt.Chat(pid, id, seat, body.Text); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && action == "cancel":
		m, err := rt.Cancel(pid, id)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"match": m})
	default:
		http.Error(w, "unsupported route", http.StatusNotFound)
	}
}

// --- helpers --------------------------------------------------------------

func httpError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func sString() map[string]any  { return map[string]any{"type": "string"} }
func sInteger() map[string]any { return map[string]any{"type": "integer"} }
func sArray(item string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": item}}
}

func stringArg(args map[string]any, key string) string {
	if s, ok := args[key].(string); ok {
		return s
	}
	return ""
}

func int64Arg(args map[string]any, key string) int64 {
	switch x := args[key].(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func intArg(args map[string]any, key string, fallback int) int {
	if n := int(int64Arg(args, key)); n != 0 {
		return n
	}
	return fallback
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return strings.Split(x, ",")
	}
	return nil
}
