package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Stub PlatformClient ──────────────────────────────────────────

// stubPlatform records every ExecuteIntegrationTool / CallApp call
// and returns a canned response. Only the methods we actually use in
// tests are non-nil; everything else panics so failures are loud.
type stubPlatform struct {
	mu             sync.Mutex
	executeCalls   []executeCall
	callAppCalls   []callAppCall
	executeReply   *sdk.ExecuteResult
	executeErr     error
	callAppReply   json.RawMessage
	callAppErr     error
}

type executeCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func (s *stubPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.mu.Lock()
	s.executeCalls = append(s.executeCalls, executeCall{ConnID: connID, Tool: tool, Input: input})
	s.mu.Unlock()
	if s.executeErr != nil {
		return nil, s.executeErr
	}
	if s.executeReply != nil {
		return s.executeReply, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"MessageId":"ses-msg-123"}`)}, nil
}

func (s *stubPlatform) CallApp(app, tool string, input map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	s.callAppCalls = append(s.callAppCalls, callAppCall{App: app, Tool: tool, Input: input})
	s.mu.Unlock()
	if s.callAppErr != nil {
		return nil, s.callAppErr
	}
	return s.callAppReply, nil
}

// Unused PlatformClient methods — return zero values; tests that hit
// them would panic, which is the intended signal.
func (s *stubPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "aws-ses", Status: "active"}, nil
}
func (s *stubPlatform) ListConnections(sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (s *stubPlatform) GetInstance(int64) (*sdk.PlatformInstance, error)        { return nil, nil }
func (s *stubPlatform) SendEvent(int64, string) error                           { return nil }
func (s *stubPlatform) SendToChannel(string, string, string) error              { return nil }
func (s *stubPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	// Provide a binding for the email_provider role so IntegrationFor returns non-nil.
	return &sdk.InstallIdentity{
		AppName:   "messaging",
		ProjectID: "test-proj",
		Bindings:  map[string]any{"email_provider": float64(1)},
	}, nil
}
// Older PlatformClient interfaces (pre-StartOAuth) require neither
// of these methods; newer ones add them. We omit them so the stub
// builds against whichever app-sdk version go.mod resolves.

// ─── Test harness ─────────────────────────────────────────────────

func newTestCtx(t *testing.T, plat *stubPlatform, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"default_from_address": "notifications@acme.com",
		}),
	}, opts...)
	if plat != nil {
		full = append(full, tk.WithPlatform(plat))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", full...)
	globalCtx = ctx
	return ctx
}

// ─── send_message ─────────────────────────────────────────────────

func TestSendMessage_PersistsAndCallsProvider(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendMessage(ctx, map[string]any{
		"to":      "alice@example.com",
		"subject": "hello",
		"body":    "hi there",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["status"] != "sent" {
		t.Fatalf("status=%v, want sent", r["status"])
	}
	if r["channel"] != "email" {
		t.Errorf("channel=%v", r["channel"])
	}
	if r["provider_message_id"] != "ses-msg-123" {
		t.Errorf("provider_message_id=%v", r["provider_message_id"])
	}
	if len(plat.executeCalls) != 1 {
		t.Fatalf("expected 1 provider call, got %d", len(plat.executeCalls))
	}
	call := plat.executeCalls[0]
	if call.Tool != "send_email" {
		t.Errorf("tool=%q", call.Tool)
	}
	if call.Input["FromEmailAddress"] != "notifications@acme.com" {
		t.Errorf("FromEmailAddress=%v", call.Input["FromEmailAddress"])
	}
	dest := call.Input["Destination"].(map[string]any)
	to := dest["ToAddresses"].([]string)
	if len(to) != 1 || to[0] != "alice@example.com" {
		t.Errorf("ToAddresses=%v", to)
	}
	content := call.Input["Content"].(map[string]any)
	simple := content["Simple"].(map[string]any)
	if simple["Subject"].(map[string]any)["Data"] != "hello" {
		t.Errorf("Subject.Data=%v", simple["Subject"])
	}
	if simple["Body"].(map[string]any)["Text"].(map[string]any)["Data"] != "hi there" {
		t.Errorf("Body.Text.Data=%v", simple["Body"])
	}
}

func TestSendMessage_RequiresBodyOrTemplate(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := app.toolSendMessage(ctx, map[string]any{
		"to":      "alice@example.com",
		"subject": "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("expected body-required error, got %v", err)
	}
}

func TestSendMessage_Idempotency(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	args := map[string]any{
		"to":              "bob@example.com",
		"body":            "yo",
		"idempotency_key": "abc-123",
	}
	out1, err := app.toolSendMessage(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := app.toolSendMessage(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if out1.(map[string]any)["id"] != out2.(map[string]any)["id"] {
		t.Errorf("idempotent calls returned different ids: %v vs %v", out1, out2)
	}
	if len(plat.executeCalls) != 1 {
		t.Errorf("expected provider called once, got %d", len(plat.executeCalls))
	}
}

func TestSendMessage_RespectsSuppression(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	if err := dbSuppressionUpsert(ctx.AppDB(), "test-proj", "email", "mailto:bad@example.com", "hard-bounce", "auto"); err != nil {
		t.Fatal(err)
	}
	_, err := app.toolSendMessage(ctx, map[string]any{
		"to":   "bad@example.com",
		"body": "you'll never see this",
	})
	if err == nil {
		t.Fatal("expected suppression error")
	}
	if !strings.Contains(err.Error(), "suppressed") {
		t.Errorf("error %v should mention suppression", err)
	}
	if len(plat.executeCalls) != 0 {
		t.Errorf("provider should not have been called")
	}
}

func TestSendMessage_ProviderErrorMarksFailed(t *testing.T) {
	plat := &stubPlatform{
		executeReply: &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{"error":"boom"}`)},
	}
	ctx := newTestCtx(t, plat)
	app := &App{}

	out, err := app.toolSendMessage(ctx, map[string]any{
		"to":   "carol@example.com",
		"body": "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["status"] != "failed" {
		t.Errorf("status=%v, want failed", r["status"])
	}
	if !strings.Contains(r["status_reason"].(string), "non-2xx") {
		t.Errorf("status_reason=%v", r["status_reason"])
	}
}

// ─── templates ────────────────────────────────────────────────────

func TestTemplate_CreateGetUpdateList(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}

	out, err := app.toolTemplateCreate(ctx, map[string]any{
		"name":      "welcome",
		"subject":   "Welcome {{name}}",
		"body_text": "Hi {{name}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl := out.(map[string]any)["template"].(*Template)
	if tpl.Name != "welcome" || tpl.Channel != "email" {
		t.Errorf("template=%+v", tpl)
	}

	updated, err := app.toolTemplateUpdate(ctx, map[string]any{
		"id":      tpl.ID,
		"subject": "Welcome back {{name}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.(map[string]any)["template"].(*Template).Subject != "Welcome back {{name}}" {
		t.Error("update did not persist")
	}

	listOut, err := app.toolTemplateList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if listOut.(map[string]any)["count"].(int) != 1 {
		t.Errorf("expected 1 template, got %v", listOut)
	}
}

func TestSendMessage_TemplateRender(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	createOut, err := app.toolTemplateCreate(ctx, map[string]any{
		"name":      "ping",
		"subject":   "Hello {{name}}",
		"body_text": "Hi {{name}}, code = {{code}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	tplID := createOut.(map[string]any)["template"].(*Template).ID

	_, err = app.toolSendMessage(ctx, map[string]any{
		"to":          "user@example.com",
		"template_id": tplID,
		"vars":        map[string]any{"name": "Alice", "code": "X-42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plat.executeCalls) != 1 {
		t.Fatal("no provider call")
	}
	call := plat.executeCalls[0]
	simple := call.Input["Content"].(map[string]any)["Simple"].(map[string]any)
	if simple["Subject"].(map[string]any)["Data"] != "Hello Alice" {
		t.Errorf("subject=%v", simple["Subject"])
	}
	if simple["Body"].(map[string]any)["Text"].(map[string]any)["Data"] != "Hi Alice, code = X-42" {
		t.Errorf("body=%v", simple["Body"])
	}
}

// ─── inbound routes ───────────────────────────────────────────────

func TestInboundRoute_SetIdempotent(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}
	args := map[string]any{
		"pattern":      "mailto:support+*@acme.com",
		"target_app":   "support",
		"target_route": "/inbound",
	}
	out1, err := app.toolInboundRouteSet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := app.toolInboundRouteSet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	id1 := out1.(map[string]any)["route"].(*InboundRoute).ID
	id2 := out2.(map[string]any)["route"].(*InboundRoute).ID
	if id1 != id2 {
		t.Errorf("expected idempotent, got ids %d vs %d", id1, id2)
	}
}

// ─── suppression ──────────────────────────────────────────────────

func TestSuppression_AddRemove(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}
	if _, err := app.toolSuppressionAdd(ctx, map[string]any{
		"address": "blocked@x.com",
		"reason":  "manual",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolSuppressionList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["count"].(int) != 1 {
		t.Errorf("expected 1, got %v", out)
	}
	if _, err := app.toolSuppressionRemove(ctx, map[string]any{"address": "blocked@x.com"}); err != nil {
		t.Fatal(err)
	}
	out, _ = app.toolSuppressionList(ctx, map[string]any{})
	if out.(map[string]any)["count"].(int) != 0 {
		t.Errorf("expected 0 after remove, got %v", out)
	}
}
