package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompletedRepliesRetryAfterDeliveryFailure(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint(remote), func(t *testing.T) {
			ctx, p := newTestEnv(t)
			a := &App{}
			var task *Task
			if remote {
				var err error
				task, err = createTask(ctx.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: "working", Direction: "outbound", FromAgentID: 41, FromThreadID: "requester"})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				got := resultMap(t)(a.toolAsk(callerCtx(41, "requester"), ctx, map[string]any{"to": "42", "message": "Do work"}))
				task, _ = getTask(ctx.AppDB(), testProject, got["task_id"].(int64))
			}
			p.failSend = true
			p.failThread = true
			if remote {
				if err := applyRemoteResult(ctx, task, "Remote", a2aTaskWire{Status: a2aTaskStatus{State: "completed", Message: &a2aMessage{Parts: []a2aPart{{Text: "Durable answer"}}}}}); err != nil {
					t.Fatal(err)
				}
			} else {
				got := resultMap(t)(a.toolReply(callerCtx(42, "responder"), ctx, map[string]any{"task_id": fmt.Sprint(task.ID), "message": "Durable answer"}))
				if got["pending_delivery"] != true {
					t.Fatalf("reply not queued: %#v", got)
				}
			}
			task, _ = getTask(ctx.AppDB(), testProject, task.ID)
			if task.Status != "completed" {
				t.Fatal(task.Status)
			}
			messages, err := listMessages(ctx.AppDB(), task.ID)
			if err != nil || messages[len(messages)-1].Body != "Durable answer" {
				t.Fatalf("lost reply: %v %v", messages, err)
			}
			p.failSend = false
			p.failThread = false
			// Fresh App mimics process restart; the pending row survives in the DB.
			if err := (&App{}).syncRemoteTasks(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			if len(p.threadEvents) != 1 || !strings.Contains(p.threadEvents[0].Message, "Durable answer") {
				t.Fatalf("retry failed: %+v", p.threadEvents)
			}
			if err := a.syncRemoteTasks(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			if len(p.threadEvents) != 1 {
				t.Fatal("delivered again after acknowledgement")
			}
		})
	}
}

func TestReplyTransactionRollsBackOnMessageFailure(t *testing.T) {
	ctx, _ := newTestEnv(t)
	task, err := createTask(ctx.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: "working", FromAgentID: 41, ToAgentID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_reply BEFORE INSERT ON a2a_messages BEGIN SELECT RAISE(FAIL,'disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	task.Status = "completed"
	if _, err := saveReply(ctx.AppDB(), task, 42, 41, "Answer", "Event", nil); err == nil {
		t.Fatal("expected write failure")
	}
	saved, _ := getTask(ctx.AppDB(), testProject, task.ID)
	if saved.Status != "working" {
		t.Fatal("lifecycle committed without reply")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM a2a_deliveries`).Scan(&count)
	if count != 0 {
		t.Fatal("orphan delivery")
	}
}

func TestPublicDiscoveryPreservesOperatorCredentials(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "Authenticated", "url": server.URL + "/rpc", "protocolVersion": "0.3.0"})
	}))
	defer server.Close()
	ctx, _ := newTestEnv(t)
	a := &App{}
	_, peer, err := a.connectPublicAgent(context.Background(), ctx, server.URL, "operator-secret", "operator")
	if err != nil {
		t.Fatal(err)
	}
	resultMap(t)(a.toolDiscover(callerCtx(41, ""), ctx, map[string]any{"card_url": server.URL}))
	saved, err := findPeer(ctx, peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != "operator-secret" || saved.ManagedBy != "operator" {
		t.Fatal("agent changed operator connection")
	}
}

// Wire fixtures deliberately use ordinary maps instead of the implementation's
// DTOs, so missing fields and incorrect envelopes cannot validate themselves.
func TestPublicTaskArtifactsAndImmediateFollowUp(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			var sends atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if legacy {
						writeJSON(w, map[string]any{"name": "External", "protocolVersion": "0.3.0", "url": server.URL + "/rpc"})
					} else {
						writeJSON(w, map[string]any{"name": "External", "supportedInterfaces": []any{map[string]any{"protocolBinding": "JSONRPC", "protocolVersion": "1.0", "url": server.URL + "/rpc"}}})
					}
					return
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Message map[string]any `json:"message"`
					} `json:"params"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				method := "SendMessage"
				if legacy {
					method = "message/send"
					if req.Params.Message["kind"] != "message" {
						t.Error("missing legacy message discriminator")
					}
				}
				if req.Method != method {
					t.Errorf("unexpected method %s", req.Method)
				}
				state := "TASK_STATE_INPUT_REQUIRED"
				if legacy {
					state = "input-required"
				}
				var result any = map[string]any{"id": "external-task", "contextId": "external-context", "status": map[string]any{"state": state}, "artifacts": []any{map[string]any{"artifactId": "a", "parts": []any{map[string]any{"text": "Important result"}, map[string]any{"data": map[string]any{"score": 7}}}}}}
				if sends.Add(1) > 1 {
					if req.Params.Message["taskId"] != "external-task" {
						t.Error("lost task correlation")
					}
					result = map[string]any{"kind": "message", "messageId": "reply", "role": "agent", "parts": []any{map[string]any{"kind": "text", "text": "Follow-up answer"}}}
					if !legacy {
						result = map[string]any{"message": result}
					}
				} else if !legacy {
					result = map[string]any{"task": result}
				}
				writeRPC(w, req.ID, result)
			}))
			defer server.Close()
			ctx, p := newTestEnv(t)
			a := &App{}
			remote, _, err := a.connectPublicAgent(context.Background(), ctx, server.URL, "", "operator")
			if err != nil {
				t.Fatal(err)
			}
			ask := resultMap(t)(a.toolAsk(callerCtx(41, "requester"), ctx, map[string]any{"to": "a2a:" + remote.Ref, "message": "Start"}))
			task, _ := getTask(ctx.AppDB(), testProject, ask["task_id"].(int64))
			if task.Status != "input_required" || !strings.Contains(string(task.Artifacts), "score") {
				t.Fatalf("lost task outputs: %+v", task)
			}
			if len(p.threadEvents) != 1 || !strings.Contains(p.threadEvents[0].Message, "Important result") {
				t.Fatal("artifact was not delivered")
			}
			follow := resultMap(t)(a.toolSend(callerCtx(41, "requester"), ctx, map[string]any{"task_id": fmt.Sprint(task.ID), "message": "Continue"}))
			if follow["reply"] != "Follow-up answer" || follow["status"] != "completed" {
				t.Fatalf("lost follow-up: %#v", follow)
			}
			saved, _ := getTask(ctx.AppDB(), testProject, task.ID)
			if !strings.Contains(string(saved.Artifacts), "Important result") {
				t.Fatal("follow-up erased artifacts")
			}
		})
	}
}

func TestPollingFairnessAndBackoff(t *testing.T) {
	ctx, _ := newTestEnv(t)
	for i := 0; i < 101; i++ {
		_, err := createTask(ctx.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: "working", Direction: "outbound", FromAgentID: int64(i/25 + 1), RemoteTaskID: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := listOpenOutboundTasks(ctx.AppDB(), testProject, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if err := markPoll(ctx, task.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	next, err := listOpenOutboundTasks(ctx.AppDB(), testProject, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 100 || next[0].ID != 101 {
		t.Fatalf("unpolled task starved: %+v", next[0])
	}
	if err := markPoll(ctx, 101, true); err != nil {
		t.Fatal(err)
	}
	next, _ = listOpenOutboundTasks(ctx.AppDB(), testProject, 100)
	for _, task := range next {
		if task.ID == 101 {
			t.Fatal("failed task ignored backoff")
		}
	}
}

func TestDirectoryRefreshDoesNotExtendCardTTL(t *testing.T) {
	ctx, _ := newTestEnv(t)
	peer := peerConfig{ID: "remote"}
	entry := directoryEntry{CardID: "card", Name: "Remote"}
	card := &AgentCard{Name: "Remote", SupportedInterfaces: []AgentInterface{{URL: "https://example.com/old"}}}
	first, err := upsertRemoteAgent(ctx.AppDB(), peer, entry, card, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := upsertRemoteAgent(ctx.AppDB(), peer, entry, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExpiresAt != second.ExpiresAt || first.FetchedAt != second.FetchedAt {
		t.Fatal("directory renewed stale card")
	}
}

func TestPublicTransportPrivateDestinationPolicy(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:9", "https://127.0.0.1", "https://10.0.0.1", "https://169.254.169.254", "https://[::1]"} {
		if _, err := cardCandidates(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1:9", "10.0.0.1:9", "[::1]:9"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := publicDialContext(false)(ctx, "tcp", raw)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "private") {
			t.Errorf("dial did not reject %s: %v", raw, err)
		}
	}
	if !publicIPAllowed(net.ParseIP("127.0.0.1"), true) || publicIPAllowed(net.ParseIP("10.0.0.1"), true) {
		t.Fatal("development exception too broad")
	}
}

func TestAttachmentRevocationAndModernInboundOneWay(t *testing.T) {
	ctx, p := newTestEnvWithConfig(t, map[string]string{"peers_json": `[{"id":"peer","base_url":"https://example.com","token":"token","discover_agents":["*"],"invoke_agents":["*"]}]`})
	a := &App{}
	old := globalCtx
	defer func() { globalCtx = old }()
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	profile, err := ensureAgentProfile(ctx.AppDB(), *p.agents[42])
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(method string, params string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/agents/"+profile.CardID, strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params)))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("A2A-Version", "1.0")
		out := httptest.NewRecorder()
		a.handleAgentProtocol(out, req)
		return out
	}
	out := invoke("SendMessage", `{"message":{"messageId":"one","role":"ROLE_USER","parts":[{"text":"One-way notification"}]},"metadata":{"apteva.one_way":true}}`)
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	var response struct {
		Result struct {
			Task struct {
				ID     string `json:"id"`
				Status struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Task.Status.State != "TASK_STATE_COMPLETED" || !strings.Contains(p.lastEvent(t).Message, "no reply required") {
		t.Fatal(out.Body.String())
	}
	get := invoke("GetTask", fmt.Sprintf(`{"id":%q}`, response.Result.Task.ID))
	if get.Code != 200 {
		t.Fatal(get.Body.String())
	}
	// Empty attachment list must not trigger the old-server allow-all fallback.
	p.attached = map[int64]bool{}
	agents, _, err := listCardAgents(ctx, "")
	if err != nil || len(agents) != 0 {
		t.Fatalf("detached agents exposed: %+v %v", agents, err)
	}
	if out := invoke("GetTask", fmt.Sprintf(`{"id":%q}`, response.Result.Task.ID)); out.Code != http.StatusForbidden {
		t.Fatal("detached agent remained callable")
	}
	req := httptest.NewRequest(http.MethodGet, "/agent-cards/"+profile.CardID, nil)
	req.Header.Set("Authorization", "Bearer token")
	card := httptest.NewRecorder()
	a.handleAgentCard(card, req)
	if card.Code != 404 {
		t.Fatal("detached card still exposed")
	}
}

func TestTerminalFollowUpStartsTrackedTaskInSameContext(t *testing.T) {
	var sends atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{"name": "External", "supportedInterfaces": []any{map[string]any{"protocolBinding": "JSONRPC", "protocolVersion": "1.0", "url": server.URL + "/rpc"}}})
			return
		}
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)
		var params sendMessageParams
		json.Unmarshal(req.Params, &params)
		n := sends.Add(1)
		if params.Message.TaskID != "" {
			t.Error("reused terminal task id")
		}
		state := "TASK_STATE_COMPLETED"
		if n == 2 {
			if params.Message.ContextID != "context-1" {
				t.Error("lost previous context")
			}
			state = "TASK_STATE_WORKING"
		}
		writeRPC(w, req.ID, map[string]any{"task": map[string]any{"id": fmt.Sprint(n), "contextId": "context-1", "status": map[string]any{"state": state}}})
	}))
	defer server.Close()
	ctx, _ := newTestEnv(t)
	a := &App{}
	remote, _, err := a.connectPublicAgent(context.Background(), ctx, server.URL, "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	first := resultMap(t)(a.toolAsk(callerCtx(41, "thread"), ctx, map[string]any{"to": "a2a:" + remote.Ref, "message": "First"}))
	second := resultMap(t)(a.toolSend(callerCtx(41, "thread"), ctx, map[string]any{"task_id": fmt.Sprint(first["task_id"]), "message": "Follow up"}))
	if first["task_id"] == second["task_id"] {
		t.Fatal("terminal ledger row was reused")
	}
	tasks, err := listOpenOutboundTasks(ctx.AppDB(), testProject, 100)
	if err != nil || len(tasks) != 1 || tasks[0].RemoteTaskID != "2" {
		t.Fatalf("new task not tracked: %+v %v", tasks, err)
	}
}

func TestPollingSlowPeersDoesNotBlockHealthyTask(t *testing.T) {
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)
		var params taskIDParams
		json.Unmarshal(req.Params, &params)
		if params.ID != "healthy" {
			<-r.Context().Done()
			return
		}
		writeRPC(w, req.ID, map[string]any{"id": "healthy", "status": map[string]any{"state": "TASK_STATE_COMPLETED"}, "artifacts": []any{map[string]any{"parts": []any{map[string]any{"text": "Healthy answer"}}}}})
	}))
	defer server.Close()
	ctx, p := newTestEnv(t)
	a := &App{}
	peer := peerConfig{ID: "node", Name: "Node", BaseURL: server.URL, Kind: "node", Token: "token", ManagedBy: "operator"}
	keys, err := loadPeerKeyring(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := storePeer(ctx.AppDB(), keys, peer, nil); err != nil {
		t.Fatal(err)
	}
	_, err = upsertRemoteAgent(ctx.AppDB(), peer, directoryEntry{CardID: "card", Name: "Remote"}, &AgentCard{Name: "Remote", SupportedInterfaces: []AgentInterface{{ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0", URL: server.URL}}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprint(i)
		if i == 4 {
			id = "healthy"
		}
		_, err = createTask(ctx.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: "working", Direction: "outbound", FromAgentID: 41, FromThreadID: "requester", PeerID: peer.ID, RemoteCardID: "card", RemoteTaskID: id})
		if err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	if err := a.syncRemoteTasks(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 3500*time.Millisecond {
		t.Fatalf("poll stalled: %s", elapsed)
	}
	if peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("unexpected concurrency %d", peak.Load())
	}
	if len(p.threadEvents) != 1 || !strings.Contains(p.threadEvents[0].Message, "Healthy answer") {
		t.Fatalf("healthy task blocked: %+v", p.threadEvents)
	}
	var deferred int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM a2a_tasks WHERE next_poll_at<>''`).Scan(&deferred)
	if deferred != 4 {
		t.Fatalf("failed peers not backed off: %d", deferred)
	}
}

func TestOldAptevaNodeRemainsCompatible(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)
		methods = append(methods, req.Method)
		if req.Method == "SendMessage" {
			writeRPCError(w, req.ID, 404, -32601, "method not found")
			return
		}
		writeRPC(w, req.ID, map[string]any{"id": "old-task", "status": map[string]any{"state": "TASK_STATE_SUBMITTED"}})
	}))
	defer server.Close()
	ctx, _ := newTestEnv(t)
	a := &App{}
	peer := peerConfig{ID: "old", Kind: "node", BaseURL: server.URL, ProtocolVersion: "1.0"}
	task, _, err := a.sendRemoteMessage(context.Background(), ctx, &peer, server.URL, remoteMessageParams(&peer, "Hello", "", ""))
	if err != nil || task.ID != "old-task" {
		t.Fatalf("legacy node broken: %+v %v", task, err)
	}
	if strings.Join(methods, ",") != "SendMessage,message/send" {
		t.Fatal(methods)
	}
}

func TestModernSecurityDeclarationsRoundTrip(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"name": "Authenticated external", "supportedInterfaces": []any{map[string]any{"protocolBinding": "JSONRPC", "protocolVersion": "1.0", "url": server.URL + "/rpc"}},
			"securitySchemes":      map[string]any{"bearer": map[string]any{"httpAuthSecurityScheme": map[string]any{"scheme": "bearer"}}},
			"securityRequirements": []any{map[string]any{"schemes": map[string]any{"bearer": map[string]any{"list": []string{}}}}},
		})
	}))
	defer server.Close()
	ctx, p := newTestEnv(t)
	card, _, err := (&App{}).fetchPublicAgentCard(context.Background(), ctx, server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(card.SecurityRequirements) != 1 || !strings.Contains(string(card.SecuritySchemes["bearer"]), "httpAuthSecurityScheme") {
		t.Fatal("lost modern security declaration")
	}
	profile, err := ensureAgentProfile(ctx.AppDB(), *p.agents[42])
	if err != nil {
		t.Fatal(err)
	}
	local := buildAgentCard(ctx, *p.agents[42], profile)
	if len(local.SecurityRequirements) != 1 || !strings.Contains(string(local.SecuritySchemes["peerBearer"]), "httpAuthSecurityScheme") {
		t.Fatal("invalid generated v1 auth")
	}
	raw, _ := json.Marshal(local)
	if strings.Contains(string(raw), `"skills":null`) {
		t.Fatal("invalid null skills")
	}
}
