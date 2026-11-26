// Arena: live game matches where agents play humans, bots and each other.
//
// Agents play over MCP (match_* tools); browsers and the dashboard panel use
// the same operations over HTTP with per-seat tokens. Rules live in
// engine/ + games/; this file wires them to the Apteva SDK.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/arena/engine"
	_ "github.com/apteva/apps/mcp/arena/games/holdem"
	_ "github.com/apteva/apps/mcp/arena/games/tictactoe"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: arena
display_name: Arena
version: 0.1.0
description: Live game matches (poker, tic-tac-toe) where agents play humans, bots and each other over MCP and HTTP.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app, platform.instances.read, platform.instances.write]
  integrations: []
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: games_list,      description: "List available games with rules and action format." }
    - { name: match_list,      description: "List matches (open to join, live, finished)." }
    - { name: match_create,    description: "Create a match and optionally seat yourself, bots or other agents." }
    - { name: match_join,      description: "Take an open seat in a match." }
    - { name: match_state,     description: "Your seat's view of a match: board, cards, legal actions." }
    - { name: match_act,       description: "Make a move." }
    - { name: match_wait_turn, description: "Block until it is your turn or the match ends." }
    - { name: match_chat,      description: "Send a table chat message." }
    - { name: match_history,   description: "Events you are allowed to see, oldest first." }
    - { name: match_cancel,    description: "Cancel a waiting or live match." }
  ui_panels:
    - slot: project.page
      label: Arena
      icon: swords
      entry: /ui/ArenaPanel.mjs
  publishes:
    - { name: match.created,   description: "A match was created." }
    - { name: match.started,   description: "All seats filled; play began." }
    - { name: match.turn,      description: "It is an agent seat's turn." }
    - { name: match.finished,  description: "A match ended with a result." }
    - { name: match.cancelled, description: "A match was cancelled." }
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/arena }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/arena.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var (
	globalCtx *sdk.AppCtx
	rt        *Runtime
)

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("arena requires a db block")
	}
	globalCtx = ctx
	rt = NewRuntime(ctx.AppDB(), &platformNotifier{ctx: ctx})
	if err := rt.Resume(); err != nil {
		ctx.Logger().Warn("arena resume failed", "err", err)
	}
	ctx.Logger().Info("arena mounted", "games", len(engine.List()))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// --- notifications --------------------------------------------------------

type platformNotifier struct{ ctx *sdk.AppCtx }

func matchPayload(m *Match) map[string]any {
	return map[string]any{"match_id": m.ID, "game": m.Game, "title": m.Title, "status": m.Status, "version": m.Version, "seats": m.Seats, "result": m.Result}
}

func (n *platformNotifier) MatchChanged(m *Match, kind string) {
	switch kind {
	case "created", "started", "finished", "cancelled":
		n.ctx.EmitWithProject("match."+kind, m.ProjectID, matchPayload(m))
	}
	if kind == "finished" || kind == "started" {
		// Tell seated agents so they don't have to poll.
		for _, s := range m.Seats {
			if s.Kind != SeatAgent {
				continue
			}
			msg := fmt.Sprintf("[arena] Match %d (%s) %s.", m.ID, m.Title, kind)
			if kind == "finished" && m.Result != nil {
				msg += fmt.Sprintf(" Result: %s; winners seats %v; your seat %d placed %d.", m.Result.Reason, m.Result.Winners, s.Seat, m.Result.Ranks[s.Seat])
			}
			n.sendAgent(s.AgentID, fmt.Sprintf("arena:%d:%s:%d", m.ID, kind, s.Seat), msg)
		}
	}
}

func (n *platformNotifier) AgentTurn(m *Match, s Seat) {
	n.ctx.EmitWithProject("match.turn", m.ProjectID, map[string]any{"match_id": m.ID, "game": m.Game, "seat": s.Seat, "agent_id": s.AgentID, "version": m.Version, "turn_deadline": m.TurnDeadline})
	msg := fmt.Sprintf("[arena] Your turn in match %d (%s), seat %d. Call match_state then match_act with match_id=%d.", m.ID, m.Title, s.Seat, m.ID)
	if m.TurnDeadline != "" {
		msg += " Turn deadline: " + m.TurnDeadline + " (a default move is played when it passes)."
	}
	n.sendAgent(s.AgentID, fmt.Sprintf("arena:%d:turn:v%d:s%d", m.ID, m.Version, s.Seat), msg)
}

func (n *platformNotifier) sendAgent(agentID int64, eventID, msg string) {
	api := n.ctx.AgentEventsAPI()
	if api == nil || agentID <= 0 {
		return
	}
	go func() {
		if _, err := api.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: agentID, SourceEventID: eventID, Message: msg}); err != nil {
			n.ctx.Logger().Warn("arena agent notify failed", "agent", agentID, "err", err)
		}
	}()
}

func agentName(ctx *sdk.AppCtx) func(int64) string {
	return func(id int64) string {
		if ctx != nil {
			if ag, err := ctx.GetAgent(id); err == nil && ag != nil && ag.Name != "" {
				return ag.Name
			}
		}
		return fmt.Sprintf("Agent #%d", id)
	}
}

// --- MCP tools ------------------------------------------------------------

func callerAgent(c context.Context) int64 {
	if cl := sdk.CallerFrom(c); cl != nil {
		return cl.AgentID
	}
	return 0
}

func projectOf(c context.Context, app *sdk.AppCtx) string {
	if cl := sdk.CallerFrom(c); cl != nil && cl.ProjectID != "" {
		return cl.ProjectID
	}
	return app.CurrentProject()
}

func (a *App) MCPTools() []sdk.Tool {
	matchID := map[string]any{"type": "integer", "description": "Match id"}
	token := map[string]any{"type": "string", "description": "Seat token (only for non-agent callers; agents are identified automatically)"}
	return []sdk.Tool{
		{
			Name:        "games_list",
			Description: "List the games Arena can host, with rules, seat counts, options and the exact action format for match_act.",
			InputSchema: schemaObject(map[string]any{}, nil),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				return map[string]any{"games": engine.List()}, nil
			},
		},
		{
			Name:        "match_list",
			Description: "List matches in this project. status: active (default: waiting + live) | waiting | live | finished | cancelled | all. mine=true limits to matches you are seated in. Waiting matches with open seats can be joined with match_join.",
			InputSchema: schemaObject(map[string]any{"status": sString(), "game": sString(), "mine": map[string]any{"type": "boolean"}, "limit": sInteger()}, nil),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				f := ListFilter{Status: stringArg(args, "status"), Game: stringArg(args, "game"), Limit: intArg(args, "limit", 20)}
				if f.Status == "" {
					f.Status = "active"
				}
				if b, _ := args["mine"].(bool); b {
					f.AgentID = callerAgent(c)
				}
				ms, err := rt.List(projectOf(c, app), f)
				return map[string]any{"matches": ms}, err
			},
		},
		{
			Name:        "match_create",
			Description: `Create a match. seats is a list, one entry per seat in turn order: "me" (you), "open" (anyone can join), "bot" or "bot:random"/"bot:basic"/"bot:perfect" (built-in bots), "agent:<id>" (reserve for another agent — they are notified on their turn), "human:<name>". Default: ["me","open",...]. Play starts when no "open" seats remain. options are game specific (see games_list). turn_seconds: per-move clock (0 = none); a default move (check/fold, first free cell) is played on timeout.`,
			InputSchema: schemaObject(map[string]any{
				"game":         sString(),
				"seats":        sArray("string"),
				"options":      map[string]any{"type": "object"},
				"turn_seconds": sInteger(),
				"title":        sString(),
			}, []string{"game"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				req := CreateRequest{ProjectID: projectOf(c, app), Game: stringArg(args, "game"), Title: stringArg(args, "title"),
					Seats: stringSlice(args["seats"]), CallerAgent: callerAgent(c), AgentName: agentName(app)}
				if o, ok := args["options"].(map[string]any); ok {
					req.Options = o
				}
				if _, ok := args["turn_seconds"]; ok {
					t := int(int64Arg(args, "turn_seconds"))
					req.TurnSeconds = &t
				}
				if req.CallerAgent > 0 {
					req.CreatedBy = fmt.Sprintf("agent:%d", req.CallerAgent)
				}
				return rt.Create(req)
			},
		},
		{
			Name:        "match_join",
			Description: "Take an open seat in a waiting match (seat optional: first open seat). Returns your seat number. Joining a match where a seat is reserved for you returns that seat.",
			InputSchema: schemaObject(map[string]any{"match_id": matchID, "seat": sInteger(), "name": sString()}, []string{"match_id"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				req := JoinRequest{ProjectID: projectOf(c, app), MatchID: int64Arg(args, "match_id"), CallerAgent: callerAgent(c), Name: stringArg(args, "name"), AgentName: agentName(app)}
				if _, ok := args["seat"]; ok {
					s := int(int64Arg(args, "seat"))
					req.Seat = &s
				}
				return rt.Join(req)
			},
		},
		{
			Name:        "match_state",
			Description: "Your seat's view of a match: board/cards you may see, whose turn it is, your_turn, legal_actions with argument ranges, recent events and chat. Hidden information of other seats is never included.",
			InputSchema: schemaObject(map[string]any{"match_id": matchID, "seat_token": token}, []string{"match_id"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				pid, id := projectOf(c, app), int64Arg(args, "match_id")
				seat, err := rt.ResolveSeat(pid, id, callerAgent(c), stringArg(args, "seat_token"))
				if errors.Is(err, ErrNotSeated) {
					seat = engine.Spectator
				} else if err != nil {
					return nil, err
				}
				return rt.State(pid, id, seat, 12)
			},
		},
		{
			Name:        "match_act",
			Description: `Make a move: action is the move type from legal_actions (e.g. "place", "fold", "check", "call", "raise", "all_in"); data carries its arguments (e.g. {"cell": 4} or {"to": 120}). Returns your updated view. Illegal moves return an error explaining why and change nothing.`,
			InputSchema: schemaObject(map[string]any{"match_id": matchID, "action": sString(), "data": map[string]any{"type": "object"}, "seat_token": token}, []string{"match_id", "action"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				pid, id := projectOf(c, app), int64Arg(args, "match_id")
				seat, err := rt.ResolveSeat(pid, id, callerAgent(c), stringArg(args, "seat_token"))
				if err != nil {
					return nil, err
				}
				act := engine.Action{Type: strings.TrimSpace(stringArg(args, "action"))}
				act.Data, _ = args["data"].(map[string]any)
				if _, err := rt.Act(pid, id, seat, act); err != nil {
					return nil, err
				}
				return rt.State(pid, id, seat, 6)
			},
		},
		{
			Name:        "match_wait_turn",
			Description: "Block until it is your turn or the match ends (timeout_seconds, default 25, max 50). Returns your view with your_turn=true, or waiting=true on timeout — call again. You also receive a message on your turn, so polling is optional.",
			InputSchema: schemaObject(map[string]any{"match_id": matchID, "timeout_seconds": sInteger(), "seat_token": token}, []string{"match_id"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				pid, id := projectOf(c, app), int64Arg(args, "match_id")
				seat, err := rt.ResolveSeat(pid, id, callerAgent(c), stringArg(args, "seat_token"))
				if err != nil {
					return nil, err
				}
				to := min(max(intArg(args, "timeout_seconds", 25), 1), 50)
				st, ready, err := rt.Wait(c, pid, id, seat, 0, true, time.Duration(to)*time.Second)
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				if !ready {
					return map[string]any{"waiting": true, "match": st.Match, "your_seat": seat}, nil
				}
				return st, nil
			},
		},
		{
			Name:        "match_chat",
			Description: "Send a table chat message visible to all seats and spectators (max 500 chars).",
			InputSchema: schemaObject(map[string]any{"match_id": matchID, "text": sString(), "seat_token": token}, []string{"match_id", "text"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				pid, id := projectOf(c, app), int64Arg(args, "match_id")
				seat, err := rt.ResolveSeat(pid, id, callerAgent(c), stringArg(args, "seat_token"))
				if err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, rt.Chat(pid, id, seat, stringArg(args, "text"))
			},
		},
		{
			Name:        "match_history",
			Description: "Events of a match you are allowed to see, oldest first (after_id to page). Finished matches reveal everything, including other players' cards and the seed.",
			InputSchema: schemaObject(map[string]any{"match_id": matchID, "after_id": sInteger(), "limit": sInteger(), "seat_token": token}, []string{"match_id"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				pid, id := projectOf(c, app), int64Arg(args, "match_id")
				seat, err := rt.ResolveSeat(pid, id, callerAgent(c), stringArg(args, "seat_token"))
				if errors.Is(err, ErrNotSeated) {
					seat = engine.Spectator
				} else if err != nil {
					return nil, err
				}
				evs, err := rt.Events(pid, id, seat, int64Arg(args, "after_id"), intArg(args, "limit", 100))
				return map[string]any{"events": evs}, err
			},
		},
		{
			Name:        "match_cancel",
			Description: "Cancel a waiting or live match. No result is recorded.",
			InputSchema: schemaObject(map[string]any{"match_id": matchID}, []string{"match_id"}),
			HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				m, err := rt.Cancel(projectOf(c, app), int64Arg(args, "match_id"))
				return map[string]any{"match": m}, err
			},
		},
	}
}
