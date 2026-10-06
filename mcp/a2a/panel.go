package main

// Operator views are authenticated by the app gateway. Local agents and task
// records are project-scoped; connections belong to the whole A2A installation.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func panelContext(w http.ResponseWriter, r *http.Request) *sdk.AppCtx {
	app := appContextForRequest(r)
	if app == nil {
		http.Error(w, "not mounted", 503)
		return nil
	}
	if app.CurrentProject() == "" {
		http.Error(w, "project_id required", 400)
		return nil
	}
	return app
}

const attentionSQL = `(t.status IN ('input_required','failed') OR (t.status IN ('submitted','working','input_required') AND t.poll_failures > 0) OR EXISTS (SELECT 1 FROM a2a_deliveries d WHERE d.task_id=t.id AND d.delivered=0))`

type panelTask struct {
	*Task
	FromThread      string `json:"from_thread_id,omitempty"`
	ToThread        string `json:"to_thread_id,omitempty"`
	Preview         string `json:"preview"`
	MessageCount    int    `json:"message_count"`
	PendingDelivery int    `json:"pending_delivery"`
	PollFailures    int    `json:"poll_failures"`
}

// Query the full ledger before pagination; search includes message text and
// remote participants, and date bounds use UTC calendar days.
func panelTaskWhere(r *http.Request, project string) (string, []any, error) {
	q := r.URL.Query()
	where, args := []string{"t.project_id = ?"}, []any{project}
	if v := q.Get("status"); v != "" {
		switch v {
		case "active":
			where = append(where, "t.status IN ('submitted','working')")
		case "open":
			where = append(where, "t.status IN ('submitted','working','input_required')")
		case "attention":
			where = append(where, attentionSQL)
		default:
			where = append(where, "t.status = ?")
			args = append(args, v)
		}
	}
	if v := q.Get("peer"); v != "" {
		if v == "local" {
			where = append(where, "t.direction='local'")
		} else {
			where = append(where, "t.peer_id=?")
			args = append(args, v)
		}
	}
	if v := q.Get("agent_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return "", nil, fmt.Errorf("invalid agent_id")
		}
		where = append(where, "(t.from_agent_id=? OR t.to_agent_id=?)")
		args = append(args, id, id)
	}
	if address := q.Get("agent_address"); address != "" {
		switch {
		case strings.HasPrefix(address, "agent:"):
			id, err := strconv.ParseInt(strings.TrimPrefix(address, "agent:"), 10, 64)
			if err != nil || id <= 0 {
				return "", nil, fmt.Errorf("invalid agent_address")
			}
			where = append(where, "(t.from_agent_id=? OR t.to_agent_id=?)")
			args = append(args, id, id)
		case strings.HasPrefix(address, "a2a:"):
			where = append(where, "EXISTS (SELECT 1 FROM a2a_remote_agents r WHERE r.ref=? AND r.peer_id=t.peer_id AND r.card_id=t.remote_card_id)")
			args = append(args, strings.TrimPrefix(address, "a2a:"))
		default:
			return "", nil, fmt.Errorf("invalid agent_address")
		}
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		where = append(where, `(instr(lower(t.from_agent_name || ' ' || t.to_agent_name || ' ' || t.peer_id),lower(?))>0 OR EXISTS (SELECT 1 FROM a2a_messages m WHERE m.task_id=t.id AND instr(lower(m.body),lower(?))>0))`)
		args = append(args, v, v)
	}
	for _, key := range []string{"from", "to"} {
		if v := q.Get(key); v != "" {
			day, err := time.Parse("2006-01-02", v)
			if err != nil {
				return "", nil, fmt.Errorf("invalid %s date", key)
			}
			op := ">="
			if key == "to" {
				day = day.AddDate(0, 0, 1)
				op = "<"
			}
			where = append(where, "t.created_at "+op+" ?")
			args = append(args, day.Format(time.RFC3339))
		}
	}
	if q.Get("from") != "" && q.Get("to") != "" && q.Get("from") > q.Get("to") {
		return "", nil, fmt.Errorf("from date must precede to date")
	}
	return strings.Join(where, " AND "), args, nil
}

func (a *App) handlePanelTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	app := panelContext(w, r)
	if app == nil {
		return
	}
	where, args, err := panelTaskWhere(r, app.CurrentProject())
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	limit, offset := intQuery(r, "limit", 50), intQuery(r, "offset", 0)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err = app.AppDB().QueryRow("SELECT COUNT(*) FROM a2a_tasks t WHERE "+where, args...).Scan(&total); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows, err := app.AppDB().Query(`SELECT t.id,
 COALESCE((SELECT substr(body,1,240) FROM a2a_messages WHERE task_id=t.id ORDER BY id LIMIT 1),''),
 (SELECT COUNT(*) FROM a2a_messages WHERE task_id=t.id),
 (SELECT COUNT(*) FROM a2a_deliveries WHERE task_id=t.id AND delivered=0), t.poll_failures
 FROM a2a_tasks t WHERE `+where+` ORDER BY t.updated_at DESC,t.id DESC LIMIT ? OFFSET ?`, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	type extra struct {
		id                          int64
		preview                     string
		messages, pending, failures int
	}
	extras := []extra{}
	for rows.Next() {
		var e extra
		if err = rows.Scan(&e.id, &e.preview, &e.messages, &e.pending, &e.failures); err != nil {
			break
		}
		extras = append(extras, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tasks := []panelTask{}
	for _, e := range extras {
		t, err := getTask(app.AppDB(), app.CurrentProject(), e.id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if t != nil {
			// Artifact payloads are loaded only when opening the exchange.
			t.Artifacts = nil
			tasks = append(tasks, panelTask{t, t.FromThreadID, t.ToThreadID, e.preview, e.messages, e.pending, e.failures})
		}
	}
	writeJSON(w, map[string]any{"tasks": tasks, "total": total, "limit": limit, "offset": offset})
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	app := panelContext(w, r)
	if app == nil {
		return
	}
	var total, active, input, failed, completed, attention, pending int
	err := app.AppDB().QueryRow(`SELECT COUNT(*),COALESCE(SUM(t.status IN ('submitted','working')),0),
 COALESCE(SUM(t.status='input_required'),0),COALESCE(SUM(t.status='failed'),0),COALESCE(SUM(t.status='completed'),0),
 COALESCE(SUM(`+attentionSQL+`),0) FROM a2a_tasks t WHERE t.project_id=?`, app.CurrentProject()).Scan(&total, &active, &input, &failed, &completed, &attention)
	if err == nil {
		err = app.AppDB().QueryRow(`SELECT COUNT(*) FROM a2a_deliveries WHERE project_id=? AND delivered=0`, app.CurrentProject()).Scan(&pending)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"total": total, "active": active, "input_required": input, "failed": failed, "completed": completed, "attention": attention, "pending_delivery": pending, "as_of": nowUTC()})
}

type networkAgent struct {
	Address     string     `json:"address"`
	ID          int64      `json:"id,omitempty"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	PeerID      string     `json:"peer_id"`
	PeerName    string     `json:"peer_name"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Skills      []string   `json:"skills"`
	Card        *AgentCard `json:"card,omitempty"`
	FetchedAt   string     `json:"fetched_at,omitempty"`
	ExpiresAt   string     `json:"expires_at,omitempty"`
}

func (a *App) handleNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	app := panelContext(w, r)
	if app == nil {
		return
	}
	agents, warnings := []networkAgent{}, []string{}
	local, err := sdk.ListAgentsVia(app.PlatformAPI(), app.CurrentProject())
	if err != nil {
		warnings = append(warnings, "Local agent directory is unavailable.")
	} else {
		for _, agent := range local {
			if agent.ProjectID != app.CurrentProject() || !agent.AttachedToCaller {
				continue
			}
			profile, err := ensureAgentProfile(app.AppDB(), agent)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if !profile.Enabled {
				continue
			}
			agents = append(agents, networkAgent{Address: fmt.Sprintf("agent:%d", agent.ID), ID: agent.ID, Name: agent.Name, Description: profile.Description, PeerID: "local", PeerName: "This installation", Kind: "local", Status: agent.Status, Skills: skillIDs(profile.Skills), Card: buildAgentCard(app, agent, profile)})
		}
	}
	rows, err := app.AppDB().Query(`SELECT r.ref,p.name,p.kind FROM a2a_remote_agents r JOIN a2a_peers p ON p.id=r.peer_id WHERE r.directory_visible=1 ORDER BY r.name`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	type cached struct{ ref, name, kind string }
	refs := []cached{}
	for rows.Next() {
		var c cached
		if err = rows.Scan(&c.ref, &c.name, &c.kind); err != nil {
			break
		}
		refs = append(refs, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, c := range refs {
		remote, err := getRemoteAgent(app.AppDB(), c.ref)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if remote == nil {
			continue
		}
		agents = append(agents, networkAgent{Address: "a2a:" + remote.Ref, Name: remote.Name, Description: remote.Description, PeerID: remote.PeerID, PeerName: c.name, Kind: c.kind, Status: "cached", Skills: remote.Skills, Card: remote.Card, FetchedAt: remote.FetchedAt, ExpiresAt: remote.ExpiresAt})
	}
	sort.Slice(agents, func(i, j int) bool { return strings.ToLower(agents[i].Name) < strings.ToLower(agents[j].Name) })
	node, err := ensureLocalNode(app)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"agents": agents, "node": node, "warnings": warnings})
}

func (a *App) handleNetworkCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	app := panelContext(w, r)
	if app == nil {
		return
	}
	remote, err := getRemoteAgent(app.AppDB(), r.URL.Query().Get("address"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if remote == nil {
		http.NotFound(w, r)
		return
	}
	peer, err := findPeer(app, remote.PeerID)
	if err != nil || peer == nil {
		http.Error(w, "connection unavailable", 404)
		return
	}
	card, err := a.fetchRemoteCard(r.Context(), app, *peer, remote.CardID)
	if err != nil {
		http.Error(w, "Could not retrieve Agent Card from connection.", 502)
		return
	}
	_, err = upsertRemoteAgent(app.AppDB(), *peer, directoryEntry{CardID: remote.CardID, Name: card.Name, Description: card.Description, Skills: skillIDs(card.Skills)}, card, configDuration(app, "card_cache_seconds", defaultCardCacheSeconds))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"card": card})
}

// Checks validate discovery/card access, never send messages or claim invocation
// health. Existing transport validation and credential handling are reused.
func (a *App) handleConnectionCheck(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	app := panelContext(w, r)
	if app == nil {
		return
	}
	peer, err := findPeer(app, id)
	if err != nil || peer == nil {
		http.Error(w, "connection not found", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	started := time.Now()
	entries := []directoryEntry{}
	var card *AgentCard
	if peer.Kind == "agent_card" {
		card, _, err = a.fetchPublicAgentCard(ctx, app, peer.DiscoveryURL, peer.Token)
		if err == nil {
			entries = append(entries, directoryEntry{CardID: publicCardID(card), Name: card.Name, Description: card.Description, Skills: skillIDs(card.Skills), Online: true})
		}
	} else {
		entries, err = a.fetchPeerDirectory(ctx, app, *peer, "", "")
	}
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "checked_at": nowUTC(), "latency_ms": time.Since(started).Milliseconds(), "message": "Discovery check failed. Verify the URL, credentials, and remote discovery grants."})
		return
	}
	// Hide agents no longer advertised, preserving records used by ongoing tasks.
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.CardID] = true
		if _, err = upsertRemoteAgent(app.AppDB(), *peer, entry, card, configDuration(app, "card_cache_seconds", defaultCardCacheSeconds)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	rows, err := app.AppDB().Query("SELECT card_id FROM a2a_remote_agents WHERE peer_id=?", id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	stale := []string{}
	for rows.Next() {
		var cid string
		if err = rows.Scan(&cid); err != nil {
			break
		}
		if !seen[cid] {
			stale = append(stale, cid)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, cid := range stale {
		if _, err = app.AppDB().Exec("UPDATE a2a_remote_agents SET directory_visible=0 WHERE peer_id=? AND card_id=?", id, cid); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "checked_at": nowUTC(), "latency_ms": time.Since(started).Milliseconds(), "agents": len(entries), "message": "Discovery verified. Sending a task has not been tested."})
}

func (a *App) handleConnectionAccess(w http.ResponseWriter, r *http.Request) {
	app := panelContext(w, r)
	if app == nil {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/connections/")
	records, err := loadPeerRecordsWhere(app, "WHERE id = ?", id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if len(records) == 0 {
		http.NotFound(w, r)
		return
	}
	peer := records[0].peerConfig
	if peer.Kind != "node" || (peer.ManagedBy != "operator" && peer.ManagedBy != "agent") {
		http.Error(w, "connection is managed by configuration or another app", 409)
		return
	}
	var input struct {
		DiscoverAgents []string `json:"discover_agents"`
		InvokeAgents   []string `json:"invoke_agents"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	peer.DiscoverAgents = input.DiscoverAgents
	peer.InvokeAgents = input.InvokeAgents
	if err = normalizePeer(&peer); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	keys, err := loadPeerKeyring(app)
	if err == nil {
		err = storePeer(app.AppDB(), keys, peer, nil)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"updated": true})
}
