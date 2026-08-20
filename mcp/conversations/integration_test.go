//go:build integration

package main

// Tier 2 — the real binary, real HTTP. Boot the sidecar, talk MCP +
// REST. Validates the SDK wiring tier 1 cannot see: manifest parsing
// at boot, migrations applied to a fresh on-disk DB, JSON-RPC
// dispatch, route mounting (including the reserved-prefix guard
// against the real mux), /health, and caller-header auth.
//
// Run with:  go test -tags integration ./...

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// mcpErr calls a tool expecting a tool-level failure and returns the
// JSON-RPC error message (tool errors surface as code -32000 through
// the real dispatch, which the testkit helpers treat as fatal).
func mcpErr(t *testing.T, sc *tk.Sidecar, tool string, args map[string]any, agentID int64) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req, _ := http.NewRequest("POST", sc.URL()+"/mcp", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	if agentID != 0 {
		req.Header.Set("X-Apteva-Caller-Agent", strconv.FormatInt(agentID, 10))
		req.Header.Set("X-Apteva-Caller-Thread", "main")
		req.Header.Set("X-Apteva-Project-ID", itProject)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mcp request: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error == nil {
		t.Fatalf("%s: expected a tool error, got success", tool)
	}
	return out.Error.Message
}

type telegramGatewayFixture struct {
	mu            sync.Mutex
	webhookURL    string
	webhookSecret string
	sent          []map[string]any
	nextMessageID int64
}

func spawnTelegramHTTPTestSidecar(t *testing.T) (*tk.Sidecar, *telegramGatewayFixture) {
	t.Helper()
	fixture := &telegramGatewayFixture{nextMessageID: 900}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/whoami":
			_, _ = w.Write([]byte(`{"app_name":"conversations","version":"0.11.0","install_id":77,"project_id":"","public_url":"https://agents.example.test","bindings":{"telegram_bot":{"ids":[9],"default_id":9}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/connections/9":
			_, _ = w.Write([]byte(`{"id":9,"app_slug":"telegram","name":"Test bot","status":"active","project_id":"test-proj"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/agents":
			_, _ = w.Write([]byte(`[{"id":41,"name":"Test","status":"running","project_id":"test-proj","attached_to_caller":true}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/integrations/9/execute":
			var body struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			var data json.RawMessage
			switch body.Tool {
			case "get_me":
				data = json.RawMessage(`{"ok":true,"result":{"id":998877,"username":"sidecar_test_bot"}}`)
			case "set_webhook":
				fixture.webhookURL, _ = body.Input["url"].(string)
				fixture.webhookSecret, _ = body.Input["secret_token"].(string)
				data = json.RawMessage(`{"ok":true,"result":true}`)
			case "get_webhook_info":
				data = json.RawMessage(fmt.Sprintf(`{"ok":true,"result":{"url":%q,"pending_update_count":0}}`, fixture.webhookURL))
			case "delete_webhook":
				fixture.webhookURL = ""
				data = json.RawMessage(`{"ok":true,"result":true}`)
			case "send_message":
				fixture.sent = append(fixture.sent, body.Input)
				fixture.nextMessageID++
				data = json.RawMessage(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, fixture.nextMessageID))
			case "edit_message_text", "answer_callback_query":
				data = json.RawMessage(`{"ok":true,"result":true}`)
			default:
				http.Error(w, "unexpected tool", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": 200, "data": data})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/apps/callback/threads/"):
			_, _ = w.Write([]byte(`{"status":"created","thread":{"agent_id":41,"thread_id":"chat-test"}}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/apps/callback/agents/"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.Error(w, "offline", http.StatusBadGateway)
		}
	}))
	t.Cleanup(gateway.Close)
	return tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL)), fixture
}

const itProject = "test-proj"

func itHTTP(sc *tk.Sidecar, method, path string, body, out any) *tk.Response {
	return sc.RequestWithHeaders(method, path, body, out, map[string]string{
		"X-User-ID": "1", "X-Apteva-Project-ID": itProject,
	})
}

func spawnHTTPTestSidecar(t *testing.T) *tk.Sidecar {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/agents" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":41,"name":"Test","status":"running","project_id":"test-proj","attached_to_caller":true}]`))
			return
		}
		http.Error(w, "offline", http.StatusBadGateway)
	}))
	t.Cleanup(gateway.Close)
	return tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
}

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

// The 0.5.x agent contract end-to-end over real JSON-RPC: create
// (title-idempotent) → alert into it → list with query → history.
func TestSidecar_AgentToolFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))

	created := sc.MCPAs("create",
		map[string]any{"title": "Infra monitoring"}, 41, "main", itProject)
	convID, _ := created["conversation_id"].(string)
	if convID == "" || created["created"] != true {
		t.Fatalf("create = %v", created)
	}
	again := sc.MCPAs("create",
		map[string]any{"title": "infra MONITORING"}, 41, "worker-1", itProject)
	if again["conversation_id"] != convID || again["created"] != false {
		t.Fatalf("second create = %v, want reuse of %s", again, convID)
	}

	alerted := sc.MCPAs("alert", map[string]any{
		"conversation_id": convID, "text": "disk almost full", "severity": "warn",
	}, 41, "main", itProject)
	if _, ok := alerted["message_id"]; !ok {
		t.Fatalf("alert = %v", alerted)
	}

	listed := sc.MCPAs("list",
		map[string]any{"query": "infra"}, 41, "main", itProject)
	conversations, _ := listed["conversations"].([]any)
	if len(conversations) != 1 {
		t.Fatalf("list query = %v, want exactly the monitoring conversation", listed)
	}

	history := sc.MCPAs("history",
		map[string]any{"conversation_id": convID}, 41, "main", itProject)
	if !strings.Contains(fmt.Sprint(history), "disk almost full") {
		t.Fatalf("history missing the alert: %v", history)
	}
}

// Omitting conversation_id must return the teaching error — through
// the real JSON-RPC error surface, not just the in-process handler.
func TestSidecar_AlertWithoutConversationTeachesFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))
	msg := mcpErr(t, sc, "alert", map[string]any{"text": "orphan"}, 41)
	if !strings.Contains(msg, "conversations_create") {
		t.Fatalf("error = %q, want the list/create teaching error", msg)
	}
}

// Fail-closed without caller headers: the platform gateway always
// stamps them; a bare call must not write.
func TestSidecar_AnonymousCallerRefused(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))
	msg := mcpErr(t, sc, "create", map[string]any{"title": "Anon"}, 0)
	if !strings.Contains(strings.ToLower(msg), "caller identity") {
		t.Fatalf("error = %q, want caller-identity refusal", msg)
	}
}

// The dashboard-facing REST surface against the real mux: user chat
// creation, message append + read-back, inbox visibility, dismissal.
func TestSidecar_HTTPSurface(t *testing.T) {
	sc := spawnHTTPTestSidecar(t)

	var conv map[string]any
	if resp := itHTTP(sc, "POST", "/chats", map[string]any{
		"agent_id": 41, "title": "Ops chat", "project_id": itProject,
	}, &conv); resp.Status != 200 {
		t.Fatalf("create chat: %d %s", resp.Status, resp.Body)
	}
	convID, _ := conv["id"].(string)
	if convID == "" {
		t.Fatalf("chat = %v", conv)
	}

	var msg map[string]any
	if resp := itHTTP(sc, "POST", "/messages?chat_id="+convID, map[string]any{
		"content": "hello over real HTTP", "client_message_id": "it-1",
	}, &msg); resp.Status != 200 {
		t.Fatalf("post message: %d %s", resp.Status, resp.Body)
	}

	var transcript []map[string]any
	if resp := itHTTP(sc, "GET", "/messages?chat_id="+convID, nil, &transcript); resp.Status != 200 {
		t.Fatalf("get messages: %d", resp.Status)
	}
	// Two rows: the user message, then the LOUD system notice — with
	// no platform behind the sidecar, agent forwarding fails and the
	// failure must be visible, never silent. Tier 2 is the only place
	// that path runs through the real HTTP stack.
	if len(transcript) != 2 || transcript[0]["content"] != "hello over real HTTP" {
		t.Fatalf("transcript = %v", transcript)
	}
	if transcript[1]["role"] != "system" ||
		!strings.Contains(fmt.Sprint(transcript[1]["content"]), "could not reach") {
		t.Fatalf("expected loud forward-failure notice, got %v", transcript[1])
	}

	// An MCP-raised alert shows up in the REST inbox — the two
	// surfaces share one store in the real binary too.
	alerted := sc.MCPAs("alert", map[string]any{
		"conversation_id": convID, "text": "http-tier alert", "severity": "error",
	}, 41, "main", itProject)
	messageID, ok := alerted["message_id"].(float64)
	if !ok {
		t.Fatalf("alert = %v", alerted)
	}

	var inbox []map[string]any
	if resp := itHTTP(sc, "GET", "/inbox?limit=10", nil, &inbox); resp.Status != 200 {
		t.Fatalf("inbox: %d", resp.Status)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox = %v, want the alert", inbox)
	}

	var dismissed map[string]any
	if resp := itHTTP(sc, "POST", "/message-dismiss", map[string]any{
		"message_id": int64(messageID),
	}, &dismissed); resp.Status != 200 {
		t.Fatalf("dismiss: %d %s", resp.Status, resp.Body)
	}
	if resp := itHTTP(sc, "GET", "/inbox?limit=10", nil, &inbox); resp.Status != 200 || len(inbox) != 0 {
		t.Fatalf("inbox after dismiss = %v (status %d), want empty", inbox, resp.Status)
	}
}

// Public audience (0.6.0) through the real binary: keyed creation is
// public find-or-create, inbox kinds are refused via real JSON-RPC,
// and replying still works.
func TestSidecar_PublicConversationFlow(t *testing.T) {
	sc := spawnHTTPTestSidecar(t)

	create := func() map[string]any {
		var conv map[string]any
		if resp := itHTTP(sc, "POST", "/chats", map[string]any{
			"agent_id": 41, "title": "Visitor", "conversation_key": "app:webchat:v-1",
		}, &conv); resp.Status != 200 {
			t.Fatalf("keyed create: %d %s", resp.Status, resp.Body)
		}
		return conv
	}
	conv := create()
	convID, _ := conv["id"].(string)
	if conv["audience"] != "public" || convID == "" {
		t.Fatalf("keyed create = %v, want public", conv)
	}
	if again := create(); again["id"] != convID {
		t.Fatalf("keyed create minted a duplicate: %v vs %v", again["id"], convID)
	}

	msg := mcpErr(t, sc, "alert", map[string]any{
		"conversation_id": convID, "text": "internal", "severity": "error",
	}, 41)
	if !strings.Contains(msg, "operator conversation") {
		t.Fatalf("alert into public = %q, want operator-conversation refusal", msg)
	}

	sent := sc.MCPAs("send", map[string]any{
		"conversation_id": convID, "text": "Happy to help!",
	}, 41, "chat-"+convID, itProject)
	if _, ok := sent["message_id"]; !ok {
		t.Fatalf("send into public conversation = %v", sent)
	}
}

func TestSidecar_TelegramPublicOnboardingWebhookAndOutboundFlow(t *testing.T) {
	sc, fixture := spawnTelegramHTTPTestSidecar(t)
	var enabled map[string]any
	if resp := itHTTP(sc, http.MethodPost, "/telegram-connections", map[string]any{"connection_id": 9}, &enabled); resp.Status != http.StatusOK {
		t.Fatalf("enable Telegram: %d %s", resp.Status, resp.Body)
	}
	var policy map[string]any
	if resp := itHTTP(sc, http.MethodPost, "/telegram-intake", map[string]any{
		"connection_id": 9, "mode": "public", "default_agent_id": 41,
		"default_title": "Telegram support", "require_group_mention": true,
	}, &policy); resp.Status != http.StatusOK {
		t.Fatalf("configure intake: %d %s", resp.Status, resp.Body)
	}

	fixture.mu.Lock()
	webhookURL, webhookSecret := fixture.webhookURL, fixture.webhookSecret
	fixture.mu.Unlock()
	parsed, err := url.Parse(webhookURL)
	if err != nil || webhookSecret == "" {
		t.Fatalf("captured webhook url=%q secret-set=%t err=%v", webhookURL, webhookSecret != "", err)
	}
	key := path.Base(parsed.Path)
	payload := `{"update_id":501,"message":{"message_id":77,"from":{"id":12345,"username":"operator"},"chat":{"id":12345,"type":"private"},"text":"real sidecar inbound"}}`
	req, _ := http.NewRequest(http.MethodPost, sc.URL()+"/telegram-webhook/"+key, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", webhookSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Telegram webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Telegram webhook status=%d", resp.StatusCode)
	}
	var routes struct {
		Bindings []map[string]any `json:"bindings"`
	}
	if got := itHTTP(sc, http.MethodGet, "/telegram-bindings", nil, &routes); got.Status != http.StatusOK || len(routes.Bindings) != 1 {
		t.Fatalf("auto-created route: %d %s %+v", got.Status, got.Body, routes)
	}
	convID, _ := routes.Bindings[0]["conversation_id"].(string)
	if convID == "" || routes.Bindings[0]["audience"] != "public" || routes.Bindings[0]["access_mode"] != "public" {
		t.Fatalf("auto-created generic conversation route = %+v", routes.Bindings[0])
	}

	var transcript []map[string]any
	if got := itHTTP(sc, http.MethodGet, "/messages?chat_id="+convID, nil, &transcript); got.Status != http.StatusOK {
		t.Fatalf("transcript: %d %s", got.Status, got.Body)
	}
	if len(transcript) == 0 || transcript[0]["content"] != "real sidecar inbound" {
		t.Fatalf("Telegram transcript = %+v", transcript)
	}

	sc.MCPAs("send", map[string]any{"conversation_id": convID, "text": "real sidecar outbound"}, 41, "main", itProject)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.sent) != 1 || fixture.sent[0]["chat_id"] != "12345" || fixture.sent[0]["text"] != "real sidecar outbound" {
		t.Fatalf("Telegram outbound = %+v", fixture.sent)
	}
}
