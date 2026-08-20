package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const testTelegramConnectionID int64 = 9

type telegramTestAPI struct {
	nextMessageID int64
	failSend      bool
	webhookURL    string
	botName       string
	setNames      []string
	failSetName   bool
	failFeedback  bool
}

func telegramResult(data string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: json.RawMessage(data)}
}

func configureTelegramTestPlatform(p *recordingPlatform) *telegramTestAPI {
	api := &telegramTestAPI{nextMessageID: 700, botName: "Apteva Conversations"}
	p.identity = &sdk.InstallIdentity{
		AppName: appName, Version: "0.13.1", InstallID: 77, ProjectID: "",
		PublicURL: "https://agents.example.test",
		Bindings: map[string]any{telegramIntegrationRole: map[string]any{
			"ids": []any{float64(testTelegramConnectionID)}, "default_id": float64(testTelegramConnectionID),
		}},
	}
	p.connections = map[int64]*sdk.PlatformConnection{
		testTelegramConnectionID: {ID: testTelegramConnectionID, AppSlug: "telegram", Name: "Support bot", Status: "active", ProjectID: testProject},
	}
	p.integrationHandler = func(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if connectionID != testTelegramConnectionID {
			return nil, fmt.Errorf("unexpected connection %d", connectionID)
		}
		switch tool {
		case "get_me":
			return telegramResult(fmt.Sprintf(`{"ok":true,"result":{"id":998877,"username":"apteva_test_bot","first_name":%q}}`, api.botName)), nil
		case "set_my_name":
			if api.failSetName {
				return nil, errors.New("temporary Telegram profile outage")
			}
			api.botName, _ = input["name"].(string)
			api.setNames = append(api.setNames, api.botName)
			return telegramResult(`{"ok":true,"result":true}`), nil
		case "set_webhook":
			api.webhookURL, _ = input["url"].(string)
			return telegramResult(`{"ok":true,"result":true}`), nil
		case "get_webhook_info":
			return telegramResult(fmt.Sprintf(`{"ok":true,"result":{"url":%q,"pending_update_count":0}}`, api.webhookURL)), nil
		case "delete_webhook":
			api.webhookURL = ""
			return telegramResult(`{"ok":true,"result":true}`), nil
		case "send_chat_action", "send_message_draft":
			if api.failFeedback {
				return nil, errors.New("Telegram feedback operation unavailable")
			}
			return telegramResult(`{"ok":true,"result":true}`), nil
		case "answer_callback_query", "edit_message_text":
			return telegramResult(`{"ok":true,"result":true}`), nil
		case "send_message":
			if api.failSend {
				return nil, errors.New("temporary Telegram outage")
			}
			api.nextMessageID++
			return telegramResult(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, api.nextMessageID)), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram tool %s", tool)
		}
	}
	return api
}

func enableTelegramForTest(t *testing.T, app *App, platform *recordingPlatform) *TelegramConnectionConfig {
	t.Helper()
	if platform.integrationHandler == nil {
		configureTelegramTestPlatform(platform)
	}
	req := httptest.NewRequest(http.MethodPost, "/telegram-connections", strings.NewReader(`{"connection_id":9}`))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramConnections(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable Telegram: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "webhook_secret") || strings.Contains(rec.Body.String(), "botToken") {
		t.Fatalf("enable response leaked a secret: %s", rec.Body.String())
	}
	cfg, err := app.store.GetTelegramConnection(testTelegramConnectionID)
	if err != nil {
		t.Fatalf("Telegram connection config: %v", err)
	}
	return cfg
}

func bindTelegramForTest(t *testing.T, app *App, conv *Conversation, chatID string, allowed []int64) *TelegramBinding {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"connection_id": testTelegramConnectionID, "conversation_id": conv.ID,
		"chat_id": chatID, "allowed_user_ids": allowed,
	})
	req := httptest.NewRequest(http.MethodPost, "/telegram-bindings", strings.NewReader(string(body)))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramBindings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bind Telegram: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var binding TelegramBinding
	if err := json.NewDecoder(rec.Body).Decode(&binding); err != nil || binding.ID == "" {
		t.Fatalf("decode binding: %+v err=%v", binding, err)
	}
	return &binding
}

func postTelegramUpdate(app *App, cfg *TelegramConnectionConfig, secret, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/telegram-webhook/"+cfg.WebhookKey, strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	rec := httptest.NewRecorder()
	app.handleTelegramWebhook(rec, req)
	return rec
}

func configureTelegramIntakeForTest(t *testing.T, app *App, mode string) *TransportIntakePolicy {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"connection_id": testTelegramConnectionID, "mode": mode, "default_agent_id": 41,
		"default_title": "Telegram conversation", "require_group_mention": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/telegram-intake", strings.NewReader(string(body)))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramIntake(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("configure Telegram intake: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var policy TransportIntakePolicy
	if err := json.NewDecoder(rec.Body).Decode(&policy); err != nil {
		t.Fatal(err)
	}
	return &policy
}

func TestTelegramManifestUsesMultiplePlatformConnectionsAndSecretWebhook(t *testing.T) {
	manifest := (&App{}).Manifest()
	var telegramDepFound bool
	for _, dep := range manifest.Requires.Integrations {
		if dep.Role == telegramIntegrationRole {
			telegramDepFound = dep.Kind == "integration" && dep.Mode == "multiple" && !dep.Required &&
				len(dep.CompatibleSlugs) == 1 && dep.CompatibleSlugs[0] == "telegram"
		}
	}
	if !telegramDepFound {
		t.Fatalf("Telegram integration dependency = %+v", manifest.Requires.Integrations)
	}
	permissions := map[sdk.Permission]bool{}
	for _, permission := range manifest.Requires.Permissions {
		permissions[permission] = true
	}
	if !permissions[sdk.PermConnectionsRead] || !permissions[sdk.PermConnectionsExecute] {
		t.Fatalf("Telegram connection permissions = %+v", manifest.Requires.Permissions)
	}
	var webhookNoAuth bool
	for _, route := range (&App{}).HTTPRoutes() {
		if route.Pattern == "/telegram-webhook/" {
			webhookNoAuth = route.NoAuth
		}
	}
	if !webhookNoAuth {
		t.Fatal("Telegram webhook route is not explicitly no_auth")
	}
}

func TestTelegramEnableUsesBoundConnectionAndHidesSecrets(t *testing.T) {
	app, _, platform := newTestEnv(t)
	cfg := enableTelegramForTest(t, app, platform)
	if cfg.BotUsername != "apteva_test_bot" || cfg.BotID != "998877" {
		t.Fatalf("bot profile = %+v", cfg)
	}
	if cfg.WebhookSecret == "" || cfg.WebhookKey == "" || !strings.HasPrefix(cfg.WebhookURL, "https://agents.example.test/") {
		t.Fatalf("webhook config = %+v", cfg)
	}
	if len(platform.integrationCalls) != 3 || platform.integrationCalls[0].Tool != "get_me" || platform.integrationCalls[1].Tool != "set_webhook" || platform.integrationCalls[2].Tool != "get_webhook_info" {
		t.Fatalf("integration calls = %+v", platform.integrationCalls)
	}
	setInput := platform.integrationCalls[1].Input
	if setInput["secret_token"] != cfg.WebhookSecret || setInput["url"] != cfg.WebhookURL {
		t.Fatalf("set_webhook input = %+v", setInput)
	}
}

func TestTelegramBindingDefaultsPrivateOperatorAllowlistAndScopesProject(t *testing.T) {
	app, _, platform := newTestEnv(t)
	enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	binding := bindTelegramForTest(t, app, conv, "12345", nil)
	if len(binding.AllowedUserIDs) != 1 || binding.AllowedUserIDs[0] != 12345 {
		t.Fatalf("private operator allowlist = %v", binding.AllowedUserIDs)
	}

	req := httptest.NewRequest(http.MethodGet, "/telegram-bindings", nil)
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-Apteva-Project-ID", "other-project")
	rec := httptest.NewRecorder()
	app.handleTelegramBindings(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), binding.ID) {
		t.Fatalf("cross-project binding leak: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTelegramWebhookAuthenticatesDeduplicatesAndDoesNotEcho(t *testing.T) {
	app, _, platform := newTestEnv(t)
	cfg := enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	binding := bindTelegramForTest(t, app, conv, "12345", nil)

	body := `{"update_id":81,"message":{"message_id":31,"from":{"id":12345,"username":"operator"},"chat":{"id":12345,"type":"private"},"text":"hello from Telegram"}}`
	if rec := postTelegramUpdate(app, cfg, "wrong", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret status=%d", rec.Code)
	}
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("valid webhook status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("duplicate webhook status=%d body=%s", rec.Code, rec.Body.String())
	}
	transcript, err := app.store.Transcript(conv.ID, 0, 100)
	if err != nil || len(transcript) != 1 {
		t.Fatalf("transcript = %+v err=%v", transcript, err)
	}
	if transcript[0].Content != "hello from Telegram" || transcript[0].ExternalSender != "telegram:9:12345" {
		t.Fatalf("inbound message = %+v", transcript[0])
	}
	if _, err := app.store.DeliveryFor(transcript[0].ID, "telegram:"+binding.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("inbound Telegram message was echoed to Telegram: %v", err)
	}
	for _, call := range platform.integrationCalls {
		if call.Tool == "send_message" {
			t.Fatalf("inbound update echoed through send_message: %+v", call)
		}
	}
}

func TestTelegramOperatorRejectsUnlistedSender(t *testing.T) {
	app, _, platform := newTestEnv(t)
	cfg := enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	bindTelegramForTest(t, app, conv, "12345", nil)
	body := `{"update_id":82,"message":{"message_id":32,"from":{"id":999},"chat":{"id":12345,"type":"private"},"text":"intrusion"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 100)
	if len(transcript) != 0 {
		t.Fatalf("unlisted sender wrote messages: %+v", transcript)
	}
}

func TestTelegramWebhookStopsWhenConnectionIsUnbound(t *testing.T) {
	app, _, platform := newTestEnv(t)
	cfg := enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	bindTelegramForTest(t, app, conv, "12345", nil)
	platform.identity.Bindings = map[string]any{}
	body := `{"update_id":84,"message":{"message_id":34,"from":{"id":12345},"chat":{"id":12345,"type":"private"},"text":"must be ignored"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 100)
	if len(transcript) != 0 {
		t.Fatalf("unbound Telegram connection still wrote messages: %+v", transcript)
	}
}

func TestTelegramPublicBindingAcceptsExternalSenderWithoutOperatorAllowlist(t *testing.T) {
	app, _, platform := newTestEnv(t)
	cfg := enableTelegramForTest(t, app, platform)
	conv, err := app.store.CreateConversation(CreateConversationInput{
		ProjectID: testProject, LeadAgentID: 41, Title: "Public Telegram", OwnerUserID: 1, Audience: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := bindTelegramForTest(t, app, conv, "-10055", nil)
	if len(binding.AllowedUserIDs) != 0 || binding.Audience != "public" {
		t.Fatalf("public binding = %+v", binding)
	}
	body := `{"update_id":83,"message":{"message_id":33,"from":{"id":777,"username":"visitor"},"chat":{"id":-10055,"type":"group"},"text":"public question"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 100)
	if len(transcript) != 1 || transcript[0].ExternalSender != "telegram:9:777" {
		t.Fatalf("public transcript = %+v", transcript)
	}
}

func TestTelegramOutboundApprovalCallbackEditsOneMessage(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	api := configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	binding := bindTelegramForTest(t, app, conv, "12345", nil)

	if _, err := app.toolRequestApproval(callerCtx(41, "ops"), ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Deploy now?",
	}); err != nil {
		t.Fatalf("request approval: %v", err)
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 100)
	if len(transcript) != 1 || transcript[0].ComponentKind != kindApproval {
		t.Fatalf("approval transcript = %+v", transcript)
	}
	link, err := app.store.GetTelegramMessageLink(binding.ID, transcript[0].ID)
	if err != nil {
		t.Fatalf("Telegram link: %v", err)
	}
	var callbackData string
	for _, call := range platform.integrationCalls {
		if call.Tool != "send_message" {
			continue
		}
		markup, _ := call.Input["reply_markup"].(map[string]any)
		rows, _ := markup["inline_keyboard"].([][]map[string]string)
		if len(rows) > 0 && len(rows[0]) > 0 {
			callbackData = rows[0][0]["callback_data"]
		}
	}
	if !strings.HasPrefix(callbackData, "cv:") {
		t.Fatalf("callback data = %q", callbackData)
	}
	other := mkConversation(t, app, 41)
	bindTelegramForTest(t, app, other, "22222", nil)
	crossBody := fmt.Sprintf(`{"update_id":89,"callback_query":{"id":"cb-cross","from":{"id":22222},"message":{"message_id":%d,"chat":{"id":22222,"type":"private"}},"data":%q}}`, link.TelegramMessageID, callbackData)
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, crossBody); rec.Code != http.StatusOK {
		t.Fatalf("cross-chat callback status=%d body=%s", rec.Code, rec.Body.String())
	}
	stillPending, _ := app.store.GetMessage(transcript[0].ID)
	if stillPending.ActionStatus != "pending" {
		t.Fatalf("cross-chat callback resolved approval: %q", stillPending.ActionStatus)
	}
	body := fmt.Sprintf(`{"update_id":90,"callback_query":{"id":"cb-1","from":{"id":12345,"username":"operator"},"message":{"message_id":%d,"chat":{"id":12345,"type":"private"}},"data":%q}}`, link.TelegramMessageID, callbackData)
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := app.store.GetMessage(transcript[0].ID)
	if updated.ActionStatus != "approve" {
		t.Fatalf("approval status = %q", updated.ActionStatus)
	}
	if got, _ := updated.Components[0].Props["resolved_by_external"].(string); got != "telegram:9:12345" {
		t.Fatalf("resolved external actor = %q", got)
	}
	if len(platform.threadEvents) != 1 || !strings.Contains(platform.threadEvents[0].Message, "action=approve") {
		t.Fatalf("approval result events = %+v", platform.threadEvents)
	}
	tools := []string{}
	for _, call := range platform.integrationCalls {
		tools = append(tools, call.Tool)
	}
	if !strings.Contains(strings.Join(tools, ","), "edit_message_text") || !strings.Contains(strings.Join(tools, ","), "answer_callback_query") {
		t.Fatalf("Telegram callback calls = %v", tools)
	}
	if api.nextMessageID != link.TelegramMessageID {
		t.Fatalf("approval resolution sent a duplicate Telegram message: next=%d link=%d", api.nextMessageID, link.TelegramMessageID)
	}
}

func TestTelegramDeliveryFailureRetriesThroughOutbox(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	api := configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	binding := bindTelegramForTest(t, app, conv, "12345", nil)
	api.failSend = true
	result, err := app.toolSend(callerCtx(41, "main"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "retry me",
	})
	if err != nil {
		t.Fatalf("toolSend: %v", err)
	}
	messageID, _ := result.(map[string]any)["message_id"].(int64)
	delivery, err := app.store.DeliveryFor(messageID, "telegram:"+binding.ID)
	if err != nil || delivery.Status != "pending" || delivery.Attempts != 1 {
		t.Fatalf("failed delivery = %+v err=%v", delivery, err)
	}
	api.failSend = false
	if _, err := app.store.db.Exec(`UPDATE deliveries SET next_attempt_at=CURRENT_TIMESTAMP WHERE id=?`, delivery.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.redeliverPending(ctx); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	delivery, _ = app.store.DeliveryFor(messageID, "telegram:"+binding.ID)
	if delivery.Status != "delivered" || delivery.Attempts != 2 {
		t.Fatalf("retried delivery = %+v", delivery)
	}
}

func TestTelegramUnknownDMCreatesPairingRequestWithoutPersistingMessage(t *testing.T) {
	app, _, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "pairing")
	before := len(platform.integrationCalls)
	body := `{"update_id":101,"message":{"message_id":41,"from":{"id":7001,"username":"marco","first_name":"Marco"},"chat":{"id":7001,"type":"private","first_name":"Marco"},"text":"private content must not be retained"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("pairing webhook status=%d body=%s", rec.Code, rec.Body.String())
	}
	requests, err := app.store.ListTransportAccessRequests(telegramTransport, testProject)
	if err != nil || len(requests) != 1 {
		t.Fatalf("access requests=%+v err=%v", requests, err)
	}
	if requests[0].DisplayName != "Marco" || requests[0].PairingCode == "" || requests[0].ExternalChatID != "7001" {
		t.Fatalf("access request=%+v", requests[0])
	}
	var messageRows int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageRows); err != nil || messageRows != 0 {
		t.Fatalf("unapproved content persisted: rows=%d err=%v", messageRows, err)
	}
	if len(platform.integrationCalls) != before+1 || platform.integrationCalls[len(platform.integrationCalls)-1].Tool != "send_message" {
		t.Fatalf("pairing reply calls=%+v", platform.integrationCalls[before:])
	}
	if text, _ := platform.integrationCalls[len(platform.integrationCalls)-1].Input["text"].(string); strings.Contains(text, "private content") || !strings.Contains(text, requests[0].PairingCode) {
		t.Fatalf("pairing reply=%q", text)
	}
	// Another message within the same request window is acknowledged silently.
	body = strings.Replace(body, `"update_id":101`, `"update_id":102`, 1)
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("repeat pairing status=%d", rec.Code)
	}
	if len(platform.integrationCalls) != before+1 {
		t.Fatalf("repeat pairing generated spam: %+v", platform.integrationCalls[before:])
	}

	approveBody := fmt.Sprintf(`{"id":%q,"action":"approve"}`, requests[0].ID)
	req := httptest.NewRequest(http.MethodPost, "/telegram-access", strings.NewReader(approveBody))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramAccess(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := app.store.GetTelegramBindingByChat(testTelegramConnectionID, "7001")
	if err != nil || binding.AccessMode != "pairing" || len(binding.AllowedUserIDs) != 1 || binding.AllowedUserIDs[0] != 7001 {
		t.Fatalf("approved binding=%+v err=%v", binding, err)
	}
	body = `{"update_id":103,"message":{"message_id":42,"from":{"id":7001,"username":"marco","first_name":"Marco"},"chat":{"id":7001,"type":"private"},"text":"accepted message"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("accepted webhook status=%d body=%s", rec.Code, rec.Body.String())
	}
	transcript, _ := app.store.Transcript(binding.ConversationID, 0, 10)
	if len(transcript) != 1 || transcript[0].Content != "accepted message" {
		t.Fatalf("approved transcript=%+v", transcript)
	}
}

func TestTelegramPublicIntakeCreatesOneGenericConversationAndNewRotates(t *testing.T) {
	app, _, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "public")
	body := `{"update_id":111,"message":{"message_id":51,"from":{"id":8001,"username":"visitor","first_name":"Visitor"},"chat":{"id":8001,"type":"private","first_name":"Visitor"},"text":"I need help"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("public webhook status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := app.store.GetTelegramBindingByChat(testTelegramConnectionID, "8001")
	if err != nil || binding.AccessMode != "public" || binding.Audience != "public" {
		t.Fatalf("public binding=%+v err=%v", binding, err)
	}
	firstConversation := binding.ConversationID
	conv, _ := app.store.GetConversation(firstConversation)
	if conv.Origin != telegramTransport || conv.ConversationKey != "telegram:9:chat:8001" || conv.LeadAgentID != 41 {
		t.Fatalf("generic public conversation=%+v", conv)
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 10)
	if len(transcript) != 1 || transcript[0].Content != "I need help" {
		t.Fatalf("first public message=%+v", transcript)
	}

	newBody := `{"update_id":112,"message":{"message_id":52,"from":{"id":8001,"username":"visitor","first_name":"Visitor"},"chat":{"id":8001,"type":"private"},"text":"/new"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, newBody); rec.Code != http.StatusOK {
		t.Fatalf("new status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, _ = app.store.GetTelegramBindingByChat(testTelegramConnectionID, "8001")
	if binding.ConversationID == firstConversation {
		t.Fatal("/new did not rotate the generic conversation route")
	}
	oldTranscript, _ := app.store.Transcript(firstConversation, 0, 10)
	if len(oldTranscript) != 1 {
		t.Fatalf("old transcript changed=%+v", oldTranscript)
	}
	var conversationCount int
	_ = app.store.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE project_id=?`, testProject).Scan(&conversationCount)
	if conversationCount != 2 {
		t.Fatalf("conversation count=%d", conversationCount)
	}
}

func TestTelegramInviteDiscoversIdentityAndBindsWithoutNumericInput(t *testing.T) {
	app, _, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "pairing")
	conv := mkConversation(t, app, 41)
	req := httptest.NewRequest(http.MethodPost, "/telegram-invites", strings.NewReader(fmt.Sprintf(
		`{"connection_id":9,"conversation_id":%q,"chat_type":"private"}`, conv.ID)))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramInvites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite status=%d body=%s", rec.Code, rec.Body.String())
	}
	var inviteResult struct {
		URL string `json:"invite_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&inviteResult); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(inviteResult.URL)
	token := parsed.Query().Get("start")
	if token == "" || !strings.Contains(inviteResult.URL, "apteva_test_bot") {
		t.Fatalf("invite url=%q", inviteResult.URL)
	}
	var rawTokenRows int
	_ = app.store.db.QueryRow(`SELECT COUNT(*) FROM transport_invites WHERE token_hash=?`, token).Scan(&rawTokenRows)
	if rawTokenRows != 0 {
		t.Fatal("raw invite token was stored")
	}
	body := fmt.Sprintf(`{"update_id":121,"message":{"message_id":61,"from":{"id":9001,"username":"invitee","first_name":"Invitee"},"chat":{"id":9001,"type":"private"},"text":"/start %s"}}`, token)
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := app.store.GetTelegramBindingByChat(testTelegramConnectionID, "9001")
	if err != nil || binding.ConversationID != conv.ID || binding.AccessMode != "invite" || binding.ChatType != "private" {
		t.Fatalf("invite binding=%+v err=%v", binding, err)
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 10)
	if len(transcript) != 0 {
		t.Fatalf("/start leaked into transcript=%+v", transcript)
	}
}

func TestTelegramInviteRejectsTheWrongChatTypeWithoutConsumingIt(t *testing.T) {
	app, _, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "pairing")
	req := httptest.NewRequest(http.MethodPost, "/telegram-invites", strings.NewReader(
		`{"connection_id":9,"chat_type":"group"}`))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramInvites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("group invite status=%d body=%s", rec.Code, rec.Body.String())
	}
	var inviteResult struct {
		URL string `json:"invite_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&inviteResult); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(inviteResult.URL)
	token := parsed.Query().Get("startgroup")
	if token == "" {
		t.Fatalf("group invite url=%q", inviteResult.URL)
	}
	body := fmt.Sprintf(`{"update_id":122,"message":{"message_id":62,"from":{"id":9002,"first_name":"Invitee"},"chat":{"id":9002,"type":"private"},"text":"/start %s"}}`, token)
	if got := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); got.Code != http.StatusOK {
		t.Fatalf("wrong chat-type redemption status=%d body=%s", got.Code, got.Body.String())
	}
	if _, err := app.store.GetTelegramBindingByChat(testTelegramConnectionID, "9002"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong chat type created binding: %v", err)
	}
	lastCall := platform.integrationCalls[len(platform.integrationCalls)-1]
	if lastCall.Tool != "send_message" || !strings.Contains(lastCall.Input["text"].(string), "different kind") {
		t.Fatalf("wrong chat-type response=%+v", lastCall)
	}
	var unused int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM transport_invites WHERE token_hash=? AND used_at IS NULL`, hashTransportToken(token)).Scan(&unused); err != nil || unused != 1 {
		t.Fatalf("invite was consumed: unused=%d err=%v", unused, err)
	}
}

func TestTelegramBlockedSenderStaysSilentUntilUnblocked(t *testing.T) {
	app, _, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "pairing")
	body := `{"update_id":125,"message":{"message_id":65,"from":{"id":9005,"first_name":"Blocked"},"chat":{"id":9005,"type":"private"},"text":"hello"}}`
	if got := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); got.Code != http.StatusOK {
		t.Fatalf("initial pairing status=%d", got.Code)
	}
	requests, _ := app.store.ListTransportAccessRequests(telegramTransport, testProject)
	if len(requests) != 1 {
		t.Fatalf("initial requests=%+v", requests)
	}
	action := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/telegram-access", strings.NewReader(fmt.Sprintf(
			`{"id":%q,"action":%q}`, requests[0].ID, name)))
		authorizeTestRequest(req)
		rec := httptest.NewRecorder()
		app.handleTelegramAccess(rec, req)
		return rec
	}
	if rec := action("block"); rec.Code != http.StatusOK {
		t.Fatalf("block status=%d body=%s", rec.Code, rec.Body.String())
	}
	before := len(platform.integrationCalls)
	body = strings.Replace(body, `"update_id":125`, `"update_id":126`, 1)
	if got := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); got.Code != http.StatusOK {
		t.Fatalf("blocked retry status=%d", got.Code)
	}
	if len(platform.integrationCalls) != before {
		t.Fatalf("blocked sender received a reply: %+v", platform.integrationCalls[before:])
	}
	blocked, _ := app.store.ListBlockedTransportAccess(telegramTransport, testProject)
	if len(blocked) != 1 || blocked[0].ExternalUserID != "9005" {
		t.Fatalf("blocked list=%+v", blocked)
	}
	if rec := action("unblock"); rec.Code != http.StatusOK {
		t.Fatalf("unblock status=%d body=%s", rec.Code, rec.Body.String())
	}
	body = strings.Replace(body, `"update_id":126`, `"update_id":127`, 1)
	if got := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); got.Code != http.StatusOK {
		t.Fatalf("unblocked retry status=%d", got.Code)
	}
	requests, _ = app.store.ListTransportAccessRequests(telegramTransport, testProject)
	if len(requests) != 1 || len(platform.integrationCalls) != before+1 {
		t.Fatalf("unblocked request=%+v calls=%+v", requests, platform.integrationCalls[before:])
	}
}

func TestTelegramGroupPairingDefaultsToMentionOnly(t *testing.T) {
	app, _, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "pairing")
	body := `{"update_id":131,"message":{"message_id":71,"from":{"id":9101,"username":"owner","first_name":"Owner"},"chat":{"id":-10055,"type":"supergroup","title":"Engineering"},"text":"/connect"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("group request status=%d body=%s", rec.Code, rec.Body.String())
	}
	requests, _ := app.store.ListTransportAccessRequests(telegramTransport, testProject)
	if len(requests) != 1 || requests[0].ChatTitle != "Engineering" {
		t.Fatalf("group request=%+v", requests)
	}
	req := httptest.NewRequest(http.MethodPost, "/telegram-access", strings.NewReader(fmt.Sprintf(`{"id":%q,"action":"approve"}`, requests[0].ID)))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramAccess(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve group status=%d body=%s", rec.Code, rec.Body.String())
	}
	binding, _ := app.store.GetTelegramBindingByChat(testTelegramConnectionID, "-10055")
	if !binding.RequireMention || binding.ChatTitle != "Engineering" {
		t.Fatalf("group binding=%+v", binding)
	}
	plain := `{"update_id":132,"message":{"message_id":72,"from":{"id":9101},"chat":{"id":-10055,"type":"supergroup","title":"Engineering"},"text":"ambient chatter"}}`
	_ = postTelegramUpdate(app, cfg, cfg.WebhookSecret, plain)
	// Approval is for the room, so another member may address the bot without
	// requiring a second numeric-id allowlist or access request.
	mentioned := `{"update_id":133,"message":{"message_id":73,"from":{"id":9102,"first_name":"Teammate"},"chat":{"id":-10055,"type":"supergroup","title":"Engineering"},"text":"@apteva_test_bot please help"}}`
	_ = postTelegramUpdate(app, cfg, cfg.WebhookSecret, mentioned)
	transcript, _ := app.store.Transcript(binding.ConversationID, 0, 10)
	if len(transcript) != 1 || transcript[0].Content != "@apteva_test_bot please help" {
		t.Fatalf("mention-gated transcript=%+v", transcript)
	}
}

func TestTelegramConnectionReportsWebhookDrift(t *testing.T) {
	app, _, platform := newTestEnv(t)
	api := configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)
	api.webhookURL = "https://other.example/webhook"
	req := httptest.NewRequest(http.MethodGet, "/telegram-connections", nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramConnections(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"webhook_status":"drifted"`) {
		t.Fatalf("drift response status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func patchTelegramAutoName(t *testing.T, app *App, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"connection_id":%d,"auto_name_enabled":%t}`, testTelegramConnectionID, enabled)
	req := httptest.NewRequest(http.MethodPatch, "/telegram-connections", strings.NewReader(body))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramConnections(rec, req)
	return rec
}

func TestTelegramBotNameFollowsSoleRoutedAgentAndRestoresWhenShared(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	api := configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)

	cfg, err := app.store.GetTelegramConnection(testTelegramConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OriginalBotName != "Apteva Conversations" || !cfg.AutoNameEnabled {
		t.Fatalf("initial identity = %+v", cfg)
	}

	configureTelegramIntakeForTest(t, app, "pairing")
	if api.botName != "Research" || len(api.setNames) != 1 {
		t.Fatalf("sole-agent name = %q calls=%v", api.botName, api.setNames)
	}
	cfg, _ = app.store.GetTelegramConnection(testTelegramConnectionID)
	if cfg.SyncedAgentID != 41 || cfg.SyncedBotName != "Research" || cfg.NameSyncError != "" {
		t.Fatalf("sole-agent sync state = %+v", cfg)
	}

	other := mkConversation(t, app, 43)
	binding := bindTelegramForTest(t, app, other, "7300", nil)
	if api.botName != "Apteva Conversations" || len(api.setNames) != 2 {
		t.Fatalf("shared name = %q calls=%v", api.botName, api.setNames)
	}

	req := httptest.NewRequest(http.MethodDelete, "/telegram-bindings?id="+url.QueryEscape(binding.ID), nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramBindings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete binding: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.botName != "Research" || len(api.setNames) != 3 {
		t.Fatalf("restored sole-agent name = %q calls=%v", api.botName, api.setNames)
	}

	// Re-saving identical routing is idempotent and does not spend another
	// Telegram profile mutation.
	configureTelegramIntakeForTest(t, app, "pairing")
	if len(api.setNames) != 3 {
		t.Fatalf("idempotent sync made calls=%v", api.setNames)
	}
}

func TestTelegramAutoNameCanBeDisabledAndRestoresOriginalName(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	api := configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "pairing")

	rec := patchTelegramAutoName(t, app, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable auto name: status=%d body=%s", rec.Code, rec.Body.String())
	}
	cfg, _ := app.store.GetTelegramConnection(testTelegramConnectionID)
	if cfg.AutoNameEnabled || cfg.SyncedAgentID != 0 || cfg.SyncedBotName != "" {
		t.Fatalf("disabled identity state = %+v", cfg)
	}
	if api.botName != "Apteva Conversations" {
		t.Fatalf("disabled bot name = %q", api.botName)
	}
}

func TestTelegramNameFailureDoesNotBreakRoutingConfiguration(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	api := configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)
	api.failSetName = true

	configureTelegramIntakeForTest(t, app, "pairing")
	cfg, _ := app.store.GetTelegramConnection(testTelegramConnectionID)
	if cfg.NameSyncError == "" || cfg.SyncedAgentID != 0 {
		t.Fatalf("failed sync state = %+v", cfg)
	}
	if policy, err := app.store.GetTransportIntakePolicy(telegramTransport, testTelegramConnectionID); err != nil || policy.DefaultAgentID != 41 {
		t.Fatalf("routing was not saved: policy=%+v err=%v", policy, err)
	}
}

func telegramCallsByTool(platform *recordingPlatform, tool string) []capturedIntegrationCall {
	out := []capturedIntegrationCall{}
	for _, call := range platform.capturedIntegrationCalls() {
		if call.Tool == tool {
			out = append(out, call)
		}
	}
	return out
}

func TestTelegramMarkdownToHTMLIsSafeAndPortable(t *testing.T) {
	source := "# Status\n\n**Ready** with `job_1` and [details](https://example.com?a=1&b=2).\n- first\n> quoted\n\n```go\nif a < b {}\n```"
	want := "<b>Status</b>\n\n<b>Ready</b> with <code>job_1</code> and <a href=\"https://example.com?a=1&amp;b=2\">details</a>.\n• first\n<blockquote>quoted</blockquote>\n\n<pre><code>if a &lt; b {}\n</code></pre>"
	if got := telegramMarkdownToHTML(source); got != want {
		t.Fatalf("Telegram HTML:\n got: %q\nwant: %q", got, want)
	}
	if got, want := telegramMarkdownToHTML("<b>unsafe</b> **safe**"), "&lt;b&gt;unsafe&lt;/b&gt; <b>safe</b>"; got != want {
		t.Fatalf("escaped Telegram HTML = %q, want %q", got, want)
	}
	if got := telegramMarkdownToHTML("```\nunclosed <code>"); got != "<pre><code>unclosed &lt;code&gt;\n</code></pre>" {
		t.Fatalf("unclosed fence = %q", got)
	}
}

func TestTelegramOutboundAdaptsMarkdownWithoutChangingTranscript(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	bindTelegramForTest(t, app, conv, "12345", nil)
	original := "# Update\n\n**Done** with `job_1`; <internal> stays literal."

	if _, err := app.toolSend(callerCtx(41, "main"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": original,
	}); err != nil {
		t.Fatalf("toolSend: %v", err)
	}

	calls := telegramCallsByTool(platform, "send_message")
	if len(calls) != 1 {
		t.Fatalf("send_message calls = %+v", calls)
	}
	if got := calls[0].Input["parse_mode"]; got != "HTML" {
		t.Fatalf("parse_mode = %v, want HTML", got)
	}
	wantTelegram := "<b>Update</b>\n\n<b>Done</b> with <code>job_1</code>; &lt;internal&gt; stays literal."
	if got := calls[0].Input["text"]; got != wantTelegram {
		t.Fatalf("Telegram text = %q, want %q", got, wantTelegram)
	}
	transcript, err := app.store.Transcript(conv.ID, 0, 10)
	if err != nil || len(transcript) != 1 || transcript[0].Content != original {
		t.Fatalf("stored transcript = %+v, err=%v", transcript, err)
	}
}

func TestTelegramResponseFeedbackStreamsPrivateDraftAndStopsCleanly(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	binding, err := app.store.CreateTelegramBinding(TelegramBinding{
		ID: "tgb-feedback-private", ConnectionID: testTelegramConnectionID,
		ProjectID: testProject, ConversationID: conv.ID, ChatID: "12345",
		ChatType: "private", AccessMode: "invite", AllowedUserIDs: []int64{12345},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.telegramFeedback.flushEvery = 20 * time.Millisecond
	app.telegramFeedback.typingEvery = 200 * time.Millisecond
	app.telegramFeedback.draftEvery = 200 * time.Millisecond
	app.telegramFeedback.timeout = time.Second

	app.telegramFeedback.Start(ctx, cfg, binding)
	time.Sleep(50 * time.Millisecond)
	if drafts := telegramCallsByTool(platform, "send_message_draft"); len(drafts) != 0 {
		t.Fatalf("typing state created an empty draft: %+v", drafts)
	}
	firstFrameAt := time.Now()
	app.streamer.Ingest("llm.tool_chunk", 41, "chat-"+conv.ID,
		`{"tool":"conversations_conversations_send","id":"draft-call","chunk":"{\"conversation_id\":\"`+conv.ID+`\",\"text\":\"Hello from the agent\"}"}`,
		time.Now())
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		drafts := telegramCallsByTool(platform, "send_message_draft")
		if len(drafts) >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if drafts := telegramCallsByTool(platform, "send_message_draft"); len(drafts) != 1 {
		t.Fatalf("first tool chunk was not drafted immediately after %s: %+v", time.Since(firstFrameAt), drafts)
	}
	app.telegramFeedback.OnFrame(StreamFrame{ConversationID: conv.ID, Text: "Hello from the agent — more detail"})
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(telegramCallsByTool(platform, "send_message_draft")) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	app.telegramFeedback.CompleteBinding(binding.ID)

	typing := telegramCallsByTool(platform, "send_chat_action")
	drafts := telegramCallsByTool(platform, "send_message_draft")
	if len(typing) == 0 || typing[0].Input["action"] != "typing" {
		t.Fatalf("typing calls = %+v", typing)
	}
	if len(drafts) < 2 || drafts[0].Input["text"] != "Hello from the agent" || drafts[len(drafts)-1].Input["text"] != "Hello from the agent — more detail" {
		t.Fatalf("draft calls = %+v", drafts)
	}
	if drafts[0].Input["parse_mode"] != "HTML" || drafts[len(drafts)-1].Input["parse_mode"] != "HTML" {
		t.Fatalf("draft parse modes = %+v", drafts)
	}
	firstID, _ := drafts[0].Input["draft_id"].(int64)
	lastID, _ := drafts[len(drafts)-1].Input["draft_id"].(int64)
	chatID, _ := drafts[0].Input["chat_id"].(int64)
	if firstID <= 0 || lastID != firstID || chatID != 12345 {
		t.Fatalf("draft identity first=%d last=%d chat=%d", firstID, lastID, chatID)
	}
	settledCount := len(platform.capturedIntegrationCalls())
	time.Sleep(60 * time.Millisecond)
	if got := len(platform.capturedIntegrationCalls()); got != settledCount {
		t.Fatalf("feedback continued after final message: calls %d -> %d", settledCount, got)
	}
}

func TestTelegramResponseFeedbackUsesTypingForGroupsAndHonorsOff(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	binding := &TelegramBinding{ID: "tgb-feedback-group", ConnectionID: testTelegramConnectionID,
		ProjectID: testProject, ConversationID: conv.ID, ChatID: "-10055", ChatType: "supergroup"}

	before := len(platform.capturedIntegrationCalls())
	app.telegramFeedback.Start(ctx, cfg, binding)
	app.telegramFeedback.CompleteBinding(binding.ID)
	calls := platform.capturedIntegrationCalls()[before:]
	if len(calls) != 1 || calls[0].Tool != "send_chat_action" {
		t.Fatalf("group feedback calls = %+v", calls)
	}

	cfg.ResponseFeedback = telegramFeedbackOff
	before = len(platform.capturedIntegrationCalls())
	app.telegramFeedback.Start(ctx, cfg, binding)
	if got := len(platform.capturedIntegrationCalls()); got != before {
		t.Fatalf("off feedback made %d call(s)", got-before)
	}
}

func TestTelegramResponseFeedbackSettingValidation(t *testing.T) {
	app, _, platform := newTestEnv(t)
	configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)

	req := httptest.NewRequest(http.MethodPatch, "/telegram-connections", strings.NewReader(`{"connection_id":9,"response_feedback":"typing"}`))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramConnections(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set feedback: status=%d body=%s", rec.Code, rec.Body.String())
	}
	cfg, _ := app.store.GetTelegramConnection(testTelegramConnectionID)
	if cfg.ResponseFeedback != telegramFeedbackTyping {
		t.Fatalf("response feedback = %q", cfg.ResponseFeedback)
	}

	req = httptest.NewRequest(http.MethodPatch, "/telegram-connections", strings.NewReader(`{"connection_id":9,"response_feedback":"noisy"}`))
	authorizeTestRequest(req)
	rec = httptest.NewRecorder()
	app.handleTelegramConnections(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid feedback: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTelegramResponseFeedbackFailureDoesNotBlockInboundDelivery(t *testing.T) {
	app, _, platform := newTestEnv(t)
	api := configureTelegramTestPlatform(platform)
	cfg := enableTelegramForTest(t, app, platform)
	conv := mkConversation(t, app, 41)
	bindTelegramForTest(t, app, conv, "12345", nil)
	api.failFeedback = true

	body := `{"update_id":901,"message":{"message_id":81,"from":{"id":12345},"chat":{"id":12345,"type":"private"},"text":"still deliver this"}}`
	if rec := postTelegramUpdate(app, cfg, cfg.WebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("feedback failure blocked webhook: status=%d body=%s", rec.Code, rec.Body.String())
	}
	transcript, err := app.store.Transcript(conv.ID, 0, 10)
	if err != nil || len(transcript) != 1 || transcript[0].Content != "still deliver this" {
		t.Fatalf("inbound transcript = %+v err=%v", transcript, err)
	}
	if len(platform.spawns) != 1 {
		t.Fatalf("inbound message was not forwarded to the agent: spawns=%+v", platform.spawns)
	}
}

func TestTelegramDisconnectRemovesOwnedWebhookAndOnboardingState(t *testing.T) {
	app, _, platform := newTestEnv(t)
	api := configureTelegramTestPlatform(platform)
	enableTelegramForTest(t, app, platform)
	configureTelegramIntakeForTest(t, app, "pairing")
	req := httptest.NewRequest(http.MethodDelete, "/telegram-connections?id=9", nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleTelegramConnections(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disconnect status=%d body=%s", rec.Code, rec.Body.String())
	}
	if api.webhookURL != "" {
		t.Fatalf("owned webhook was not removed: %q", api.webhookURL)
	}
	if _, err := app.store.GetTelegramConnection(testTelegramConnectionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("connection remains after disconnect: %v", err)
	}
	if _, err := app.store.GetTransportIntakePolicy(telegramTransport, testTelegramConnectionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("intake policy remains after disconnect: %v", err)
	}
}
