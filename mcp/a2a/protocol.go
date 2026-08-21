package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type a2aPart struct {
	Text      string         `json:"text,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Type      string         `json:"type,omitempty"`
}

type a2aMessage struct {
	MessageID string    `json:"messageId,omitempty"`
	ContextID string    `json:"contextId,omitempty"`
	TaskID    string    `json:"taskId,omitempty"`
	Role      string    `json:"role"`
	Parts     []a2aPart `json:"parts"`
}

type sendMessageParams struct {
	Message   a2aMessage `json:"message"`
	ContextID string     `json:"contextId,omitempty"`
	TaskID    string     `json:"taskId,omitempty"`
}

type taskIDParams struct {
	ID string `json:"id"`
}

type a2aTaskStatus struct {
	State   string      `json:"state"`
	Message *a2aMessage `json:"message,omitempty"`
}

type a2aTaskWire struct {
	ID        string        `json:"id"`
	ContextID string        `json:"contextId"`
	Status    a2aTaskStatus `json:"status"`
}

func extractA2AText(message a2aMessage) string {
	var parts []string
	for _, part := range message.Parts {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func localStateFromA2A(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	state = strings.TrimPrefix(state, "task_state_")
	switch state {
	case "submitted", "working", "input_required", "completed", "failed", "canceled", "cancelled":
		if state == "cancelled" {
			return "canceled"
		}
		return state
	case "rejected":
		return "failed"
	default:
		return "working"
	}
}

func a2aStateFromLocal(state string) string {
	return "TASK_STATE_" + strings.ToUpper(state)
}

func taskWire(dbTask *Task, messages []*Message) a2aTaskWire {
	w := a2aTaskWire{
		ID:        dbTask.ProtocolTaskID,
		ContextID: dbTask.ProtocolContextID,
		Status:    a2aTaskStatus{State: a2aStateFromLocal(dbTask.Status)},
	}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.FromAgentID == dbTask.ToAgentID && strings.TrimSpace(message.Body) != "" {
			w.Status.Message = &a2aMessage{
				MessageID: "message-" + strconv.FormatInt(message.ID, 10),
				ContextID: dbTask.ProtocolContextID,
				TaskID:    dbTask.ProtocolTaskID,
				Role:      "ROLE_AGENT",
				Parts:     []a2aPart{{Text: message.Body, MediaType: "text/plain"}},
			}
			break
		}
	}
	return w
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: message},
	})
}

func appContextForRequest(r *http.Request) *sdk.AppCtx {
	ctx := globalCtx
	if ctx == nil {
		return nil
	}
	if projectID := strings.TrimSpace(r.URL.Query().Get("project_id")); projectID != "" {
		return ctx.WithProject(projectID)
	}
	return ctx
}

func (a *App) handleDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	app := appContextForRequest(r)
	if app == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	peer, err := authenticatePeer(app, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	agents, _, err := listCardAgents(app, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	capability := strings.TrimSpace(r.URL.Query().Get("capability"))
	entries := make([]directoryEntry, 0, len(agents))
	for _, agent := range agents {
		profile, profileErr := ensureAgentProfile(app.AppDB(), agent)
		if profileErr != nil || !profile.Enabled || !peerAllows(peer, "discover", profile, &agent) {
			continue
		}
		sk := skillIDs(profile.Skills)
		if !matchesAgentQuery(agent.Name, profile.Description, sk, query, capability) {
			continue
		}
		entries = append(entries, directoryEntry{
			CardID: profile.CardID, Name: agent.Name, Description: profile.Description,
			Online: strings.EqualFold(agent.Status, "running"), Skills: sk,
		})
	}
	node, _ := ensureLocalNode(app)
	writeJSON(w, map[string]any{"node": node, "agents": entries})
}

func pathTail(path, prefix string) string {
	return strings.Trim(strings.TrimPrefix(path, prefix), "/")
}

func (a *App) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	app := appContextForRequest(r)
	if app == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	peer, err := authenticatePeer(app, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	cardID, _ := url.PathUnescape(pathTail(r.URL.Path, "/agent-cards/"))
	profile, err := getAgentProfileByCard(app.AppDB(), cardID)
	if err != nil || profile == nil || !profile.Enabled {
		http.NotFound(w, r)
		return
	}
	agent, err := app.PlatformAPI().GetAgent(profile.LocalAgentID)
	if err != nil || agent == nil || !peerAllows(peer, "discover", profile, agent) {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, buildAgentCard(app, *agent, profile))
}

func (a *App) handleAgentProtocol(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	app := appContextForRequest(r)
	if app == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	peer, err := authenticatePeer(app, r)
	if err != nil {
		writeRPCError(w, nil, http.StatusUnauthorized, -32050, err.Error())
		return
	}
	cardID, _ := url.PathUnescape(pathTail(r.URL.Path, "/agents/"))
	profile, err := getAgentProfileByCard(app.AppDB(), cardID)
	if err != nil || profile == nil || !profile.Enabled {
		writeRPCError(w, nil, http.StatusNotFound, -32001, "agent not found")
		return
	}
	agent, err := app.PlatformAPI().GetAgent(profile.LocalAgentID)
	if err != nil || agent == nil || !peerAllows(peer, "invoke", profile, agent) {
		writeRPCError(w, nil, http.StatusForbidden, -32050, "peer may not invoke this agent")
		return
	}
	var request jsonRPCRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request); err != nil {
		writeRPCError(w, nil, http.StatusBadRequest, -32700, "parse error")
		return
	}
	switch request.Method {
	case "message/send":
		var params sendMessageParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			writeRPCError(w, request.ID, http.StatusBadRequest, -32602, "invalid message params")
			return
		}
		result, rpcErr := a.receiveRemoteMessage(app, peer, agent, profile, params)
		if rpcErr != nil {
			writeRPCError(w, request.ID, http.StatusBadRequest, -32602, rpcErr.Error())
			return
		}
		writeRPC(w, request.ID, result)
	case "tasks/get":
		var params taskIDParams
		_ = json.Unmarshal(request.Params, &params)
		task, getErr := getTaskByProtocolID(app.AppDB(), params.ID)
		if getErr != nil || task == nil || task.PeerID != peer.ID || task.RemoteCardID != profile.CardID {
			writeRPCError(w, request.ID, http.StatusNotFound, -32001, "task not found")
			return
		}
		messages, _ := listMessages(app.AppDB(), task.ID)
		writeRPC(w, request.ID, taskWire(task, messages))
	case "tasks/cancel":
		var params taskIDParams
		_ = json.Unmarshal(request.Params, &params)
		task, getErr := getTaskByProtocolID(app.AppDB(), params.ID)
		if getErr != nil || task == nil || task.PeerID != peer.ID || task.RemoteCardID != profile.CardID {
			writeRPCError(w, request.ID, http.StatusNotFound, -32001, "task not found")
			return
		}
		if !openStatuses[task.Status] {
			writeRPCError(w, request.ID, http.StatusConflict, -32002, "task is not cancelable")
			return
		}
		_ = setTaskStatus(app.AppDB(), task.ProjectID, task.ID, "canceled")
		task.Status = "canceled"
		_ = deliverToParticipant(app, task, task.ToAgentID, formatRemoteCancelEvent(task, peer.Name))
		messages, _ := listMessages(app.AppDB(), task.ID)
		writeRPC(w, request.ID, taskWire(task, messages))
	default:
		writeRPCError(w, request.ID, http.StatusNotFound, -32601, "method not found")
	}
}

func (a *App) receiveRemoteMessage(app *sdk.AppCtx, peer *peerConfig, agent *sdk.PlatformAgent, profile *agentProfile, params sendMessageParams) (a2aTaskWire, error) {
	message := strings.TrimSpace(extractA2AText(params.Message))
	if message == "" {
		return a2aTaskWire{}, errors.New("message must contain text")
	}
	recent, err := peerDeliveriesInWindow(app.AppDB(), peer.ID, time.Minute)
	if err != nil {
		return a2aTaskWire{}, err
	}
	limit := configInt(app, "rate_limit_per_minute", defaultRateLimitPerMinute)
	if recent >= limit {
		return a2aTaskWire{}, fmt.Errorf("peer rate limit reached: %d messages in the last minute", recent)
	}
	taskID := params.Message.TaskID
	if taskID == "" {
		taskID = params.TaskID
	}
	if taskID != "" {
		task, err := getTaskByProtocolID(app.AppDB(), taskID)
		if err != nil || task == nil || task.Direction != "inbound" || task.PeerID != peer.ID || task.RemoteCardID != profile.CardID {
			return a2aTaskWire{}, errors.New("task not found")
		}
		if task.Status == "input_required" {
			_ = setTaskStatus(app.AppDB(), task.ProjectID, task.ID, "working")
			task.Status = "working"
		}
		remote := &callIdentity{AgentName: peer.Name, ProjectID: task.ProjectID}
		if err := deliverToParticipant(app, task, task.ToAgentID, formatFollowUpEvent(task, remote, message)); err != nil {
			return a2aTaskWire{}, err
		}
		_ = recordMessage(app.AppDB(), task.ID, 0, task.ToAgentID, message, task.Status)
		messages, _ := listMessages(app.AppDB(), task.ID)
		return taskWire(task, messages), nil
	}
	contextID := params.Message.ContextID
	if contextID == "" {
		contextID = params.ContextID
	}
	if contextID == "" {
		contextID = randomID("context_")
	}
	task, err := createTask(app.AppDB(), &Task{
		ProjectID: agent.ProjectID, Kind: "ask", Status: "submitted", Direction: "inbound",
		FromAgentName: peer.Name, ToAgentID: agent.ID, ToAgentName: agent.Name,
		PeerID: peer.ID, RemoteCardID: profile.CardID,
		ProtocolTaskID: randomID("task_"), ProtocolContextID: contextID,
	})
	if err != nil {
		return a2aTaskWire{}, err
	}
	if err := deliver(app, agent.ID, formatAskEvent(task, message)); err != nil {
		_ = setTaskStatus(app.AppDB(), task.ProjectID, task.ID, "failed")
		return a2aTaskWire{}, err
	}
	_ = recordMessage(app.AppDB(), task.ID, 0, agent.ID, message, "submitted")
	emitTask(app, "task.created", task)
	messages, _ := listMessages(app.AppDB(), task.ID)
	return taskWire(task, messages), nil
}

func formatRemoteCancelEvent(task *Task, peerName string) string {
	return fmt.Sprintf("[a2a task:%d status:canceled] Remote requester %q canceled this task.\n%s", task.ID, peerName, trustFooter)
}

func (a *App) callRemoteRPC(ctx context.Context, app *sdk.AppCtx, peer *peerConfig, endpoint, method string, params any, out any) error {
	peerBase, _ := url.Parse(peer.BaseURL)
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme != peerBase.Scheme || !strings.EqualFold(target.Host, peerBase.Host) {
		return errors.New("remote interface is outside the configured peer origin")
	}
	paramsRaw, _ := json.Marshal(params)
	reqBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(strconv.Quote(randomID("rpc_"))), Method: method, Params: paramsRaw,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+peer.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.httpClient(app).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var response jsonRPCResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&response); err != nil {
		return fmt.Errorf("remote returned HTTP %d with invalid JSON: %w", res.StatusCode, err)
	}
	if response.Error != nil {
		return fmt.Errorf("remote A2A error %d: %s", response.Error.Code, response.Error.Message)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("remote returned HTTP %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (a *App) ensureRemoteCard(ctx context.Context, app *sdk.AppCtx, remote *remoteAgent, peer *peerConfig) (*remoteAgent, error) {
	if remote.Card != nil && remote.EndpointURL != "" {
		return remote, nil
	}
	card, err := a.fetchRemoteCard(ctx, app, *peer, remote.CardID)
	if err != nil {
		return nil, err
	}
	return upsertRemoteAgent(app.AppDB(), *peer, directoryEntry{
		CardID: remote.CardID, Name: card.Name, Description: card.Description, Online: true, Skills: skillIDs(card.Skills),
	}, card, configDuration(app, "card_cache_seconds", defaultCardCacheSeconds))
}

func configDuration(app *sdk.AppCtx, key string, fallbackSeconds int) time.Duration {
	return time.Duration(configInt(app, key, fallbackSeconds)) * time.Second
}

func (a *App) startRemoteTask(ctx context.Context, app *sdk.AppCtx, from *callIdentity, remote *remoteAgent, message string, oneWay bool) (map[string]any, error) {
	peer, err := findPeer(app, remote.PeerID)
	if err != nil {
		return nil, err
	}
	remote, err = a.ensureRemoteCard(ctx, app, remote, peer)
	if err != nil {
		return nil, err
	}
	status := "submitted"
	kind := "ask"
	if oneWay {
		status = "completed"
		kind = "message"
	}
	task, err := createTask(app.AppDB(), &Task{
		ProjectID: from.ProjectID, Kind: kind, Status: status, Direction: "outbound",
		FromAgentID: from.AgentID, FromAgentName: from.AgentName, FromThreadID: from.ThreadID,
		ToAgentName: remote.Name, PeerID: peer.ID, RemoteCardID: remote.CardID,
	})
	if err != nil {
		return nil, err
	}
	request := sendMessageParams{Message: a2aMessage{
		MessageID: randomID("message_"), Role: "ROLE_USER",
		Parts: []a2aPart{{Text: message, MediaType: "text/plain"}},
	}}
	var response a2aTaskWire
	if err := a.callRemoteRPC(ctx, app, peer, remote.EndpointURL, "message/send", request, &response); err != nil {
		_ = setTaskStatus(app.AppDB(), from.ProjectID, task.ID, "failed")
		return nil, fmt.Errorf("remote agent %q could not be reached: %w", remote.Name, err)
	}
	if response.ID == "" {
		_ = setTaskStatus(app.AppDB(), from.ProjectID, task.ID, "failed")
		return nil, errors.New("remote agent returned no task id")
	}
	_ = setRemoteTaskCorrelation(app.AppDB(), from.ProjectID, task.ID, response.ID, response.ContextID)
	_ = recordMessage(app.AppDB(), task.ID, from.AgentID, 0, message, status)
	emitTask(app, "task.created", task)
	note := "message accepted by remote agent; no reply is expected"
	if !oneWay {
		note = fmt.Sprintf("request accepted as task %d; the reply will arrive asynchronously", task.ID)
	}
	return map[string]any{
		"task_id": task.ID, "delivered": true,
		"to":   map[string]any{"address": "a2a:" + remote.Ref, "name": remote.Name, "peer": peer.Name},
		"note": note,
	}, nil
}

func resolveRemoteAddress(app *sdk.AppCtx, raw string) (*remoteAgent, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "a2a:") && !strings.HasPrefix(raw, "remote_") {
		return nil, nil
	}
	remote, err := getRemoteAgent(app.AppDB(), raw)
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return nil, errors.New("remote agent address is unknown or expired — run agents_discover again")
	}
	return remote, nil
}

func (a *App) continueRemoteTask(ctx context.Context, app *sdk.AppCtx, task *Task, message string) (map[string]any, error) {
	remote, err := getRemoteAgentByPeerCard(app.AppDB(), task.PeerID, task.RemoteCardID)
	if err != nil || remote == nil {
		return nil, errors.New("remote agent metadata is unavailable — run agents_discover again")
	}
	peer, err := findPeer(app, task.PeerID)
	if err != nil {
		return nil, err
	}
	remote, err = a.ensureRemoteCard(ctx, app, remote, peer)
	if err != nil {
		return nil, err
	}
	params := sendMessageParams{Message: a2aMessage{
		MessageID: randomID("message_"), ContextID: task.RemoteContextID, TaskID: task.RemoteTaskID,
		Role: "ROLE_USER", Parts: []a2aPart{{Text: message, MediaType: "text/plain"}},
	}}
	var response a2aTaskWire
	if err := a.callRemoteRPC(ctx, app, peer, remote.EndpointURL, "message/send", params, &response); err != nil {
		return nil, err
	}
	status := task.Status
	if status == "input_required" {
		status = "working"
		_ = setTaskSyncState(app.AppDB(), task.ProjectID, task.ID, status)
	}
	_ = recordMessage(app.AppDB(), task.ID, task.FromAgentID, 0, message, status)
	return map[string]any{
		"task_id": task.ID, "delivered": true, "status": status,
		"note": "follow-up delivered to the remote A2A task",
	}, nil
}

func (a *App) cancelRemoteTask(ctx context.Context, app *sdk.AppCtx, task *Task, message string) (map[string]any, error) {
	remote, err := getRemoteAgentByPeerCard(app.AppDB(), task.PeerID, task.RemoteCardID)
	if err != nil || remote == nil {
		return nil, errors.New("remote agent metadata is unavailable")
	}
	peer, err := findPeer(app, task.PeerID)
	if err != nil {
		return nil, err
	}
	remote, err = a.ensureRemoteCard(ctx, app, remote, peer)
	if err != nil {
		return nil, err
	}
	var response a2aTaskWire
	if err := a.callRemoteRPC(ctx, app, peer, remote.EndpointURL, "tasks/cancel", taskIDParams{ID: task.RemoteTaskID}, &response); err != nil {
		return nil, err
	}
	_ = setTaskSyncState(app.AppDB(), task.ProjectID, task.ID, "canceled")
	_ = recordMessage(app.AppDB(), task.ID, task.FromAgentID, 0, message, "canceled")
	task.Status = "canceled"
	emitTask(app, "task.updated", task)
	return map[string]any{"task_id": task.ID, "status": "canceled", "delivered": true}, nil
}
