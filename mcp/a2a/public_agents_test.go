package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func publicAgentServer(t *testing.T) (*httptest.Server, *[]jsonRPCRequest) {
	t.Helper()
	requests := &[]jsonRPCRequest{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent-card.json":
			writeJSON(w, AgentCard{
				Name: "Public Echo", Description: "Replies to public A2A requests", Version: "1.0.0",
				SupportedInterfaces: []AgentInterface{{ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0", URL: server.URL + "/rpc"}},
				Capabilities:        AgentCapabilities{}, DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
				Skills: []AgentSkill{{ID: "echo", Name: "Echo", Description: "Returns a concise acknowledgement"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rpc":
			if got := r.Header.Get("A2A-Version"); got != "1.0" {
				t.Errorf("A2A-Version = %q, want 1.0", got)
			}
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("anonymous public request sent Authorization %q", got)
			}
			var request jsonRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			*requests = append(*requests, request)
			if request.Method != "SendMessage" {
				writeRPCError(w, request.ID, http.StatusOK, -32601, "Method not found")
				return
			}
			writeRPC(w, request.ID, remoteSendResult{Message: &a2aMessage{
				MessageID: "public-reply-1", ContextID: "public-context-1", Role: "ROLE_AGENT",
				Parts: []a2aPart{{Text: "Public Echo received the Apteva request."}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	return server, requests
}

func TestDiscoverPublicCardAndAskAgent(t *testing.T) {
	server, requests := publicAgentServer(t)
	defer server.Close()
	ctx, _ := newTestEnv(t)
	app := &App{}

	discovered := resultMap(t)(app.toolDiscover(callerCtx(41, "public-test"), ctx, map[string]any{
		"card_url": server.URL, "capability": "echo",
	}))
	agents, ok := discovered["agents"].([]discoverEntry)
	if !ok || len(agents) != 1 {
		t.Fatalf("discovered agents = %#v, want one public agent", discovered["agents"])
	}
	if agents[0].Name != "Public Echo" || !strings.HasPrefix(agents[0].Address, "a2a:remote_") {
		t.Fatalf("public discovery entry = %+v", agents[0])
	}

	answer := resultMap(t)(app.toolAsk(callerCtx(41, "public-test"), ctx, map[string]any{
		"to": agents[0].Address, "message": "Confirm this public exchange.",
	}))
	if answer["status"] != "completed" || answer["reply"] != "Public Echo received the Apteva request." {
		t.Fatalf("public reply = %#v", answer)
	}
	if len(*requests) != 1 || (*requests)[0].Method != "SendMessage" {
		t.Fatalf("public RPC requests = %#v", *requests)
	}
	task, err := getTask(ctx.AppDB(), testProject, answer["task_id"].(int64))
	if err != nil || task == nil || task.Status != "completed" || task.Direction != "outbound" {
		t.Fatalf("public task = %+v, err %v", task, err)
	}
	var kind, managedBy string
	if err := ctx.AppDB().QueryRow(`SELECT kind, managed_by FROM a2a_peers WHERE id = ?`, agents[0].Peer).Scan(&kind, &managedBy); err == nil {
		// Peer is the display name in the discover response, not its id; verify below by kind count.
		t.Fatalf("unexpected peer lookup by display name succeeded: %s %s", kind, managedBy)
	}
	if err := ctx.AppDB().QueryRow(`SELECT kind, managed_by FROM a2a_peers LIMIT 1`).Scan(&kind, &managedBy); err != nil || kind != "agent_card" || managedBy != "agent" {
		t.Fatalf("cached connection kind/owner = %q/%q, err %v", kind, managedBy, err)
	}
}

func TestConnectionsAPIAddsListsAndRemovesPublicAgent(t *testing.T) {
	server, _ := publicAgentServer(t)
	defer server.Close()
	ctx, _ := newTestEnv(t)
	previous := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = previous }()
	app := &App{}

	body := fmt.Sprintf(`{"kind":"agent_card","card_url":%q}`, server.URL+"/.well-known/agent-card.json")
	addReq := httptest.NewRequest(http.MethodPost, "/connections?project_id="+testProject, strings.NewReader(body))
	add := httptest.NewRecorder()
	app.handleConnections(add, addReq)
	if add.Code != http.StatusOK {
		t.Fatalf("add public connection: HTTP %d: %s", add.Code, add.Body.String())
	}
	var added struct {
		Connection connectionView `json:"connection"`
		Agent      discoverEntry  `json:"agent"`
	}
	if err := json.Unmarshal(add.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.Connection.Kind != "agent_card" || added.Connection.ManagedBy != "operator" || added.Agent.Name != "Public Echo" {
		t.Fatalf("added connection = %+v, agent = %+v", added.Connection, added.Agent)
	}

	list := httptest.NewRecorder()
	app.handleConnections(list, httptest.NewRequest(http.MethodGet, "/connections?project_id="+testProject, nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "Public Echo") || strings.Contains(list.Body.String(), "encrypted_token") {
		t.Fatalf("list public connections: HTTP %d: %s", list.Code, list.Body.String())
	}

	remove := httptest.NewRecorder()
	path := "/connections/" + added.Connection.ID + "?project_id=" + testProject
	app.handleConnectionItem(remove, httptest.NewRequest(http.MethodDelete, path, nil))
	if remove.Code != http.StatusOK || !strings.Contains(remove.Body.String(), `"removed":true`) {
		t.Fatalf("remove public connection: HTTP %d: %s", remove.Code, remove.Body.String())
	}
}

func TestLegacyPublicCardAdapter(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent-card.json":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent.json":
			writeJSON(w, map[string]any{
				"protocolVersion": "0.3.0", "name": "Legacy Public", "description": "Legacy A2A agent",
				"url": server.URL + "/a2a", "version": "0.3.0", "capabilities": map[string]any{},
				"defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"},
				"skills": []map[string]any{{"id": "legacy-echo", "name": "Legacy echo"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/a2a":
			var request jsonRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Method != "message/send" {
				t.Errorf("legacy method = %q", request.Method)
			}
			var params sendMessageParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Message.Role != "user" || params.Message.Parts[0].Kind != "text" {
				t.Errorf("legacy message = %+v", params.Message)
			}
			writeRPC(w, request.ID, a2aMessage{MessageID: "legacy-reply", Role: "agent",
				Parts: []a2aPart{{Kind: "text", Text: "Legacy public reply."}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, _ := newTestEnv(t)
	app := &App{}
	discovered := resultMap(t)(app.toolDiscover(callerCtx(41, "legacy"), ctx, map[string]any{
		"card_url": server.URL, "capability": "legacy-echo",
	}))
	agents := discovered["agents"].([]discoverEntry)
	answer := resultMap(t)(app.toolAsk(callerCtx(41, "legacy"), ctx, map[string]any{
		"to": agents[0].Address, "message": "Legacy test",
	}))
	if answer["status"] != "completed" || answer["reply"] != "Legacy public reply." {
		t.Fatalf("legacy answer = %#v", answer)
	}
}

func TestPublicCardRejectsPrivateNetworkTargets(t *testing.T) {
	for _, target := range []string{"https://10.0.0.1", "https://169.254.169.254/latest/meta-data"} {
		if _, err := cardCandidates(target); err == nil || !strings.Contains(err.Error(), "private") {
			t.Errorf("cardCandidates(%q) error = %v, want private-network rejection", target, err)
		}
	}
}
